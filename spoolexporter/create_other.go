//go:build !unix

package spoolexporter

import "os"

// createExclusive creates a new file; O_EXCL refuses an existing name, including a link.
func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
}
