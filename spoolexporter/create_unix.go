//go:build unix

package spoolexporter

import (
	"os"
	"syscall"
)

// createExclusive creates a new file, never following a link planted at its name.
func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
}
