package textx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// Cutting mid-rune would emit invalid UTF-8 into the activity log.
	got := Truncate("héllo wörld", 2)
	if !strings.ContainsRune(got, '…') {
		t.Errorf("expected an ellipsis, got %q", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Errorf("truncate produced invalid UTF-8: %q", got)
		}
	}
	if Truncate("short", 100) != "short" {
		t.Error("truncate altered a string under the limit")
	}
}

// Tail keeps the end of a command's output for an error message. Cutting
// mid-rune would put invalid UTF-8 into the log and the dashboard.
func TestTailDoesNotSplitRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"  short\n", 100, "short"},
		{"exactly", 7, "exactly"},
		{"abcdef", 3, "…def"},
		{"ééé", 4, "…éé"},
		{"ééé", 3, "…é"}, // the last 3 bytes start inside an é
		{"x 🙂", 3, "…"},  // so do the last 3 bytes here, of the 4-byte 🙂
	}
	for _, c := range cases {
		got := Tail(c.in, c.n)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("tail(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
