package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestExpectedCommandFailureDoesNotPoisonRuntime(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		result := actool.Errorf("(no output)\n\n[exit code 1]")
		if calls > 1 {
			result = actool.Text("42")
		}
		writeRuntimeJSON(w, Reply{Result: result})
	}))
	defer srv.Close()
	workspace := t.TempDir()
	c := &Client{TaskID: "1", Workspace: workspace, BaseURL: srv.URL, HTTP: srv.Client()}
	for i := range 2 {
		result, err := c.Call(context.Background(), "Bash", []byte(`{}`), &actool.ToolContext{WorkingDir: workspace})
		if err != nil || result.IsError != (i == 0) || c.Blocked() {
			t.Fatalf("ordinary command outcome changed infrastructure readiness: %v %+v", err, result)
		}
	}
}

func TestShellCommandStatusIsNotAnExecutorLaunchFailure(t *testing.T) {
	for _, text := range []string{"which: command not found\n[exit code 127]", "permission denied\n[exit code 126]", "negative probe\n[exit code 1]"} {
		if shellLaunchFailure(text) {
			t.Fatalf("ordinary command result would block goal completion: %s", text)
		}
	}
	if !shellLaunchFailure("[failed to run: cannot start /bin/bash]") {
		t.Fatal("actual shell launch failure must retain infrastructure protection")
	}
}
