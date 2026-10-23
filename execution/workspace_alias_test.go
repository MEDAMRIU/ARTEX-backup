package execution

import (
	"strings"
	"testing"
)

func TestWorkspaceAliasOnlyMapsOwnTaskIntoEmptyParents(t *testing.T) {
	alias := "/var/lib/artex/data/tasks/7"
	if err := validateWorkspaceAlias("7", alias); err != nil {
		t.Fatal(err)
	}
	args := workspaceAliasArgs(alias)
	joined := strings.Join(args, " ")
	if !strings.HasSuffix(joined, "--symlink /workspace "+alias) || strings.Contains(joined, "--bind") {
		t.Fatalf("unexpected alias mounts: %s", joined)
	}
	for _, bad := range []string{"/var/lib/artex/data/tasks/8", "/var/lib/artex/data/tasks/07", "/var/lib/artex/data/tasks/../7", "relative/tasks/7", "/proc/tasks/7", "/usr/tasks/7", "/workspace/tasks/7", "/opt/hidden/tasks/7"} {
		if validateWorkspaceAlias("7", bad) == nil {
			t.Errorf("unsafe alias accepted: %s", bad)
		}
	}
}
