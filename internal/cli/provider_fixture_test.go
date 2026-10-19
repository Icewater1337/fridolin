package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// versionCLI writes a stand-in for the Claude CLI that answers --version
// with out, and returns its path.
func versionCLI(t *testing.T, path, out string) string {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "claude")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
