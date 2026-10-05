// Package store is the object store port (ADR-005): where ynr serve ships batches and where the
// reader of a store finds them. Configuration is a URL. The laptop adapter is a folder
// (file:///…); S3 follows in the cloud slice.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Store holds objects by key. Keys use '/' and are relative.
type Store interface {
	// Put writes an object whole: a reader sees all of it or none of it.
	Put(ctx context.Context, key string, data []byte) error
	// URL is the store's configuration, for logs and ynr info.
	URL() string
}

// Open opens a store from its URL.
func Open(raw string) (Store, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("store %q: %w", raw, err)
	}
	switch u.Scheme {
	case "file":
		if u.Host != "" && u.Host != "localhost" {
			return nil, fmt.Errorf("store %q: a folder store has no host", raw)
		}
		if !filepath.IsAbs(u.Path) {
			return nil, fmt.Errorf("store %q: the folder must be an absolute path", raw)
		}
		return &Folder{root: filepath.Clean(u.Path), raw: raw}, nil
	case "":
		return nil, fmt.Errorf("store %q: give a URL such as file:///path/to/folder", raw)
	}
	return nil, fmt.Errorf("store %q: no adapter for %s yet", raw, u.Scheme)
}

// FolderURL is the URL of a folder store at an absolute path.
func FolderURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)

// ValidKey reports whether a key is relative, has no empty, '.' or '..' segments, and uses only
// characters every adapter accepts.
func ValidKey(key string) bool {
	if !keyPattern.MatchString(key) {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Folder is the laptop adapter: a store in a local folder.
type Folder struct {
	root string
	raw  string
}

// URL returns the folder's URL.
func (f *Folder) URL() string { return f.raw }

// Put writes the object to a temporary file beside it, flushes it, and renames it into place, so
// a reader never sees part of an object.
func (f *Folder) Put(_ context.Context, key string, data []byte) (err error) {
	if !ValidKey(key) {
		return fmt.Errorf("store: invalid key %q", key)
	}
	path := filepath.Join(f.root, filepath.FromSlash(key))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.Join(os.ErrExist, fmt.Errorf("store: %s already exists", key))
	}
	return os.Rename(tmp.Name(), path)
}
