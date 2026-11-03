package query

import (
	"strings"
	"testing"
	"time"
)

func TestCheckLabel(t *testing.T) {
	for _, ok := range []string{"wheelhouse:ynw", "team:red", "batch:2026-11.03", "ticket:ynw/17#4", "a:b"} {
		if err := CheckLabel(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"wheelhouse", "Wheelhouse:a", ":a", "k:", "k:has space", "k:a@b.c", "k:/etc", "k:~home", "k:" + strings.Repeat("x", 65), strings.Repeat("k", 33) + ":v", "1k:v"} {
		if err := CheckLabel(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestResolveCarriesAndChecksLabels(t *testing.T) {
	q, _ := Lookup("recent")
	n := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	p, err := q.Resolve(Raw{Labels: []string{"wheelhouse:a", "team:red"}}, n)
	if err != nil || len(p.Labels) != 2 {
		t.Fatalf("%v %v", p.Labels, err)
	}
	if _, err := q.Resolve(Raw{Labels: []string{"nope"}}, n); err == nil {
		t.Error("an invalid label was accepted")
	}
	if got := LabelsParam([]string{"a:b", "c:d"}); got != "a:b\x1fc:d" {
		t.Errorf("param %q", got)
	}
}
