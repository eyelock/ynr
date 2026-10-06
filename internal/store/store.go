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
func (f *Folder) Put(_ context.Context, key string, data []byte) error {
	return f.write(key, data, false)
}

func (f *Folder) write(key string, data []byte, replace bool) (err error) {
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
	if !replace {
		if _, err := os.Lstat(path); err == nil {
			return errors.Join(os.ErrExist, fmt.Errorf("store: %s already exists", key))
		}
	}
	return os.Rename(tmp.Name(), path)
}

// Replace writes the object as Put does, replacing any already there.
func (f *Folder) Replace(_ context.Context, key string, data []byte) error {
	return f.write(key, data, true)
}

// Reader lists a store's objects and says where DuckDB can read each one. Adapters provide only
// this file access; the queries are the same over every adapter (ADR-005).
type Reader interface {
	Store
	// List returns the keys under a prefix that ends in '/', in key order. A prefix with nothing
	// under it is empty, not an error.
	List(ctx context.Context, prefix string) ([]string, error)
	// Location is where DuckDB reads a key: a path for a folder, a URL for a bucket.
	Location(key string) string
	// Get reads a whole object. A missing one is an error that matches os.ErrNotExist.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes an object; one already gone is not an error.
	Delete(ctx context.Context, key string) error
	// Replace writes an object whole, replacing any already there: for what only the store's
	// reader writes, such as the item index, never for batches.
	Replace(ctx context.Context, key string, data []byte) error
	// Sizes is List with each object's size in bytes, for retention by total size.
	Sizes(ctx context.Context, prefix string) (map[string]int64, error)
}

// OpenReader opens a store for reading from its URL.
func OpenReader(raw string) (Reader, error) {
	s, err := Open(raw)
	if err != nil {
		return nil, err
	}
	r, ok := s.(Reader)
	if !ok {
		return nil, fmt.Errorf("store %q: this adapter cannot be read yet", raw)
	}
	return r, nil
}

// List walks the folder under prefix. Temporary files from a Put in progress are skipped.
func (f *Folder) List(_ context.Context, prefix string) ([]string, error) {
	if prefix != "" && (!strings.HasSuffix(prefix, "/") || !ValidKey(strings.TrimSuffix(prefix, "/"))) {
		return nil, fmt.Errorf("store: invalid prefix %q", prefix)
	}
	base := filepath.Join(f.root, filepath.FromSlash(prefix))
	var keys []string
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		if key := filepath.ToSlash(rel); ValidKey(key) && !strings.HasPrefix(d.Name(), ".") {
			keys = append(keys, key)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return keys, err
}

// Location is the object's path on this machine.
func (f *Folder) Location(key string) string {
	return filepath.Join(f.root, filepath.FromSlash(key))
}

// Get reads the object.
func (f *Folder) Get(_ context.Context, key string) ([]byte, error) {
	if !ValidKey(key) {
		return nil, fmt.Errorf("store: invalid key %q", key)
	}
	return os.ReadFile(f.Location(key))
}

// Delete removes the object.
func (f *Folder) Delete(_ context.Context, key string) error {
	if !ValidKey(key) {
		return fmt.Errorf("store: invalid key %q", key)
	}
	if err := os.Remove(f.Location(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Sizes lists the folder under prefix with each object's size.
func (f *Folder) Sizes(ctx context.Context, prefix string) (map[string]int64, error) {
	keys, err := f.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(keys))
	for _, k := range keys {
		fi, err := os.Lstat(f.Location(k))
		if errors.Is(err, os.ErrNotExist) {
			continue // removed since it was listed
		}
		if err != nil {
			return nil, err
		}
		out[k] = fi.Size()
	}
	return out, nil
}
