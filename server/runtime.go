package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Autumn-27/artex/execution"
)

func (s *Server) initRuntime() {
	c := s.runner
	if c == nil {
		return
	}
	configure := func(c *execution.Client) {
		c.ExternalBlocked = func() bool { v, _, e := s.m.pg.GetSetting("runtime_blocked_" + c.TaskID); return e != nil || v != "" }
		c.OnFailure = func(detail string) { _ = s.m.pg.SetSetting("runtime_blocked_"+c.TaskID, detail) }
	}
	c.ConfigureChild = configure
	if c.TaskID != "" {
		configure(c)
	}
}
func (s *Server) requireRuntime(task string, clearFailure bool) error {
	c := s.runner
	if c == nil {
		return nil
	}
	c, e := c.ForTask(task)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(s.ctx, 100*time.Second)
	defer cancel()
	r, e := c.Readiness(ctx, true)
	if e != nil {
		return e
	}
	if !r.Ready {
		return fmt.Errorf("isolated runtime failed readiness; open Tools to inspect the failed checks")
	}
	if clearFailure {
		if e := s.m.pg.SetSetting("runtime_blocked_"+task, ""); e != nil {
			return e
		}
		c.ClearFailure()
	}
	return nil
}

// Restore cached client readiness from the still-running task runner after an
// app restart. This never resets deadlines/budgets or clears a failure barrier.
func (s *Server) restoreRuntime(task string) error {
	if s.runner == nil {
		return nil
	}
	c, err := s.runner.ForTask(task)
	if err != nil {
		return err
	}
	if c.Blocked() {
		return fmt.Errorf("runtime failure is awaiting repair and explicit resume")
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	r, err := c.Readiness(ctx, false)
	if err != nil {
		return err
	}
	if !r.Ready {
		return fmt.Errorf("task runtime is not ready; run checks and resume after repair")
	}
	return nil
}
func (s *Server) runtimeReadiness(w http.ResponseWriter, r *http.Request) {
	c := s.runner
	if c == nil {
		writeJSON(w, http.StatusOK, execution.Readiness{Mode: "local", Checks: []execution.Check{{Name: "execution_isolation", Required: true, Status: "unconfigured", Detail: "Local mode runs tools with the application identity. Configure the isolated runner before an assessment."}}})
		return
	}
	task := r.URL.Query().Get("task")
	if task == "" {
		if t := s.m.ActiveTask(); t != nil {
			task = t.ID
		}
	}
	var err error
	c, err = c.ForTask(task)
	if err != nil {
		writeJSON(w, http.StatusOK, execution.Readiness{Mode: "isolated", TaskID: task, Checks: []execution.Check{{Name: "task_runner", Required: true, Status: "unconfigured", Detail: err.Error()}}})
		return
	}
	result, e := c.Readiness(r.Context(), r.Method == http.MethodPost)
	if e != nil {
		writeJSON(w, http.StatusOK, execution.Readiness{Mode: "isolated", TaskID: c.TaskID, Checks: []execution.Check{{Name: "runner", Required: true, Status: "failed", Detail: e.Error()}}})
		return
	}
	if c.Blocked() {
		result.Checks = append(append([]execution.Check(nil), result.Checks...), execution.Check{Name: "task_failure_barrier", Required: true, Status: "failed", Detail: "A runtime failure was recorded. Repair the runtime, then explicitly resume this task."})
		result.Summarize()
	}
	writeJSON(w, http.StatusOK, result)
}
