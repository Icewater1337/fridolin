package brain

import (
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/history"
)

// The answers on their way go among the history's lines by time, and one
// the history holds by then, delivered after they were read, only once.
func TestWithUndelivered(t *testing.T) {
	at := func(hhmmss string) string { return "2026-10-18T" + hhmmss + "+02:00" }
	in := func(ts, text string) history.Entry {
		return history.Entry{TS: at(ts), Chat: testDMChat, From: "15551234567", Text: text, Type: history.In}
	}
	out := func(ts, text string) history.Entry {
		return history.Entry{TS: at(ts), Chat: testDMChat, From: "main", Text: text, Type: history.Out}
	}
	cases := []struct {
		name             string
		history, pending []history.Entry
		want             []string
	}{
		{"nothing waits", []history.Entry{in("10:00:00", "a")}, nil, []string{"a"}},
		{"after what came before it", []history.Entry{in("10:00:00", "a"), in("10:00:01", "b")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "b", "A"}},
		{"before what came after it", []history.Entry{in("10:00:00", "a"), in("10:00:05", "c")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "A", "c"}},
		{"in order among themselves", []history.Entry{in("10:00:00", "a")}, []history.Entry{out("10:00:02", "A"), out("10:00:03", "B")}, []string{"a", "A", "B"}},
		{"delivered meanwhile", []history.Entry{in("10:00:00", "a"), out("10:00:04", "A")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "A"}},
		{"the same words said before", []history.Entry{out("09:00:00", "OK"), in("10:00:00", "a")}, []history.Entry{out("10:00:02", "OK")}, []string{"OK", "a", "OK"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, e := range withUndelivered(slices.Clone(c.history), c.pending) {
				got = append(got, e.Text)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("withUndelivered = %q, want %q", got, c.want)
			}
		})
	}
}
