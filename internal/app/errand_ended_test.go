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
