package history

import (
	"bytes"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// countingReader counts what is read of the history.
type countingReader struct {
	r    *bytes.Reader
	read int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += int64(n)
	return n, err
}

// The backward reader hands each line over once, newest first, however the
// blocks cut the file, and keeps going past a line too long to take.
func TestScanHistoryBackward(t *testing.T) {
	long := strings.Repeat("x", MaxLine+1)
	block := strings.Repeat("y", historyBlock-3) // with "\n", it ends a byte short of a block
	// several is a line across four blocks whose parts read differently,
	// so that parts joined out of order show.
	var several strings.Builder
	for i := 0; several.Len() < 3*historyBlock+100; i++ {
		fmt.Fprintf(&several, "%d,", i)
	}
	cases := []struct {
		name    string
		file    string
		budget  int64
		stopAt  string // visit returns false for this line
		want    []string
		skipped int
	}{
		{"lines, newest first", "a\nb\nc\n", 0, "", []string{"c", "b", "a"}, 0},
		{"no newline at the end", "a\nb", 0, "", []string{"b", "a"}, 0},
		{"blank lines and spaces", "\n a \n\n\r\nb\r\n", 0, "", []string{"b", "a"}, 0},
		{"empty", "", 0, "", nil, 0},
		{"stopped by visit", "a\nb\nc\n", 0, "b", []string{"c", "b"}, 0},
		{"a line across blocks", "first\n" + block + "zz\nlast\n", 0, "", []string{"last", block + "zz", "first"}, 0},
		{"a line across several blocks", "first\n" + several.String() + "\nlast\n", 0, "", []string{"last", several.String(), "first"}, 0},
		{"the first line across several blocks", several.String() + "\nlast", 0, "", []string{"last", several.String()}, 0},
		{"a long line is skipped", "old\n" + long + "\nnew\n", 0, "", []string{"new", "old"}, 1},
		{"a long first line", long + "\nnew\n", 0, "", []string{"new"}, 1},
		{"a long last line", "old\n" + long, 0, "", []string{"old"}, 1},
		{"the budget ends the reading", "old\n" + block + "\nnew\n", historyBlock, "", []string{"new"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			r := &countingReader{r: bytes.NewReader([]byte(c.file))}
			skipped, err := scanHistoryBackward(r, int64(len(c.file)), c.budget, func(line []byte) bool {
				got = append(got, string(line))
				return string(line) != c.stopAt
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") || skipped != c.skipped {
				short := func(ss []string) []string {
					var out []string
					for _, s := range ss {
						out = append(out, s[:min(len(s), 20)])
					}
					return out
				}
				t.Errorf("lines %q, skipped %d; want %q, %d", short(got), skipped, short(c.want), c.skipped)
			}
			if c.budget > 0 && r.read > c.budget {
				t.Errorf("read %d bytes, over the budget of %d", r.read, c.budget)
			}
		})
	}
}

// The prompt's quick look takes the lines that hold one of the chat's
// JIDs, and costs no allocation for each of the many lines it looks at.
func TestMentionsChat(t *testing.T) {
	// A JID longer than the 32 bytes Go converts on the stack.
	long := strings.Repeat("1", 40) + "@lid"
	want := mentionsChat([]string{"15551234567@s.whatsapp.net", long})
	cases := []struct {
		line string
		want bool
	}{
		{`{"chat":"` + "15551234567@s.whatsapp.net" + `","text":"hi"}`, true},
		{`{"chat":"` + long + `","text":"hi"}`, true},
		{`{"chat":"` + "120363000000000000@g.us" + `","text":"hi"}`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := want([]byte(c.line)); got != c.want {
			t.Errorf("mentionsChat(%q) = %v, want %v", c.line, got, c.want)
		}
	}
	line := []byte(`{"chat":"` + "120363000000000000@g.us" + `","text":"` + strings.Repeat("chatter ", 50) + `"}`)
	if n := testing.AllocsPerRun(100, func() { want(line) }); n != 0 {
		t.Errorf("%v allocations a line, want none", n)
	}
}

// A line of megabytes is copied once or twice, not again for each block it
// crosses: that cost about 136 MB for a line of 4 MB, on every reading
// that went past it.
func TestScanHistoryBackwardCopiesALongLineOnce(t *testing.T) {
	line := strings.Repeat("x", MaxLine-historyBlock)
	file := []byte("old\n" + line + "\nnew\n")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var got []int
	if _, err := scanHistoryBackward(bytes.NewReader(file), int64(len(file)), 0, func(l []byte) bool {
		got = append(got, len(l))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if !slices.Equal(got, []int{3, len(line), 3}) {
		t.Fatalf("line lengths %v, want 3, %d and 3", got, len(line))
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > uint64(4*len(line)) {
		t.Errorf("reading a line of %d KiB allocated %d KiB, want at most four times the line", len(line)>>10, n>>10)
	}
}

func TestHoldsID(t *testing.T) {
	line := `{"ts":"2026-10-18T10:00:00+02:00","from":"15551234567","chat":"15551234567@s.whatsapp.net","text":"hi","type":"in","id":"3EB0A"}` + "\n"
	filler := strings.Repeat(`{"text":"`+strings.Repeat("x", 1000)+`"}`+"\n", 100) // past one read
	cases := []struct {
		name, data, id string
		want           bool
	}{
		{"there", line, "3EB0A", true},
		{"after much else", filler + line, "3EB0A", true},
		{"absent", filler + line, "3EB0B", false},
		{"a prefix of another", line, "3EB0", false},
		{"only in a text", `{"text":"say \"id\":\"3EB0A\""}` + "\n", "3EB0A", false},
		{"empty", "", "3EB0A", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := holdsID(strings.NewReader(c.data), c.id); err != nil || got != c.want {
				t.Errorf("holdsID(%q) = %v, %v; want %v", c.id, got, err, c.want)
			}
			// A byte at a time, so the ID is cut by every read boundary.
			if got, err := holdsID(iotest.OneByteReader(strings.NewReader(c.data)), c.id); err != nil || got != c.want {
				t.Errorf("a byte at a time: holdsID(%q) = %v, %v; want %v", c.id, got, err, c.want)
			}
		})
	}
}
