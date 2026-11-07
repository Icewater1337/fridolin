package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// TestErrandEndedWhileTheRunThinks: an errand stopped while its run
// thinks gets nothing sent to the person from the run's answer, and
// nothing recorded; an abort still blocks them, without words.
func TestErrandEndedWhileTheRunThinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, answer string
		blocked      bool
	}{
		{"reply", `{"action":"reply","text":"Tuesday it is!"}`, false},
		{"ask_owner", `{"action":"ask_owner","text":"One moment.","question":"Is Friday fine?"}`, false},
		{"done", `{"action":"done","text":"Bye!","summary":"Settled."}`, false},
		{"abort", `{"action":"abort","text":"Sure, Sam lives at …","reason":"asked for Sam's address","severe":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			started, release := make(chan struct{}, 1), make(chan struct{})
			optionsOf(t).hooks.errandRun = func(errand.Errand) (string, error) {
				started <- struct{}{}
				<-release
				return tc.answer, nil
			}
			optionsOf(t).waits.errandGather = 10 * time.Millisecond
			h := coreHarness(t, nil)
			m := h.Approved()["core"]
			m.Errands = &manifest.Errands{OwnerName: "Sam"}
			if w := h.Do("PUT", "/api/brains/core", map[string]any{"manifest": m}); w.Code != http.StatusOK {
				t.Fatalf("turning errands on: %d %s", w.Code, w.Body.String())
			}
			ctx := context.Background()
			h.knowAlex()
			if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
				t.Fatal(err)
			}
			h.WaitSent(errandJID, "When would dinner")
			e := h.onlyErrand()
			h.theyWrite("Tuesday works")
			<-started
			if ended, err := h.b.store.EndErrand(ctx, e.ID, store.ErrandStopped, "the owner stopped it", time.Now()); err != nil || !ended {
				t.Fatalf("stopping: %v %v", ended, err)
			}
			sent, toOwner := len(h.SentTo(errandJID)), len(h.SentTo(harnessDM))
			lines, _ := h.b.store.ErrandLines(ctx, e.ID)
			close(release)
			// The run's answer is acted on before the contact is looked at.
			h.WaitFor("the answer acted on", func() bool {
				c, err := h.b.store.ErrandContactOf(ctx, errandNumber)
				return err == nil && (c.State == store.ContactBlocked) == tc.blocked
			})
			time.Sleep(200 * time.Millisecond)
			if got := h.SentTo(errandJID); len(got) != sent {
				t.Errorf("sent to the person after the end: %q", got[sent:])
			}
			for _, text := range h.SentTo(errandJID) {
				if text == outboundmsg.ErrandAbortMessage("Sam") || strings.Contains(text, "Sam lives at") {
					t.Errorf("sent %q", text)
				}
			}
			if tc.name == "ask_owner" && len(h.SentTo(harnessDM)) != toOwner {
				t.Errorf("the owner was asked about an ended errand: %q", h.SentTo(harnessDM)[toOwner:])
			}
			after, _ := h.b.store.ErrandLines(ctx, e.ID)
			if len(after) != len(lines) {
				t.Errorf("lines recorded after the end: %+v", after[len(lines):])
			}
			if got := h.errandOf(e.ID); got.State != store.ErrandStopped || got.Sent != e.Sent {
				t.Errorf("the errand: %+v", got)
			}
		})
	}
}

// TestErrandNoteSurvivesAFailedReply: what the person asked to pass on
// reaches the owner once, whether or not the reply that came with it could
// be sent.
func TestErrandNoteSurvivesAFailedReply(t *testing.T) {
	t.Parallel()
	const reply, note = "I'll tell Sam.", "Alex will be 20 minutes late."
	for name, c := range map[string]struct {
		sent    bool
		breakIt func(t *testing.T, h *harness, runs *errandRuns, e store.Errand)
	}{
		"sent": {sent: true, breakIt: func(*testing.T, *harness, *errandRuns, store.Errand) {}},
		"stopped while the run thinks": {breakIt: func(t *testing.T, h *harness, runs *errandRuns, e store.Errand) {
			runs.during = func() {
				if ended, err := h.b.store.EndErrand(context.Background(), e.ID, store.ErrandStopped, "the owner stopped it", time.Now()); err != nil || !ended {
					t.Errorf("stopping: %v %v", ended, err)
				}
			}
		}},
		"send refused": {breakIt: func(t *testing.T, h *harness, _ *errandRuns, _ store.Errand) {
			// Only the person's: the owner's notice takes the same outbox.
			db := testStoreSQL(t, h.b.cfgs.Get().DBPath)
			if _, err := db.Exec(`CREATE TRIGGER refuse_person BEFORE INSERT ON outbox WHEN NEW.chat_jid = '` + errandJID + `' BEGIN SELECT RAISE(FAIL, 'disk full'); END`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, runs := errandHarness(t)
			ctx := context.Background()
			h.knowAlex()
			if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
				t.Fatal(err)
			}
			e := h.onlyErrand()
			if err := h.b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromThem, Text: "Tell Sam I'll be late", At: time.Now()}, false); err != nil {
				t.Fatal(err)
			}
			c.breakIt(t, h, runs, e)
			runs.say(`{"action":"reply","text":"` + reply + `","note":"` + note + `"}`)
			h.b.errandTurn(e.ID)

			notice := outboundmsg.ErrandNoteNotice("Alex", e.ID, note)
			h.WaitSent(harnessDM, notice)
			if got := countOf(h.SentTo(harnessDM), notice); got != 1 {
				t.Errorf("the owner got the note %d times", got)
			}
			if got := passedOnLines(h); got != 1 {
				t.Errorf("%d passed on lines in the audit log", got)
			}
			if c.sent {
				h.WaitSent(errandJID, reply)
			} else if got := countOf(h.SentTo(errandJID), reply); got != 0 {
				t.Errorf("the reply went out %d times", got)
			}
		})
	}
}

// TestErrandNoteSurvivesACallOutOfTime: a call whose time ran out while
// Fridolin spoke fails the send with its context, which must not take the
// note with it.
func TestErrandNoteSurvivesACallOutOfTime(t *testing.T) {
	t.Parallel()
	h, _ := errandHarness(t)
	h.knowAlex()
	if _, err := h.b.openErrand(context.Background(), h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	e := h.onlyErrand()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const note = "Alex will be 20 minutes late."
	h.b.errandAct(ctx, e, errand.Answer{Action: errand.Reply, Text: "I'll tell Sam.", Note: note}, nil, errandChannel{say: func(string) error { return ctx.Err() }})
	h.WaitSent(harnessDM, outboundmsg.ErrandNoteNotice("Alex", e.ID, note))
}

// countOf is how many of texts contain substr.
func countOf(texts []string, substr string) int {
	n := 0
	for _, text := range texts {
		if strings.Contains(text, substr) {
			n++
		}
	}
	return n
}

// passedOnLines is how many notes the audit log says were passed on.
func passedOnLines(h *harness) int {
	n := 0
	for _, l := range h.auditOf("errand") {
		if l["detail"] == "passed on" {
			n++
		}
	}
	return n
}
