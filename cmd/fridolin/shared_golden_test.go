package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertGolden compares got with the golden file name (a path below
// goldenDir), or writes it there with -update.
func sharedAssertGolden(t testing.TB, root, name string, got []byte, update bool) {
	t.Helper()
	// The tests run in an empty directory whose testdata links to the
	// package's (linkTestdata), so the path is the same either way.
	path := filepath.Join(root, filepath.FromSlash(name))
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s: %v\nwrite it with: go test -run '%s' -update .", name, err, testPattern(t))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (-want +got):\n%s\nIf the change is intended, rewrite it with: go test -run '%s' -update .",
			name, lineDiff(string(want), string(got)), testPattern(t))
	}
}

// testPattern is a -run pattern for the top-level test t belongs to.
func testPattern(t testing.TB) string {
	top, _, _ := strings.Cut(t.Name(), "/")
	return "^" + top + "$"
}

// lineDiff shows the lines of want and got that differ, with a line of
// context, as -want and +got. It is a plain longest-common-subsequence
// diff: golden files are small.
func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	// lcs[i][j] is the length of the longest common subsequence of a[i:]
	// and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte // ' ', '-' or '+'
		text string
	}
	var lines []line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			lines = append(lines, line{' ', a[i]})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, line{'-', a[i]})
			i++
		default:
			lines = append(lines, line{'+', b[j]})
			j++
		}
	}
	var sb strings.Builder
	shown, last := 0, -2
	for k, l := range lines {
		near := func(k int) bool { return k >= 0 && k < len(lines) && lines[k].op != ' ' }
		if l.op == ' ' && !near(k-1) && !near(k+1) {
			continue
		}
		if shown == 60 {
			sb.WriteString("…\n")
			break
		}
		if last >= 0 && k > last+1 {
			sb.WriteString("@@\n")
		}
		fmt.Fprintf(&sb, "%c%s\n", l.op, l.text)
		shown, last = shown+1, k
	}
	return sb.String()
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc\nd\ne\n", "a\nB\nc\nd\ne\n")
	if want := " a\n-b\n+B\n c\n"; got != want {
		t.Errorf("diff = %q, want %q", got, want)
	}
}
