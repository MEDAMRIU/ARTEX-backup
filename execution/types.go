// Package execution provides the isolated assessment runner and its egress
// broker. The app retains model/database credentials; tool children never receive
// them. An operator provisions one runner and one immutable policy per task.
package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // passed, failed, unconfigured, untested
	Required bool   `json:"required"`
	Detail   string `json:"detail"`
}
type Readiness struct {
	Mode      string    `json:"mode"`
	TaskID    string    `json:"task_id,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Ready     bool      `json:"ready"`
	Checks    []Check   `json:"checks"`
}

func (r *Readiness) Summarize() {
	r.Ready = len(r.Checks) > 0
	for _, c := range r.Checks {
		if c.Required && c.Status != "passed" {
			r.Ready = false
		}
	}
}

type Call struct {
	Name           string          `json:"name"`
	UseID          string          `json:"use_id,omitempty"`
	Input          json.RawMessage `json:"input"`
	Directory      string          `json:"directory"` // relative to the fixed task workspace
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
}
type Reply struct {
	Result actool.Result `json:"result"`
	Error  string        `json:"error,omitempty"`
}

type Client struct {
	mu              sync.RWMutex
	readiness       Readiness
	failure         string
	BaseURL         string
	Token           string
	TaskID          string
	Workspace       string // application's task root; never sent as an execution mount
	OnFailure       func(string)
	ExternalBlocked func() bool
	HTTP            *http.Client
}

func NewClientFromEnv(workspace string) (*Client, error) {
	mode := os.Getenv("ARTEX_EXECUTION_MODE")
	if mode == "local" {
		return nil, nil
	}
	if mode == "" {
		return &Client{}, nil
	}
	if mode != "isolated" {
		return nil, fmt.Errorf("ARTEX_EXECUTION_MODE must be isolated or explicit trusted local")
	}
	base := strings.TrimRight(os.Getenv("ARTEX_RUNNER_URL"), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("ARTEX_RUNNER_URL must be an http(s) origin")
	}
	token, err := os.ReadFile(os.Getenv("ARTEX_RUNNER_TOKEN_FILE"))
	if err != nil {
		return nil, fmt.Errorf("read runner token: %w", err)
	}
	taskID := os.Getenv("ARTEX_RUNNER_TASK_ID")
	if !validID(taskID) || len(strings.TrimSpace(string(token))) < 32 {
		return nil, fmt.Errorf("runner requires a canonical task id and a token of at least 32 characters")
	}
	return &Client{BaseURL: base, Token: strings.TrimSpace(string(token)), TaskID: taskID, Workspace: filepath.Join(workspace, "tasks", taskID), HTTP: &http.Client{Timeout: 130 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("isolated runner unavailable: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("isolated runner returned HTTP %d: %.500s", res.StatusCode, data)
	}
	return json.Unmarshal(data, out)
}
func (c *Client) Readiness(ctx context.Context, refresh bool) (Readiness, error) {
	var r Readiness
	if c.BaseURL == "" {
		return Readiness{Mode: "unconfigured", Checks: []Check{{Name: "isolated_runner", Status: "unconfigured", Required: true, Detail: "Provision a task-bound isolated runner before starting assessments. Local execution requires explicit ARTEX_EXECUTION_MODE=local and is not isolated."}}}, nil
	}
	method, path := "GET", "/v1/readiness"
	if refresh {
		method = "POST"
	}
	err := c.request(ctx, method, path, nil, &r)
	if err == nil && r.TaskID != c.TaskID {
		err = fmt.Errorf("runner is bound to a different task")
	}
	if err == nil && r.Ready {
		r.ValidateRequired()
		if r.Mode != "isolated" || r.CheckedAt.IsZero() || r.CheckedAt.After(time.Now().Add(time.Minute)) || !r.ExpiresAt.After(r.CheckedAt) || !time.Now().Before(r.ExpiresAt) {
			err = fmt.Errorf("runner readiness has invalid mode or validity dates")
		}
	}
	if err != nil {
		r.Ready = false
	}
	c.mu.Lock()
	c.readiness = r
	c.mu.Unlock()
	return r, err
}
func (c *Client) Call(ctx context.Context, name string, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if c.BaseURL == "" {
		return actool.Errorf("Tool execution is disabled until an isolated runner is configured."), nil
	}
	if tc == nil {
		return actool.Errorf("isolated runner requires an attributed workspace"), nil
	}
	rel, err := filepath.Rel(c.Workspace, tc.WorkingDir)
	if err != nil || !filepath.IsLocal(rel) {
		return actool.Errorf("This task has no isolated runner. Execution was blocked."), nil
	}
	request := Call{Name: name, UseID: tc.ToolUseID, Input: input, Directory: filepath.ToSlash(rel), TimeoutSeconds: 120}
	data, err := json.Marshal(request)
	if err != nil {
		return actool.Result{}, err
	}
	var reply Reply
	if err = c.request(ctx, "POST", "/v1/call", strings.NewReader(string(data)), &reply); err != nil {
		c.Fail(err.Error())
		return actool.Errorf(err.Error()), nil
	}
	if reply.Error != "" {
		c.Fail(reply.Error)
		return actool.Errorf(reply.Error), nil
	}
	// IsError also represents expected command/target outcomes (grep misses,
	// rejected HTTP requests, nonexistent selectors). Infrastructure faults are
	// carried separately in Reply.Error and persist the completion barrier above.
	return reply.Result, nil
}

func validID(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) <= 18
}

func (r *Readiness) ValidateRequired() {
	present := map[string]bool{}
	for _, c := range r.Checks {
		if c.Required && c.Status == "passed" {
			present[c.Name] = true
		}
	}
	for _, name := range []string{"filesystem_and_process_isolation", "shell_and_dependencies", "direct_egress_denied", "broker_scope_and_budget", "chromium_sandbox", "browser_cli", "browser_mcp"} {
		if !present[name] {
			r.Checks = append(r.Checks, Check{Name: name, Status: "failed", Required: true, Detail: "Required acceptance check did not pass."})
		}
	}
	r.Summarize()
}

func (c *Client) ReadyFor(task string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return task == c.TaskID && c.readiness.Ready && time.Now().Before(c.readiness.ExpiresAt.Add(time.Minute))
}

func (c *Client) Fail(detail string) {
	c.mu.Lock()
	c.failure = detail
	c.mu.Unlock()
	if c.OnFailure != nil {
		c.OnFailure(detail)
	}
}
func (c *Client) Blocked() bool {
	c.mu.RLock()
	blocked := c.failure != ""
	c.mu.RUnlock()
	return blocked || (c.ExternalBlocked != nil && c.ExternalBlocked())
}
func (c *Client) ClearFailure() { c.mu.Lock(); c.failure = ""; c.mu.Unlock() }
