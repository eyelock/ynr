package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Options configures a conformance run.
type Options struct {
	// File is the conformance file's path.
	File string
	// Dir is the folder it was invoked from: the registry path is relative to it, and
	// scenarios run with it as their working directory, and it is also in $YNR_CONFORMANCE_ROOT.
	Dir string
	// Timeout bounds one run of a scenario.
	Timeout time.Duration
	// FlushLimit is the tool's bound on its exit flush (rule 12, 2 seconds by default); the
	// slow runs may take this much longer than the normal one, plus Slack.
	FlushLimit time.Duration
	Slack      time.Duration
	// KillWait is how long the kill run waits for a started event before giving up.
	KillWait time.Duration
	// Env is set in every scenario's environment, after the inherited one: for tests, which
	// need not change their own.
	Env map[string]string
	// Keep leaves the temporary folders in place, and names them in the report.
	Keep bool
}

func (o *Options) defaults() {
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.FlushLimit <= 0 {
		o.FlushLimit = 2 * time.Second
	}
	if o.Slack <= 0 {
		o.Slack = time.Second
	}
	if o.KillWait <= 0 {
		o.KillWait = 15 * time.Second
	}
	if o.Dir == "" {
		o.Dir, _ = os.Getwd()
	}
}

// Run runs every scenario in the conformance file and checks what each wrote.
func Run(ctx context.Context, o Options) (*Report, error) {
	o.defaults()
	f, err := LoadFile(o.File)
	if err != nil {
		return nil, err
	}
	rep := &Report{File: o.File, Service: f.Service}

	var reg *Registry
	var regErr error
	if f.Registry != "" {
		p := f.Registry
		if !filepath.IsAbs(p) {
			p = filepath.Join(o.Dir, p)
		}
		reg, regErr = LoadRegistry(p)
	}

	vendorPath := ""
	var vendorNames []string
	if f.Vendor != "" {
		if p, err := exec.LookPath(f.Vendor); err == nil {
			vendorPath = p
			vendorNames = append([]string{f.Vendor}, f.VendorAliases...)
		} else {
			rep.Notes = append(rep.Notes, fmt.Sprintf("the stub vendor %q is not on the PATH: scenarios run without it", f.Vendor))
		}
	}

	root, err := os.MkdirTemp("", "ynr-conformance-")
	if err != nil {
		return nil, err
	}
	if o.Keep {
		rep.Notes = append(rep.Notes, "temporary folders kept in "+root)
	} else {
		defer func() { _ = os.RemoveAll(root) }()
	}

	for i, sc := range f.Scenarios {
		sb := &sandbox{
			root: filepath.Join(root, fmt.Sprintf("scenario-%d", i+1)), service: f.Service,
			vendorPath: vendorPath, vendorName: vendorNames, invoked: o.Dir, timeout: o.Timeout, killWait: o.KillWait, env: o.Env,
		}
		can := newCanaries()
		command := can.substitute(sc.Run)
		runs := map[string]*runOutcome{}
		sr := ScenarioReport{Name: sc.Name}
		for _, kind := range []string{runSpool, runEndpoint, runNeither, runUnwritable, runHung, runKill} {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if kind == runUnwritable && runs[runSpool] != nil {
				// A blocked tool never finishes: give it the normal time, the flush limit and
				// the slack, then a margin to be sure, and call it blocked.
				sb.limit12 = min(o.Timeout, runs[runSpool].Duration+o.FlushLimit+o.Slack+3*time.Second)
			}
			r, err := sb.execRun(ctx, kind, command)
			if err != nil {
				return nil, fmt.Errorf("scenario %q, run %s: %w", sc.Name, kind, err)
			}
			runs[kind] = r
			sr.Runs = append(sr.Runs, RunInfo{
				Run: kind, Exit: r.Exit, DurationMS: float64(r.Duration) / float64(time.Millisecond),
				TimedOut: r.TimedOut, Killed: r.Killed, SpoolRecs: len(r.Spool.Recs), EndpointRecs: len(r.Endpoint.Recs),
			})
		}
		k := &checker{f: f, sc: sc, o: o, reg: reg, regErr: regErr, runs: runs, planted: can.planted(sc.Run)}
		sr.Checks = k.all()
		sr.Review = append(reviewLines("spool", runs[runSpool].Spool), reviewLines("operator endpoint", runs[runEndpoint].Endpoint)...)
		rep.Scenarios = append(rep.Scenarios, sr)
	}
	rep.summarise()
	return rep, nil
}
