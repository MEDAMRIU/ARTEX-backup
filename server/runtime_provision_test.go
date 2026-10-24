package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func fakeProvisionerSocket(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "artex-operator-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "operator.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	t.Cleanup(func() { server.Close() })
	go server.Serve(listener)
	t.Setenv("ARTEX_PROVISIONER_SOCKET", socket)
}

func TestProvisionerUnixBoundaryAndErrors(t *testing.T) {
	var requests atomic.Int64
	fakeProvisionerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == "GET" && r.URL.Path == "/v1/profiles" {
			w.Write([]byte(`{"enabled":true,"profiles":[]}`))
			return
		}
		if r.URL.Path == "/bad" {
			w.Write([]byte(`not-json`))
			return
		}
		if r.URL.Path == "/conflict" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"Task deadline has expired; retries cannot renew it"}`))
			return
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if len(input) != 2 || input["task_id"] != "4" || input["profile_id"] != "private-lab" {
			t.Errorf("unexpected operator arguments: %v", input)
		}
		w.Write([]byte(`{"task_id":"4","profile_id":"private-lab","ready":true}`))
	})
	_, code, err := provisionerRequest(context.Background(), "POST", "/v1/provision", map[string]string{"task_id": "4", "profile_id": "private-lab"})
	if err != nil || code != 200 {
		t.Fatalf("unix call: %d %v", code, err)
	}
	if _, code, err = provisionerRequest(context.Background(), "POST", "/conflict", nil); code != 409 || err == nil {
		t.Fatalf("expired budget conflict lost: %d %v", code, err)
	}
	if _, code, err = provisionerRequest(context.Background(), "GET", "/bad", nil); code != 503 || err == nil {
		t.Fatalf("invalid root response accepted: %d %v", code, err)
	}
	t.Setenv("ARTEX_PROVISIONER_SOCKET", "")
	w := httptest.NewRecorder()
	(&Server{}).runtimeProfiles(w, httptest.NewRequest("GET", "/api/runtime/profiles", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || requests.Load() != 3 {
		t.Fatal("disabled profile listing should not connect to the operator service")
	}
}

func TestProvisioningAPIAuthorizationAndPauseBoundary(t *testing.T) {
	t.Setenv("ARTEX_EXECUTION_MODE", "isolated")
	t.Setenv("ARTEX_RUNNER_REGISTRY", t.TempDir())
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	previous := agent.CurrentExecution()
	defer agent.ConfigureExecution(previous)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	created := request("POST", "/api/tasks", `{"description":"Private synthetic environment","goal":"Persist evidence"}`, true)
	var dto TaskDTO
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &dto) != nil {
		t.Fatalf("task creation failed: %s", created.Body.String())
	}
	task, _ := m.Task(dto.ID)
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(taskID)
	if !task.lifecycleSnapshot().Paused {
		t.Fatal("fresh unprovisioned task did not pause")
	}
	var calls atomic.Int64
	var unrelated atomic.Bool
	fakeProvisionerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input map[string]string
		json.NewDecoder(r.Body).Decode(&input)
		if len(input) != 2 || input["task_id"] != task.ID || input["profile_id"] != "private-lab" {
			t.Errorf("unapproved root arguments: %v", input)
		}
		id := task.ID
		if unrelated.Load() {
			id = "999999"
		}
		json.NewEncoder(w).Encode(map[string]any{"task_id": id, "profile_id": "private-lab", "ready": true})
	})
	path := "/api/tasks/" + task.ID + "/runtime/provision"
	if result := request("POST", path, `{"profile_id":"private-lab"}`, false); result.Code != 401 || calls.Load() != 0 {
		t.Fatal("unauthenticated user reached operator service")
	}
	for _, input := range []string{`{"profile_id":"private-lab","image":"attacker"}`, `{"profile_id":"../root"}`, `{"profile_id":"private-lab"} {}`, strings.Repeat("x", 1025)} {
		if result := request("POST", path, input, true); result.Code != 400 || calls.Load() != 0 {
			t.Fatalf("extra operator arguments accepted: %d", result.Code)
		}
	}
	if result := request("POST", path, `{"profile_id":"private-lab"}`, true); result.Code != 200 || !task.lifecycleSnapshot().Paused || calls.Load() != 1 {
		t.Fatal("provisioning must preserve pause until explicit Resume")
	}
	unrelated.Store(true)
	if result := request("POST", path, `{"profile_id":"private-lab"}`, true); result.Code != 503 {
		t.Fatal("unrelated root task response accepted")
	}
}
