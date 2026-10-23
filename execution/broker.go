package execution

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Broker struct {
	Policy  Policy
	Meter   *Meter
	ca      *x509.Certificate
	key     *ecdsa.PrivateKey
	caPEM   []byte
	audit   io.Writer
	auditMu sync.Mutex
	slots   chan struct{}
}

func NewBroker(policy Policy, state string) (*Broker, error) {
	if err := os.MkdirAll(state, 0700); err != nil {
		return nil, err
	}
	meter, err := OpenMeter(filepath.Join(state, "budget.sqlite"), policy)
	if err != nil {
		return nil, err
	}
	b := &Broker{Policy: policy, Meter: meter, slots: make(chan struct{}, 4)}
	if err = b.loadCA(state); err != nil {
		meter.Close()
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(state, "egress.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		meter.Close()
		return nil, err
	}
	b.audit = log
	return b, nil
}
func (b *Broker) Close() {
	b.Meter.Close()
	if c, ok := b.audit.(io.Closer); ok {
		c.Close()
	}
}
func (b *Broker) loadCA(state string) error {
	certPath, keyPath := filepath.Join(state, "ca.pem"), filepath.Join(state, "ca-key.pem")
	certBytes, certErr := os.ReadFile(certPath)
	keyBytes, keyErr := os.ReadFile(keyPath)
	if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
		// The browser's NSS trust store supports P-256 certificate keys. Ed25519
		// certificates are rejected by the NSS version in the pinned runtime.
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return err
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ARTEX task egress CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
		if err != nil {
			return err
		}
		priv, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		certBytes = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyBytes = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})
		if err = os.WriteFile(keyPath, keyBytes, 0600); err != nil {
			return err
		}
		if err = os.WriteFile(certPath, certBytes, 0644); err != nil {
			return err
		}
	} else if certErr != nil || keyErr != nil {
		return fmt.Errorf("incomplete broker CA: certificate=%v key=%v", certErr, keyErr)
	}
	certBlock, _ := pem.Decode(certBytes)
	keyBlock, _ := pem.Decode(keyBytes)
	if certBlock == nil || keyBlock == nil {
		return errors.New("invalid broker CA PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return err
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	private, ok := key.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return errors.New("broker CA requires an ECDSA P-256 key; provision a new task state for legacy CAs")
	}
	if _, err := tls.X509KeyPair(certBytes, keyBytes); err != nil {
		return fmt.Errorf("broker CA certificate/key mismatch: %w", err)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("broker certificate is not a signing CA")
	}
	b.ca, b.key, b.caPEM = cert, private, certBytes
	return nil
}
func (b *Broker) certificate(host string) (tls.Certificate, error) {
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return tls.Certificate{}, e
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		cert.IPAddresses = []net.IP{ip}
	} else {
		cert.DNSNames = []string{host}
	}
	der, e := x509.CreateCertificate(rand.Reader, cert, b.ca, b.key.Public(), b.key)
	return tls.Certificate{Certificate: [][]byte{der, b.ca.Raw}, PrivateKey: b.key}, e
}
func (b *Broker) log(entry map[string]any) error {
	entry["at"] = time.Now().UTC()
	entry["task_id"] = b.Policy.TaskID
	b.auditMu.Lock()
	defer b.auditMu.Unlock()
	if err := json.NewEncoder(b.audit).Encode(entry); err != nil {
		return err
	}
	if f, ok := b.audit.(*os.File); ok {
		return f.Sync()
	}
	return nil
}

