package relay

import (
	"bytes"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
)

func writeKey(t *testing.T, dir string, key []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "relay.key")
	if err := os.WriteFile(p, key, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, keyLen) }

// sendAccount runs a relay with cfg, sends one log request whose resource and record both carry
// the four account ids, and returns what reached the spool.
func sendAccount(t *testing.T, cfg Config) (spooled string, r *Relay) {
	t.Helper()
	cfg.Dir = t.TempDir()
	r, stop := start(t, cfg)
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("claude_code.api_request")
	for _, m := range []pcommon.Map{rl.Resource().Attributes(), lr.Attributes()} {
		m.PutStr("user.id", "u-1234")
		m.PutStr("user.account_uuid", "0b1c-uuid")
		m.PutStr("user.account_id", "acct_42")
		m.PutStr("organization.id", "org-7")
		m.PutStr("model", "claude")
	}
	body, _ := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if resp := post(t, r.Endpoint()+"/v1/logs", "application/x-protobuf", body, false); resp.StatusCode != 200 {
		t.Fatalf("logs: %d", resp.StatusCode)
	}
	stop()
	var all strings.Builder
	_ = filepath.WalkDir(cfg.Dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	return all.String(), r
}

func TestAccountIDs_HashedAndStable(t *testing.T) {
	keyDir := t.TempDir()
	kf := writeKey(t, keyDir, testKey(7), 0o600)
	a, r := sendAccount(t, Config{KeyFile: kf})
	// A second relay, a restart, with the same key gives the same values.
	b, _ := sendAccount(t, Config{KeyFile: kf})
	for _, raw := range []string{"u-1234", "0b1c-uuid", "acct_42", "org-7"} {
		if strings.Contains(a, raw) {
			t.Errorf("spool still carries %q: %s", raw, a)
		}
	}
	h := newHasher(testKey(7))
	for _, raw := range []string{"u-1234", "0b1c-uuid", "acct_42", "org-7"} {
		if !strings.Contains(a, h.hash(raw)) || !strings.Contains(b, h.hash(raw)) {
			t.Errorf("hash of %q missing or unstable", raw)
		}
	}
	if !strings.HasPrefix(h.hash("x"), h.KeyID()+":") || len(h.KeyID()) != 8 {
		t.Errorf("value %q does not carry the key id %q", h.hash("x"), h.KeyID())
	}
	if !strings.Contains(a, `"model"`) {
		t.Error("an unrelated attribute was lost")
	}
	if s := r.Stats(); s.Stripped != 0 {
		t.Errorf("hashing counted as stripping: %+v", s)
	}
}

func TestAccountIDs_DifferentKeysDiffer(t *testing.T) {
	h1, h2 := newHasher(testKey(1)), newHasher(testKey(2))
	if h1.hash("u-1") == h2.hash("u-1") || h1.KeyID() == h2.KeyID() {
		t.Error("two keys gave the same hash or key id")
	}
	if h1.hash("u-1") == h1.hash("u-2") {
		t.Error("two ids gave the same hash")
	}
}

func TestAccountIDs_NoKeyDrops(t *testing.T) {
	a, r := sendAccount(t, Config{})
	for _, bad := range []string{"u-1234", "0b1c-uuid", "acct_42", "org-7", `"user.id"`, `"organization.id"`} {
		if strings.Contains(a, bad) {
			t.Errorf("spool still carries %q: %s", bad, a)
		}
	}
	if !strings.Contains(a, `"model"`) {
		t.Error("an unrelated attribute was lost")
	}
	if s := r.Stats(); s.Stripped != 8 {
		t.Errorf("stripped = %d, want 8", s.Stripped)
	}
}

func TestAccountIDs_KeyNeverInSpool(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	kf := writeKey(t, t.TempDir(), key, 0o600)
	a, _ := sendAccount(t, Config{KeyFile: kf})
	if strings.Contains(a, string(key)) || strings.Contains(a, hex.EncodeToString(key)) {
		t.Error("the key reached the spool")
	}
}

func TestNew_RefusesBadKeyFile(t *testing.T) {
	keyDir := t.TempDir()
	spoolDir := t.TempDir()
	secret := "s3cret-s3cret-s3cret-s3cret-s3cr"
	inSpool := writeKey(t, spoolDir, testKey(3), 0o600)
	link := filepath.Join(keyDir, "link.key")
	_ = os.Symlink(writeKey(t, keyDir, testKey(3), 0o600), link)
	cases := map[string]struct {
		path string
		want string
	}{
		"group readable": {writeKey(t, t.TempDir(), testKey(3), 0o640), "group and others"},
		"world readable": {writeKey(t, t.TempDir(), testKey(3), 0o604), "group and others"},
		"under spool":    {inSpool, "outside the spool"},
		"too short":      {writeKey(t, t.TempDir(), []byte(secret[:31]), 0o600), "must hold 32 bytes"},
		"missing":        {filepath.Join(keyDir, "nope"), "no such file"},
		"symlink":        {link, "regular file"},
	}
	for name, c := range cases {
		_, err := New(Config{Dir: spoolDir, KeyFile: c.path})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), secret[:31]) {
			t.Errorf("%s: the error quotes the key", name)
		}
	}
}

func TestReadKeyFile_HexAndRaw(t *testing.T) {
	raw := testKey(9)
	hx := []byte(hex.EncodeToString(raw) + "\n")
	for name, content := range map[string][]byte{"raw": raw, "hex": hx} {
		got, err := readKeyFile(writeKey(t, t.TempDir(), content, 0o600))
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%s: %v %x", name, err, got)
		}
	}
}
