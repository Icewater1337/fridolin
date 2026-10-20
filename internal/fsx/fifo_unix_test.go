//go:build unix

package fsx

import (
	"syscall"
	"testing"
)

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
