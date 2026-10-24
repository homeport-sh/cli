package buildcache

import (
	"bytes"
	"strings"
	"testing"
)

// The control plane reads a saved cache's lock from its first HeaderLen
// bytes (an object store's ranged read), without decompressing anything:
// the same header Pack writes and Unpack checks.
func TestReadLock(t *testing.T) {
	lock := strings.Repeat("c", 64)
	var buf bytes.Buffer
	if _, err := Pack(&buf, t.TempDir(), lock, MaxSaved); err != nil {
		t.Fatal(err)
	}
	if got, ok := ReadLock(buf.Bytes()[:HeaderLen]); !ok || got != lock {
		t.Errorf("lock %q %v", got, ok)
	}
	for name, b := range map[string][]byte{
		"empty":           nil,
		"cut short":       buf.Bytes()[:HeaderLen-1],
		"not a cache":     []byte(strings.Repeat("<html>", 20)),
		"another version": bytes.Replace(bytes.Clone(buf.Bytes()), []byte("homeport-cache 1"), []byte("homeport-cache 2"), 1),
		"a bad lock":      bytes.Replace(bytes.Clone(buf.Bytes()), []byte(lock), []byte(strings.Repeat("Z", 64)), 1),
	} {
		if _, ok := ReadLock(b); ok {
			t.Errorf("%s: read", name)
		}
	}
}

// Both sides hold a cache to the same limits: what a builder saves, the
// control plane keeps, and the links a job carries.
func TestLimits(t *testing.T) {
	if MaxSaved != 1<<30 || MaxRestore != 4 {
		t.Errorf("MaxSaved %d MaxRestore %d", MaxSaved, MaxRestore)
	}
}
