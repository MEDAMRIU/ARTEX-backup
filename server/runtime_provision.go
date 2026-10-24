package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Autumn-27/artex/db"
)

// The operator service owns Docker and the approved scope profiles. The app can
// request only a task ID/profile ID over its Unix socket; tools get neither.
func provisionerRequest(ctx context.Context, method, path string, body any) (json.RawMessage, int, error) {
	socket := os.Getenv("ARTEX_PROVISIONER_SOCKET")
	if socket == "" || !filepath.IsAbs(socket) {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("automatic runner provisioning is not configured")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 175 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, method, "http://operator"+path, bytes.NewReader(data))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("approved runner service is unavailable; task remains paused")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || !json.Valid(raw) {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("invalid response from approved runner service")
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error == "" || len(failure.Error) > 300 {
			failure.Error = "Runner provisioning did not finish; task remains paused"
		}
		code := http.StatusServiceUnavailable
		if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusBadRequest {
			code = response.StatusCode
		}
		return nil, code, fmt.Errorf("%s", failure.Error)
	}
	return raw, http.StatusOK, nil
}

func (s *Server) runtimeProfiles(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ARTEX_PROVISIONER_SOCKET") == "" {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "profiles": []any{}})
		return
	}
	raw, code, err := provisionerRequest(r.Context(), http.MethodGet, "/v1/profiles", nil)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

var approvedRuntimeProfileID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func (s *Server) provisionTaskRuntime(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, http.StatusConflict, "Task is being removed")
		return
	}
	defer s.engine.decInflight(t.ID)
	if current, exists := s.m.Task(t.ID); !exists || current != t {
		writeErr(w, http.StatusConflict, "Task is no longer available")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	if s.runner == nil || os.Getenv("ARTEX_RUNNER_REGISTRY") == "" || !lifecycle.Paused || isTerminalStatus(lifecycle.Status) || s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "Provision a paused isolated task; existing assessments cannot receive a fresh budget")
		return
	}
	var request struct {
		ProfileID string `json:"profile_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "Select an approved assessment environment")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || !approvedRuntimeProfileID.MatchString(request.ProfileID) {
		writeErr(w, http.StatusBadRequest, "Select an approved assessment environment")
		return
	}
	raw, code, err := provisionerRequest(r.Context(), http.MethodPost, "/v1/provision",
		map[string]string{"task_id": t.ID, "profile_id": request.ProfileID})
	if err != nil {
		s.engine.emitActivity(t, db.Activity{Worker: "runtime", Kind: "error", Summary: err.Error()})
		writeErr(w, code, err.Error())
		return
	}
	var result struct {
		TaskID    string `json:"task_id"`
		ProfileID string `json:"profile_id"`
		Ready     bool   `json:"ready"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Ready || result.TaskID != t.ID || result.ProfileID != request.ProfileID {
		writeErr(w, http.StatusServiceUnavailable, "Runner service returned an unrelated or unready task")
		return
	}
	s.engine.emitActivity(t, db.Activity{Worker: "runtime", Kind: "text", Summary: "Approved assessment environment is ready. Resume starts this task with its original budget."})
	writeJSON(w, http.StatusOK, result)
}
