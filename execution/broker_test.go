package execution

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	actool "github.com/Autumn-27/norma/tool"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func policyFor(t *testing.T, target string, budget int64) Policy {
	t.Helper()
	u, e := url.Parse(target)
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	return Policy{TaskID: "1", ExpiresAt: time.Now().Add(time.Minute), HTTPRequests: budget, TCPConnections: 1, Targets: []Target{{Host: "approved.test", IPs: []string{"127.0.0.1"}, HTTPPorts: []int{port}}}}
}
func TestMeterConcurrentDurableAndImmutable(t *testing.T) {
	p := policyFor(t, "http://localhost:80", 7)
	path := filepath.Join(t.TempDir(), "meter.db")
	m, e := OpenMeter(path, p)
	if e != nil {
		t.Fatal(e)
	}
	var passed atomic.Int64
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Reserve(context.Background(), "http") == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 7 {
		t.Fatalf("reserved %d", passed.Load())
	}
	m.Close()
	m, e = OpenMeter(path, p)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	if m.Reserve(context.Background(), "http") == nil {
		t.Fatal("budget reset after restart")
	}
	p.HTTPRequests = 8
	if other, e := OpenMeter(path, p); e == nil {
		other.Close()
		t.Fatal("policy change accepted")
	}
}
func TestMeterExpiryAndSharedPacing(t *testing.T) {
	p := policyFor(t, "http://localhost:80", 3)
	p.MinIntervalMS = 70
	m, e := OpenMeter(filepath.Join(t.TempDir(), "meter.db"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	if e = m.Reserve(context.Background(), "http"); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	if e = m.Reserve(context.Background(), "tcp"); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) < 60*time.Millisecond {
		t.Fatal("HTTP/TCP pacing not shared")
	}
	m.policy.ExpiresAt = time.Now().Add(-time.Second)
	if m.Reserve(context.Background(), "http") == nil {
		t.Fatal("expired policy accepted")
	}
}
func TestBrokerHTTPBudgetRedirectHostAndPinnedIP(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://outside-scope.invalid/", 302)
			return
		}
		fmt.Fprint(w, "fixture42")
	}))
	defer target.Close()
	p := policyFor(t, target.URL, 2)
	b, e := NewBroker(p, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	root := fmt.Sprintf("http://approved.test:%d", p.Targets[0].HTTPPorts[0])
	for _, step := range []struct {
		u      string
		status int
	}{{root + "/", 200}, {root + "/redirect", 403}, {root + "/", 429}, {"http://127.0.0.1:1/", 403}} {
		r, e := client.Get(step.u)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != step.status {
			t.Fatalf("%s: %d %s", step.u, r.StatusCode, body)
		}
	}
	if reached.Load() != 2 {
		t.Fatalf("upstream count=%d", reached.Load())
	}
	req, _ := http.NewRequest("GET", root, nil)
	req.Host = "other.test"
	r, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("Host spoof accepted")
	}
}
func TestBrokerHTTPSCountsEveryRequestNoOpaqueTunnel(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); fmt.Fprint(w, "tls42") }))
	defer target.Close()
	p := policyFor(t, target.URL, 2)
	p.InsecureUpstreamTLS = true
	b, e := NewBroker(p, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(b.caPEM)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	root := fmt.Sprintf("https://approved.test:%d", p.Targets[0].HTTPPorts[0])
	for _, want := range []int{200, 200, 429} {
		r, e := client.Get(root)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("got %d want %d", r.StatusCode, want)
		}
	}
	if reached.Load() != 2 {
		t.Fatal("TLS budget bypass")
	}
	conn, e := net.DialTimeout("tcp", pu.Host, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprintf(conn, "CONNECT approved.test:%d HTTP/1.1\r\nHost: approved.test:%d\r\n\r\n", p.Targets[0].HTTPPorts[0], p.Targets[0].HTTPPorts[0])
	buf := make([]byte, 128)
	conn.Read(buf)
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: approved.test\r\n\r\n"))
	conn.Read(buf)
	if reached.Load() != 2 {
		t.Fatal("plaintext tunnel reached target")
	}
}
func TestBrokerTCPPortSeparationAndBudget(t *testing.T) {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e == nil {
			defer c.Close()
			c.Write([]byte("SSH-fixture\r\n"))
		}
	}()
	p := policyFor(t, "http://localhost:80", 1)
	p.Targets[0].TCPPorts = []int{ln.Addr().(*net.TCPAddr).Port}
	b, e := NewBroker(p, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	api := httptest.NewServer(b.APIHandler())
	defer api.Close()
	for _, step := range []struct{ port, status int }{{80, 403}, {p.Targets[0].TCPPorts[0], 200}, {p.Targets[0].TCPPorts[0], 429}} {
		r, e := http.Post(api.URL+"/v1/tcp-probe", "application/json", strings.NewReader(fmt.Sprintf(`{"host":"approved.test","port":%d,"read_bytes":32}`, step.port)))
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != step.status {
			t.Fatalf("%d %s", r.StatusCode, raw)
		}
	}
}

