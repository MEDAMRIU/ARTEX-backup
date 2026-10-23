package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func TestWebFetchReportsBodyInterruptedAtDeadline(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "partial evidence")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer target.Close()
	p := policyFor(t, target.URL, 2)
	p.ExpiresAt = time.Now().Add(400 * time.Millisecond)
	b, err := NewBroker(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, b.caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("CURL_CA_BUNDLE", ca)
	input := fmt.Sprintf(`{"url":"http://approved.test:%d/partial"}`, p.Targets[0].HTTPPorts[0])
	result, err := newStrictWebFetch().Call(context.Background(), []byte(input), &actool.ToolContext{WorkingDir: t.TempDir()})
	if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "INCOMPLETE RESPONSE") || !strings.Contains(result.Flatten(), "partial evidence") {
		t.Fatalf("partial body misrepresented: %v %+v", err, result)
	}
}

func TestBrokerInFlightHTTPStopsAtPolicyDeadline(t *testing.T) {
	closed := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(closed)
	}))
	defer target.Close()
	p := policyFor(t, target.URL, 2)
	p.ExpiresAt = time.Now().Add(400 * time.Millisecond)
	b, err := NewBroker(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	proxy := httptest.NewServer(b.ProxyHandler())
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	res, err := client.Get(fmt.Sprintf("http://approved.test:%d/slow", p.Targets[0].HTTPPorts[0]))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 502 {
		t.Fatalf("slow target unexpectedly completed: %d", res.StatusCode)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream request survived policy deadline")
	}
	if time.Now().After(p.ExpiresAt.Add(500 * time.Millisecond)) {
		t.Fatal("HTTP exceeded cutoff tolerance")
	}
}

func TestBrokerInFlightTCPStopsAtPolicyDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Read(make([]byte, 1))
		close(closed)
	}()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	p := policyFor(t, "http://localhost:80", 2)
	p.ExpiresAt = time.Now().Add(400 * time.Millisecond)
	p.Targets[0].TCPPorts = []int{port}
	b, err := NewBroker(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	server := httptest.NewServer(b.APIHandler())
	defer server.Close()
	body := fmt.Sprintf(`{"host":"approved.test","port":%d,"read_bytes":1,"timeout_ms":5000}`, port)
	res, err := http.Post(server.URL+"/v1/tcp-probe", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["connected"] != true || result["read_error"] == nil {
		t.Fatalf("unexpected probe outcome: %+v", result)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("TCP connection survived policy deadline")
	}
	if time.Now().After(p.ExpiresAt.Add(500 * time.Millisecond)) {
		t.Fatal("TCP exceeded cutoff tolerance")
	}
}
