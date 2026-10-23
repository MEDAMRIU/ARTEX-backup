package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type RunnerConfig struct {
	Listen             string    `json:"listen"`
	TaskID             string    `json:"task_id"`
	Workspace          string    `json:"workspace"`
	TokenFile          string    `json:"token_file"`
	ProxyURL           string    `json:"proxy_url"`
	BrokerAPI          string    `json:"broker_api"`
	CAFile             string    `json:"ca_file"`
	DeniedProbeAddress string    `json:"denied_probe_address"`
	ExpiresAt          time.Time `json:"expires_at"`
}
type Runner struct {
	cfg       RunnerConfig
	token     string
	mu        sync.Mutex
	sessions  map[string]*sandboxSession
	readiness Readiness
	probing   bool
	slots     chan struct{}
}
type sandboxSession struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	done     chan struct{}
	stderr   *limitedBuffer
	lastUsed time.Time
	users    int // guarded by Runner.mu; includes callers waiting for session.mu
	closed   bool
}
type limitedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := (16 << 10) - len(b.data)
	if room > 0 {
		b.data = append(b.data, p[:min(len(p), room)]...)
	}
	return len(p), nil
}
func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("isolated runner requires Linux")
	}
	if !validID(cfg.TaskID) || !filepath.IsAbs(cfg.Workspace) || cfg.ExpiresAt.IsZero() || cfg.ProxyURL == "" || cfg.BrokerAPI == "" || cfg.DeniedProbeAddress == "" {
		return nil, errors.New("incomplete isolated runner configuration")
	}
	if !strings.HasSuffix(cfg.Listen, ":8080") {
		return nil, errors.New("runner control port must be 8080 for the firewall policy")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return nil, errors.New("bubblewrap is required")
	}
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 {
		return nil, errors.New("runner token must be at least 32 characters")
	}
	info, err := os.Lstat(cfg.Workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("workspace must be a mounted, regular directory")
	}
	if _, err := os.Stat("/run/artex-control-canary"); err != nil {
		return nil, fmt.Errorf("missing isolation canary: %w", err)
	}
	r := &Runner{cfg: cfg, token: token, sessions: map[string]*sandboxSession{}, slots: make(chan struct{}, 4), readiness: Readiness{Mode: "isolated", TaskID: cfg.TaskID, Checks: []Check{{Name: "runner_preflight", Status: "untested", Required: true, Detail: "Run readiness checks before starting a task."}}}}
	return r, nil
}
func (r *Runner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		s.close()
	}
	r.sessions = map[string]*sandboxSession{}
}
func (s *sandboxSession) close() { s.mu.Lock(); defer s.mu.Unlock(); s.killLocked() }
func (s *sandboxSession) killLocked() {
	if s.closed {
		return
	}
	s.closed = true
	_ = s.stdin.Close()
	killSandbox(s.cmd)
}

