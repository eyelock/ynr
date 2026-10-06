package conformance

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var canaryRef = regexp.MustCompile(`@canary:([A-Za-z]+)`)

// canaryRefs lists the kinds a command names.
func canaryRefs(cmd string) []string {
	var out []string
	for _, m := range canaryRef.FindAllStringSubmatch(cmd, -1) {
		out = append(out, m[1])
	}
	return out
}

// canaries are the unique strings planted for one scenario, by kind.
type canaries map[string]string

// newCanaries makes a fresh, unguessable string for each kind. A secret looks like one, so the
// tool's redaction has something to recognise.
func newCanaries() canaries {
	c := canaries{}
	for _, k := range CanaryKinds {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		v := "ynrcanary" + k + hex.EncodeToString(b)
		if k == "secret" {
			v = "sk-" + v
		}
		c[k] = v
	}
	return c
}

// substitute replaces each @canary:<kind> in cmd.
func (c canaries) substitute(cmd string) string {
	return canaryRef.ReplaceAllStringFunc(cmd, func(m string) string {
		return c[strings.TrimPrefix(m, "@canary:")]
	})
}

// planted is the kinds a command uses, so only those are searched for.
func (c canaries) planted(cmd string) map[string]string {
	out := map[string]string{}
	for _, k := range canaryRefs(cmd) {
		out[k] = c[k]
	}
	return out
}
