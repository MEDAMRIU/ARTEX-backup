package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func TestRegistryDiscoversNewTasksAndKeepsBoundaries(t *testing.T) {
	registry, data := t.TempDir(), t.TempDir()
	t.Setenv("ARTEX_EXECUTION_MODE", "isolated")
	t.Setenv("ARTEX_RUNNER_REGISTRY", registry)
	client, err := NewClientFromEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ForTask("7"); err == nil {
		t.Fatal("unprovisioned task admitted")
	}
	register := func(id string) *httptest.Server {
		token := strings.Repeat(id, 40)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("cross-task token")
				w.WriteHeader(401)
				return
			}
			if r.URL.Path == "/v1/readiness" {
				checks := []Check{}
				for _, name := range []string{"filesystem_and_process_isolation", "shell_and_dependencies", "direct_egress_denied", "broker_scope_and_budget", "chromium_sandbox", "browser_cli", "browser_mcp"} {
					checks = append(checks, Check{Name: name, Required: true, Status: "passed"})
				}
				json.NewEncoder(w).Encode(Readiness{Mode: "isolated", TaskID: id, Ready: true, CheckedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Checks: checks})
				return
			}
			var call Call
			json.NewDecoder(r.Body).Decode(&call)
			if call.Directory != "i3" {
				t.Errorf("wrong worker path %q", call.Directory)
			}
			json.NewEncoder(w).Encode(Reply{Result: actool.Text("task-" + id)})
		}))
		t.Cleanup(srv.Close)
		file := filepath.Join(registry, id+".token")
		if err := os.WriteFile(file, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"task_id": id, "url": srv.URL, "token_file": file})
		if err := os.WriteFile(filepath.Join(registry, id+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	register("7")
	register("8")
	for _, id := range []string{"7", "8"} {
		child, err := client.ForTask(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := child.Readiness(context.Background(), true); err != nil || !client.ReadyFor(id) {
			t.Fatalf("task%s readiness: %v", id, err)
		}
		result, err := client.Call(context.Background(), "Bash", json.RawMessage(`{}`), &actool.ToolContext{WorkingDir: filepath.Join(data, "tasks", id, "i3")})
		if err != nil || result.Flatten() != "task-"+id {
			t.Fatalf("task%s routed incorrectly: %s %v", id, result.Flatten(), err)
		}
	}
	seven, _ := client.ForTask("7")
	eight, _ := client.ForTask("8")
	seven.Fail("broken sandbox")
	if !seven.Blocked() || eight.Blocked() {
		t.Fatal("failure barrier leaked between tasks")
	}
	// Replacing the operator file cannot reset an already bound task's failure.
	if err := os.WriteFile(filepath.Join(registry, "7.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	again, _ := client.ForTask("7")
	if again != seven || !again.Blocked() {
		t.Fatal("binding/failure reset")
	}
	for _, bad := range []string{"../7", "07", "0", "7/../../8", ""} {
		if _, err := client.ForTask(bad); err == nil {
			t.Fatalf("invalid id %q accepted", bad)
		}
	}
	result, _ := client.Call(context.Background(), "Bash", json.RawMessage(`{}`), &actool.ToolContext{WorkingDir: filepath.Join(data, "tasks-other", "8", "i3")})
	if !result.IsError {
		t.Fatal("unattributed workspace escaped")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := client.ForTask("8")
			if e != nil || c != eight {
				t.Error("unstable concurrent binding")
			}
		}()
	}
	wg.Wait()
}

func TestReadinessDoesNotExtendExpiredBudget(t *testing.T) {
	c := &Client{TaskID: "3", readiness: Readiness{Ready: true, ExpiresAt: time.Now().Add(-time.Second)}}
	if c.ReadyFor("3") {
		t.Fatal("expired runtime was treated as ready")
	}
}