func TestScopeRejectsPhysicalEndpointAliases(t *testing.T) {
	p := policyFor(t, "http://localhost:80", 1)
	p.Targets = append(p.Targets, Target{Host: "alias.test", IPs: []string{"::ffff:127.0.0.1"}, TCPPorts: []int{80}})
	if p.Validate() == nil {
		t.Fatal("HTTP budget bypass through TCP alias accepted")
	}
}
func TestReadinessRequiresCompleteChecks(t *testing.T) {
	r := Readiness{}
	r.Summarize()
	if r.Ready {
		t.Fatal("empty checks accepted")
	}
	r.Checks = []Check{{Name: "shell_and_dependencies", Status: "passed", Required: true}}
	r.ValidateRequired()
	if r.Ready {
		t.Fatal("partial checks accepted")
	}
}

func TestClientRejectsInvalidReadinessAndClearsCachedSuccess(t *testing.T) {
	valid := Readiness{Mode: "isolated", TaskID: "1", Ready: true, CheckedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	for _, name := range []string{"filesystem_and_process_isolation", "shell_and_dependencies", "direct_egress_denied", "broker_scope_and_budget", "chromium_sandbox", "browser_cli", "browser_mcp"} {
		valid.Checks = append(valid.Checks, Check{Name: name, Required: true, Status: "passed"})
	}
	for _, kind := range []string{"wrong_task", "missing_checks", "missing_dates", "future_check", "expired", "wrong_mode"} {
		t.Run(kind, func(t *testing.T) {
			response := valid
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeRuntimeJSON(w, response) }))
			defer srv.Close()
			c := &Client{TaskID: "1", BaseURL: srv.URL, HTTP: srv.Client()}
			if r, e := c.Readiness(context.Background(), true); e != nil || !r.Ready || !c.ReadyFor("1") {
				t.Fatalf("valid response rejected: %v", e)
			}
			switch kind {
			case "wrong_task":
				response.TaskID = "2"
			case "missing_checks":
				response.Checks = nil
			case "missing_dates":
				response.CheckedAt = time.Time{}
			case "future_check":
				response.CheckedAt = time.Now().Add(2 * time.Minute)
			case "expired":
				response.ExpiresAt = time.Now().Add(-time.Minute)
			case "wrong_mode":
				response.Mode = "local"
			}
			if r, _ := c.Readiness(context.Background(), true); r.Ready || c.ReadyFor("1") {
				t.Fatal("invalid response retained cached readiness")
			}
		})
	}
}

type failingAudit struct{}

func (failingAudit) Write([]byte) (int, error) { return 0, fmt.Errorf("disk full") }
func TestBrokerAuditFailurePreventsOutbound(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	p := policyFor(t, target.URL, 2)
	b, e := NewBroker(p, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if c, ok := b.audit.(io.Closer); ok {
		c.Close()
	}
	b.audit = failingAudit{}
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	defer client.CloseIdleConnections()
	r, e := client.Get(fmt.Sprintf("http://approved.test:%d/", p.Targets[0].HTTPPorts[0]))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 503 || reached.Load() != 0 {
		t.Fatal("probe ran without audit evidence")
	}
}
func TestBrokerMarksChunkedTruncation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		for range 33 {
			w.Write(make([]byte, 1<<20))
		}
	}))
	defer target.Close()
	p := policyFor(t, target.URL, 1)
	b, e := NewBroker(p, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	defer client.CloseIdleConnections()
	r, e := client.Get(fmt.Sprintf("http://approved.test:%d/", p.Targets[0].HTTPPorts[0]))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	n, _ := io.Copy(io.Discard, r.Body)
	if r.Header.Get("X-Artex-Truncated") == "" || n != 32<<20 {
		t.Fatalf("silent truncation header=%q size=%d", r.Header.Get("X-Artex-Truncated"), n)
	}
}

func TestDefaultClientFailsClosed(t *testing.T) {
	t.Setenv("ARTEX_EXECUTION_MODE", "")
	c, e := NewClientFromEnv(t.TempDir())
	if e != nil || c == nil {
		t.Fatalf("no default gate: %v", e)
	}
	r, e := c.Readiness(context.Background(), true)
	if e != nil || r.Ready || r.Mode != "unconfigured" {
		t.Fatal("default ready")
	}
	result, e := c.Call(context.Background(), "Bash", []byte(`{}`), nil)
	if e != nil || !result.IsError {
		t.Fatal("unconfigured tool allowed")
	}
	t.Setenv("ARTEX_EXECUTION_MODE", "typo")
	if _, e = NewClientFromEnv(t.TempDir()); e == nil {
		t.Fatal("invalid mode silently allowed")
	}
	t.Setenv("ARTEX_EXECUTION_MODE", "local")
	if c, e = NewClientFromEnv(t.TempDir()); e != nil || c != nil {
		t.Fatal("explicit trusted-local compatibility failed")
	}
}

func TestRuntimeErrorReplyAndRestartBarrier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRuntimeJSON(w, Reply{Error: "runtime unavailable"})
	}))
	defer srv.Close()
	workspace := t.TempDir()
	saved := ""
	c := &Client{TaskID: "1", Workspace: workspace, BaseURL: srv.URL, HTTP: srv.Client(), OnFailure: func(s string) { saved = s }}
	result, e := c.Call(context.Background(), "Bash", []byte(`{}`), &actool.ToolContext{WorkingDir: workspace})
	if e != nil || !result.IsError || !c.Blocked() || saved == "" {
		t.Fatal("runtime error reply did not block completion")
	}
	fresh := &Client{ExternalBlocked: func() bool { return saved != "" }}
	if !fresh.Blocked() {
		t.Fatal("persisted barrier lost after restart")
	}
	saved = ""
	if fresh.Blocked() {
		t.Fatal("cleared persisted barrier remains")
	}
}
