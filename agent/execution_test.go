package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/execution"
	"github.com/Autumn-27/norma/agentcore"
	actool "github.com/Autumn-27/norma/tool"
)

func TestIsolatedToolsRemoteOnlyAndTaskBoundary(t *testing.T) {
	old := CurrentExecution()
	defer ConfigureExecution(old)
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Authorization") != "Bearer private-control-token" {
			t.Error("no authentication")
		}
		var in execution.Call
		json.NewDecoder(r.Body).Decode(&in)
		if in.Name != "Bash" || in.Directory != "i2" {
			t.Errorf("bad attribution %+v", in)
		}
		json.NewEncoder(w).Encode(execution.Reply{Result: actool.Text("remote42")})
	}))
	defer srv.Close()
	ConfigureExecution(&execution.Client{TaskID: "7", Workspace: filepath.Join(t.TempDir(), "tasks", "7"), BaseURL: srv.URL, Token: "private-control-token", HTTP: srv.Client()})
	localCalled := false
	forbidden := actool.Build(actool.Spec{Name: "custom_unsafe", Schema: map[string]any{}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		localCalled = true
		return actool.Text("bad"), nil
	}})
	opts := executionOptions(agentcore.Options{Tools: append(actool.DefaultTools(), forbidden), EnableWebFetch: true, EnableWebSearch: true})
	if opts.EnableWebFetch || opts.EnableWebSearch || !opts.DisableBackgroundTasks {
		t.Fatal("SDK side channels enabled")
	}
	for _, tool := range opts.Tools {
		if tool.Name() == "custom_unsafe" {
			t.Fatal("unknown tool survived")
		}
		if tool.Name() == "Bash" {
			r, e := tool.Call(context.Background(), json.RawMessage(`{"command":"printf 42"}`), &actool.ToolContext{WorkingDir: filepath.Join(CurrentExecution().Workspace, "i2")})
			if e != nil || r.Flatten() != "remote42" || !called {
				t.Fatalf("not remote %+v %v", r, e)
			}
			called = false
			r, _ = tool.Call(context.Background(), json.RawMessage(`{}`), &actool.ToolContext{WorkingDir: filepath.Dir(CurrentExecution().Workspace)})
			if called || !r.IsError {
				t.Fatal("other workspace accepted")
			}
		}
	}
	if localCalled {
		t.Fatal("local execution occurred")
	}
}
func TestIsolatedCompletionBlockedAfterRuntimeFailure(t *testing.T) {
	old := CurrentExecution()
	defer ConfigureExecution(old)
	ConfigureExecution(&execution.Client{TaskID: "7"})
	CurrentExecution().Fail("test runtime failure")
	called := false
	tool := actool.Build(actool.Spec{Name: "prove_goal", Schema: map[string]any{}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		called = true
		return actool.Text("bad"), nil
	}})
	wrapped := isolatedTools([]actool.CoreTool{tool})[0]
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 7})
	r, e := wrapped.Call(ctx, json.RawMessage(`{}`), nil)
	if e != nil || !r.IsError || called {
		t.Fatal("completion permitted after runtime failure")
	}
}
