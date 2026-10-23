package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTaskDetailMatchesListMetricsAndReportKeepsEvidence(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	task, err := m.CreateTask("export baseline", "baseline goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() { _ = m.pg.DeleteTask(id) }()
	if _, err := m.pg.Exec(`INSERT INTO activity(exploration_id,kind,input_tokens,output_tokens) VALUES ($1,'result',11,7)`, task.ExpID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state) VALUES ($1,'goal','{}','met'),($1,'fact','{"summary":"negative SSH baseline","evidence":"43 bytes"}','confirmed')`, task.ExpID); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.dir, "tasks", task.ID, "i3")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("Worker assessment: ten timeouts, no confirmed vulnerability."), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s status=%d: %s", path, w.Code, w.Body.String())
		}
		return w
	}
	var list struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	if err := json.Unmarshal(get("/api/tasks").Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var detail TaskDTO
	if err := json.Unmarshal(get("/api/tasks/"+task.ID).Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range list.Tasks {
		if row.ID == task.ID {
			found = true
			a, _ := json.Marshal(row.Tokens)
			b, _ := json.Marshal(detail.Tokens)
			if string(a) != string(b) || row.GoalsTotal != detail.GoalsTotal || row.GoalsMet != detail.GoalsMet || detail.GoalsTotal != 1 || detail.GoalsMet != 1 || row.LastActivity != detail.LastActivity {
				t.Fatalf("detail/list metrics diverge: list=%+v detail=%+v", row, detail)
			}
		}
	}
	if !found {
		t.Fatal("task missing from list")
	}
	body := get("/api/report?task=" + task.ID).Body.String()
	for _, want := range []string{"negative SSH baseline", "43 bytes", "Worker assessment: ten timeouts", "tasks/" + task.ID + "/i3/report.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("export omits %q", want)
		}
	}
}
