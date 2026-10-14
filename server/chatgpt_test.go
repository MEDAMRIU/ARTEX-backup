package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatgpt"
	"github.com/Autumn-27/norma/harness"
)

func TestChatGPTRoutesRequireAuthAndLocalSignIn(t *testing.T) {
	m, err := chatgpt.New(t.TempDir(), []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{chatGPT: m, jwtKey: []byte(strings.Repeat("j", 32))}
	mux := http.NewServeMux()
	s.chatGPTRoutes(mux)
	h := s.requireAuth(mux)
	req := httptest.NewRequest("GET", "/api/chatgpt/accounts", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	token, _ := signJWT(s.jwtKey)
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:1234"} {
		req = httptest.NewRequest("POST", "/api/chatgpt/sign-in", strings.NewReader(`{}`))
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		want := 400
		if strings.HasPrefix(remote, "127.") {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("%s %d %s", remote, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "access_token") {
			t.Fatal("token leak")
		}
	}
}
func TestChatGPTProfileConfigCannotRedirectCredentials(t *testing.T) {
	s := &Server{}
	c := agent.ConfigFrom("anthropic", "gpt-6-sol", "https://evil.test", "other-secret", "http://evil.test")
	s.configureChatGPT(&c, "account")
	if c.BaseURL != chatgpt.Resource || c.APIKey != "" || c.Proxy != "" || !c.Stream || c.Provider() != "openai-responses" {
		t.Fatal("unsafe provider config")
	}
	if _, e := c.NewProvider(); e == nil {
		t.Fatal("nil token manager accepted")
	}
	if s.poolChain(1, nil, c) != nil {
		t.Fatal("plan should not fall back to API key")
	}
	if isQuotaExhaustedError(&chatgpt.PlanError{Cause: errors.New("quota exhausted")}) {
		t.Fatal("plan error rotates explicit task billing")
	}
	_ = context.Background()
}

func TestChatGPTPlanErrorNeverReplaysIntent(t *testing.T) {
	for _, message := range []string{"quota exhausted", "status 429", "EOF", "permission removed"} {
		err := &chatgpt.PlanError{Cause: errors.New(message)}
		if isRetryableStreamError(err) || retryableWorkerModelError(harness.ReasonModelError, err) {
			t.Fatalf("plan error retried: %s", message)
		}
	}
}
