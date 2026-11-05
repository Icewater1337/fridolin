package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// dropsWhileSpeaking is a call that is lost as Fridolin starts to speak.
type dropsWhileSpeaking struct{ *fakeCall }

func (d dropsWhileSpeaking) Say(pcm []byte) <-chan struct{} {
	d.fakeCall.Say(pcm)
	d.end("lost")
	return make(chan struct{})
}

// openerHarness opens a call errand; placing the call runs before, then
// returns what call makes.
func openerHarness(t *testing.T, req errandRequest, before func(h *harness, e store.Errand), call func(number string) whatsapp.Call) *harness {
	t.Helper()
	optionsOf(t).waits.callRing = 200 * time.Millisecond
	placed := make(chan struct{})
	var h *harness
	optionsOf(t).hooks.placeCall = func(_ context.Context, number string) (whatsapp.Call, error) {
		defer close(placed)
		if before != nil {
			before(h, h.onlyErrand())
		}
		return call(number), nil
	}
	h = newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}, Errands: &manifest.Errands{OwnerName: "Sam"}})
	h.setConnectionKeys()
	ctx := context.Background()
	_ = h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactKnown, UpdatedAt: time.Now()})
	c := req
	c.Call = true
	if _, err := h.b.openErrand(ctx, h.ownerRun(), c); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the call", func() bool {
		select {
		case <-placed:
			return true
		default:
			return false
		}
	})
	return h
}

// TestCallOpenerFailureEndsTheErrand: the call is picked up and speech
// fails; the errand ends expired and the owner is told once, and nothing
// else goes out.
func TestCallOpenerFailureEndsTheErrand(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h := openerHarness(t, dinner, nil, func(number string) whatsapp.Call {
		return dropsWhileSpeaking{&fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}}
	})
	e := h.onlyErrand()
	h.WaitFor("the errand to end", func() bool { return h.errandOf(e.ID).State == store.ErrandExpired })
	why := "the call failed: " + errCallEnded.Error()
	h.WaitSent(harnessDM, outboundmsg.ErrandFailedNotice("Alex", e.ID, why))
	time.Sleep(200 * time.Millisecond)
	if got := h.SentTo(harnessDM); len(got) != 1 {
		t.Errorf("to the owner: %q", got)
	}
	if got := h.SentTo(errandJID); len(got) != 0 {
		t.Errorf("to them: %q", got)
	}
}

// TestCallOpenerAfterTheEndSaysNothing: the errand ended before the
// opener; nothing is spoken and the owner is not told of a failure.
func TestCallOpenerAfterTheEndSaysNothing(t *testing.T) {
	t.Parallel()
	var fc *fakeCall
	useFakeAI(t)
	h := openerHarness(t, dinner, func(h *harness, e store.Errand) {
		if ended, err := h.b.store.EndErrand(context.Background(), e.ID, store.ErrandStopped, "the owner stopped it", time.Now()); err != nil || !ended {
			t.Errorf("ending: %v %v", ended, err)
		}
	}, func(number string) whatsapp.Call {
		fc = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}
		return fc
	})
	h.WaitFor("the call over", func() bool { return fc != nil && fc.isOver() })
	time.Sleep(200 * time.Millisecond)
	e := h.onlyErrand()
	if e.State != store.ErrandStopped || fc.spoke() != 0 {
		t.Errorf("errand %+v, spoke %d", e, fc.spoke())
	}
	if got := h.SentTo(harnessDM); len(got) != 0 {
		t.Errorf("to the owner: %q", got)
	}
}

// TestCallRecordsWhatWasSpoken: a long first message is cut when read
// aloud, and the recorded opener is what the call spoke; a reply that is
// only markup is neither recorded nor counted as sent.
func TestCallRecordsWhatWasSpoken(t *testing.T) {
	t.Parallel()
	runs := &errandRuns{}
	optionsOf(t).hooks.errandRun = runs.run
	runs.say(`{"action":"reply","text":"[POLL]\nA\nB\n[/POLL]"}`, `{"action":"done","text":"Bye!","summary":"Done."}`)
	long := dinner
	long.Message = strings.Repeat("This is one more sentence of the long message. ", 33)
	ai := useFakeAI(t)
	var fc *fakeCall
	h := openerHarness(t, long, nil, func(number string) whatsapp.Call {
		fc = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}
		return fc
	})
	h.WaitFor("the opener", func() bool { return fc.spoke() >= 1 })
	fc.speak()
	h.WaitFor("the markup reply", func() bool { return len(runs.handed()) >= 1 })
	fc.speak()
	h.WaitFor("the end", fc.isOver)
	e := h.onlyErrand()
	h.WaitFor("the errand done", func() bool { return h.errandOf(e.ID).State == store.ErrandDone })
	lines, _ := h.b.store.ErrandLines(context.Background(), e.ID)
	ai.mu.Lock()
	spoken := append([]string(nil), ai.spoken...)
	ai.mu.Unlock()
	if len(spoken) < 2 || len(lines) != 4 || lines[0].Text != spoken[0] || len([]rune(spoken[0])) > maxSpoken {
		t.Fatalf("recorded %d lines %q, spoke %q", len(lines), lines, spoken)
	}
	if lines[3].Text != "Bye!" || h.errandOf(e.ID).Sent != 2 {
		t.Errorf("lines %q, sent %d", lines, h.errandOf(e.ID).Sent)
	}
}
