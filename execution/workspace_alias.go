package execution

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateWorkspaceAlias(task, alias string) error {
	if alias == "" {
		return nil
	}
	if !filepath.IsAbs(alias) || filepath.Clean(alias) != alias || filepath.Base(alias) != task || filepath.Base(filepath.Dir(alias)) != "tasks" {
		return fmt.Errorf("host workspace alias must be a canonical absolute tasks/%s path", task)
	}
	for _, reserved := range strings.Fields("/proc /sys /dev /run /tmp /etc /usr /bin /sbin /lib /lib64 /opt /workspace /app /artex-tool-worker") {
		if alias == reserved || strings.HasPrefix(alias, reserved+"/") {
			return fmt.Errorf("host workspace alias overlaps a runtime path")
		}
	}
	return nil
}

func workspaceAliasArgs(alias string) []string {
	if alias == "" {
		return nil
	}
	var args []string
	parent := ""
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(alias), "/"), "/") {
		parent += "/" + part
		args = append(args, "--dir", parent)
	}
	return append(args, "--symlink", "/workspace", alias)
}
