package spoolreceiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
	"github.com/eyelock/ynr/internal/store"
)

// NewFactory returns the receiver's factory. One spool feeds all three signals, so the three
// pipelines share one receiver per configuration.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(Type, createDefaultConfig,
		receiver.WithTraces(createTraces, component.StabilityLevelDevelopment),
		receiver.WithLogs(createLogs, component.StabilityLevelDevelopment),
		receiver.WithMetrics(createMetrics, component.StabilityLevelDevelopment),
	)
}

var (
	sharedMu sync.Mutex
	shared   = map[*Config]*spoolReceiver{}
)

func get(set receiver.Settings, cfg component.Config) *spoolReceiver {
	c := cfg.(*Config)
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if r, ok := shared[c]; ok {
		return r
	}
	r := &spoolReceiver{cfg: c, logger: set.Logger}
	shared[c] = r
	return r
}

func createTraces(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Traces) (receiver.Traces, error) {
	r := get(set, cfg)
	r.traces = next
	return r, nil
}

func createLogs(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Logs) (receiver.Logs, error) {
	r := get(set, cfg)
	r.logs = next
	return r, nil
}

func createMetrics(_ context.Context, set receiver.Settings, cfg component.Config, next consumer.Metrics) (receiver.Metrics, error) {
	r := get(set, cfg)
	r.metrics = next
	return r, nil
}

type spoolReceiver struct {
	cfg    *Config
	logger *zap.Logger

	traces  consumer.Traces
	logs    consumer.Logs
	metrics consumer.Metrics

	reader    *spool.Reader
	manifests map[string]manifestResult

	// Shipping to the store: each signal's stamped lines from the file being read.
	st                  store.Store
	bufs                map[string]*bytes.Buffer
	batches, forwardErr atomic.Int64

	startOnce, stopOnce sync.Once
	cancel              context.CancelFunc
	done                chan struct{}
	startErr            error
}

type manifestResult struct {
	m       *stamp.Manifest
	warning string
}

// Start begins polling. The Collector calls it once per pipeline; only the first starts.
func (r *spoolReceiver) Start(_ context.Context, _ component.Host) error {
	r.startOnce.Do(func() {
		reader, err := spool.NewReader(r.cfg.Root, r.cfg.MaxLine)
		if err != nil {
			r.startErr = err
			return
		}
		reader.RunUser = r.runUser
		if r.cfg.Store != "" {
			st, err := store.Open(r.cfg.Store)
			if err != nil {
				r.startErr = err
				return
			}
			r.st = st
			r.bufs = map[string]*bytes.Buffer{store.Traces: {}, store.Logs: {}, store.Metrics: {}}
			reader.Ship, reader.ShipAge, reader.ShipBytes = r.ship, r.cfg.ShipAge, r.cfg.ShipBytes
		}
		r.reader = reader
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel, r.done = cancel, make(chan struct{})
		go r.run(ctx)
	})
	return r.startErr
}

// Shutdown stops polling after the poll in progress, and logs the final counts.
func (r *spoolReceiver) Shutdown(context.Context) error {
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
			<-r.done
			if r.st != nil {
				// Ship whatever is waiting, however young, before stopping.
				r.reader.Force = true
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := r.PollOnce(ctx); err != nil {
					r.logger.Warn("final shipment incomplete; the spool keeps the rest", zap.Error(err))
				}
				cancel()
			}
			r.logCounts("spool reader stopped")
		}
		sharedMu.Lock()
		delete(shared, r.cfg)
		sharedMu.Unlock()
	})
	return nil
}

func (r *spoolReceiver) run(ctx context.Context) {
	defer close(r.done)
	wait := r.cfg.PollInterval
	last := spool.Snapshot{}
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	for {
		if err := r.PollOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("spool poll stopped early; retrying", zap.Error(err), zap.Duration("in", wait))
			wait = min(wait*2, 30*time.Second)
		} else {
			wait = r.cfg.PollInterval
		}
		before := r.reader.Counters.Evicted.Load()
		if err := r.reader.Evict(r.cfg.SpoolCap); err != nil && ctx.Err() == nil {
			r.logger.Warn("evicting from the spool", zap.Error(err))
		}
		if n := r.reader.Counters.Evicted.Load() - before; n > 0 {
			r.logger.Warn("ynr.spool.evicted: the spool reached its cap, so its oldest closed files were deleted unshipped",
				zap.Int64("files", n), zap.Int64("total_files", r.reader.Counters.Evicted.Load()),
				zap.Int64("total_bytes", r.reader.Counters.EvictedBytes.Load()), zap.Int64("cap", r.cfg.SpoolCap))
		}
		select {
		case <-ctx.Done():
			return
		case <-report.C:
			if s := r.reader.Counters.Snapshot(); s != last {
				last = s
				r.logCounts("spool reader")
			}
		case <-time.After(wait):
		}
	}
}

// PollOnce reads the spool once. It is exported for tests.
func (r *spoolReceiver) PollOnce(ctx context.Context) error {
	r.manifests = map[string]manifestResult{}
	r.resetBufs()
	return r.reader.Poll(ctx, func(w spool.Writer, line []byte) error { return r.handle(ctx, w, line) })
}

func (r *spoolReceiver) logCounts(msg string) {
	s := r.reader.Counters.Snapshot()
	r.logger.Info(msg, zap.Int64("lines", s.Lines), zap.Int64("deleted_files", s.Deleted),
		zap.Int64("ignored", s.Ignored), zap.Int64("rejected", s.Rejected),
		zap.Int64("oversized", s.Oversized), zap.Int64("malformed", s.Malformed),
		zap.Int64("evicted_files", s.Evicted), zap.Int64("evicted_bytes", s.EvictedBytes),
		zap.Int64("batches_stored", r.batches.Load()), zap.Int64("forward_errors", r.forwardErr.Load()))
}

