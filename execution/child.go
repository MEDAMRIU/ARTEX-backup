package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

type ToolDescription struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

// ChildMain runs only inside the runner's namespace sandbox. Its entire inherited
// environment is constructed by the runner; no application credentials enter it.
func ChildMain(in io.Reader, out io.Writer) error {
	if os.Getenv("ARTEX_TOOL_SANDBOX") != "1" {
		return fmt.Errorf("tool worker requires the isolated sandbox launcher")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if dir != "/workspace" && !strings.HasPrefix(dir, "/workspace/") {
		return fmt.Errorf("invalid sandbox workspace")
	}
	// Chromium uses the per-session NSS store for the broker's public CA.
	nss := filepath.Join(os.Getenv("HOME"), ".pki", "nssdb")
	if err = os.MkdirAll(nss, 0700); err != nil {
		return err
	}
	if _, e := os.Stat(filepath.Join(nss, "cert9.db")); os.IsNotExist(e) {
		if out, e := exec.Command("certutil", "-N", "-d", "sql:"+nss, "--empty-password").CombinedOutput(); e != nil {
			return fmt.Errorf("initialize browser trust: %v: %s", e, out)
		}
	}
	if out, e := exec.Command("certutil", "-A", "-d", "sql:"+nss, "-n", "ARTEX task broker", "-t", "C,,", "-i", os.Getenv("CURL_CA_BUNDLE")).CombinedOutput(); e != nil {
		return fmt.Errorf("install public broker CA: %v: %s", e, out)
	}
	tools := map[string]actool.CoreTool{}
	for _, t := range actool.DefaultTools() {
		tools[t.Name()] = t
	}
	for _, t := range actool.ShellSessionTools() {
		tools[t.Name()] = t
	}
	manager, err := actool.NewManager("isolated")
	if err != nil {
		return err
	}
	defer manager.Cleanup()
	for _, t := range []actool.CoreTool{actool.NewTaskOutput(), actool.NewTaskStop(), actool.NewTaskList(), actool.NewMonitor(), newStrictWebFetch()} {
		tools[t.Name()] = t
	}
	var browser *mcp.Client
	defer func() {
		if browser != nil {
			browser.Close()
		}
	}()
	initBrowser := func(ctx context.Context) error {
		if browser != nil {
			return nil
		}
		var e error
		// Keep one browser context for this worker's lifetime, without persisting
		// Chromium singleton locks into the workspace across container restarts.
		browser, e = mcp.NewStdioClient(ctx, "browser", "node", nil, "/opt/artex-tools/node_modules/@playwright/mcp/cli.js", "--headless", "--isolated", "--no-webmcp")
		if e != nil {
			return e
		}
		ts, e := browser.Tools(ctx)
		if e != nil {
			browser.Close()
			browser = nil
			return e
		}
		for _, t := range ts {
			tools[t.Name()] = t
		}
		return nil
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		var request Call
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err = enc.Encode(Reply{Error: "invalid tool request"}); err != nil {
				return err
			}
			continue
		}
		timeout := request.TimeoutSeconds
		if timeout <= 0 || timeout > 120 {
			timeout = 120
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		reply := Reply{}
		switch request.Name {
		case "__browser_tools":
			if err := initBrowser(ctx); err != nil {
				reply.Error = err.Error()
				break
			}
			var descriptions []ToolDescription
			for name, t := range tools {
				if strings.HasPrefix(name, "mcp__browser__") {
					descriptions = append(descriptions, ToolDescription{Name: name, Description: t.Description(), Schema: t.InputSchema()})
				}
			}
			data, _ := json.Marshal(descriptions)
			reply.Result = actool.Text(string(data))
		case "__readiness":
			cmd := exec.CommandContext(ctx, "node", "/opt/artex-runtime/readiness.cjs")
			cmd.Dir = dir
			// Node/browser diagnostics belong to stderr; mixing them with stdout
			// corrupts the JSON readiness protocol even when the checks succeed.
			output, err := cmd.Output()
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					output = append(output, exit.Stderr...)
				}
				reply.Error = fmt.Sprintf("runtime checks failed: %v: %.2000s", err, output)
			} else {
				var checks []Check
				if e := json.Unmarshal(output, &checks); e != nil {
					reply.Error = fmt.Sprintf("invalid readiness output: %.1000s", output)
					break
				}
				check := Check{Name: "browser_mcp", Required: true, Status: "failed"}
				if e := initBrowser(ctx); e != nil {
					check.Detail = e.Error()
				} else if tool := tools["mcp__browser__browser_run_code_unsafe"]; tool == nil {
					check.Detail = "browser_run_code_unsafe unavailable in the pinned MCP runtime"
				} else {
					input, _ := json.Marshal(map[string]string{"code": "async (page) => { await page.setContent('<button>Calculate</button><output>0</output>'); await page.locator('button').evaluate(el => el.addEventListener('click', () => document.querySelector('output').textContent='42')); await page.getByRole('button').click(); return await page.locator('output').textContent(); }"})
					result, e := tool.Call(ctx, input, &actool.ToolContext{WorkingDir: dir, OutputDir: filepath.Join(dir, "cmd-output")})
					if e != nil {
						check.Detail = e.Error()
					} else if result.IsError || !strings.Contains(result.Flatten(), "42") {
						check.Detail = "MCP local interaction failed: " + result.Flatten()
					} else {
						check.Status = "passed"
						check.Detail = "Locked MCP server launched sandboxed Chromium and completed a local interaction"
					}
				}
				checks = append(checks, check)
				data, _ := json.Marshal(checks)
				reply.Result = actool.Text(string(data))
			}
		default:
			if strings.HasPrefix(request.Name, "mcp__browser__") {
				if err := initBrowser(ctx); err != nil {
					reply.Error = err.Error()
					break
				}
			}
			t, ok := tools[request.Name]
			if !ok {
				reply.Error = "tool is unavailable in the isolated runner: " + request.Name
				break
			}
			outputDir := filepath.Join(dir, "cmd-output")
			_ = os.MkdirAll(outputDir, 0700)
			useID := fmt.Sprintf("%x", sha256.Sum256([]byte(request.UseID+time.Now().String())))[:24]
			tc := &actool.ToolContext{WorkingDir: dir, AgentID: "isolated", ToolUseID: useID, MaxOutputChars: 30000, OutputDir: outputDir, Tasks: manager}
			result, err := t.Call(ctx, request.Input, tc)
			reply.Result = result
			if err != nil {
				reply.Error = err.Error()
			} else if result.IsError && strings.HasPrefix(request.Name, "mcp__browser__") {
				// Norma represents both valid MCP tool errors and transport failures
				// as IsError. A tools/list round trip distinguishes a dead connection
				// from an ordinary failed selector or target navigation.
				_, healthErr := browser.Tools(ctx)
				if healthErr != nil || strings.HasPrefix(result.Flatten(), "Error: bad MCP response:") {
					reply.Error = fmt.Sprintf("Browser MCP transport/protocol failed: %v; %.1500s", healthErr, result.Flatten())
					_ = browser.Close()
					browser = nil
				}
			} else if result.IsError && request.Name == "Bash" {
				text := strings.TrimSpace(result.Flatten())
				// A shell which ran and returned 126/127 is an ordinary command
				// observation (e.g. an optional `which` diagnostic). Readiness
				// checks the required dependencies; only launch failure latches
				// the infrastructure barrier here.
				if shellLaunchFailure(text) {
					reply.Error = "Required shell command could not run: " + text[:min(len(text), 2000)]
				}
			}
		}
		cancel()
		if err := enc.Encode(reply); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func shellLaunchFailure(text string) bool {
	return strings.Contains(text, "[failed to run:")
}
