package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/chatgpt"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func (s *Server) configureChatGPT(c *agent.Config, id string) {
	if id == "" {
		return
	}
	c.ChatGPT = s.chatGPT
	c.ChatGPTAccountID = id
	c.APIKey = ""
	c.Format = llm.FormatOpenAIResponses
	c.BaseURL = chatgpt.Resource
	c.Proxy = ""
	c.Stream = true
	c.MaxTokens = 0
	c.ThinkingType = ""
	c.SessionHeaderKey = ""
}
func (s *Server) validateChatGPTProfile(ctx context.Context, p *db.LLMProfile) error {
	if p.ChatGPTAccountID == "" {
		return nil
	}
	if s.chatGPT == nil || !s.chatGPT.Ready(ctx, p.ChatGPTAccountID) {
		return errors.New("Connect ChatGPT and enable plan usage before saving this profile")
	}
	// Do not silently charge an API-key provider when plan usage is unavailable.
	p.Format = "openai-responses"
	p.BaseURL = chatgpt.Resource
	p.Proxy = ""
	p.APIKey = ""
	p.APIKeyHint = ""
	p.Streaming = true
	p.MaxTokens = 0
	p.MaxTokensField = ""
	p.ThinkingType = ""
	p.SessionHeaderKey = ""
	p.PoolExclude = true
	if strings.TrimSpace(p.Model) == "" {
		return errors.New("Choose a model from your ChatGPT account")
	}
	return nil
}
func (s *Server) chatGPTRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chatgpt/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !s.chatGPTAvailable(w) {
			return
		}
		as, err := s.chatGPT.Accounts(r.Context())
		if err != nil {
			writeErr(w, 500, "Cannot read ChatGPT account storage")
			return
		}
		writeJSON(w, 200, map[string]any{"accounts": as})
	})
	mux.HandleFunc("POST /api/chatgpt/sign-in", func(w http.ResponseWriter, r *http.Request) {
		if !s.chatGPTAvailable(w) {
			return
		}
		// The callback is on the machine running ARTEX. Remote browser sessions must
		// use a tunnel/local runtime; do not create an unreachable authorization URL.
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			writeErr(w, 400, "ChatGPT sign-in currently requires ARTEX running locally on this computer")
			return
		}
		var body struct {
			AccountID string `json:"account_id"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, 400, "Invalid sign-in request")
			return
		}
		login, err := s.chatGPT.Begin(r.Context(), body.AccountID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, login)
	})
	mux.HandleFunc("GET /api/chatgpt/sign-in/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.chatGPTAvailable(w) {
			return
		}
		status, message, id := s.chatGPT.LoginStatus(r.PathValue("id"))
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"status": status, "error": message, "account_id": id})
	})
	mux.HandleFunc("POST /api/chatgpt/sign-out", func(w http.ResponseWriter, r *http.Request) {
		if !s.chatGPTAvailable(w) {
			return
		}
		var body struct {
			AccountID string `json:"account_id"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, 400, "Invalid sign-out request")
			return
		}
		confirmed, err := s.chatGPT.SignOut(r.Context(), body.AccountID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		s.invalidateProfileAgents()
		s.reapplyActiveProfile()
		writeJSON(w, 200, map[string]any{"ok": true, "revocation_confirmed": confirmed})
	})
}
func (s *Server) chatGPTAvailable(w http.ResponseWriter) bool {
	if s.chatGPT == nil {
		writeErr(w, 503, "ChatGPT connection is unavailable")
		return false
	}
	return true
}
func (s *Server) chatGPTModelsFor(w http.ResponseWriter, r *http.Request, id string) {
	if !s.chatGPTAvailable(w) {
		return
	}
	models, err := s.chatGPT.Models(r.Context(), id)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	names := []string{}
	for _, m := range models {
		names = append(names, m.Slug)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "models": names, "catalog": models})
}
