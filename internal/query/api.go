package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Runner runs named queries: the hot tier in a running ynr serve.
type Runner interface {
	Run(ctx context.Context, q *Query, p Params) (*Result, error)
}

// Doc is a query's answer as JSON, the same from ynr query and the server's endpoint (FR-13).
// Rows are objects in the order of Columns.
type Doc struct {
	Query   string           `json:"query"`
	Arg     string           `json:"arg,omitempty"`
	Lane    string           `json:"lane,omitempty"`
	Since   time.Time        `json:"since"`
	Until   time.Time        `json:"until"`
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// Raw are a query's inputs as a person or a URL gives them.
type Raw struct {
	Arg, Since, Until, Lane string
}

// Resolve turns raw inputs into params, measuring durations back from now and defaulting the
// window to the query's own.
func (q *Query) Resolve(raw Raw, now time.Time) (Params, error) {
	now = now.UTC()
	p := Params{Arg: raw.Arg, Lane: raw.Lane, Since: now.Add(-q.Since), Until: now}
	var err error
	if raw.Since != "" {
		if p.Since, err = ParseWhen(raw.Since, now); err != nil {
			return p, fmt.Errorf("--since: %w", err)
		}
	}
	if raw.Until != "" {
		if p.Until, err = ParseWhen(raw.Until, now); err != nil {
			return p, fmt.Errorf("--until: %w", err)
		}
	}
	return p, q.Check(p)
}

// ParseWhen reads a duration back from now, allowing days (7d), or an RFC 3339 time.
func ParseWhen(s string, now time.Time) (time.Time, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if v, err := strconv.Atoi(n); err == nil && v >= 0 {
			return now.Add(-time.Duration(v) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return time.Time{}, errors.New("give a duration back from now, without a sign")
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is neither a duration (24h, 7d) nor an RFC 3339 time", s)
}

// EncodeJSON writes a query's answer, each row an object in column order.
func EncodeJSON(w io.Writer, q *Query, p Params, res *Result) error {
	head, err := json.Marshal(struct {
		Query   string    `json:"query"`
		Arg     string    `json:"arg,omitempty"`
		Lane    string    `json:"lane,omitempty"`
		Since   time.Time `json:"since"`
		Until   time.Time `json:"until"`
		Columns []string  `json:"columns"`
	}{q.Name, p.Arg, p.Lane, p.Since, p.Until, res.Columns})
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(head[:len(head)-1])
	b.WriteString(`,"rows":[`)
	for i, row := range res.Rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		for j, c := range res.Columns {
			if j > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(c)
			v, err := json.Marshal(row[j])
			if err != nil {
				return fmt.Errorf("column %s: %w", c, err)
			}
			b.Write(k)
			b.WriteByte(':')
			b.Write(v)
		}
		b.WriteByte('}')
	}
	b.WriteString("]}\n")
	_, err = w.Write(b.Bytes())
	return err
}

// DecodeJSON reads a query's answer back into a result, its numbers as json.Number.
func DecodeJSON(r io.Reader) (*Doc, *Result, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var doc Doc
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, err
	}
	res := &Result{Columns: doc.Columns}
	for _, row := range doc.Rows {
		vals := make([]any, len(doc.Columns))
		for i, c := range doc.Columns {
			vals[i] = row[c]
		}
		res.Rows = append(res.Rows, vals)
	}
	return &doc, res, nil
}

// Handler serves the named queries as JSON (ADR-005):
//
//	GET /api/queries                                         the named queries
//	GET /api/query/<name>?arg=&since=&until=&lane=           one query's answer
func Handler(r Runner, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/queries", func(w http.ResponseWriter, _ *http.Request) {
		type entry struct {
			Name  string `json:"name"`
			Help  string `json:"help"`
			Arg   string `json:"arg,omitempty"`
			Since string `json:"since"`
		}
		var out []entry
		for _, n := range Names() {
			q, _ := Lookup(n)
			out = append(out, entry{q.Name, q.Help, q.Arg, q.Since.String()})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/query/{name}", func(w http.ResponseWriter, req *http.Request) {
		q, ok := Lookup(req.PathValue("name"))
		if !ok {
			fail(w, http.StatusNotFound, fmt.Errorf("no query %q", req.PathValue("name")))
			return
		}
		v := req.URL.Query()
		p, err := q.Resolve(Raw{Arg: v.Get("arg"), Since: v.Get("since"), Until: v.Get("until"), Lane: v.Get("lane")}, now())
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		res, err := r.Run(req.Context(), q, p)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		var b bytes.Buffer
		if err := EncodeJSON(&b, q, p, res); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b.Bytes())
	})
	return mux
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// ErrNoServer means nothing answers on the socket, so the caller reads the store itself.
var ErrNoServer = errors.New("no ynr serve is answering queries")

// Ask asks the ynr serve listening on a Unix socket for a query's answer. A bad request comes
// back as an error carrying the server's message.
func Ask(ctx context.Context, socket, name string, raw Raw) (*Doc, *Result, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	v := url.Values{}
	for k, s := range map[string]string{"arg": raw.Arg, "since": raw.Since, "until": raw.Until, "lane": raw.Lane} {
		if s != "" {
			v.Set(k, s)
		}
	}
	u := "http://ynr/api/query/" + url.PathEscape(name) + "?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return nil, nil, ErrNoServer
		}
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, nil, &ServerError{Status: resp.StatusCode, Message: e.Error}
	}
	return DecodeJSON(resp.Body)
}

// ServerError is a query the server refused or failed.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string { return e.Message }
