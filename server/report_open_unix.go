//go:build !windows

package server

import (
	"os"
	"syscall"
)

// Nonblocking avoids a regular-file-to-FIFO swap hanging export. NOFOLLOW rejects
// a last-component symlink; os.Root also confines all intermediate resolution.
func openReportFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
