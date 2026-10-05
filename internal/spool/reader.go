package spool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrMalformed is returned by a Handler for a line it cannot parse; the line is skipped and
// counted.
var ErrMalformed = errors.New("spool: malformed line")

// Handler is given each complete line in a writer folder, in order. It returns nil once the line
// is handed on, ErrMalformed to skip it, or any other error to stop: the line and everything
// after it is read again on the next poll.
type Handler func(w Writer, line []byte) error

// DefaultMaxLine bounds a line's length; longer lines are skipped and counted.
const DefaultMaxLine = 4 << 20

// Suffixes of spool files: an open file is renamed to the closed suffix when its writer closes it
// (ADR-004).
const (
	OpenSuffix   = ".open.jsonl"
	ClosedSuffix = ".jsonl"
)

// Reader reads a spool root.
type Reader struct {
	Root     string
	MaxLine  int
	Counters *Counters
	// RunUser returns the user a run writes as, from its manifest, which ynf writes out of the
	// run's reach. A run in an image writes as the image's user, not the folder's owner, so its
	// files are accepted from that user too. Nil, or false, accepts only the folder's owner.
	RunUser func(w Writer) (uid uint32, ok bool)

	// Ship, when set, makes the reader batch (ADR-005): a file is read only once it is due,
	// and after its new lines have gone to the handler, Ship is called with the file's source
	// name and the byte range read. The position is committed only if Ship succeeds, so a batch
	// is never lost between reading and shipping. Without Ship every poll reads everything new.
	Ship func(w Writer, source string, from, to int64) error
	// ShipAge and ShipBytes say when a file is due: once its unshipped bytes are ShipAge old or
	// reach ShipBytes. A closed file is always due.
	ShipAge   time.Duration
	ShipBytes int64
	// Force makes every file due, for the final flush on shutdown.
	Force bool

	pos     *positions
	pending map[string]time.Time // when each file's unshipped bytes were first seen
	now     func() time.Time
}

// Init creates a spool root with its state folder and the laptop's local writer folder.
func Init(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, StateDir), 0o700); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(root, LocalDir), 0o700)
}

// NewReader opens an existing spool root.
func NewReader(root string, maxLine int) (*Reader, error) {
	if _, _, err := dirOwnerAndDev(root); err != nil {
		return nil, err
	}
	pos, err := loadPositions(root)
	if err != nil {
		return nil, err
	}
	if maxLine <= 0 {
		maxLine = DefaultMaxLine
	}
	return &Reader{Root: root, MaxLine: maxLine, Counters: &Counters{}, pos: pos, pending: map[string]time.Time{}, now: time.Now}, nil
}