// probe finds which signal a line carries: each line is one OTLP export request.
type probe struct {
	Spans   json.RawMessage `json:"resourceSpans"`
	Logs    json.RawMessage `json:"resourceLogs"`
	Metrics json.RawMessage `json:"resourceMetrics"`
}

func (r *spoolReceiver) handle(ctx context.Context, w spool.Writer, line []byte) error {
	var p probe
	if err := json.Unmarshal(line, &p); err != nil {
		return spool.ErrMalformed
	}
	n := 0
	for _, f := range []json.RawMessage{p.Spans, p.Logs, p.Metrics} {
		if f != nil {
			n++
		}
	}
	if n != 1 {
		return spool.ErrMalformed
	}
	id := stamp.Identity{ID: r.cfg.CollectorID, Instance: r.cfg.CollectorInstance}
	mr := r.manifest(w)
	var err error
	switch {
	case p.Spans != nil:
		td, uerr := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
		if uerr != nil {
			return spool.ErrMalformed
		}
		for i := 0; i < td.ResourceSpans().Len(); i++ {
			stamp.Resource(td.ResourceSpans().At(i).Resource().Attributes(), w, id, mr.m, mr.warning)
		}
		if err := r.buffer(store.Traces, func() ([]byte, error) { return (&ptrace.JSONMarshaler{}).MarshalTraces(td) }); err != nil {
			return err
		}
		err = r.traces.ConsumeTraces(ctx, td)
	case p.Logs != nil:
		ld, uerr := (&plog.JSONUnmarshaler{}).UnmarshalLogs(line)
		if uerr != nil {
			return spool.ErrMalformed
		}
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			stamp.Resource(ld.ResourceLogs().At(i).Resource().Attributes(), w, id, mr.m, mr.warning)
		}
		if err := r.buffer(store.Logs, func() ([]byte, error) { return (&plog.JSONMarshaler{}).MarshalLogs(ld) }); err != nil {
			return err
		}
		err = r.logs.ConsumeLogs(ctx, ld)
	default:
		md, uerr := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(line)
		if uerr != nil {
			return spool.ErrMalformed
		}
		for i := 0; i < md.ResourceMetrics().Len(); i++ {
			stamp.Resource(md.ResourceMetrics().At(i).Resource().Attributes(), w, id, mr.m, mr.warning)
		}
		if err := r.buffer(store.Metrics, func() ([]byte, error) { return (&pmetric.JSONMarshaler{}).MarshalMetrics(md) }); err != nil {
			return err
		}
		err = r.metrics.ConsumeMetrics(ctx, md)
	}
	if r.st != nil && err != nil {
		// With a store, the store is what commits; the pipeline is a best-effort copy, so a slow
		// or absent upstream never holds the spool back.
		r.forwardErr.Add(1)
		return nil
	}
	if err != nil && consumererror.IsPermanent(err) {
		// The destination will never accept this data; retrying would block the spool forever.
		r.logger.Warn("dropping a line the pipeline permanently rejected", zap.String("writer", w.Rel), zap.Error(err))
		return spool.ErrMalformed
	}
	return err
}

// runUser is the run's user from its manifest, for the reader's ownership check.
func (r *spoolReceiver) runUser(w spool.Writer) (uint32, bool) {
	if mr := r.manifest(w); mr.m != nil && mr.m.UID != nil {
		return *mr.m.UID, true
	}
	return 0, false
}

// manifest returns the run's manifest, read once per poll.
func (r *spoolReceiver) manifest(w spool.Writer) manifestResult {
	if w.Class != spool.Run {
		return manifestResult{}
	}
	if mr, ok := r.manifests[w.Name]; ok {
		return mr
	}
	var mr manifestResult
	b, err := spool.ReadManifest(r.cfg.Root, w.Name)
	switch {
	case err != nil && errors.Is(err, spool.ErrRejected):
		mr.warning = stamp.BadManifest
	case err != nil:
		mr.warning = stamp.NoManifest
	default:
		if m, ok := stamp.ParseManifest(b, w.Name); ok {
			mr.m = m
		} else {
			mr.warning = stamp.BadManifest
		}
	}
	r.manifests[w.Name] = mr
	return mr
}

// buffer adds a stamped line to its signal's batch, when shipping to a store.
func (r *spoolReceiver) buffer(signal string, marshal func() ([]byte, error)) error {
	if r.st == nil {
		return nil
	}
	b, err := marshal()
	if err != nil {
		return spool.ErrMalformed
	}
	buf := r.bufs[signal]
	buf.Write(b)
	buf.WriteByte('\n')
	return nil
}

func (r *spoolReceiver) resetBufs() {
	for _, b := range r.bufs {
		b.Reset()
	}
}

// ship stores one file's new lines as one compressed batch per signal, named by the file and
// byte range they came from (ADR-005). The reader commits the file's position only if this
// returns nil; a batch stored before a later one failed is re-shipped, and recognised by its
// source, so nothing is counted twice.
func (r *spoolReceiver) ship(_ spool.Writer, source string, from, to int64) error {
	defer r.resetBufs()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()
	for _, signal := range []string{store.Traces, store.Logs, store.Metrics} {
		buf := r.bufs[signal]
		if buf.Len() == 0 {
			continue
		}
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		if _, err := zw.Write(buf.Bytes()); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		key := store.BatchKey(signal, now, r.cfg.CollectorID, store.NewULID(now), source, from, to)
		if err := r.st.Put(ctx, key, gz.Bytes()); err != nil {
			return err
		}
		r.batches.Add(1)
	}
	return nil
}
