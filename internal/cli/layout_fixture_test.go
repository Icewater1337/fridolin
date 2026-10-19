package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFiles creates each path under root, a folder when it ends in "/".
func makeFiles(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}
