package server

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReportArtifactsPreserveActualPathsAndExcludeSymlinks(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "tasks", "1")
	if err := os.MkdirAll(filepath.Join(task, "i3", "i3"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "i3", "report.md"), []byte("10 timeouts; no confirmed vulnerability"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "i3", "i3", "probes.jsonl"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.md")
	if err := os.WriteFile(outside, []byte("do not export"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(task, "assessment.md")); err != nil {
		t.Fatal(err)
	}
	artifacts, assessments, limits := collectReportArtifacts(root, "1")
	if len(artifacts) != 2 || len(assessments) != 1 || len(limits) != 1 {
		t.Fatalf("artifacts=%+v assessments=%+v limits=%v", artifacts, assessments, limits)
	}
	if artifacts[0].Path != "tasks/1/i3/i3/probes.jsonl" || !strings.Contains(artifacts[0].URL, "tasks%2F1%2Fi3%2Fi3%2Fprobes.jsonl") {
		t.Fatalf("noncanonical artifact: %+v", artifacts[0])
	}
	if assessments[0].Path != "tasks/1/i3/report.md" || !strings.Contains(assessments[0].Content, "10 timeouts") {
		t.Fatalf("assessment=%+v", assessments[0])
	}
	if a, _, l := collectReportArtifacts(root, "../1"); len(a) != 0 || len(l) == 0 {
		t.Fatal("accepted invalid task root")
	}
}

func TestReportArtifactsSizeLimitIsExplicit(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "tasks", "2")
	if err := os.MkdirAll(task, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "report.md"), []byte(strings.Repeat("x", (512<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	a, reports, limits := collectReportArtifacts(root, "2")
	if len(a) != 1 || len(reports) != 0 || len(limits) != 1 {
		t.Fatalf("artifacts=%d reports=%d limits=%v", len(a), len(reports), limits)
	}
}

func TestReportArtifactsRejectTaskRootSwap(t *testing.T) {
	root := t.TempDir()
	tasks := filepath.Join(root, "tasks")
	own, other, parked := filepath.Join(tasks, "1"), filepath.Join(tasks, "2"), filepath.Join(tasks, "parked")
	for _, d := range []string{own, other} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(own, "report.md"), []byte("OWN"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "report.md"), []byte("OTHER-TASK-PRIVATE"), 0600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if os.Rename(own, parked) != nil {
				continue
			}
			_ = os.Symlink("2", own)
			_ = os.Remove(own)
			_ = os.Rename(parked, own)
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 300; i++ {
		_, assessments, _ := collectReportArtifacts(root, "1")
		for _, a := range assessments {
			if strings.Contains(a.Content, "OTHER-TASK-PRIVATE") {
				t.Fatal("export crossed into a sibling task during a root swap")
			}
		}
	}
}

func TestFinalReportExportOmitsRuntimeTrustStores(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "tasks", "1")
	for name, content := range map[string]string{"i4/final-report.md": "Verified assessment with controls", ".runtime-home/worker/.pki/nssdb/key4.db": "runtime-only"} {
		p := filepath.Join(task, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifacts, assessments, limits := collectReportArtifacts(root, "1")
	if len(artifacts) != 1 || len(assessments) != 1 || len(limits) != 0 || assessments[0].Path != "tasks/1/i4/final-report.md" {
		t.Fatalf("artifacts=%v assessments=%v limits=%v", artifacts, assessments, limits)
	}
}
