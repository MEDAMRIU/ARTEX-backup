package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/execution"
	actool "github.com/Autumn-27/norma/tool"
)

func TestNewTaskRunnerAdmissionAndSharedReportExport(t *testing.T) {
	registry, workspace := t.TempDir(), t.TempDir()
	t.Setenv("ARTEX_EXECUTION_MODE", "isolated")
	t.Setenv("ARTEX_RUNNER_REGISTRY", registry)
	m, err := NewManager(workspace, "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	previous := agent.CurrentExecution()
	defer agent.ConfigureExecution(previous)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, t.TempDir(), workspace, t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	created := request("POST", "/api/tasks", `{"name":"runtime integration","description":"Private synthetic fixture","goal":"Save report evidence"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var dto TaskDTO
	if err = json.Unmarshal(created.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	task, ok := m.Task(dto.ID)
	if !ok {
		t.Fatal("created task absent")
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() { _ = m.pg.DeleteTask(id) }()
	readiness := request("GET", "/api/runtime/readiness?task="+task.ID, "")
	var ready execution.Readiness
	json.Unmarshal(readiness.Body.Bytes(), &ready)
	if ready.Ready || ready.TaskID != task.ID || len(ready.Checks) == 0 {
		t.Fatal("missing runner not surfaced")
	}
	if !task.lifecycleSnapshot().Paused {
		t.Fatal("unprovisioned task cannot be resumed because it was not paused")
	}
	var expired atomic.Bool
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 40) {
			t.Error("missing runner auth")
		}
		if r.URL.Path == "/v1/readiness" {
			expires := time.Now().Add(time.Hour)
			if expired.Load() {
				expires = time.Now().Add(-time.Second)
			}
			checks := []execution.Check{}
			for _, name := range strings.Fields("filesystem_and_process_isolation shell_and_dependencies direct_egress_denied broker_scope_and_budget chromium_sandbox browser_cli browser_mcp") {
				checks = append(checks, execution.Check{Name: name, Status: "passed", Required: true})
			}
			json.NewEncoder(w).Encode(execution.Readiness{Mode: "isolated", TaskID: task.ID, Ready: true, CheckedAt: time.Now(), ExpiresAt: expires, Checks: checks})
			return
		}
		var call execution.Call
		json.NewDecoder(r.Body).Decode(&call)
		if call.Name != "Write" || call.Directory != "i12" {
			t.Errorf("wrong runner dispatch: %+v", call)
		}
		dir := filepath.Join(workspace, "tasks", task.ID, call.Directory)
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "report.md"), []byte("Shared runner report: SYNTHETIC-REPORT-7294"), 0600)
		json.NewEncoder(w).Encode(execution.Reply{Result: actool.Text("report saved")})
	}))
	defer runner.Close()
	tokenFile := filepath.Join(registry, "token")
	os.WriteFile(tokenFile, []byte(strings.Repeat("t", 40)), 0600)
	raw, _ := json.Marshal(map[string]string{"task_id": task.ID, "url": runner.URL, "token_file": tokenFile})
	os.WriteFile(filepath.Join(registry, task.ID+".json"), raw, 0600)
	if err := s.requireRuntime(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if !s.runner.ReadyFor(task.ID) {
		t.Fatal("fresh task not admitted")
	}
	resumed := request("POST", "/api/tasks/"+task.ID+"/control", `{"action":"resume"}`)
	if resumed.Code != 200 {
		t.Fatalf("provision-then-resume failed: %d %s", resumed.Code, resumed.Body.String())
	}
	result, err := s.runner.Call(ctx, "Write", json.RawMessage(`{}`), &actool.ToolContext{WorkingDir: filepath.Join(workspace, "tasks", task.ID, "i12")})
	if err != nil || result.IsError {
		t.Fatalf("runner write: %+v %v", result, err)
	}
	report := request("GET", "/api/report?task="+task.ID, "")
	if report.Code != 200 || !strings.Contains(report.Body.String(), "SYNTHETIC-REPORT-7294") {
		t.Fatalf("report lost runner artifact: %d %s", report.Code, report.Body.String())
	}
	download := request("GET", "/api/workspace/download?path="+url.QueryEscape("tasks/"+task.ID+"/i12/report.md"), "")
	if download.Code != 200 || !strings.Contains(download.Body.String(), "SYNTHETIC-REPORT-7294") {
		t.Fatal("evidence download missed shared artifact")
	}
	// Simulate a cold app-side client cache while the same runner stays up.
	coldClient, err := execution.NewClientFromEnv(workspace)
	if err != nil {
		t.Fatal(err)
	}
	coldServer := &Server{runner: coldClient, m: m, ctx: ctx}
	coldServer.initRuntime()
	if coldClient.ReadyFor(task.ID) {
		t.Fatal("new client unexpectedly had cached readiness")
	}
	if err := coldServer.restoreRuntime(task.ID); err != nil || !coldClient.ReadyFor(task.ID) {
		t.Fatalf("restart failed to recover runtime readiness: %v", err)
	}
	child, _ := coldClient.ForTask(task.ID)
	child.Fail("persisted sandbox failure")
	if err := coldServer.restoreRuntime(task.ID); err == nil {
		t.Fatal("restart cleared recorded failure")
	}
	if err := s.requireRuntime(task.ID, true); err != nil {
		t.Fatal(err)
	}
	expired.Store(true)
	if err := s.requireRuntime(task.ID, true); err == nil {
		t.Fatal("expired policy accepted")
	}
	if s.runner.ReadyFor(task.ID) {
		t.Fatal("expired cached readiness retained")
	}
}