func (b *Broker) ProxyHandler() http.Handler {
	return b.bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			b.connect(w, r)
			return
		}
		if !r.URL.IsAbs() || r.URL.Scheme != "http" {
			http.Error(w, "absolute HTTP proxy URL required", 400)
			return
		}
		b.forward(w, r, "")
	}))
}
func authority(u *url.URL) (string, error) {
	if u.User != nil || u.Hostname() == "" || u.Fragment != "" {
		return "", errors.New("invalid destination URL")
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", errors.New("unsupported URL scheme")
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}
func (b *Broker) forward(w http.ResponseWriter, r *http.Request, tunnelAuthority string) {
	if r.Method == http.MethodConnect || r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "opaque tunnels and protocol upgrades are disabled", 403)
		return
	}
	dest, err := authority(r.URL)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if tunnelAuthority != "" && dest != tunnelAuthority {
		http.Error(w, "CONNECT authority mismatch", 403)
		return
	}
	address, hostname, err := b.Policy.Resolve(dest, "http")
	if err != nil {
		b.log(map[string]any{"kind": "http", "destination": dest, "layer": "policy", "error": err.Error()})
		http.Error(w, err.Error(), 403)
		return
	}
	if r.Host != "" {
		hostURL := &url.URL{Scheme: r.URL.Scheme, Host: r.Host}
		h, e := authority(hostURL)
		if e != nil || h != dest {
			http.Error(w, "Host header does not match approved destination", 403)
			return
		}
	}
	if err = b.Meter.Reserve(r.Context(), "http"); err != nil {
		b.log(map[string]any{"kind": "http", "destination": dest, "layer": "budget", "error": err.Error()})
		http.Error(w, err.Error(), 429)
		return
	}
	if e := b.log(map[string]any{"kind": "http", "destination": dest, "path": r.URL.Path, "phase": "reserved", "method": r.Method}); e != nil {
		http.Error(w, "evidence log unavailable", 503)
		return
	}
	ctx, cancel := b.operationContext(r.Context(), 30*time.Second)
	defer cancel()
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	out.Host = dest
	stripHop(out.Header)
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	out.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname, InsecureSkipVerify: b.Policy.InsecureUpstreamTLS}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	res, err := transport.RoundTrip(out)
	if err != nil {
		b.log(map[string]any{"kind": "http", "destination": dest, "path": r.URL.Path, "layer": "upstream", "error": err.Error()})
		http.Error(w, "upstream request failed: "+err.Error(), 502)
		return
	}
	defer res.Body.Close()
	// Read one extra byte so unknown-length/chunked replies cannot be silently
	// represented as complete evidence. Buffering is bounded per request.
	body, readErr := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	truncated := len(body) > 32<<20
	if truncated {
		body = body[:32<<20]
	}
	stripHop(res.Header)
	for k, v := range res.Header {
		for _, x := range v {
			w.Header().Add(k, x)
		}
	}
	w.Header().Del("Content-Length")
	w.Header().Set("X-Artex-Egress", "policy-enforced")
	if truncated {
		w.Header().Set("X-Artex-Truncated", "response exceeds 32 MiB")
	}
	if readErr != nil {
		w.Header().Set("X-Artex-Incomplete", "upstream body read failed")
	}
	w.WriteHeader(res.StatusCode)
	n, copyErr := w.Write(body)
	entry := map[string]any{"kind": "http", "destination": dest, "method": r.Method, "path": r.URL.Path, "layer": "http", "status": res.StatusCode, "response_bytes": n, "truncated": truncated}
	if readErr != nil {
		entry["read_error"] = readErr.Error()
	}
	if copyErr != nil {
		entry["error"] = copyErr.Error()
	}
	b.log(entry)
}
func stripHop(h http.Header) {
	for _, key := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(key))
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(key)
	}
}

// CONNECT is terminated here as TLS. There is deliberately no transparent relay
// fallback, so every decrypted HTTP request passes through the same policy/meter.
func (b *Broker) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "explicit CONNECT host:port required", 400)
		return
	}
	dest := net.JoinHostPort(strings.ToLower(host), port)
	if _, _, err = b.Policy.Resolve(dest, "http"); err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if !time.Now().Before(b.Policy.ExpiresAt) {
		http.Error(w, "task time budget exhausted", 429)
		return
	}
	cert, err := b.certificate(host)
	if err != nil {
		http.Error(w, "certificate unavailable", 500)
		return
	}
	hijack, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "TLS termination unavailable", 500)
		return
	}
	raw, buffered, err := hijack.Hijack()
	if err != nil {
		return
	}
	defer raw.Close()
	if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffered.Flush(); err != nil {
		return
	}
	conn := net.Conn(raw)
	if buffered.Reader.Buffered() > 0 {
		conn = &readBufferedConn{Conn: raw, reader: buffered.Reader}
	}
	_ = conn.SetDeadline(minDeadline(time.Now().Add(2*time.Minute), b.Policy.ExpiresAt))
	secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if hello.ServerName != "" && !strings.EqualFold(hello.ServerName, host) {
			return nil, errors.New("SNI does not match CONNECT authority")
		}
		return nil, nil
	}})
	ctx, cancel := b.operationContext(r.Context(), 10*time.Second)
	err = secured.HandshakeContext(ctx)
	cancel()
	if err != nil {
		b.log(map[string]any{"kind": "connect", "destination": dest, "layer": "tls", "error": err.Error()})
		return
	}
	listener := &singleConnListener{conn: secured, done: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, inner *http.Request) {
		inner.URL.Scheme = "https"
		inner.URL.Host = inner.Host
		b.forward(w, inner, dest)
	}), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10}
	_ = server.Serve(listener)
}

type readBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *readBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type singleConnListener struct {
	conn net.Conn
	mu   sync.Mutex
	used bool
	done chan struct{}
	once sync.Once
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.used {
		l.used = true
		l.mu.Unlock()
		return &notifyCloseConn{Conn: l.conn, close: func() { l.once.Do(func() { close(l.done) }) }}, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.conn.Close()
}
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyCloseConn struct {
	net.Conn
	close func()
}

func (c *notifyCloseConn) Close() error { e := c.Conn.Close(); c.close(); return e }

type TCPProbe struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	PayloadBase64 string `json:"payload_base64,omitempty"`
	ReadBytes     int    `json:"read_bytes,omitempty"`
	TimeoutMS     int    `json:"timeout_ms,omitempty"`
}

func (b *Broker) APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s, e := b.Meter.Status()
		if e != nil {
			http.Error(w, "budget store unavailable", 503)
			return
		}
		writeRuntimeJSON(w, s)
	})
	mux.HandleFunc("GET /ca.pem", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write(b.caPEM)
	})
	mux.HandleFunc("POST /v1/tcp-probe", func(w http.ResponseWriter, r *http.Request) {
		var req TCPProbe
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); e != nil {
			http.Error(w, "invalid probe", 400)
			return
		}
		dest := net.JoinHostPort(req.Host, strconv.Itoa(req.Port))
		address, _, err := b.Policy.Resolve(dest, "tcp")
		if err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		payload, err := base64.StdEncoding.DecodeString(req.PayloadBase64)
		if err != nil || len(payload) > 8192 || req.ReadBytes < 0 || req.ReadBytes > 65536 || req.TimeoutMS < 0 || req.TimeoutMS > 5000 {
			http.Error(w, "probe exceeds bounded operation limits", 400)
			return
		}
		if req.TimeoutMS == 0 {
			req.TimeoutMS = 3000
		}
		if err = b.Meter.Reserve(r.Context(), "tcp"); err != nil {
			http.Error(w, err.Error(), 429)
			return
		}
		if e := b.log(map[string]any{"kind": "tcp", "destination": dest, "phase": "reserved"}); e != nil {
			http.Error(w, "evidence log unavailable", 503)
			return
		}
		started := time.Now().UTC()
		ctx, cancel := b.operationContext(r.Context(), time.Duration(req.TimeoutMS)*time.Millisecond)
		defer cancel()
		result := map[string]any{"started_at": started, "destination": dest, "layer": "tcp", "connected": false}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err != nil {
			result["error"] = err.Error()
			result["response_bytes"] = nil
			b.log(result)
			writeRuntimeJSON(w, result)
			return
		}
		defer conn.Close()
		result["connected"] = true
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		if len(payload) > 0 {
			if _, err = conn.Write(payload); err != nil {
				result["write_error"] = err.Error()
			}
		}
		data := make([]byte, req.ReadBytes)
		n := 0
		if req.ReadBytes > 0 {
			n, err = conn.Read(data)
			if err != nil && err != io.EOF {
				result["read_error"] = err.Error()
			}
		}
		result["response_bytes"] = n
		result["data_base64"] = base64.StdEncoding.EncodeToString(data[:n])
		b.log(result)
		writeRuntimeJSON(w, result)
	})
	return b.bounded(mux)
}
func writeRuntimeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (b *Broker) operationContext(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, minDeadline(time.Now().Add(limit), b.Policy.ExpiresAt))
}

func (b *Broker) bounded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case b.slots <- struct{}{}:
			defer func() { <-b.slots }()
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "broker concurrency limit", 429)
		}
	})
}
