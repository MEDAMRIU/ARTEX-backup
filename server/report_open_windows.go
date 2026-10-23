//go:build windows

package server

import "os"

// Windows named pipes live outside the filesystem root; os.Root forbids escape
// to that namespace. The caller still validates the opened descriptor's type.
func openReportFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
