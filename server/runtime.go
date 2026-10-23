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
	if c == nil || c.BaseURL == "" {
		return
	}
	c.ExternalBlocked = func() bool { v, _, e := s.m.pg.GetSetting("runtime_blocked_" + c.TaskID); return e != nil || v != "" }
	c.OnFailure = func(detail string) { _ = s.m.pg.SetSetting("runtime_blocked_"+c.TaskID, detail) }
}
func (s *Server) requireRuntime(task string, clearFailure bool) error {
	c := s.runner
	if c == nil {
		return nil
	}
	if task != c.TaskID {
		return fmt.Errorf("no isolated runner is provisioned for task %s", task)
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
func (s *Server) runtimeReadiness(w http.ResponseWriter, r *http.Request) {
	c := s.runner
	if c == nil {
		writeJSON(w, http.StatusOK, execution.Readiness{Mode: "local", Checks: []execution.Check{{Name: "execution_isolation", Required: true, Status: "unconfigured", Detail: "Local mode runs tools with the application identity. Configure the isolated runner before an assessment."}}})
		return
	}
	result, e := c.Readiness(r.Context(), r.Method == http.MethodPost)
	if e != nil {
		writeJSON(w, http.StatusOK, execution.Readiness{Mode: "isolated", TaskID: c.TaskID, Checks: []execution.Check{{Name: "runner", Required: true, Status: "failed", Detail: e.Error()}}})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
