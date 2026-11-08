package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/errand"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// TestCallNoteNobodyHeardGoesOutOnce (#611): on a call, a reply with a note
// is not heard (the call ends, or its speech fails). The note still reaches
// the owner (#597) and the reply is taken back (#578), so the next turn
// answers the same words again, with the same note: that one is shown to
// the run as passed on and does not go out a second time. A different
// note later does.
func TestCallNoteNobodyHeardGoesOutOnce(t *testing.T) {
	t.Parallel()
	const note, again, other = "Alex says: I will be late.", "  alex says: I will be LATE. ", "Alex says: bring wine."
	for name, c := range map[string]struct {
		call        func(fc *fakeCall) whatsapp.Call
		speechFails string
		// next has the person say something more after the second turn.
		next func(h *harness, fc *fakeCall, e store.Errand)
	}{
		"call ended": {
			call: func(fc *fakeCall) whatsapp.Call { return dropsOnSecondSay{fc} },
			next: func(h *harness, _ *fakeCall, e store.Errand) {
				h.WaitSent(errandJID, "Thursday, then?")
				if err := h.b.store.AddErrandLine(context.Background(), e.ID, store.ErrandLine{Sender: errand.FromThem, Text: "And bring wine.", At: time.Now()}, false); err != nil {
					t.Fatal(err)
				}
				h.b.errandTurn(e.ID)
			},
		},
		"speech failed": {
			call:        func(fc *fakeCall) whatsapp.Call { return fc },
			speechFails: "I'll tell Sam.",
			next: func(h *harness, fc *fakeCall, _ store.Errand) {
				h.WaitFor("the second reply said", func() bool { return fc.spoke() >= 2 })
				fc.speak()
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runs := &errandRuns{}
			optionsOf(t).hooks.errandRun = runs.run
			optionsOf(t).waits.errandGather = time.Millisecond
			runs.say(`{"action":"reply","text":"I'll tell Sam.","note":"`+note+`"}`,
				`{"action":"reply","text":"Thursday, then?","note":"`+again+`"}`,
				`{"action":"reply","text":"Anything else?","note":"`+other+`"}`)
			ai := useFakeAI(t)
			ai.mu.Lock()
			ai.speechFails = c.speechFails
			ai.mu.Unlock()
			var fc *fakeCall
			h := openerHarness(t, dinner, nil, func(number string) whatsapp.Call {
				fc = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}
				return c.call(fc)
			})
			h.WaitFor("the opener", func() bool { return fc.spoke() >= 1 })
			fc.speak()
			if c.speechFails != "" { // the call goes on: they say it again
				h.WaitFor("the first turn", func() bool { return len(runs.handed()) >= 1 })
				fc.speak()
			}
			e := h.onlyErrand()
			h.WaitFor("the second turn", func() bool { return len(runs.handed()) >= 2 })
			c.next(h, fc, e)
			h.WaitSent(harnessDM, outboundmsg.ErrandNoteNotice("Alex", e.ID, other))
			time.Sleep(200 * time.Millisecond)
			if got := countOf(h.SentTo(harnessDM), outboundmsg.ErrandNoteNotice("Alex", e.ID, note)); got != 1 {
				t.Errorf("the owner got the note %d times", got)
			}
			if got := countOf(h.SentTo(harnessDM), "LATE"); got != 0 {
				t.Errorf("the repeated note went out %d times", got)
			}
			if got := passedOnLines(h); got != 2 {
				t.Errorf("%d passed on lines in the audit log, want 2", got)
			}
			handed := runs.handed()
			if len(handed) != 3 || len(handed[0].Passed) != 0 || !slices.Equal(handed[1].Passed, []string{note}) || !slices.Equal(handed[2].Passed, []string{note}) {
				for i, d := range handed {
					t.Errorf("turn %d was shown passed on %q", i+1, d.Passed)
				}
			}
			n := 0
			for _, l := range h.auditOf("errand") {
				if l["detail"] == "not passed on again" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d not passed on again lines in the audit log, want 1", n)
			}
		})
	}
}
