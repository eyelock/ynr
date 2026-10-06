//go:build full

package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// chunk is how many batch files the hot tier reads in one statement.
const chunk = 256

// Hot is the hot tier (ADR-005): a local DuckDB database holding the store's records from the
// last window, fed from the store as batches land. It is a cache. It starts empty and fills
// from the store, so losing it loses nothing (NFR-19).
type Hot struct {
	db     *sql.DB
	r      store.Reader
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex // one Sync at a time
	seen    map[string]bool
	synced  time.Time // when the last Sync began; zero before the first
	evicted time.Time
	// Failed counts batch files the hot tier could not read; they are skipped, not retried.
	Failed int
}

// OpenHot creates the hot tier's database at path, replacing whatever was there.
func OpenHot(ctx context.Context, path string, r store.Reader, window time.Duration) (*Hot, error) {
	for _, p := range []string{path, path + ".wal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	setup := macros + batchSQL(nil) + `
CREATE TABLE spans AS FROM batch_spans;
CREATE TABLE logs AS FROM batch_logs;
CREATE TABLE metric_points AS FROM batch_metric_points;`
	if _, err := db.ExecContext(ctx, setup); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("hot tier: %w", err)
	}
	return &Hot{db: db, r: r, window: window, now: time.Now, seen: map[string]bool{}}, nil
}

// Close closes the database.
func (h *Hot) Close() error { return h.db.Close() }

// Run runs a named query on the hot tier. A window reaching back before what the hot tier holds
// reads the store directly instead.
func (h *Hot) Run(ctx context.Context, q *Query, p Params) (*Result, error) {
	if err := q.Check(p); err != nil {
		return nil, err
	}
	// A minute's grace, since a default window and the hot tier's are measured moments apart.
	if p.Since.Before(h.now().Add(-h.window - time.Minute)) {
		return Run(ctx, h.r, q, p)
	}
	return runOn(ctx, h.db, q, p)
}

// Sync reads the batches that have landed since the last Sync: the whole window the first time,
// then the hours since the last Sync began, with an hour's margin.
func (h *Hot) Sync(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now().UTC()
	from := now.Add(-h.window)
	if !h.synced.IsZero() {
		from = h.synced.Add(-time.Hour)
	}
	h.synced = now
	var all []time.Time
	for t := from.Truncate(time.Hour); !t.After(now); t = t.Add(time.Hour) {
		all = append(all, t)
	}
	var errs []error
	for _, sig := range signals {
		keys, err := h.newKeys(ctx, sig.name, all)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for len(keys) > 0 {
			n := min(chunk, len(keys))
			errs = append(errs, h.ingest(ctx, sig.name, keys[:n], now))
			keys = keys[n:]
		}
	}
	if now.Sub(h.evicted) >= time.Hour {
		errs = append(errs, h.evict(ctx, now))
		h.evicted = now
	}
	return errors.Join(errs...)
}

func (h *Hot) newKeys(ctx context.Context, signal string, hours []time.Time) ([]string, error) {
	var out []string
	for _, t := range hours {
		keys, err := h.r.List(ctx, HourPrefix(signal, t))
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if strings.HasSuffix(k, ".jsonl.gz") && !h.seen[k] {
				out = append(out, k)
			}
		}
	}
	return out, nil
}

// ingest adds the records in a signal's batch files that the hot tier does not hold yet. If the
// files cannot be read together, each is read alone, and one that still fails is counted and
// skipped, so a damaged file never blocks the rest.
func (h *Hot) ingest(ctx context.Context, signal string, keys []string, now time.Time) error {
	err := h.insert(ctx, signal, keys, now)
	if err == nil || len(keys) == 1 {
		if err != nil {
			h.Failed++
		}
		for _, k := range keys {
			h.seen[k] = true
		}
		return err
	}
	var errs []error
	for _, k := range keys {
		errs = append(errs, h.ingest(ctx, signal, []string{k}, now))
	}
	return errors.Join(errs...)
}

func (h *Hot) insert(ctx context.Context, signal string, keys []string, now time.Time) error {
	files := make([]string, len(keys))
	for i, k := range keys {
		files[i] = h.r.Location(k)
	}
	table := map[string]string{store.Traces: "spans", store.Logs: "logs", store.Metrics: "metric_points"}[signal]
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, batchSQL(map[string][]string{signal: files})); err != nil {
		return fmt.Errorf("hot tier: %w", err)
	}
	stmt := fmt.Sprintf(`INSERT INTO %[1]s
SELECT * FROM batch_%[1]s b
WHERE (b.time IS NULL OR b.time >= $since)
  AND NOT EXISTS (SELECT 1 FROM %[1]s t WHERE t.record_id = b.record_id)`, table)
	if _, err := conn.ExecContext(ctx, stmt, sql.Named("since", now.Add(-h.window))); err != nil {
		return fmt.Errorf("hot tier: reading %d %s batches: %w", len(keys), signal, err)
	}
	return nil
}

// evict drops records older than the window, and forgets batch files received before it.
func (h *Hot) evict(ctx context.Context, now time.Time) error {
	cut := now.Add(-h.window)
	for _, table := range []string{"spans", "logs", "metric_points"} {
		if _, err := h.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE time < $cut", sql.Named("cut", cut)); err != nil {
			return fmt.Errorf("hot tier: evicting: %w", err)
		}
	}
	oldest := HourPrefix("", cut.Add(-time.Hour))
	for k := range h.seen {
		// A key is <signal>/<yyyy>/<mm>/<dd>/<hh>/...: compare its hour with the cut.
		if i := strings.IndexByte(k, '/'); i > 0 && len(k) > i+14 && k[i:i+15] < oldest {
			delete(h.seen, k)
		}
	}
	return nil
}

// Count is how many records the hot tier holds of each kind, for ynr info and tests.
func (h *Hot) Count(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"spans", "logs", "metric_points"} {
		var n int64
		if err := h.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

// ServeHot runs the hot tier until ctx ends: it fills from the store every Poll and answers
// the named queries on a Unix socket only its owner can open (NFR-18).
func ServeHot(ctx context.Context, cfg HotConfig) error {
	h, err := OpenHot(ctx, cfg.Path, cfg.Store, cfg.Window)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ln, err := listenUnix(cfg.Socket)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(h, time.Now), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = os.Remove(cfg.Socket)
	}()
	tick := time.NewTicker(cfg.Poll)
	defer tick.Stop()
	for {
		if err := h.Sync(ctx); err != nil && ctx.Err() == nil {
			cfg.Logf("hot tier: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// listenUnix listens on a socket only the owner can connect to, replacing a stale one left by a
// process that died: ynr serve holds the spool's lock, so no live server owns it.
func listenUnix(socket string) (net.Listener, error) {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("hot tier: listening on %s: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
