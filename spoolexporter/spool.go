package spoolexporter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Suffixes of spool files. A file is written under OpenSuffix and renamed to ClosedSuffix when
// it is rotated or the spool is closed.
const (
	OpenSuffix   = ".open.jsonl"
	ClosedSuffix = ".jsonl"
)

// Defaults for the limits a Spool enforces.
const (
	DefaultMaxFile     = 8 << 20  // rotate a file past this size
	DefaultMaxTotal    = 64 << 20 // drop records while this process's files hold this much
	DefaultMaxLine     = 4 << 20  // drop an export request larger than this
	DefaultSyncTimeout = 2 * time.Second
)

// Stats counts what a Spool has written and dropped.
type Stats struct {
	Lines        uint64 // export requests written
	Bytes        uint64 // bytes written
	Dropped      uint64 // export requests dropped: over the total cap, after Close, or a write failed
	Oversized    uint64 // export requests dropped for exceeding the line limit
	Malformed    uint64 // export requests the exporters sent that could not be decoded
	SyncTimeouts uint64 // flushes to disk abandoned past their time limit
}

// Option configures a Spool.
type Option func(*options)

type options struct {
	service     string
	instance    string
	maxFile     int64
	maxTotal    int64
	maxLine     int
	syncTimeout time.Duration
}

// WithService names the files this process writes. It defaults to the executable's name.
func WithService(name string) Option { return func(o *options) { o.service = name } }

// WithInstance identifies this process among others of the same service, typically its
// service.instance.id. It defaults to a random value.
func WithInstance(id string) Option { return func(o *options) { o.instance = id } }

// WithMaxFile sets the size past which the active file is rotated.
func WithMaxFile(n int64) Option { return func(o *options) { o.maxFile = n } }

// WithMaxTotal sets how much this process's files may hold, open and closed but not yet removed
// by a reader, before new records are dropped.
func WithMaxTotal(n int64) Option { return func(o *options) { o.maxTotal = n } }

// WithMaxLine sets the largest export request written; larger ones are dropped. A reader skips
// lines longer than its own limit, so this should not exceed it.
func WithMaxLine(n int) Option { return func(o *options) { o.maxLine = n } }

// WithSyncTimeout bounds each flush to disk; a flush that takes longer is abandoned, so a slow
// filesystem never blocks the caller.
func WithSyncTimeout(d time.Duration) Option { return func(o *options) { o.syncTimeout = d } }

// Spool writes export requests from the exporters it creates into one folder. It is safe for
// concurrent use; one Spool is shared by a process's trace, metric and log exporters.
type Spool struct {
	dir    string
	prefix string
	opts   options

	mu       sync.Mutex
	f        *os.File
	path     string // the active file, under OpenSuffix
	size     int64
	seq      int
	finished []string // this process's closed files, until a reader removes them
	closed   bool

	lines, bytes, dropped, oversized, malformed, syncTimeouts atomic.Uint64
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Open prepares to write into dir, which must already exist and be a directory, not a link to
// one. No file is created until the first record is written.
func Open(dir string, opts ...Option) (*Spool, error) {
	o := options{
		maxFile:     DefaultMaxFile,
		maxTotal:    DefaultMaxTotal,
		maxLine:     DefaultMaxLine,
		syncTimeout: DefaultSyncTimeout,
	}
	for _, opt := range opts {
		opt(&o)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("spool %s: not a directory", dir)
	}
	if o.service == "" {
		o.service = strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	}
	if o.instance == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		o.instance = hex.EncodeToString(b[:])
	}
	if o.maxFile <= 0 || o.maxTotal <= 0 || o.maxLine <= 0 || o.syncTimeout <= 0 {
		return nil, errors.New("spool: limits must be positive")
	}
	return &Spool{dir: dir, prefix: clean(o.service) + "-" + clean(o.instance), opts: o}, nil
}

func clean(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "_"), "._-")
	if s == "" {
		return "unknown"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// Stats reports what the spool has written and dropped so far.
func (s *Spool) Stats() Stats {
	return Stats{
		Lines:        s.lines.Load(),
		Bytes:        s.bytes.Load(),
		Dropped:      s.dropped.Load(),
		Oversized:    s.oversized.Load(),
		Malformed:    s.malformed.Load(),
		SyncTimeouts: s.syncTimeouts.Load(),
	}
}

// write appends one export request as one line, in a single write so a crash leaves at most an
// incomplete last line, which readers ignore.
func (s *Spool) write(line []byte) {
	if len(line)+1 > s.opts.maxLine {
		s.oversized.Add(1)
		return
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	n := int64(len(buf))

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.total()+n > s.opts.maxTotal {
		s.dropped.Add(1)
		return
	}
	if s.f != nil && s.size > 0 && s.size+n > s.opts.maxFile {
		s.rotate()
	}
	if s.f == nil {
		if err := s.create(); err != nil {
			s.dropped.Add(1)
			return
		}
	}
	w, err := s.f.Write(buf)
	s.size += int64(w)
	if err != nil {
		s.dropped.Add(1)
		// A partial line cannot be completed safely; start a fresh file for the next record.
		s.rotate()
		return
	}
	s.lines.Add(1)
	s.bytes.Add(uint64(w))
}

// total is what this process's files hold now. Closed files a reader has removed no longer count.
func (s *Spool) total() int64 {
	t := s.size
	kept := s.finished[:0]
	for _, p := range s.finished {
		if fi, err := os.Lstat(p); err == nil {
			t += fi.Size()
			kept = append(kept, p)
		}
	}
	s.finished = kept
	return t
}

func (s *Spool) create() error {
	for range 100 {
		s.seq++
		p := filepath.Join(s.dir, fmt.Sprintf("%s-%06d%s", s.prefix, s.seq, OpenSuffix))
		f, err := createExclusive(p)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		s.f, s.path, s.size = f, p, 0
		return nil
	}
	return errors.New("spool: no free file name")
}

// rotate closes the active file and renames it to ClosedSuffix. Called with mu held.
func (s *Spool) rotate() {
	if s.f == nil {
		return
	}
	s.syncFile(context.Background(), s.f)
	_ = s.f.Close()
	done := strings.TrimSuffix(s.path, OpenSuffix) + ClosedSuffix
	if s.size == 0 {
		_ = os.Remove(s.path)
	} else if err := os.Rename(s.path, done); err == nil {
		s.finished = append(s.finished, done)
	} else {
		s.finished = append(s.finished, s.path)
	}
	s.f, s.path, s.size = nil, "", 0
}

// Sync flushes the active file to disk, so records already written survive the host crashing.
// Call it after flushing the providers at the end of each unit of work. It waits at most the
// sync timeout, or until ctx is done, and never fails: an abandoned flush is counted.
func (s *Spool) Sync(ctx context.Context) {
	s.mu.Lock()
	f := s.f
	s.mu.Unlock()
	if f != nil {
		s.syncFile(ctx, f)
	}
}

func (s *Spool) syncFile(ctx context.Context, f *os.File) {
	done := make(chan struct{})
	go func() {
		_ = f.Sync()
		close(done)
	}()
	t := time.NewTimer(s.opts.syncTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		s.syncTimeouts.Add(1)
	case <-ctx.Done():
		s.syncTimeouts.Add(1)
	}
}

// Close flushes and closes the active file and renames it, so a reader may remove it once
// shipped. Call it after the providers have shut down; records exported afterwards are dropped.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.rotate()
	return nil
}

var errUnknownSignal = errors.New("spool: unknown signal")
