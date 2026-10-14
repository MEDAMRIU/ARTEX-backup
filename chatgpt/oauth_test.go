package chatgpt

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/golang-jwt/jwt/v5"
)

type fixture struct {
	m            *Manager
	server       *httptest.Server
	key          *rsa.PrivateKey
	nonce        string
	clientID     string
	subject      string
	grant        string
	refreshError string
	jwksFail     atomic.Bool
	refreshes    atomic.Int32
	requests     atomic.Int32
	tokenForm    url.Values
	inference    func(http.ResponseWriter, *http.Request)
}

func setup(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, clientID: "oaiapp_testclient", subject: "user-123", grant: scopes}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			if f.jwksFail.Load() {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "testkey", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"revocation_endpoint": f.server.URL + "/revoke"})
		case "/revoke":
			r.ParseForm()
			if r.Form.Get("token_type_hint") != "refresh_token" || r.Form.Get("client_id") != f.clientID {
				t.Error("bad revocation request")
			}
			w.WriteHeader(200)
		case "/api/accounts/oauth/token":
			r.ParseForm()
			f.tokenForm = r.Form
			access, refresh := "access-initial", "refresh-initial"
			if r.Form.Get("grant_type") == "refresh_token" {
				n := f.refreshes.Add(1)
				if f.refreshError != "" {
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]string{"error": f.refreshError})
					return
				}
				access = fmt.Sprint("access-", n)
				refresh = fmt.Sprint("refresh-", n)
			}
			if r.Form.Get("client_id") != f.clientID || r.Form.Get("resource") != Resource {
				t.Errorf("wrong token exchange identity/resource: %v", r.Form)
			}
			raw := f.signed(t, jwt.MapClaims{"iss": f.server.URL, "aud": f.clientID, "sub": f.subject, "email": "probe@example.test", "nonce": f.nonce, "exp": time.Now().Add(time.Hour).Unix()})
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: refresh, IDToken: raw, TokenType: "Bearer", ExpiresIn: 3600, Scope: f.grant})
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"models": []Model{{Slug: "gpt-6-sol", DisplayName: "GPT-6 Sol", Visibility: "list"}, {Slug: "hidden", Visibility: "hide"}}})
		case "/v1/responses":
			f.requests.Add(1)
			if f.inference != nil {
				f.inference(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	m, err := New(t.TempDir(), []byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	m.issuer = f.server.URL
	m.api = f.server.URL + "/v1"
	f.m = m
	t.Cleanup(m.Close)
	t.Cleanup(f.server.Close)
	return f
}
func (f *fixture) signed(t *testing.T, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "testkey"
	s, e := tok.SignedString(f.key)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *fixture) start(t *testing.T, id string) (Login, url.Values) {
	t.Helper()
	login, err := f.m.Begin(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.URL)
	f.nonce = u.Query().Get("nonce")
	return login, u.Query()
}
func (f *fixture) callback(t *testing.T, login Login, q url.Values, changes url.Values) int {
	t.Helper()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	params := url.Values{"code": {"test-code"}, "state": {q.Get("state")}, "client_id": {f.clientID}}
	for k, v := range changes {
		params[k] = v
	}
	cb.RawQuery = params.Encode()
	resp, e := http.Get(cb.String())
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
func (f *fixture) login(t *testing.T) string {
	t.Helper()
	l, q := f.start(t, "")
	if code := f.callback(t, l, q, nil); code != 200 {
		_, msg, _ := f.m.LoginStatus(l.ID)
		t.Fatalf("callback %d: %s", code, msg)
	}
	_, _, id := f.m.LoginStatus(l.ID)
	return id
}
func (f *fixture) expire(t *testing.T, id string) {
	t.Helper()
	if e := f.m.store.with(context.Background(), func(d *diskState) (bool, error) {
		d.Accounts[id].ExpiresAt = time.Now().Add(-time.Minute)
		return true, nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestSignInPersistenceAndPKCE(t *testing.T) {
	f := setup(t)
	l, q := f.start(t, "")
	if q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "ARTEX" || !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") {
		t.Fatal(q)
	}
	if code := f.callback(t, l, q, nil); code != 200 {
		t.Fatal(code)
	}
	sum := sha256.Sum256([]byte(f.tokenForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") || f.tokenForm.Get("redirect_uri") != q.Get("redirect_uri") {
		t.Fatal("PKCE/callback mismatch")
	}
	_, _, id := f.m.LoginStatus(l.ID)
	as, err := f.m.Accounts(context.Background())
	if err != nil || len(as) != 1 || !as[0].Connected {
		t.Fatal(as, err)
	}
	b, _ := json.Marshal(as)
	if strings.Contains(string(b), "access-initial") || strings.Contains(string(b), "refresh-initial") {
		t.Fatal("tokens exposed")
	}
	b, err = os.ReadFile(filepath.Join(f.m.store.dir, "credentials.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "access-initial") || strings.Contains(string(b), "probe@") {
		t.Fatal("plaintext at rest")
	}
	st, _ := os.Stat(filepath.Join(f.m.store.dir, "credentials.enc"))
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	m2, err := New(f.m.store.dir, []byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	m2.issuer = f.server.URL
	got, err := m2.AccessToken(context.Background(), id)
	if got != "access-initial" || err != nil {
		t.Fatal(got, err)
	}
	l2, q2 := f.start(t, id)
	if q2.Get("client_id") != f.clientID || q2.Get("agent_name_hint") != "" || q2.Get("ext_agent_host_id") != q.Get("ext_agent_host_id") {
		t.Fatal("registration/host not reused")
	}
	if f.callback(t, l2, q2, url.Values{"client_id": {""}}) != 200 {
		t.Fatal("returning callback")
	}
}
func TestCallbackRejectsStateAndConsent(t *testing.T) {
	f := setup(t)
	l, q := f.start(t, "")
	if f.callback(t, l, q, url.Values{"state": {"wrong"}}) != 400 {
		t.Fatal("state accepted")
	}
	if f.callback(t, l, q, url.Values{"error": {"access_denied"}}) != 400 {
		t.Fatal("denial accepted")
	}
	as, _ := f.m.Accounts(context.Background())
	if len(as) != 0 || f.tokenForm != nil {
		t.Fatal("denied flow exchanged/stored credentials")
	}
}
func TestIDTokenValidation(t *testing.T) {
	f := setup(t)
	for _, bad := range []string{"issuer", "audience", "nonce", "expiry", "signature", "subject"} {
		t.Run(bad, func(t *testing.T) {
			c := jwt.MapClaims{"iss": f.server.URL, "aud": f.clientID, "sub": "user", "nonce": "nonce", "exp": time.Now().Add(time.Hour).Unix()}
			switch bad {
			case "issuer":
				c["iss"] = "https://wrong.test"
			case "audience":
				c["aud"] = "wrong"
			case "nonce":
				c["nonce"] = "wrong"
			case "expiry":
				c["exp"] = time.Now().Add(-time.Hour).Unix()
			case "subject":
				delete(c, "sub")
			}
			raw := f.signed(t, c)
			if bad == "signature" {
				raw = raw[:len(raw)-8] + "AAAAAAAA"
			}
			if _, _, err := f.m.validateID(context.Background(), raw, f.clientID, "nonce"); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
func TestRefreshSerializedAndRotated(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	f.expire(t, id)
	m2, _ := New(f.m.store.dir, []byte(strings.Repeat("a", 32)))
	m2.issuer = f.server.URL
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := f.m
			if i%2 == 0 {
				m = m2
			}
			tok, err := m.AccessToken(context.Background(), id)
			if err != nil || tok != "access-1" {
				t.Errorf("refresh: %q %v", tok, err)
			}
		}(i)
	}
	wg.Wait()
	if f.refreshes.Load() != 1 {
		t.Fatal(f.refreshes.Load())
	}
	_ = f.m.store.with(context.Background(), func(d *diskState) (bool, error) {
		if d.Accounts[id].RefreshToken != "refresh-1" {
			t.Error("rotated token not saved")
		}
		return false, nil
	})
	if f.tokenForm.Get("scope") != "" {
		t.Fatal("refresh scope must be omitted")
	}
}
func TestRefreshTerminalAndTemporaryErrors(t *testing.T) {
	for _, code := range []string{"invalid_grant", "server_error"} {
		t.Run(code, func(t *testing.T) {
			f := setup(t)
			id := f.login(t)
			f.expire(t, id)
			f.refreshError = code
			if _, err := f.m.AccessToken(context.Background(), id); err == nil {
				t.Fatal("expected failure")
			}
			as, _ := f.m.Accounts(context.Background())
			if as[0].Connected != (code == "server_error") {
				t.Fatal("wrong credential cleanup")
			}
		})
	}
}
func TestScopesAndSignOut(t *testing.T) {
	f := setup(t)
	f.grant = "openid email"
	id := f.login(t)
	if _, e := f.m.AccessToken(context.Background(), id); e == nil {
		t.Fatal("missing plan scope accepted")
	}
	confirmed, err := f.m.SignOut(context.Background(), id)
	if err != nil || !confirmed {
		t.Fatal(confirmed, err)
	}
	as, _ := f.m.Accounts(context.Background())
	if as[0].Connected || as[0].ClientID != f.clientID {
		t.Fatal("sign-out must clear credentials and retain registration")
	}
}
func TestReturningIdentityMismatch(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	l, q := f.start(t, id)
	f.subject = "other-user"
	if f.callback(t, l, q, nil) != 400 {
		t.Fatal("swapped subject accepted")
	}
	as, _ := f.m.Accounts(context.Background())
	if len(as) != 1 || !as[0].Connected {
		t.Fatal("original credentials lost")
	}
}
func TestPlanRequestAndStreamingComplete(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	models, err := f.m.Models(context.Background(), id)
	if err != nil || len(models) != 1 || models[0].Slug != "gpt-6-sol" {
		t.Fatal(models, err)
	}
	f.inference = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-initial" {
			t.Error("missing managed token")
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["stream"] != true || b["store"] != false || b["max_output_tokens"] != nil || b["temperature"] != nil {
			t.Error(b)
		}
		tools := b["tools"].([]any)
		if tools[0].(map[string]any)["type"] != "namespace" {
			t.Error("tools not namespaced")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_item.added\ndata: {\"item\":{\"type\":\"function_call\",\"call_id\":\"call1\",\"name\":\"read_file\",\"namespace\":\"artex\"}}\n\nevent: response.function_call_arguments.delta\ndata: {\"delta\":\"{}\"}\n\nevent: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n")
	}
	client := &http.Client{Transport: f.m.Transport(id, http.DefaultTransport)}
	p, err := llm.NewProvider(llm.Config{Format: llm.FormatOpenAIResponses, Model: "gpt-6-sol", APIKey: "placeholder", BaseURL: f.m.api, HTTPClient: client, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	p = Streaming(p)
	temp := 0.2
	msg, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 8192, Temperature: &temp, Tools: []llm.ToolSchema{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}}})
	if err != nil || len(msg.Content) != 1 || msg.Content[0].Name != "read_file" || msg.Content[0].ID != "call1" {
		t.Fatal(msg, err)
	}
}
func TestStreamsRequireCompletion(t *testing.T) {
	for _, event := range []string{"", "event: response.incomplete\ndata: {}\n\n", "event: response.failed\ndata: {\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", "data: not-json\n\n"} {
		t.Run(fmt.Sprint(len(event)), func(t *testing.T) {
			source := io.NopCloser(strings.NewReader("event: response.output_text.delta\ndata: {\"delta\":\"partial\"}\n\n" + event))
			s := &checkedStream{source: source, reader: bufio.NewReader(source)}
			_, err := io.ReadAll(s)
			if err == nil {
				t.Fatal("false success without completion")
			}
		})
	}
}
func TestCredentialsNeverForwardedElsewhere(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	req, _ := http.NewRequest("POST", "https://attacker.invalid/responses", strings.NewReader(`{}`))
	if _, err := f.m.Transport(id, http.DefaultTransport).RoundTrip(req); err == nil {
		t.Fatal("credential destination not confined")
	}
}

func TestRefreshRetainsRotationAcrossJWKSFailure(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	f.expire(t, id)
	f.jwksFail.Store(true)
	if token, err := f.m.AccessToken(context.Background(), id); err == nil || token != "" {
		t.Fatal("unverified token escaped")
	}
	_ = f.m.store.with(context.Background(), func(d *diskState) (bool, error) {
		a := d.Accounts[id]
		if a.RefreshToken != "refresh-1" || a.PendingIDToken == "" {
			t.Fatal("rotation was lost")
		}
		return false, nil
	})
	f.jwksFail.Store(false)
	m2, _ := New(f.m.store.dir, []byte(strings.Repeat("a", 32)))
	m2.issuer = f.server.URL
	token, err := m2.AccessToken(context.Background(), id)
	if err != nil || token != "access-1" || f.refreshes.Load() != 1 {
		t.Fatal(token, err, f.refreshes.Load())
	}
}

func TestCrossManagerSignOutCancelsReturningLogin(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	l, q := f.start(t, id)
	m2, _ := New(f.m.store.dir, []byte(strings.Repeat("a", 32)))
	m2.issuer = f.server.URL
	if _, err := m2.SignOut(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.callback(t, l, q, nil) != 400 {
		t.Fatal("sign-in restored signed-out account")
	}
	accounts, _ := f.m.Accounts(context.Background())
	if accounts[0].Connected {
		t.Fatal("sign-out did not persist")
	}
}

func TestPlanStreamWithoutContentType(t *testing.T) {
	f := setup(t)
	id := f.login(t)
	f.inference = func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n")
	}
	req, _ := http.NewRequest("POST", f.m.api+"/responses", strings.NewReader(`{"model":"gpt-6-sol","input":[]}`))
	client := &http.Client{Transport: f.m.Transport(id, http.DefaultTransport)}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "OK") {
		t.Fatal(string(body), err)
	}
	// Missing metadata must not make a JSON body or interrupted stream succeed.
	f.inference = func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		fmt.Fprint(w, `{"output":"not an event stream"}`)
	}
	req, _ = http.NewRequest("POST", f.m.api+"/responses", strings.NewReader(`{"model":"gpt-6-sol","input":[]}`))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err = io.ReadAll(resp.Body); err == nil {
		t.Fatal("non-SSE body accepted")
	}
}
