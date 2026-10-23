package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ForTask resolves an operator-provisioned, immutable runner. The application
// never gets a Docker socket or permission to widen a task's egress policy.
// Newly provisioned tasks are discovered without restarting the app. Once used,
// a task binding stays fixed for this process; replacing a file cannot silently
// reset its deadline, accounting, or failure barrier.
func (c *Client) ForTask(task string) (*Client, error) {
	if !validID(task) {
		return nil, fmt.Errorf("select a valid task to inspect its runtime")
	}
	if c.registryDir == "" {
		if c.TaskID != task {
			return nil, fmt.Errorf("no isolated runner is provisioned for task %s", task)
		}
		return c, nil
	}
	c.registryMu.Lock()
	defer c.registryMu.Unlock()
	if child := c.clients[task]; child != nil {
		return child, nil
	}
	root, err := os.OpenRoot(c.registryDir)
	if err != nil {
		return nil, fmt.Errorf("runner registry unavailable")
	}
	defer root.Close()
	f, err := root.Open(task + ".json")
	if err != nil {
		return nil, fmt.Errorf("no isolated runner is provisioned for task %s; provision its approved scope, then resume", task)
	}
	defer f.Close()
	var cfg struct {
		TaskID    string `json:"task_id"`
		URL       string `json:"url"`
		TokenFile string `json:"token_file"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || cfg.TaskID != task {
		return nil, fmt.Errorf("invalid runner registration for task %s", task)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(cfg.TokenFile) {
		return nil, fmt.Errorf("invalid runner endpoint or token path for task %s", task)
	}
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		return nil, fmt.Errorf("runner token unavailable for task %s", task)
	}
	child := &Client{BaseURL: cfg.URL, TaskID: task, Token: strings.TrimSpace(string(token)), Workspace: filepath.Join(c.dataDir, "tasks", task), HTTP: &http.Client{Timeout: 130 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.ConfigureChild != nil {
		c.ConfigureChild(child)
	}
	c.clients[task] = child
	return child, nil
}
