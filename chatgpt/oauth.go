package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const Resource = "https://api.openai.com/v1"
const UsageURL = "https://chatgpt.com/settings/usage"
const scopes = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"

type Manager struct {
	store       *store
	client      *http.Client
	issuer, api string
	mu          sync.Mutex
	pending     map[string]*attempt
}
type attempt struct {
	generation                                                     uint64
	state, nonce, verifier, redirect, clientID, accountID, subject string
	expires                                                        time.Time
	server                                                         *http.Server
	Status                                                         string `json:"status"`
	Error                                                          string `json:"error,omitempty"`
	AccountID                                                      string `json:"account_id,omitempty"`
}
type Login struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func New(dir string, key []byte) (*Manager, error) {
	s, err := newStore(dir, key)
	if err != nil {
		return nil, err
	}
	return &Manager{store: s, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, issuer: "https://auth.openai.com", api: Resource, pending: map[string]*attempt{}}, nil
}
func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hasScope(s, want string) bool {
	for _, v := range strings.Fields(s) {
		if v == want {
			return true
		}
	}
	return false
}
func (m *Manager) Accounts(ctx context.Context) ([]AccountView, error) {
	out := []AccountView{}
	err := m.store.with(ctx, func(d *diskState) (bool, error) {
		for _, a := range d.Accounts {
			out = append(out, a.view())
		}
		return false, nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}
func (m *Manager) Ready(ctx context.Context, id string) bool {
	as, err := m.Accounts(ctx)
	if err != nil {
		return false
	}
	for _, a := range as {
		if a.ID == id {
			return a.Connected && a.PlanEnabled
		}
	}
	return false
}
func (m *Manager) Begin(ctx context.Context, accountID string) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.pending {
		if time.Now().After(a.expires) {
			_ = a.server.Close()
			delete(m.pending, id)
		}
	}
	if len(m.pending) >= 8 {
		return Login{}, errors.New("too many sign-in attempts; wait for the current attempt to expire")
	}
	a := &attempt{state: random(), nonce: random(), verifier: random(), clientID: "dynamic_agent_client", expires: time.Now().Add(5 * time.Minute), Status: "pending"}
	var hostID string
	err := m.store.with(ctx, func(d *diskState) (bool, error) {
		hostID = d.HostID
		if accountID != "" {
			old := d.Accounts[accountID]
			if old == nil {
				return false, errors.New("unknown ChatGPT account")
			}
			a.clientID = old.ClientID
			a.accountID = old.ID
			a.subject = old.Subject
			a.generation = old.Generation
		}
		return true, nil
	})
	if err != nil {
		return Login{}, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Login{}, err
	}
	a.redirect = "http://" + listener.Addr().String() + "/auth/callback"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) { m.callback(a, w, r) })
	a.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	id := random()
	m.pending[id] = a
	go func() { _ = a.server.Serve(listener) }()
	go func() {
		timer := time.NewTimer(time.Until(a.expires))
		defer timer.Stop()
		<-timer.C
		m.mu.Lock()
		if a.Status == "pending" {
			a.Status = "expired"
			a.Error = "Sign-in expired. Try again."
		}
		m.mu.Unlock()
		_ = a.server.Close()
	}()
	sum := sha256.Sum256([]byte(a.verifier))
	q := url.Values{"client_id": {a.clientID}, "ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {a.redirect}, "scope": {scopes}, "resource": {Resource}, "state": {a.state}, "nonce": {a.nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	if accountID == "" {
		q.Set("agent_name_hint", "ARTEX")
	}
	return Login{id, m.issuer + "/api/accounts/authorize?" + q.Encode()}, nil
}
func (m *Manager) LoginStatus(id string) (string, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.pending[id]
	if a == nil {
		return "expired", "Sign-in attempt not found.", ""
	}
	return a.Status, a.Error, a.AccountID
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.pending {
		_ = a.server.Close()
	}
}
func (m *Manager) callback(a *attempt, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	m.mu.Lock()
	if r.Host != strings.TrimPrefix(strings.TrimSuffix(a.redirect, "/auth/callback"), "http://") || r.URL.Query().Get("state") != a.state || a.Status != "pending" || time.Now().After(a.expires) {
		m.mu.Unlock()
		http.Error(w, "Invalid or expired sign-in attempt.", 400)
		return
	}
	a.Status = "exchanging"
	m.mu.Unlock()
	err := m.finish(r.Context(), a, r.URL.Query())
	m.mu.Lock()
	if err != nil {
		a.Status = "failed"
		a.Error = err.Error()
	} else {
		a.Status = "complete"
	}
	m.mu.Unlock()
	// Do not echo authorization codes, OAuth error descriptions or provider bodies.
	if err != nil {
		http.Error(w, "ChatGPT sign-in failed. Return to ARTEX for details.", 400)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "ChatGPT connected. You can close this tab and return to ARTEX.")
	}
	go a.server.Shutdown(context.Background())
}

