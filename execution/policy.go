package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Target struct {
	Host      string   `json:"host"`
	IPs       []string `json:"ips"` // pinned by the operator; never resolve from tool input
	HTTPPorts []int    `json:"http_ports"`
	TCPPorts  []int    `json:"tcp_ports"`
}
type Policy struct {
	TaskID              string    `json:"task_id"`
	ExpiresAt           time.Time `json:"expires_at"`
	HTTPRequests        int64     `json:"http_requests"`
	TCPConnections      int64     `json:"tcp_connections"`
	MinIntervalMS       int64     `json:"min_interval_ms"`
	Targets             []Target  `json:"targets"`
	InsecureUpstreamTLS bool      `json:"insecure_upstream_tls,omitempty"` // explicit local lab only
}

func LoadPolicy(file string) (Policy, error) {
	var p Policy
	b, e := os.ReadFile(file)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	if e != nil {
		return p, e
	}
	return p, p.Validate()
}
func (p Policy) Validate() error {
	if !validID(p.TaskID) || p.ExpiresAt.IsZero() || p.HTTPRequests < 0 || p.TCPConnections < 0 || p.MinIntervalMS < 0 || len(p.Targets) == 0 {
		return errors.New("invalid task policy or budgets")
	}
	seen := map[string]bool{}
	endpoints := map[string]string{}
	for _, t := range p.Targets {
		if t.Host == "" || strings.ContainsAny(t.Host, "/\\@?#% \t\r\n") || t.Host != strings.ToLower(t.Host) || len(t.IPs) == 0 || seen[t.Host] {
			return errors.New("invalid or duplicate target host")
		}
		seen[t.Host] = true
		for _, ip := range t.IPs {
			a, e := netip.ParseAddr(ip)
			if e != nil || a.IsUnspecified() || a.IsMulticast() {
				return fmt.Errorf("invalid pinned IP %q", ip)
			}
		}
		ports := map[int]bool{}
		for kind, list := range [][]int{t.HTTPPorts, t.TCPPorts} {
			for _, port := range list {
				if port < 1 || port > 65535 || ports[port] {
					return errors.New("invalid/overlapping HTTP and TCP ports")
				}
				ports[port] = true
				for _, ip := range t.IPs {
					a, _ := netip.ParseAddr(ip)
					endpoint := net.JoinHostPort(a.Unmap().String(), strconv.Itoa(port))
					label := strconv.Itoa(kind)
					if prior, ok := endpoints[endpoint]; ok && prior != label {
						return errors.New("HTTP/TCP endpoint overlap across target aliases")
					}
					endpoints[endpoint] = label
				}
			}
		}
	}
	return nil
}
func (p Policy) Resolve(authority, kind string) (string, string, error) {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		return "", "", errors.New("explicit destination port required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", "", errors.New("invalid port")
	}
	host = strings.ToLower(host)
	for _, t := range p.Targets {
		if host != t.Host {
			continue
		}
		ports := t.HTTPPorts
		if kind == "tcp" {
			ports = t.TCPPorts
		}
		for _, allowed := range ports {
			if port == allowed {
				return net.JoinHostPort(t.IPs[0], strconv.Itoa(port)), t.Host, nil
			}
		}
	}
	return "", "", errors.New("destination is outside the operator-approved scope")
}

type Meter struct {
	db     *sql.DB
	mu     sync.Mutex
	policy Policy
}

func OpenMeter(file string, p Policy) (*Meter, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	d, e := sql.Open("sqlite", file)
	if e != nil {
		return nil, e
	}
	d.SetMaxOpenConns(1)
	fail := func(e error) (*Meter, error) { d.Close(); return nil, e }
	if _, e = d.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS budget (task TEXT PRIMARY KEY, http_used INTEGER NOT NULL DEFAULT 0, tcp_used INTEGER NOT NULL DEFAULT 0, last_start INTEGER NOT NULL DEFAULT 0, policy TEXT NOT NULL);`); e != nil {
		return fail(e)
	}
	raw, _ := json.Marshal(p)
	if _, e = d.Exec(`INSERT INTO budget(task,policy) VALUES(?,?) ON CONFLICT(task) DO NOTHING`, p.TaskID, string(raw)); e != nil {
		return fail(e)
	}
	var saved string
	if e = d.QueryRow(`SELECT policy FROM budget WHERE task=?`, p.TaskID).Scan(&saved); e != nil {
		return fail(e)
	}
	if saved != string(raw) {
		return fail(errors.New("persisted task policy differs; use a new task rather than resetting its budget"))
	}
	return &Meter{db: d, policy: p}, nil
}
func (m *Meter) Close() error { return m.db.Close() }

// Reserve commits before outbound activity. Failures and cancellation consume the
// reservation; a restart or concurrent caller cannot restore spent allowance.
func (m *Meter) Reserve(ctx context.Context, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind != "http" && kind != "tcp" {
		return errors.New("invalid budget kind")
	}
	for {
		now := time.Now()
		if !now.Before(m.policy.ExpiresAt) {
			return errors.New("task time budget exhausted")
		}
		tx, e := m.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var h, t, last int64
		e = tx.QueryRowContext(ctx, `SELECT http_used,tcp_used,last_start FROM budget WHERE task=?`, m.policy.TaskID).Scan(&h, &t, &last)
		if e != nil {
			tx.Rollback()
			return e
		}
		if (kind == "http" && h >= m.policy.HTTPRequests) || (kind == "tcp" && t >= m.policy.TCPConnections) {
			tx.Rollback()
			return fmt.Errorf("%s budget exhausted", kind)
		}
		wait := time.Duration(last+m.policy.MinIntervalMS-now.UnixMilli()) * time.Millisecond
		if wait > 0 {
			tx.Rollback()
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		column := "http_used"
		if kind == "tcp" {
			column = "tcp_used"
		}
		// The predicate rejects a stale reservation if a second broker process won.
		result, e := tx.ExecContext(ctx, `UPDATE budget SET `+column+`=`+column+`+1,last_start=? WHERE task=? AND http_used=? AND tcp_used=? AND last_start=?`, now.UnixMilli(), m.policy.TaskID, h, t, last)
		if e != nil {
			tx.Rollback()
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			tx.Rollback()
			return errors.New("concurrent budget reservation rejected")
		}
		return tx.Commit()
	}
}
func (m *Meter) Status() (map[string]any, error) {
	var h, t int64
	e := m.db.QueryRow(`SELECT http_used,tcp_used FROM budget WHERE task=?`, m.policy.TaskID).Scan(&h, &t)
	return map[string]any{"task_id": m.policy.TaskID, "http_used": h, "http_limit": m.policy.HTTPRequests, "tcp_used": t, "tcp_limit": m.policy.TCPConnections, "expires_at": m.policy.ExpiresAt, "min_interval_ms": m.policy.MinIntervalMS}, e
}
