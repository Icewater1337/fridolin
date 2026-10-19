package brain

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Admission decides queue contents only. History, fairness, interruption and
// completion callbacks stay with the caller under its existing lock ordering.
func TestAdmitPreservesQueueBoundaries(t *testing.T) {
	message := func(id, jid, sender string, silent bool, source string) Inbound {
		return Inbound{Inbound: chat.Inbound{ID: id, JID: jid, Sender: sender, Silent: silent, Source: source}}
	}
	request := message("new", "group", "sender", false, "")
	own := message("own", "group", "sender", false, "")
	other := message("other", "group", "someone-else", false, "")
	elsewhere := message("elsewhere", "another-chat", "sender", false, "")
	chatter := message("chatter", "group", "someone-else", true, "")
	otherChatter := message("other-chatter", "another-chat", "sender", true, "")
	ingest := message("upload", "group", "sender", false, chat.SourceIngest)
	ids := func(ms []Inbound) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}
	for _, tc := range []struct {
		name, mode      string
		pending         []Inbound
		message         Inbound
		limit           int
		want, gone      []string
		interrupt, full bool
	}{
		{"queue keeps requests", "queue", []Inbound{own, other, chatter}, request, 5, []string{"own", "other", "new"}, []string{"chatter"}, false, false},
		{"interrupt stays within sender and chat", "interrupt", []Inbound{own, other, elsewhere, chatter, ingest}, request, 5, []string{"other", "elsewhere", "upload", "new"}, []string{"own", "chatter"}, true, false},
		{"upload is not a correction", "interrupt", []Inbound{own, chatter}, ingest, 5, []string{"own", "upload"}, []string{"chatter"}, false, false},
		{"chatter does not interrupt requests", "interrupt", []Inbound{own, chatter, elsewhere}, chatter, 5, []string{"own", "elsewhere", "chatter"}, []string{"chatter"}, false, false},
		{"oldest other chatter yields capacity", "queue", []Inbound{own, otherChatter, elsewhere}, request, 3, []string{"own", "elsewhere", "new"}, []string{}, false, false},
		{"full requests are unchanged", "queue", []Inbound{own, other, elsewhere}, request, 3, []string{"own", "other", "elsewhere"}, []string{}, false, true},
		{"replacement creates room", "interrupt", []Inbound{own, other, elsewhere}, request, 3, []string{"other", "elsewhere", "new"}, []string{"own"}, true, false},
		{"refusal still removes superseded chatter", "queue", []Inbound{chatter, other, elsewhere}, request, 2, []string{"other", "elsewhere"}, []string{"chatter"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := slices.Clone(tc.pending)
			next, gone, interrupt, err := admit(tc.pending, tc.message, tc.mode, tc.limit)
			if !reflect.DeepEqual(ids(next), tc.want) || !reflect.DeepEqual(ids(gone), tc.gone) || interrupt != tc.interrupt || errors.Is(err, ErrQueueFull) != tc.full {
				t.Fatalf("got queue=%v gone=%v interrupt=%t err=%v; want queue=%v gone=%v interrupt=%t full=%t", ids(next), ids(gone), interrupt, err, tc.want, tc.gone, tc.interrupt, tc.full)
			}
			if !reflect.DeepEqual(tc.pending, before) {
				t.Fatal("admission mutated its input queue")
			}
		})
	}
}
