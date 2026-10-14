package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility,omitempty"`
}

func (m *Manager) Models(ctx context.Context, id string) ([]Model, error) {
	token, err := m.AccessToken(ctx, id)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", m.api+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("ChatGPT model catalog unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, responseError(resp)
	}
	var data struct {
		Models []Model `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil {
		return nil, errors.New("invalid ChatGPT model catalog")
	}
	out := []Model{}
	for _, m := range data.Models {
		if m.Visibility == "list" && m.Slug != "" {
			out = append(out, m)
		}
	}
	return out, nil
}
func responseError(resp *http.Response) error {
	var data struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data)
	code := data.Error.Code
	if !safeCode(code) {
		code = "request_failed"
	}
	return fmt.Errorf("ChatGPT: %s (HTTP %d). Check connection or Manage usage in model settings.", code, resp.StatusCode)
}

type planTransport struct {
	manager *Manager
	account string
	base    http.RoundTripper
}

// Transport authenticates each request just in time. Refreshed tokens never get
// captured in provider caches, profiles, logs or the browser.
func (m *Manager) Transport(id string, base http.RoundTripper) http.RoundTripper {
	return &planTransport{m, id, base}
}
func (t *planTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.manager.api+"/responses" || req.Method != "POST" {
		return nil, errors.New("ChatGPT credentials may only be used with the official Responses endpoint")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 32<<20))
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	body, err = planBody(body)
	if err != nil {
		return nil, err
	}
	token, err := t.manager.AccessToken(req.Context(), t.account)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Del("OpenAI-Organization")
	r.Header.Del("OpenAI-Project")
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, errors.New("ChatGPT inference connection failed")
	}
	if resp.StatusCode != 200 {
		defer resp.Body.Close()
		return nil, responseError(resp)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		return nil, errors.New("ChatGPT returned a non-streaming response")
	}
	resp.Body = &checkedStream{source: resp.Body, reader: bufio.NewReader(resp.Body)}
	return resp, nil
}
func planBody(body []byte) ([]byte, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	// Allowlist the adapter's supported fields. Preview-only unsupported options
	// must not reappear through future changes to Norma.
	for k := range data {
		switch k {
		case "model", "instructions", "input", "tools", "reasoning":
		default:
			delete(data, k)
		}
	}
	data["stream"] = json.RawMessage("true")
	data["store"] = json.RawMessage("false")
	var items []map[string]any
	if err := json.Unmarshal(data["input"], &items); err != nil {
		return nil, errors.New("ChatGPT requires input history")
	}
	if items == nil {
		items = []map[string]any{}
	}
	for _, item := range items {
		if item["role"] == "system" {
			item["role"] = "developer"
		}
		if item["type"] == "function_call" {
			item["namespace"] = "artex"
		}
	}
	data["input"], _ = json.Marshal(items)
	if len(data["tools"]) > 0 {
		var tools []map[string]any
		if err := json.Unmarshal(data["tools"], &tools); err != nil {
			return nil, err
		}
		for _, tool := range tools {
			if tool["type"] != "function" {
				return nil, errors.New("unsupported ChatGPT tool")
			}
		}
		if len(tools) > 0 {
			data["tools"], _ = json.Marshal([]any{map[string]any{"type": "namespace", "name": "artex", "description": "ARTEX tools", "tools": tools}})
		} else {
			delete(data, "tools")
		}
	}
	return json.Marshal(data)
}

// checkedStream keeps the existing Norma parser, but distinguishes completed,
// failed, incomplete, malformed and interrupted streams before it sees EOF.
type checkedStream struct {
	source    io.ReadCloser
	reader    *bufio.Reader
	pending   []byte
	completed bool
	terminal  error
}

func (s *checkedStream) Close() error { return s.source.Close() }
func (s *checkedStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.pending) == 0 && s.terminal == nil {
		var frame bytes.Buffer
		var data []string
		event := ""
		for {
			line, err := s.reader.ReadString('\n')
			frame.WriteString(line)
			if frame.Len() > 4<<20 {
				s.terminal = errors.New("ChatGPT event exceeds size limit")
				break
			}
			trim := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trim, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(trim, "event:"))
			}
			if strings.HasPrefix(trim, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(trim, "data:")))
			}
			if err != nil {
				if err == io.EOF && s.completed {
					s.terminal = io.EOF
				} else {
					s.terminal = errors.New("ChatGPT stream interrupted before response.completed")
				}
				break
			}
			if trim == "" {
				break
			}
		}
		if len(data) > 0 && s.terminal == nil {
			var f struct {
				Type     string `json:"type"`
				Response struct {
					Status string `json:"status"`
					Error  struct {
						Code string `json:"code"`
					} `json:"error"`
				} `json:"response"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &f); err != nil {
				s.terminal = errors.New("invalid ChatGPT stream event")
			} else {
				if event == "" {
					event = f.Type
				}
				switch event {
				case "response.completed":
					if f.Response.Status != "" && f.Response.Status != "completed" {
						s.terminal = errors.New("ChatGPT response did not complete")
					} else {
						s.completed = true
					}
				case "response.incomplete":
					s.terminal = errors.New("ChatGPT response incomplete")
				case "response.failed", "error":
					code := f.Response.Error.Code
					if code == "" {
						code = f.Error.Code
					}
					if !safeCode(code) {
						code = "response_failed"
					}
					s.terminal = fmt.Errorf("ChatGPT: %s. Check Manage usage or reconnect in model settings.", code)
				}
			}
		}
		if s.terminal == nil {
			s.pending = frame.Bytes()
		}
	}
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	return 0, s.terminal
}

// PlanError stops profile failover from changing billing after a plan error.
type PlanError struct{ Cause error }

func (e *PlanError) Error() string { return e.Cause.Error() }
func (e *PlanError) Unwrap() error { return e.Cause }

func IsPlanError(err error) bool {
	var plan *PlanError
	return errors.As(err, &plan)
}

type streamingProvider struct{ llm.Provider }

// Some harness paths (e.g. compaction) call Complete regardless of UI streaming.
// Preserve that interface while using the mandatory stream on the wire.
func Streaming(p llm.Provider) llm.Provider { return &streamingProvider{p} }
func (p *streamingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	var a llm.Accumulator
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		a.Add(ev)
	}
	return a.Message(), a.StopReason, a.Usage, nil
}
func (p *streamingProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for event, err := range p.Provider.Stream(ctx, req) {
			if err != nil {
				yield(llm.StreamEvent{}, &PlanError{err})
				return
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}
