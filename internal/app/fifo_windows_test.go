package app

import "testing"

func mkfifo(t *testing.T, _ string) {
	t.Helper()
	t.Skip("Windows has no FIFOs in the file system")
}