type tokenResponse struct {
	AccessToken       string `json:"access_token"`
	RefreshToken      string `json:"refresh_token"`
	IDToken           string `json:"id_token"`
	TokenType         string `json:"token_type"`
	ExpiresIn         int64  `json:"expires_in"`
	Scope             string `json:"scope"`
	EarliestRefreshAt int64  `json:"earliest_refresh_at"`
}
type oauthError struct {
	Code   string
	Status int
}

func (e *oauthError) Error() string {
	return fmt.Sprintf("ChatGPT authorization failed (%s, HTTP %d).", e.Code, e.Status)
}
func (m *Manager) token(ctx context.Context, v url.Values) (tokenResponse, error) {
	var t tokenResponse
	req, _ := http.NewRequestWithContext(ctx, "POST", m.issuer+"/api/accounts/oauth/token", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return t, errors.New("ChatGPT token service unavailable; try again later")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		if !safeCode(e.Error) {
			e.Error = "token_request_failed"
		}
		return t, &oauthError{e.Error, resp.StatusCode}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return t, errors.New("invalid ChatGPT token response")
	}
	if t.AccessToken == "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 || t.ExpiresIn > 86400 {
		return t, errors.New("incomplete ChatGPT token response")
	}
	return t, nil
}
func safeCode(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
func (m *Manager) finish(ctx context.Context, a *attempt, q url.Values) error {
	if q.Get("error") != "" {
		return errors.New("ChatGPT sign-in was declined or cancelled. Try again when ready.")
	}
	clientID := q.Get("client_id")
	if a.accountID != "" {
		if clientID != "" && clientID != a.clientID {
			return errors.New("ChatGPT registration changed unexpectedly")
		}
		clientID = a.clientID
	}
	if !strings.HasPrefix(clientID, "oaiapp_") || len(clientID) > 256 || q.Get("code") == "" {
		return errors.New("incomplete ChatGPT registration")
	}
	t, err := m.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {q.Get("code")}, "code_verifier": {a.verifier}, "redirect_uri": {a.redirect}, "resource": {Resource}})
	if err != nil {
		return err
	}
	subject, email, err := m.validateID(ctx, t.IDToken, clientID, a.nonce)
	if err != nil {
		return err
	}
	if a.subject != "" && a.subject != subject {
		return errors.New("ChatGPT account does not match the selected registration")
	}
	sum := sha256.Sum256([]byte(clientID + "\x00" + subject))
	id := hex.EncodeToString(sum[:16])
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Status != "exchanging" {
		return errors.New("ChatGPT sign-in cancelled")
	}
	err = m.store.with(ctx, func(d *diskState) (bool, error) {
		if a.accountID != "" {
			old := d.Accounts[a.accountID]
			if old == nil || old.Generation != a.generation {
				return false, errors.New("ChatGPT sign-in cancelled by sign-out")
			}
		}
		d.Accounts[id] = &Account{Generation: a.generation, ID: id, ClientID: clientID, Subject: subject, Email: email, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken, Scope: t.Scope, ExpiresAt: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second), EarliestRefreshAt: t.EarliestRefreshAt}
		return true, nil
	})
	if err == nil {
		a.AccountID = id
	}
	return err
}
func (m *Manager) validateID(ctx context.Context, raw, clientID, nonce string) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", m.issuer+"/.well-known/jwks.json", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", "", errors.New("cannot fetch OpenAI signing keys")
	}
	defer resp.Body.Close()
	var keys struct {
		Keys []struct{ Kty, Kid, Alg, Use, N, E string }
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&keys) != nil {
		return "", "", errors.New("invalid OpenAI signing keys")
	}
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		for _, k := range keys.Keys {
			if k.Kid == kid && k.Kty == "RSA" && (k.Alg == "" || k.Alg == "RS256") && (k.Use == "" || k.Use == "sig") {
				n, e1 := base64.RawURLEncoding.DecodeString(k.N)
				eb, e2 := base64.RawURLEncoding.DecodeString(k.E)
				if e1 != nil || e2 != nil || len(eb) > 4 {
					return nil, errors.New("invalid signing key")
				}
				e := 0
				for _, b := range eb {
					e = e*256 + int(b)
				}
				if e < 3 || len(n) < 256 {
					return nil, errors.New("invalid signing key")
				}
				return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}, nil
			}
		}
		return nil, errors.New("signing key not found")
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(m.issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	sub, _ := claims["sub"].(string)
	gotNonce, _ := claims["nonce"].(string)
	email, _ := claims["email"].(string)
	if err != nil || sub == "" || nonce != "" && gotNonce != nonce {
		return "", "", errors.New("ChatGPT identity validation failed")
	}
	return sub, email, nil
}
func (m *Manager) AccessToken(ctx context.Context, id string) (string, error) {
	var result string
	err := m.store.with(ctx, func(d *diskState) (bool, error) {
		a := d.Accounts[id]
		if a == nil || a.AccessToken == "" {
			return false, errors.New("Sign in to ChatGPT again in model settings")
		}
		if !hasScope(a.Scope, "chatgpt.tokens.use.direct") {
			return false, errors.New("ChatGPT plan usage is not enabled; manage access in ChatGPT settings")
		}
		changed := a.PendingIDToken != ""
		if err := m.validatePending(ctx, a); err != nil {
			return changed, err
		}
		now := time.Now()
		if now.Before(a.ExpiresAt.Add(-60*time.Second)) || now.Unix() < a.EarliestRefreshAt && now.Before(a.ExpiresAt) {
			result = a.AccessToken
			return changed, nil
		}
		if a.RefreshToken == "" {
			return changed, errors.New("ChatGPT session expired; sign in again")
		}
		t, err := m.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {a.ClientID}, "refresh_token": {a.RefreshToken}, "resource": {Resource}})
		if err != nil {
			var oe *oauthError
			if errors.As(err, &oe) {
				switch oe.Code {
				case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
					a.clear()
					return true, errors.New("ChatGPT session ended; sign in again")
				}
			}
			return changed, err
		}
		if t.RefreshToken == "" {
			a.clear()
			return true, errors.New("ChatGPT renewal did not return a replacement refresh token; sign in again")
		}
		a.PendingIDToken = t.IDToken
		a.AccessToken = t.AccessToken
		a.RefreshToken = t.RefreshToken
		a.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
		a.EarliestRefreshAt = t.EarliestRefreshAt
		if t.Scope != "" {
			a.Scope = t.Scope
		}
		if !hasScope(a.Scope, "chatgpt.tokens.use.direct") {
			return true, errors.New("ChatGPT plan usage permission was removed")
		}
		// Persist rotated material even if signing-key retrieval is unavailable.
		// Pending identity must validate before any access token can leave this store.
		if err := m.validatePending(ctx, a); err != nil {
			return true, err
		}
		result = a.AccessToken
		return true, nil
	})
	return result, err
}
func (m *Manager) validatePending(ctx context.Context, a *Account) error {
	if a.PendingIDToken == "" {
		return nil
	}
	sub, email, err := m.validateID(ctx, a.PendingIDToken, a.ClientID, "")
	if err != nil {
		return err
	}
	if sub != a.Subject {
		a.clear()
		return errors.New("ChatGPT renewal identity mismatch; sign in again")
	}
	a.IDToken, a.Email, a.PendingIDToken = a.PendingIDToken, email, ""
	return nil
}
func (m *Manager) SignOut(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	for _, a := range m.pending {
		if a.accountID == id && (a.Status == "pending" || a.Status == "exchanging") {
			a.Status = "cancelled"
			_ = a.server.Close()
		}
	}
	m.mu.Unlock()
	confirmed := false
	err := m.store.with(ctx, func(d *diskState) (bool, error) {
		a := d.Accounts[id]
		if a == nil {
			return false, errors.New("unknown ChatGPT account")
		}
		if a.RefreshToken != "" {
			req, _ := http.NewRequestWithContext(ctx, "GET", m.issuer+"/.well-known/openid-configuration", nil)
			resp, err := m.client.Do(req)
			var meta struct {
				Revocation string `json:"revocation_endpoint"`
			}
			if err == nil {
				if resp.StatusCode == 200 {
					_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta)
				}
				resp.Body.Close()
			}
			// Never forward a refresh token to another origin, even through metadata.
			if strings.HasPrefix(meta.Revocation, m.issuer+"/") {
				req, _ = http.NewRequestWithContext(ctx, "POST", meta.Revocation, strings.NewReader(url.Values{"token": {a.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {a.ClientID}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				resp, err = m.client.Do(req)
				if err == nil {
					confirmed = resp.StatusCode == 200
					resp.Body.Close()
				}
			}
		} else {
			confirmed = true
		}
		a.clear()
		a.Generation++
		return true, nil
	})
	return confirmed, err
}
