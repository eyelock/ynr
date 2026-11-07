package query

import (
	"fmt"
	"regexp"
	"strings"
)

// The label grammar of ADR-006 rule 10: a key and a value a user chose, never content.
var (
	labelKey   = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)
	labelValue = regexp.MustCompile(`^[A-Za-z0-9._/#-]{1,64}$`)
)

// CheckLabel checks one --label argument, written key:value.
func CheckLabel(l string) error {
	k, v, ok := strings.Cut(l, ":")
	if !ok {
		return fmt.Errorf("--label %q: write it key:value", l)
	}
	if !labelKey.MatchString(k) {
		return fmt.Errorf("--label %q: a key starts with a lower-case letter and holds up to 32 of a-z, 0-9, _, . and -", l)
	}
	if !labelValue.MatchString(v) || strings.HasPrefix(v, "/") {
		return fmt.Errorf("--label %q: a value is 1 to 64 of A-Z, a-z, 0-9, . _ / # and -, and does not start with /", l)
	}
	return nil
}

// LabelsParam joins labels into the text the queries' labels_match takes: empty for no filter.
func LabelsParam(labels []string) string { return strings.Join(labels, "\x1f") }
