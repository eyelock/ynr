//go:build !unix

package conformance

import "os/exec"

func ownGroup(*exec.Cmd) {}

func killTree(c *exec.Cmd) {
	if c.Process != nil {
		_ = c.Process.Kill()
	}
}
