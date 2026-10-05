// Package spool reads the spool: the folder tools write OpenTelemetry JSON lines into, with one
// subfolder per writer (ADR-003, ADR-004). Everything it reads is treated as hostile input.
package spool

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Class is a record's provenance, decided by the folder it was written in, never by the record.
type Class string

// The provenance classes (ADR-003).
const (
	Factory Class = "factory"
	Service Class = "service"
	Run     Class = "run"
	Local   Class = "local"
)

// Folder names at the spool root.
const (
	FactoryDir   = "factory"
	ServicesDir  = "services"
	RunsDir      = "runs"
	LocalDir     = "local"
	ManifestsDir = "manifests"
	StateDir     = ".ynr"
)

// Writer is one writer folder in the spool.
type Writer struct {
	Class Class
	// Name is the service name or the run id; empty for the factory and local folders.
	Name string
	// Dir is the folder's absolute path.
	Dir string
	// Rel is the folder's path relative to the spool root, such as runs/01J9….
	Rel string
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidName reports whether s can name a service or a run folder.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// DefaultRoot is $XDG_STATE_HOME/ynr/spool, or ~/.local/state/ynr/spool when XDG_STATE_HOME is
// unset.
func DefaultRoot() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "ynr", "spool"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "ynr", "spool"), nil
}

// Writers lists the writer folders under root. Anything else at the root, and any folder that is
// a link rather than a directory, is ignored and counted.
func Writers(root string, c *Counters) ([]Writer, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Writer
	for _, e := range entries {
		name := e.Name()
		switch name {
		case ManifestsDir, StateDir:
			continue
		case FactoryDir, LocalDir:
			if !isDir(e) {
				c.Ignored.Add(1)
				continue
			}
			class := Factory
			if name == LocalDir {
				class = Local
			}
			out = append(out, Writer{Class: class, Dir: filepath.Join(root, name), Rel: name})
		case ServicesDir, RunsDir:
			if !isDir(e) {
				c.Ignored.Add(1)
				continue
			}
			class := Service
			if name == RunsDir {
				class = Run
			}
			subs, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, err
			}
			for _, s := range subs {
				if !isDir(s) || !ValidName(s.Name()) {
					c.Ignored.Add(1)
					continue
				}
				out = append(out, Writer{
					Class: class,
					Name:  s.Name(),
					Dir:   filepath.Join(root, name, s.Name()),
					Rel:   name + "/" + s.Name(),
				})
			}
		default:
			c.Ignored.Add(1)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// isDir is true only for a real directory: a symbolic link to one is not a writer folder.
func isDir(e os.DirEntry) bool { return e.Type().IsDir() && e.Type()&os.ModeSymlink == 0 }
