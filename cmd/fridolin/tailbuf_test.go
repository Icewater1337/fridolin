package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/textx"
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

// A CLI that floods stderr cannot make the bridge hold all of it: the
// run's error keeps a short tail, and the activity log gets the end, with
// the line that says what went wrong.
func TestRunStderrIsBounded(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 0, "")
	t.Setenv("FRIDOLIN_FAKE_STDERR_BYTES", "1000000")
	if _, _, _, err := mgr.Submit(dm("fail loudly")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the failed run", func() bool { return d.count() == 1 })
	res := d.all()[0].res
	if res.Err == nil || !strings.Contains(res.Err.Error(), "fatal: the last thing the CLI said") {
		t.Fatalf("err = %v, want the end of stderr", res.Err)
	}
	if len(res.Err.Error()) > 1000 {
		t.Errorf("the error holds %d bytes of stderr, want a short tail", len(res.Err.Error()))
	}
	data, err := os.ReadFile(filepath.Join(mgr.Get("a").Dir(), streamLogName))
	if err != nil {
		t.Fatal(err)
	}
	// The block names its run, as the run's heading does.
	if !strings.Contains(string(data), "stderr of the run for "+testDMChat+" from 15551234567, pid ") {
		t.Errorf("activity log = %q, want the stderr block to name its run", textx.Tail(string(data), 300))
	}
	if !strings.Contains(string(data), ":\n…") || !strings.Contains(string(data), "fatal: the last thing the CLI said") {
		t.Errorf("activity log = %q, want the end of stderr", textx.Tail(string(data), 300))
	}
	if len(data) > 2*stderrLogBytes {
		t.Errorf("activity log holds %d bytes, want at most the last %d of stderr", len(data), stderrLogBytes)
	}
}