func (r *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/readiness", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		result := r.readiness
		r.mu.Unlock()
		if !time.Now().Before(r.cfg.ExpiresAt) {
			result.Checks = append(result.Checks, Check{Name: "task_time", Status: "failed", Required: true, Detail: "Task time budget exhausted."})
			result.Summarize()
		}
		writeRuntimeJSON(w, result)
	})
	mux.HandleFunc("POST /v1/readiness", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.probing {
			r.mu.Unlock()
			http.Error(w, "readiness check already running", 409)
			return
		}
		r.probing = true
		r.mu.Unlock()
		result := r.check(req.Context())
		r.mu.Lock()
		r.readiness = result
		r.probing = false
		r.mu.Unlock()
		writeRuntimeJSON(w, result)
	})
	mux.HandleFunc("POST /v1/call", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		ready := r.readiness.Ready
		r.mu.Unlock()
		if !ready {
			http.Error(w, "isolated runtime has not passed readiness", 503)
			return
		}
		// A short settlement window permits report writes after network expiry. The
		// broker still rejects all network operations at its immutable deadline.
		if time.Now().After(r.cfg.ExpiresAt.Add(time.Minute)) {
			http.Error(w, "task runner expired", 410)
			return
		}
		var call Call
		if e := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<20)).Decode(&call); e != nil {
			http.Error(w, "invalid tool call", 400)
			return
		}
		if call.Name == "__readiness" {
			http.Error(w, "use the readiness endpoint", 403)
			return
		}
		reply := r.call(req.Context(), call)
		writeRuntimeJSON(w, reply)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		supplied := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(r.token)) != 1 {
			http.Error(w, "runner authentication required", 401)
			return
		}
		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		case <-req.Context().Done():
			return
		default:
			http.Error(w, "runner concurrency limit reached", 429)
			return
		}
		mux.ServeHTTP(w, req)
	})
}
func (r *Runner) session(directory string) (*sandboxSession, error) {
	if directory == "" {
		directory = "."
	}
	if !filepath.IsLocal(directory) || strings.Contains(directory, "\\") || len(directory) > 512 {
		return nil, errors.New("invalid task workspace directory")
	}
	directory = filepath.Clean(directory)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[directory]; s != nil {
		select {
		case <-s.done:
			delete(r.sessions, directory)
		default:
			s.users++
			return s, nil
		}
	}
	if len(r.sessions) >= 8 {
		// Worker directories accumulate across sequential intents. Retain at most
		// eight sessions, evicting only sessions with no active or queued caller.
		var oldest *sandboxSession
		var oldestDir string
		for dir, candidate := range r.sessions {
			if candidate.users == 0 && (oldest == nil || candidate.lastUsed.Before(oldest.lastUsed)) {
				oldest, oldestDir = candidate, dir
			}
		}
		if oldest == nil {
			return nil, errors.New("maximum concurrent task sandboxes reached")
		}
		oldest.close()
		delete(r.sessions, oldestDir)
	}
	root, err := os.OpenRoot(r.cfg.Workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	dirRoot, err := root.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	dirRoot.Close()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--cap-drop", "ALL", "--clearenv"}
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt/artex-tools", "/opt/playwright", "/opt/artex-runtime", "/app/skills"} {
		if _, e := os.Stat(dir); e == nil {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run", "--dir", "/etc", "--dir", "/home", "--bind", r.cfg.Workspace, "/workspace")
	for _, file := range []string{"/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/resolv.conf", "/etc/ld.so.cache", "/etc/ssl/certs", "/etc/fonts"} {
		if _, e := os.Stat(file); e == nil {
			args = append(args, "--ro-bind", file, file)
		}
	}
	args = append(args, "--ro-bind", executable, "/artex-tool-worker", "--ro-bind", r.cfg.CAFile, "/etc/artex-egress-ca.pem")
	homeID := fmt.Sprintf("%x", sha256.Sum256([]byte(directory)))[:16]
	brokerURL, _ := url.Parse(r.cfg.BrokerAPI)
	noProxy := "localhost,127.0.0.1,::1," + brokerURL.Hostname()
	environment := map[string]string{
		"PATH": "/opt/artex-tools/node_modules/.bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/workspace/.runtime-home/" + homeID, "TMPDIR": "/tmp", "LANG": "C.UTF-8", "ARTEX_TOOL_SANDBOX": "1", "NORMA_RIPGREP_NO_INSTALL": "1", "NO_UPDATE_NOTIFIER": "1",
		"NODE_PATH": "/opt/artex-tools/node_modules", "PLAYWRIGHT_BROWSERS_PATH": "/opt/playwright", "CHROMIUM": "/usr/local/bin/artex-chromium", "PLAYWRIGHT_MCP_EXECUTABLE_PATH": "/usr/local/bin/artex-chromium", "PLAYWRIGHT_MCP_SANDBOX": "true", "PLAYWRIGHT_MCP_PROXY_SERVER": r.cfg.ProxyURL, "PLAYWRIGHT_MCP_PROXY_BYPASS": "localhost,127.0.0.1,[::1]",
		"HTTP_PROXY": r.cfg.ProxyURL, "HTTPS_PROXY": r.cfg.ProxyURL, "http_proxy": r.cfg.ProxyURL, "https_proxy": r.cfg.ProxyURL, "NO_PROXY": noProxy, "no_proxy": noProxy,
		"NODE_USE_ENV_PROXY": "1", "NODE_EXTRA_CA_CERTS": "/etc/artex-egress-ca.pem", "CURL_CA_BUNDLE": "/etc/artex-egress-ca.pem", "SSL_CERT_FILE": "/etc/artex-egress-ca.pem", "REQUESTS_CA_BUNDLE": "/etc/artex-egress-ca.pem",
		"ARTEX_BROKER_API": r.cfg.BrokerAPI, "ARTEX_DENIED_PROBE_ADDRESS": r.cfg.DeniedProbeAddress, "ARTEX_TASK_ID": r.cfg.TaskID,
	}
	for key, value := range environment {
		args = append(args, "--setenv", key, value)
	}
	args = append(args, "--chdir", filepath.Join("/workspace", directory), "--", "/artex-tool-worker", "runtime-worker")
	cmd := exec.Command("bwrap", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	configureSandboxProcess(cmd)
	s := &sandboxSession{cmd: cmd, done: make(chan struct{}), stderr: &limitedBuffer{}, lastUsed: time.Now(), users: 1}
	cmd.Stderr = s.stderr
	s.stdin, err = cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.stdin.Close()
		return nil, err
	}
	s.stdout = bufio.NewReaderSize(stdout, 64<<10)
	if err = cmd.Start(); err != nil {
		s.stdin.Close()
		return nil, err
	}
	go func() { _ = cmd.Wait(); close(s.done) }()
	r.sessions[directory] = s
	return s, nil
}
func (r *Runner) releaseSession(s *sandboxSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.users--
	s.lastUsed = time.Now()
}
func (r *Runner) call(ctx context.Context, call Call) Reply {
	if err := ctx.Err(); err != nil {
		return Reply{Error: "tool request cancelled before dispatch"}
	}
	s, err := r.session(call.Directory)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	defer r.releaseSession(s)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Reply{Error: "queued tool request cancelled before dispatch"}
	}
	if s.closed {
		return Reply{Error: "sandbox was terminated; resume after a successful readiness check"}
	}
	timeout := call.TimeoutSeconds
	if timeout <= 0 || timeout > 120 {
		timeout = 120
	}
	call.TimeoutSeconds = timeout
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+2)*time.Second)
	defer cancel()
	type outcome struct {
		reply Reply
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		if err := json.NewEncoder(s.stdin).Encode(call); err != nil {
			done <- outcome{err: err}
			return
		}
		line, err := readBoundedLine(s.stdout, 8<<20)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		var reply Reply
		err = json.Unmarshal(line, &reply)
		done <- outcome{reply: reply, err: err}
	}()
	select {
	case <-ctx.Done():
		s.killLocked()
		return Reply{Error: "sandbox operation cancelled or exceeded its time limit"}
	case result := <-done:
		if result.err != nil {
			s.killLocked()
			return Reply{Error: fmt.Sprintf("sandbox failed: %v: %.1000s", result.err, s.stderr.String())}
		}
		return result.reply
	}
}
func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(out)+len(part) > limit {
			return nil, errors.New("sandbox response exceeds output limit")
		}
		out = append(out, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return out, err
	}
}
func (r *Runner) check(ctx context.Context) Readiness {
	result := Readiness{Mode: "isolated", TaskID: r.cfg.TaskID, CheckedAt: time.Now().UTC(), ExpiresAt: r.cfg.ExpiresAt}
	reply := r.call(ctx, Call{Name: "__readiness", Directory: ".readiness", TimeoutSeconds: 90})
	if reply.Error != "" {
		result.Checks = []Check{{Name: "sandbox", Status: "failed", Required: true, Detail: reply.Error}}
		return result
	}
	if err := json.Unmarshal([]byte(reply.Result.Flatten()), &result.Checks); err != nil {
		result.Checks = []Check{{Name: "runtime_checks", Status: "failed", Required: true, Detail: "Invalid readiness result: " + err.Error()}}
	}
	if !time.Now().Before(r.cfg.ExpiresAt) {
		result.Checks = append(result.Checks, Check{Name: "task_time", Status: "failed", Required: true, Detail: "Task time budget exhausted."})
	}
	result.ValidateRequired()
	return result
}
