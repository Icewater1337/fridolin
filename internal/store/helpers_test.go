package store

import (
	"log"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/logx"
)

func openTestStore(t *testing.T, dbPath string) *Store {
	t.Helper()
	s, err := Open(dbPath, "error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func captureLog(t *testing.T) func() []string {
	t.Helper()
	ring := logx.NewRing(50)
	prev := log.Writer()
	log.SetOutput(ring)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		lines, _ := ring.Since(0, 50)
		var out []string
		for _, line := range lines {
			out = append(out, line.Text)
		}
		return out
	}
}

func logContains(logs func() []string, text string) bool {
	return slices.ContainsFunc(logs(), func(line string) bool { return strings.Contains(line, text) })
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}
