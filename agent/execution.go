package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Autumn-27/artex/execution"
	"github.com/Autumn-27/norma/agentcore"
	actool "github.com/Autumn-27/norma/tool"
)

// Set once during startup, before any model session is admitted.
var executionClient atomic.Pointer[execution.Client]

func ConfigureExecution(c *execution.Client) { executionClient.Store(c) }
func CurrentExecution() *execution.Client    { return executionClient.Load() }

func executionOptions(opts agentcore.Options) agentcore.Options {
	if CurrentExecution() == nil {
		return opts
	}
	opts.EnableWebFetch = false
	opts.EnableWebSearch = false
	opts.DisableBackgroundTasks = true
	opts.Memory = nil
	opts.Plan = nil
	opts.Skills = nil
	opts.BashEnv = nil
	opts.Tools = isolatedTools(opts.Tools)
	opts.SystemPrompt = append(opts.SystemPrompt, "Assessment tools execute in a separate task sandbox. Paths are under /workspace; use relative paths for evidence. Direct network connections are blocked. HTTP clients must use the configured proxy. For bounded TCP banner probes POST JSON {host,port,read_bytes,timeout_ms,payload_base64} to $ARTEX_BROKER_API/v1/tcp-probe. The operator's immutable scope, shared request limits and deadline are enforced outside this conversation. Raw-packet scanners and arbitrary remote tools are unavailable. A tool error, missing test or empty finding list is not proof of security. Save negative observations, exact response byte lengths, limitations and evidence paths in report.md.")
	return opts
}
func isolatedTools(in []actool.CoreTool) []actool.CoreTool {
	if CurrentExecution() == nil {
		return in
	}
	remote := map[string]bool{}
	for _, t := range actool.DefaultTools() {
		remote[t.Name()] = true
	}
	for _, t := range actool.ShellSessionTools() {
		remote[t.Name()] = true
	}
	for _, t := range []actool.CoreTool{actool.NewWebFetch(actool.WebFetchConfig{}), actool.NewTaskOutput(), actool.NewTaskStop(), actool.NewTaskList(), actool.NewMonitor()} {
		remote[t.Name()] = true
	}
	// Only task-local data handlers remain in the application. Unknown and global
	// asset/config/custom handlers fail closed, including newly added tools.
	local := map[string]bool{}
	for _, n := range strings.Fields("graph_overview list_findings list_facts node_detail expand_digest expand_index get_worker_output get_worker_trace search_all_worker_traces list_goals add_intent prove_goal goal_met kill_work steer_work report_finding report_hint record_fact add_hint set_goals set_constraints") {
		local[n] = true
	}
	out := []actool.CoreTool{}
	hasExecution := false
	seen := map[string]bool{}
	for _, t := range in {
		if seen[t.Name()] {
			continue
		}
		seen[t.Name()] = true
		if remote[t.Name()] || strings.HasPrefix(t.Name(), "mcp__browser__") {
			hasExecution = true
			out = append(out, &executionTool{CoreTool: t, client: CurrentExecution()})
			continue
		}
		if local[t.Name()] {
			out = append(out, &taskLocalTool{CoreTool: t, client: CurrentExecution()})
		}
	}
	if hasExecution {
		for _, t := range []actool.CoreTool{actool.NewWebFetch(actool.WebFetchConfig{}), actool.NewTaskOutput(), actool.NewTaskStop(), actool.NewTaskList(), actool.NewMonitor()} {
			if !seen[t.Name()] {
				out = append(out, &executionTool{CoreTool: t, client: CurrentExecution()})
			}
		}
	}
	return out
}

type executionTool struct {
	actool.CoreTool
	client *execution.Client
}

func (t *executionTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return t.client.Call(ctx, t.Name(), input, tc)
}

type taskLocalTool struct {
	actool.CoreTool
	client *execution.Client
}

func (t *taskLocalTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	ri := RunInfoFrom(ctx)
	if strconv.FormatInt(ri.TaskID, 10) != t.client.TaskID {
		return actool.Errorf("Task-local tool is unavailable outside the isolated task."), nil
	}
	if (t.Name() == "prove_goal" || t.Name() == "goal_met") && t.client.Blocked() {
		return actool.Errorf("Runtime failed during this assessment. Repair the runtime and resume after readiness passes before declaring completion."), nil
	}
	return t.CoreTool.Call(ctx, input, tc)
}
func IsolatedBrowserTools(ctx context.Context) ([]actool.CoreTool, error) {
	c := CurrentExecution()
	if c == nil {
		return nil, nil
	}
	if strconv.FormatInt(RunInfoFrom(ctx).TaskID, 10) != c.TaskID {
		return nil, nil
	}
	r, e := c.Call(ctx, "__browser_tools", json.RawMessage(`{}`), &actool.ToolContext{WorkingDir: c.Workspace})
	if e != nil {
		return nil, e
	}
	var desc []execution.ToolDescription
	if e = json.Unmarshal([]byte(r.Flatten()), &desc); e != nil {
		return nil, fmt.Errorf("isolated browser unavailable: %w", e)
	}
	var out []actool.CoreTool
	for _, d := range desc {
		d := d
		out = append(out, actool.Build(actool.Spec{Name: d.Name, Description: d.Description, Schema: d.Schema, Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			return c.Call(ctx, d.Name, in, tc)
		}}))
	}
	return out, nil
}
