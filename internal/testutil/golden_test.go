package testutil

import "testing"

func TestLineDiff(t *testing.T) {
	got := LineDiff("a\nb\nc\nd\ne\n", "a\nB\nc\nd\ne\n")
	if want := " a\n-b\n+B\n c\n"; got != want {
		t.Errorf("diff = %q, want %q", got, want)
	}
}
