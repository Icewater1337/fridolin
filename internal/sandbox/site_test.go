package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// Optional software prefixes have always been included by directory-entry
// existence. A dangling symlink is still such an entry: resolving it during
// this check silently changes the set of profile rules after extraction.
func TestSiteSoftwareReadPathIncludesDanglingSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	link := filepath.Join(dir, "software")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	if !sandboxPathExists(link) {
		t.Fatal("dangling software-prefix entry was omitted")
	}
	if sandboxPathExists(missing) {
		t.Fatal("missing entry was included")
	}
}
