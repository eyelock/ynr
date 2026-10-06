//go:build unix

package conformance

import (
	"os/exec"
	"syscall"
)

// ownGroup puts the command in its own process group, so the whole tree can be killed.
func ownGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree kills the command and everything it started.
func killTree(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); err != nil {
		_ = c.Process.Kill()
	}
}
