package hostsecrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	key := bytes.Repeat([]byte{7}, 32)
	s := Sealed{Dir: dir, Key: key}
	if _, err := s.Get(ExportKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	const v = "test-value-not-a-secret"
	if err := s.Set(ExportKey, v); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ExportKey); err != nil || got != v {
		t.Fatalf("get: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ExportKey+".sealed"))
	if bytes.Contains(raw, []byte(v)) {
		t.Fatal("the value is on disk in clear")
	}
	// Moved to another name, it does not open as that one.
	if err := os.WriteFile(filepath.Join(dir, ResticPassword+".sealed"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ResticPassword); err == nil {
		t.Fatal("a file renamed to another secret opens")
	}
	if _, err := (Sealed{Dir: dir, Key: bytes.Repeat([]byte{8}, 32)}).Get(ExportKey); err == nil {
		t.Fatal("opens with another key")
	}
	if err := s.Delete(ExportKey); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Has(ExportKey); ok || err != nil {
		t.Fatalf("after delete: %v %v", ok, err)
	}
	if err := s.Delete(ExportKey); err != nil {
		t.Fatalf("deleting a missing one: %v", err)
	}
}
