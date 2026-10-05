//go:build unix

package spool

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrRejected marks a file the hostile-input rules refuse to read.
var ErrRejected = errors.New("spool: file rejected")

// identity is what a file is, independent of its name: renaming an .open.jsonl file when it
// closes keeps its identity, so its position survives the rename.
type identity struct {
	Dev uint64
	Ino uint64
}

func (id identity) String() string { return fmt.Sprintf("%d:%d", id.Dev, id.Ino) }

// devNum widens a device number, whose type differs by platform (int32 on macOS, uint64 on
// Linux), without a conversion that is redundant on one of them.
func devNum[T ~int32 | ~uint32 | ~int64 | ~uint64](d T) uint64 { return uint64(d) }

func statOf(fi os.FileInfo) (*syscall.Stat_t, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

// openSafe opens path for reading only if it is a regular file with one link, on the root's
// device, owned by the owner of the folder it is in. It never follows a symbolic link, so a link
// planted in a writer folder to a manifest, the factory folder or a host file is refused rather
// than read, shipped or deleted.
func openSafe(path string, dirOwner uint32, rootDev uint64) (*os.File, identity, int64, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, identity{}, 0, fmt.Errorf("%w: %s is a symbolic link", ErrRejected, path)
		}
		return nil, identity{}, 0, err
	}
	f := os.NewFile(uintptr(fd), path)
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, identity{}, 0, err
	}
	st, ok := statOf(fi)
	reject := func(why string) (*os.File, identity, int64, error) {
		_ = f.Close()
		return nil, identity{}, 0, fmt.Errorf("%w: %s %s", ErrRejected, path, why)
	}
	switch {
	case !ok:
		return reject("has no unix file status")
	case !fi.Mode().IsRegular():
		return reject("is not a regular file")
	case st.Nlink != 1:
		return reject("has more than one link")
	case devNum(st.Dev) != rootDev:
		return reject("is on another device")
	case st.Uid != dirOwner:
		return reject("is not owned by its folder's owner")
	}
	return f, identity{Dev: devNum(st.Dev), Ino: st.Ino}, fi.Size(), nil
}

// dirOwnerAndDev returns a directory's owner and device, refusing a symbolic link.
func dirOwnerAndDev(path string) (uint32, uint64, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !fi.IsDir() {
		return 0, 0, fmt.Errorf("%w: %s is not a directory", ErrRejected, path)
	}
	st, ok := statOf(fi)
	if !ok {
		return 0, 0, fmt.Errorf("%w: %s has no unix file status", ErrRejected, path)
	}
	return st.Uid, devNum(st.Dev), nil
}
