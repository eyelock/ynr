package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AccountIDs names the attributes that identify an account. The relay never keeps them raw
// (ADR-006 rule 10): with a key it replaces each by a keyed one-way hash, and with none it drops
// them, since a plain hash of a guessable id can be reversed by trying ids.
var AccountIDs = []string{"user.id", "user.account_uuid", "user.account_id", "organization.id"}

const keyLen = 32

// hasher holds the relay's HMAC key. It is never written, logged or printed; only keyID, which
// cannot be turned back into the key, appears in the values it makes.
type hasher struct {
	key   []byte
	keyID string
}

func newHasher(key []byte) *hasher {
	// The key id is the HMAC of a fixed label under the key, so it is one-way and cannot be
	// mistaken for a hash of an id.
	m := hmac.New(sha256.New, key)
	m.Write([]byte("ynr relay key id"))
	return &hasher{key: key, keyID: hex.EncodeToString(m.Sum(nil))[:8]}
}

// hash returns "<key id>:<hex HMAC-SHA-256 of v>". The same v and key always give the same value.
func (h *hasher) hash(v string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(v))
	return h.keyID + ":" + hex.EncodeToString(m.Sum(nil))
}

// KeyID returns the public id of the key, for the relay's output and tests.
func (h *hasher) KeyID() string { return h.keyID }

// reason is the operating system's words for a failure, without the path.
func reason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// readKeyFile reads the HMAC key from path and refuses a file that is not safe to hold one. The
// key is 32 raw bytes, or 64 hex characters with an optional trailing newline. Errors never
// quote the file's content.
func readKeyFile(path string, spoolDirs ...string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("relay key file %s: %s", path, reason(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("relay key file %s must be a regular file, not a link or device", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("relay key file %s has mode %04o: group and others must not be able to read it (chmod 600)", path, fi.Mode().Perm())
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("relay key file %s: %s", path, reason(err))
	}
	for _, d := range spoolDirs {
		if d == "" {
			continue
		}
		if rd, err := filepath.EvalSymlinks(d); err == nil {
			d = rd
		}
		if rel, err := filepath.Rel(d, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("relay key file %s lies under the spool folder %s: keep the key outside the spool", path, d)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("relay key file %s: %s", path, reason(err))
	}
	if len(b) == keyLen {
		return b, nil
	}
	if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) == keyLen {
		return k, nil
	}
	return nil, fmt.Errorf("relay key file %s must hold %d bytes, raw or as %d hex characters", path, keyLen, 2*keyLen)
}
