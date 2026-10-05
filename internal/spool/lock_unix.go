//go:build unix

package spool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrServing means another ynr serve already holds the spool.
var ErrServing = errors.New("spool: another ynr serve holds this spool")

// LockInfo is what the serving process records in its lock file, for ynr info (ADR-004).
type LockInfo struct {
	PID   int       `json:"pid"`
	Since time.Time `json:"since"`
}

// Lock is ynr serve's hold on a spool root: one reader per spool.
type Lock struct{ f *os.File }

func lockPath(root string) string { return filepath.Join(root, StateDir, "serve.lock") }

// Acquire takes the spool's lock, or returns ErrServing if another process holds it.
func Acquire(root string) (*Lock, error) {
	f, err := os.OpenFile(lockPath(root), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrServing
		}
		return nil, err
	}
	b, _ := json.Marshal(LockInfo{PID: os.Getpid(), Since: time.Now().UTC().Truncate(time.Second)})
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(b, 0)
	}
	return &Lock{f: f}, nil
}

// Release gives the lock up.
func (l *Lock) Release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// Holder reports which process serves root, if any.
func Holder(root string) (*LockInfo, error) {
	f, err := os.Open(lockPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return nil, nil
	}
	var info LockInfo
	if err := json.NewDecoder(f).Decode(&info); err != nil {
		return &LockInfo{}, nil
	}
	return &info, nil
}
