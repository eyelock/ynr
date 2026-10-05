// Package ui is the local dashboard (ADR-005): server-rendered pages, written with templ and made
// live with htmx, each fragment from a named query. It is off by default; ynr serve --ui turns it
// on, bound to loopback. Its scripts and styles are embedded, so it works offline.
package ui

//go:generate go run github.com/a-h/templ/cmd/templ@v0.3.1070 generate

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/tail"
)

//go:embed static
var static embed.FS

// Config is what the dashboard reads.
type Config struct {
	Runner query.Runner
	// Spool is the spool root the live tail follows.
	Spool string
	Now   func() time.Time
}

// Handler serves the dashboard.
func Handler(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(time.Hour, http.FileServerFS(sub))))
	mux.HandleFunc("GET /{$}", s.lanes)
	mux.HandleFunc("GET /items", s.items)
	mux.HandleFunc("GET /item", s.item)
	mux.HandleFunc("GET /trace/{id}", s.trace)
	mux.HandleFunc("GET /tail", s.tailPage)
	mux.HandleFunc("GET /tail/stream", s.tailStream)
	return guard(mux)
}

type server struct{ cfg Config }

// guard answers only requests addressed to this machine, so a web page elsewhere cannot reach
// the dashboard by rebinding its own name to 127.0.0.1, and sets a policy that runs only the
// dashboard's own scripts.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "the dashboard answers only on this machine's loopback address", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

// windows are the windows the pages offer.
var windows = []string{"1h", "24h", "7d", "30d"}

func window(r *http.Request, def string) string {
	w := r.URL.Query().Get("since")
	for _, ok := range windows {
		if w == ok {
			return w
		}
	}
	return def
}

// ask runs a named query with the page's inputs.
func (s *server) ask(ctx context.Context, name string, raw query.Raw) (*query.Result, error) {
	q, ok := query.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("no query %s", name)
	}
	p, err := q.Resolve(raw, s.cfg.Now())
	if err != nil {
		return nil, err
	}
	return s.cfg.Runner.Run(ctx, q, p)
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	var b bytes.Buffer
	if err := c.Render(r.Context(), &b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}

func fail(w http.ResponseWriter, r *http.Request, title string, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	render(w, r, errorPage(title, err.Error()))
}

func (s *server) lanes(w http.ResponseWriter, r *http.Request) {
	since := window(r, "24h")
	runs, err := s.ask(r.Context(), "runs", query.Raw{Since: since})
	if err != nil {
		fail(w, r, "Lanes", err)
		return
	}
	cost, err := s.ask(r.Context(), "cost", query.Raw{Since: since})
	if err != nil {
		fail(w, r, "Lanes", err)
		return
	}
	recent, err := s.ask(r.Context(), "recent", query.Raw{Since: since})
	if err != nil {
		fail(w, r, "Lanes", err)
		return
	}
	render(w, r, lanesPage(buildLanes(since, runs, cost, recent)))
}

func (s *server) items(w http.ResponseWriter, r *http.Request) {
	since := window(r, "7d")
	res, err := s.ask(r.Context(), "items", query.Raw{Since: since})
	if err != nil {
		fail(w, r, "Items", err)
		return
	}
	render(w, r, itemsPage(since, table(res)))
}

func (s *server) item(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	since := window(r, "7d")
	if key == "" {
		http.Redirect(w, r, "/items", http.StatusSeeOther)
		return
	}
	res, err := s.ask(r.Context(), "item", query.Raw{Arg: key, Since: since})
	if err != nil {
		fail(w, r, "Item", err)
		return
	}
	render(w, r, itemPage(key, since, table(res)))
}

func (s *server) trace(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	since := window(r, "7d")
	res, err := s.ask(r.Context(), "trace", query.Raw{Arg: id, Since: since})
	if err != nil {
		fail(w, r, "Trace", err)
		return
	}
	render(w, r, tracePage(buildTrace(id, res)))
}

func (s *server) tailPage(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	render(w, r, tailPage(v.Get("service"), v.Get("item")))
}

// tailStream sends the live tail as server-sent events, each a rendered table row.
func (s *server) tailStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	v := r.URL.Query()
	o := tail.Options{Root: s.cfg.Spool, Poll: 500 * time.Millisecond,
		Filter: tail.Filter{Service: v.Get("service"), Item: v.Get("item")}}
	_ = tail.Stream(r.Context(), o, func(rec tail.Record) error {
		var b bytes.Buffer
		if err := tailRow(rec).Render(r.Context(), &b); err != nil {
			return err
		}
		row := strings.ReplaceAll(b.String(), "\n", " ")
		if _, err := fmt.Fprintf(w, "event: record\ndata: %s\n\n", row); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
}
