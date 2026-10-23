package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Command runs the separate data plane. No server or provider initialization is
// performed by these entry points.
func Command(args []string) error {
	if args[0] == "runtime-worker" {
		return ChildMain(os.Stdin, os.Stdout)
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: %s configuration.json", args[0])
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var servers []*http.Server
	if args[0] == "runtime-runner" {
		var cfg RunnerConfig
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		r, e := NewRunner(cfg)
		if e != nil {
			return e
		}
		defer r.Close()
		servers = append(servers, &http.Server{Addr: cfg.Listen, Handler: r.Handler(), ReadHeaderTimeout: 5 * time.Second})
	} else {
		var cfg struct {
			PolicyFile   string `json:"policy_file"`
			State        string `json:"state"`
			ProxyListen  string `json:"proxy_listen"`
			APIListen    string `json:"api_listen"`
			CanaryListen string `json:"canary_listen"`
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.State == "" || cfg.ProxyListen == "" || cfg.APIListen == "" || cfg.CanaryListen == "" {
			return fmt.Errorf("incomplete broker configuration")
		}
		p, e := LoadPolicy(cfg.PolicyFile)
		if e != nil {
			return e
		}
		b, e := NewBroker(p, cfg.State)
		if e != nil {
			return e
		}
		defer b.Close()
		servers = append(servers, &http.Server{Addr: cfg.ProxyListen, Handler: b.ProxyHandler(), ReadHeaderTimeout: 5 * time.Second}, &http.Server{Addr: cfg.APIListen, Handler: b.APIHandler(), ReadHeaderTimeout: 5 * time.Second}, &http.Server{Addr: cfg.CanaryListen, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("network canary")) }), ReadHeaderTimeout: 5 * time.Second})
	}
	failures := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *http.Server) { failures <- s.ListenAndServe() }(s)
	}
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	for _, s := range servers {
		_ = s.Close()
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
