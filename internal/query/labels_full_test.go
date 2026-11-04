//go:build full

package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

const (
	traceC = "11111111111111111111111111111111"
	traceD = "22222222222222222222222222222222"
)

// labelled is a run of the Wheelhouse (and optionally team) given, labelled on the resource the
// way ynh stamps it (ADR-006 rule 10).
func labelled(trace, id, wheelhouse, team string, at time.Time) span {
	s := runSpan(trace, id, "", "converged", "m", 1, at)
	s.res["ynh.label.wheelhouse"] = wheelhouse
	if team != "" {
		s.res["ynh.label.team"] = team
	}
	return s
}

// twoWheelhouses is a spool with the records of two Wheelhouses side by side in local/, as ynr
// serve keeps them (wheelhouse:a and wheelhouse:b), plus one run with no labels at all.
func twoWheelhouses(t *testing.T) store.Reader {
	r := openStore(t)
	ship(t, r, store.Traces, now.Add(-20*time.Minute), "local.ynh-1-000001", traceLine(t,
		labelled(traceA, "1111111111111111", "a", "red", now.Add(-time.Hour)),
		labelled(traceB, "2222222222222222", "a", "blue", now.Add(-50*time.Minute)),
		labelled(traceC, "3333333333333333", "b", "red", now.Add(-40*time.Minute)),
		runSpan(traceD, "4444444444444444", "", "converged", "m", 1, now.Add(-30*time.Minute))))
	return r
}

func traceIDs(t *testing.T, res *Result) string {
	t.Helper()
	var ids []string
	c := col(t, res, "trace_id")
	for _, row := range res.Rows {
		ids = append(ids, fmt.Sprint(row[c]))
	}
	return strings.Join(ids, ",")
}

func TestLabelFiltersToOneWheelhouse(t *testing.T) {
	r := twoWheelhouses(t)
	if got := traceIDs(t, run(t, r, "recent", Params{})); strings.Count(got, ",") != 3 {
		t.Fatalf("no label should keep all four runs, got %s", got)
	}
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:a"}})); got != traceB+","+traceA {
		t.Errorf("wheelhouse:a = %s", got)
	}
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:b"}})); got != traceC {
		t.Errorf("wheelhouse:b = %s", got)
	}
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:c"}})); got != "" {
		t.Errorf("a wheelhouse with no runs = %q", got)
	}
}

func TestTwoLabelsCombineWithAnd(t *testing.T) {
	r := twoWheelhouses(t)
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:a", "team:red"}})); got != traceA {
		t.Errorf("wheelhouse:a and team:red = %s", got)
	}
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"team:red"}})); got != traceC+","+traceA {
		t.Errorf("team:red across both = %s", got)
	}
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:b", "team:blue"}})); got != "" {
		t.Errorf("a pair no run has = %s", got)
	}
}

func TestLabelFiltersTheOtherQueries(t *testing.T) {
	r := twoWheelhouses(t)
	res := run(t, r, "runs", Params{Labels: []string{"wheelhouse:b"}})
	if len(res.Rows) != 1 || res.Rows[0][col(t, res, "runs")] != int64(1) {
		t.Errorf("runs = %v", rows(res))
	}
	res = run(t, r, "cost", Params{Labels: []string{"wheelhouse:a"}})
	if len(res.Rows) != 1 || res.Rows[0][col(t, res, "runs")] != int64(2) {
		t.Errorf("cost = %v", rows(res))
	}
}

func TestLabelsColumnIsPopulated(t *testing.T) {
	r := twoWheelhouses(t)
	q := &Query{Name: "labels", Since: 24 * time.Hour, Signals: []string{store.Traces},
		SQL: `SELECT trace_id, labels['wheelhouse'] AS wheelhouse, labels['team'] AS team, cardinality(labels) AS n
FROM spans WHERE name = 'ynh.run' AND time >= $since AND time < $until ORDER BY time`}
	res, err := Run(context.Background(), r, q, Params{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range res.Rows {
		got = append(got, fmt.Sprint(row[1], "/", row[2], "/", row[3]))
	}
	if want := "[a/red/2 a/blue/2 b/red/2 <nil>/<nil>/0]"; fmt.Sprint(got) != want {
		t.Errorf("labels = %v, want %s", got, want)
	}
}

func TestARecordAttributeIsNotALabel(t *testing.T) {
	r := openStore(t)
	s := labelled(traceA, "1111111111111111", "a", "", now.Add(-time.Hour))
	s.attrs["ynh.label.wheelhouse"] = "ignored" // a record attribute is not a label
	ship(t, r, store.Traces, now.Add(-20*time.Minute), "local.ynh-1-000001", traceLine(t, s))
	if got := traceIDs(t, run(t, r, "recent", Params{Labels: []string{"wheelhouse:a"}})); got != traceA {
		t.Errorf("got %s", got)
	}
}

func TestLabelRefusedOverRollupWindows(t *testing.T) {
	q, _ := Lookup("runs")
	p := Params{Since: now.Add(-30 * 24 * time.Hour), Until: now, Labels: []string{"wheelhouse:a"}}
	if err := q.Check(p); err == nil || !strings.Contains(err.Error(), "--label cannot filter runs") {
		t.Errorf("err = %v", err)
	}
}

// TestServeHotFiltersByLabelOverTheSocket: --label travels to ynr serve (and so to ynr central,
// which answers on the same endpoint) and its hot tier answers with the labels applied.
func TestServeHotFiltersByLabelOverTheSocket(t *testing.T) {
	r := openStore(t)
	at := time.Now().UTC()
	ship(t, r, store.Traces, at, "local.a", traceLine(t,
		labelled(traceA, "1111111111111111", "a", "", at.Add(-2*time.Minute)),
		labelled(traceC, "3333333333333333", "b", "", at.Add(-time.Minute))))
	dir, err := os.MkdirTemp("", "ynr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "serve.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHot(ctx, HotConfig{Path: filepath.Join(dir, "hot.duckdb"), Socket: socket, Store: r,
			Window: 7 * 24 * time.Hour, Poll: 50 * time.Millisecond, Logf: t.Logf})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		doc, res, err := Ask(ctx, socket, "recent", Raw{Labels: []string{"wheelhouse:b"}})
		if err == nil && len(res.Rows) == 1 {
			if got := res.Rows[0][col(t, res, "trace_id")]; got != traceC {
				t.Fatalf("wheelhouse:b = %v", got)
			}
			if len(doc.Labels) != 1 || doc.Labels[0] != "wheelhouse:b" {
				t.Fatalf("the answer does not echo the labels: %+v", doc.Labels)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer: %v %v", err, res)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var se *ServerError
	if _, _, err := Ask(ctx, socket, "recent", Raw{Labels: []string{"no colon"}}); !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("an invalid label = %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
