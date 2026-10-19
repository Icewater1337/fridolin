package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailBuffer(t *testing.T) {
	cases := []struct {
		name   string
		limit  int
		writes []string
		want   string
	}{
		{"fits", 10, []string{"abc", "def"}, "abcdef"},
		{"exactly the limit", 6, []string{"abc", "def"}, "abcdef"},
		{"older bytes go first", 6, []string{"abcd", "efgh"}, "…cdefgh"},
		{"one write over the limit", 4, []string{"abcdefgh"}, "…efgh"},
		{"one write of the limit after others", 4, []string{"ab", "cdef"}, "…cdef"},
		{"a cut rune is dropped", 4, []string{"aaaa", "é" + "bcd"}, "…bcd"},
		{"nothing written", 4, nil, ""},
		{"many small writes", 4, strings.Split("abcdefghijklmnopqrstuvwxyz", ""), "…wxyz"},
		{"a write of the limit into a full buffer", 4, []string{"abcdef", "ghij"}, "…ghij"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newTailBuffer(c.limit)
			for _, w := range c.writes {
				if n, err := b.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write(%q) = %d, %v; want %d, nil", w, n, err, len(w))
				}
			}
			if got := b.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
			if !utf8.ValidString(b.String()) {
				t.Errorf("String() = %q is not valid UTF-8", b.String())
			}
		})
	}
}

// A flood of tiny writes into a full buffer does not move the kept bytes
// at every write: the buffer is cut back only once it holds twice the
// limit, so n writes move about n bytes in all, not n times the limit.
func TestTailBufferCutsSeldom(t *testing.T) {
	const limit = 100
	b := newTailBuffer(limit)
	cuts := 0
	for i := 0; i < 1000; i++ {
		before := len(b.buf)
		_, _ = b.Write([]byte{'a' + byte(i%26)})
		if len(b.buf) != before+1 {
			cuts++
		}
		if len(b.buf) > 2*limit {
			t.Fatalf("after %d writes the buffer holds %d bytes, want at most %d", i+1, len(b.buf), 2*limit)
		}
	}
	if cuts > 10 {
		t.Errorf("1000 one-byte writes cut the buffer %d times, want about one per %d writes", cuts, limit)
	}
	if got := b.String(); len(got) != len("…")+limit {
		t.Errorf("String() holds %d bytes, want the last %d", len(got), limit)
	}
}
