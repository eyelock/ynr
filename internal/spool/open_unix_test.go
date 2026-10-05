package spool

import (
	"os"
	"path/filepath"
	"testing"
)

// openSafe accepts a file owned by any of the given owners, so a run that writes as its image's
// user is read once its manifest names that user, and refuses one owned by anyone else.
func TestOpenSafe_OwnersAndDevice(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	me, dev, err := dirOwnerAndDev(dir)
	if err != nil {
		t.Fatal(err)
	}
	other := me + 1
	tests := []struct {
		name   string
		owners []uint32
		dev    uint64
		ok     bool
	}{
		{"folder owner", []uint32{me}, dev, true},
		{"run user named by the manifest", []uint32{other, me}, dev, true},
		{"nobody it knows", []uint32{other}, dev, false},
		{"another device than its folder", []uint32{me}, dev + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _, err := openSafe(p, tt.owners, tt.dev)
			if f != nil {
				_ = f.Close()
			}
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}
