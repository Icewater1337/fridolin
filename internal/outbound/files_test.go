package outbound

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/store"
)

// A shelf makes blobs of their own, names only those, and takes away the
// ones it is told to and those older than a time.
func TestShelf(t *testing.T) {
	s := Shelf{Dir: filepath.Join(t.TempDir(), "outbox-files")}
	f, name, err := s.Create(".PDF")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	g, other, err := s.Create("../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if !strings.HasSuffix(name, ".pdf") || strings.Contains(other, ".") || name == other {
		t.Errorf("blobs %q and %q", name, other)
	}
	p, err := s.Path(name)
	if err != nil || filepath.Dir(p) != s.Dir {
		t.Errorf("Path(%q) = %q, %v", name, p, err)
	}
	// Windows has no Unix permission bits: os.Stat reports 0666 there.
	if st, err := os.Stat(p); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("the blob: %v, %v", st, err)
	}
	for _, bad := range []string{"", "../x", "a/b", ".hidden", "/etc/passwd"} {
		if _, err := s.Path(bad); err == nil {
			t.Errorf("Path(%q) took it", bad)
		}
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	s.Prune(time.Now().Add(-2 * time.Hour))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the old blob stayed: %v", err)
	}
	s.Remove(other)
	s.Remove("../nothing")
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
	// A shelf without a folder keeps nothing.
	if _, _, err := (Shelf{}).Create(".pdf"); err == nil {
		t.Error("a shelf without a folder made a blob")
	}
	(Shelf{}).Prune(time.Now())
}

// The outbox takes a file only as a blob of its shelf.
func TestOutboxFileNeedsABlob(t *testing.T) {
	o := NewPausedOutbox(nil, nil, 0, nil)
	defer o.Stop()
	if err := o.File(context.Background(), "main", "chat", store.OutboxFile{Blob: "../x"}, ""); err == nil {
		t.Error("took a file without a shelf")
	}
}
