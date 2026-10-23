//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReportOpenFIFOIsNonblocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "report.md"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		f, err := openReportFile(root, "report.md")
		if err == nil {
			defer f.Close()
			info, e := f.Stat()
			if e != nil {
				err = e
			} else if info.Mode().IsRegular() {
				t.Error("FIFO reported regular")
			}
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("artifact open blocked on a FIFO")
	}
}