// Poll reads every writer folder once, handing each new complete line to h. A file's position
// is committed only after h has accepted its lines, and a closed file is deleted only once
// everything in it is committed, so a crash re-reads rather than loses (shipping is at least
// once, ADR-004).
func (r *Reader) Poll(ctx context.Context, h Handler) error {
	writers, err := Writers(r.Root, r.Counters)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	defer func() {
		r.pos.prune(seen)
		for k := range r.pending {
			if !seen[k] {
				delete(r.pending, k)
			}
		}
		_ = r.pos.save()
	}()
	for _, w := range writers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.pollWriter(ctx, w, h, seen); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) pollWriter(ctx context.Context, w Writer, h Handler, seen map[string]bool) error {
	owner, dev, err := dirOwnerAndDev(w.Dir)
	if err != nil {
		if errors.Is(err, ErrRejected) {
			r.Counters.Rejected.Add(1)
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// A writer folder may be its own volume, such as a run's size-limited one: each file is
	// checked against its folder's device, and nlink == 1 already refuses hard links.
	owners := []uint32{owner}
	if w.Class == Run && r.RunUser != nil {
		if uid, ok := r.RunUser(w); ok && uid != owner {
			owners = append(owners, uid)
		}
	}
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ClosedSuffix) {
			r.Counters.Ignored.Add(1)
			continue
		}
		if !e.Type().IsRegular() {
			r.Counters.Rejected.Add(1)
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.pollFile(w, filepath.Join(w.Dir, n), owners, dev, h, seen); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) pollFile(w Writer, path string, owners []uint32, dev uint64, h Handler, seen map[string]bool) error {
	f, id, size, err := openSafe(path, owners, dev)
	if err != nil {
		if errors.Is(err, ErrRejected) {
			r.Counters.Rejected.Add(1)
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()
	key := id.String()
	seen[key] = true
	offset := r.pos.Files[key]
	if offset > size {
		offset = 0 // replaced or truncated: start again
	}
	closed := !strings.HasSuffix(path, OpenSuffix)
	if r.Ship != nil && !r.due(key, size-offset, closed) {
		return nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	consumed, handErr := r.readLines(w, bufio.NewReaderSize(io.LimitReader(f, size-offset), 64<<10), h)
	if r.Ship != nil && consumed > 0 {
		if err := r.Ship(w, Source(w, path), offset, offset+consumed); err != nil {
			return fmt.Errorf("%s: shipping: %w", w.Rel, err)
		}
	}
	delete(r.pending, key)
	committed := offset + consumed
	if handErr == nil && closed && committed < size {
		// A closed file ending without a newline: the remainder can never become a line.
		r.Counters.Malformed.Add(1)
		committed = size
	}
	if handErr == nil && closed && committed == size {
		delete(r.pos.Files, key)
		delete(seen, key)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		r.Counters.Deleted.Add(1)
		return nil
	}
	r.pos.Files[key] = committed
	if err := r.pos.save(); err != nil {
		return err
	}
	return handErr
}

// readLines hands each complete line to h and returns how many bytes were consumed: only whole
// lines count, so a trailing partial line is read again on the next poll. The line passed to h is
// only valid during the call.
func (r *Reader) readLines(w Writer, br *bufio.Reader, h Handler) (int64, error) {
	var consumed, pending int64
	var buf []byte
	oversized := false
	reset := func() { buf, pending, oversized = buf[:0], 0, false }
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			pending += int64(len(chunk))
			if !oversized && len(buf)+len(chunk) <= r.MaxLine+1 {
				buf = append(buf, chunk...)
			} else {
				oversized, buf = true, buf[:0]
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
		total := pending + int64(len(chunk))
		if oversized || len(buf)+len(chunk)-1 > r.MaxLine {
			r.Counters.Oversized.Add(1)
			consumed += total
			reset()
			continue
		}
		line := append(buf, chunk...)
		line = line[:len(line)-1]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if len(bytes.TrimSpace(line)) == 0 {
			consumed += total
			reset()
			continue
		}
		switch herr := h(w, line); {
		case herr == nil:
			r.Counters.Lines.Add(1)
		case errors.Is(herr, ErrMalformed):
			r.Counters.Malformed.Add(1)
		default:
			return consumed, fmt.Errorf("%s: %w", w.Rel, herr)
		}
		consumed += total
		reset()
	}
}

// due reports whether a file's unshipped bytes should be read and shipped now.
func (r *Reader) due(key string, unshipped int64, closed bool) bool {
	if r.Force || closed || unshipped <= 0 || (r.ShipBytes > 0 && unshipped >= r.ShipBytes) {
		return true
	}
	first, ok := r.pending[key]
	if !ok {
		r.pending[key] = r.now()
		return r.ShipAge <= 0
	}
	return r.now().Sub(first) >= r.ShipAge
}

// Source names a spool file in batch keys: its writer folder and its name without the suffix,
// the same whether the file is still open or closed.
func Source(w Writer, path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, OpenSuffix)
	base = strings.TrimSuffix(base, ClosedSuffix)
	return strings.ReplaceAll(w.Rel, "/", ".") + "." + base
}
