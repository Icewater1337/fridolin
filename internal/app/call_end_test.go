package app

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// TestCallEndOf (#679): meowcaller's end reasons, before and after the
// other side picked up, are how a call ended.
func TestCallEndOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		reason   string
		pickedUp bool
		how      callEnd
		why      string
	}{
		{"rejected", false, endedDeclined, "they declined it"},
		{"rejected_elsewhere", false, endedDeclined, "they declined it"},
		{"server:503", false, endedFailed, "WhatsApp's server ended it (server:503)"},
		{"server:403", true, endedFailed, "WhatsApp's server ended it (server:403)"},
		{"offer_failed", false, endedFailed, "WhatsApp's server ended it (offer_failed)"},
		{"", false, endedNoAnswer, "nobody picked up"},
		{"hangup", false, endedNoAnswer, "nobody picked up"},
		{"timeout", false, endedNoAnswer, "nobody picked up (timeout)"},
		{"hangup", true, endedHungUp, "Fridolin hung up"},
		{"", true, endedHungUp, "they ended it"},
		{"timeout", true, endedHungUp, "they ended it (timeout)"},
	} {
		how, why := callEndOf(c.reason, c.pickedUp)
		if how != c.how || why != c.why {
			t.Errorf("callEndOf(%q, %v) = %s, %q; want %s, %q", c.reason, c.pickedUp, how, why, c.how, c.why)
		}
	}
}

// callEndedLine waits for the errand's "call ended" audit line and
// returns its outcome.
func callEndedLine(h *harness) string {
	h.t.Helper()
	var got string
	h.WaitFor("the call's end in the audit log", func() bool {
		for _, l := range h.auditOf("errand") {
			if l["detail"] == "call ended" {
				got, _ = l["outcome"].(string)
				return true
			}
		}
		return false
	})
	return got
}

// TestErrandCallEndsBeforePickUp (#679): a call they decline leaves the
// errand open and its message goes to them as one; a call that fails
// before they pick up ends the errand, and the owner hears that it
// failed, not that they did not pick up.
func TestErrandCallEndsBeforePickUp(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		noMedia bool
		ends    string
		line    string
		notice  func(id string) string
		state   string
		message bool
	}{
		"declined": {
			ends: "rejected", line: "declined (they declined it)",
			notice: func(id string) string { return outboundmsg.ErrandCallDeclinedNotice("Alex", id) },
			state:  store.ErrandOpen, message: true,
		},
		"server refused": {
			ends: "server:503", line: "failed (WhatsApp's server ended it (server:503))",
			notice: func(id string) string {
				return outboundmsg.ErrandCallFailedNotice("Alex", id, "WhatsApp's server ended it (server:503)")
			},
			state: store.ErrandExpired,
		},
		"no audio after pick up": {
			noMedia: true, line: "failed (the audio did not start)",
			notice: func(id string) string {
				return outboundmsg.ErrandCallFailedNotice("Alex", id, "the audio did not start")
			},
			state: store.ErrandExpired,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			useFakeAI(t)
			var fc *fakeCall
			h := openerHarness(t, dinner, nil, func(number string) whatsapp.Call {
				fc = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), noMedia: c.noMedia, endsWith: c.ends}
				return fc
			})
			e := h.onlyErrand()
			h.WaitSent(harnessDM, c.notice(e.ID))
			if got := callEndedLine(h); got != c.line {
				t.Errorf("the call's end in the audit log: %q, want %q", got, c.line)
			}
			if got := h.errandOf(e.ID); got.State != c.state {
				t.Errorf("the errand: %+v", got)
			}
			if c.message {
				h.WaitSent(errandJID, dinner.Message)
				if got := h.errandOf(e.ID); got.Sent != 1 {
					t.Errorf("sent %d, want the message", got.Sent)
				}
			}
			time.Sleep(100 * time.Millisecond)
			if got := h.SentTo(harnessDM); len(got) != 1 || strings.Contains(got[0], "did not pick up") {
				t.Errorf("to the owner: %q", got)
			}
			if !c.message {
				if got := h.SentTo(errandJID); len(got) != 0 {
					t.Errorf("to them: %q", got)
				}
			}
			if fc.spoke() != 0 {
				t.Errorf("spoke %d times on a call nobody heard", fc.spoke())
			}
		})
	}
}

// TestErrandCallWithoutSound (#679): no sound comes from them once the
// call's audio flows: Fridolin says it cannot hear them, hangs up, and
// the errand goes on by message, with the owner told.
func TestErrandCallWithoutSound(t *testing.T) {
	t.Parallel()
	ai := useFakeAI(t)
	optionsOf(t).waits.callNoAudio = 100 * time.Millisecond
	var fc *fakeCall
	h := openerHarness(t, dinner, nil, func(number string) whatsapp.Call {
		fc = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}
		return fc
	})
	e := h.onlyErrand()
	h.WaitSent(harnessDM, outboundmsg.ErrandCallNoAudioNotice("Alex", e.ID))
	h.WaitSent(errandJID, dinner.Message)
	if !fc.isOver() {
		t.Error("the call goes on")
	}
	ai.mu.Lock()
	spoken := append([]string(nil), ai.spoken...)
	ai.mu.Unlock()
	if len(spoken) != 2 || spoken[1] != callCantHear {
		t.Errorf("spoke %q, want the opener and %q", spoken, callCantHear)
	}
	if got := callEndedLine(h); got != "failed (no inbound audio)" {
		t.Errorf("the call's end in the audit log: %q", got)
	}
	if got := h.errandOf(e.ID); got.State != store.ErrandOpen || got.Sent != 2 {
		t.Errorf("the errand: %+v", got)
	}
}

// TestOwnerCallWithoutSound (#679): no sound comes from the owner on
// their call: Fridolin says it cannot hear them, hangs up, and tells them
// in the chat; the call's audit line says why it ended.
func TestOwnerCallWithoutSound(t *testing.T) {
	t.Parallel()
	ai := useFakeAI(t)
	optionsOf(t).waits.callNoAudio = 100 * time.Millisecond
	h := newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.setConnectionKeys()
	owner := &fakeCall{peer: types.NewJID(chat.NormalizeJID(harnessDM), types.DefaultUserServer)}
	done := make(chan struct{})
	go func() { h.b.takeCall(owner); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not end")
	}
	h.WaitSent(harnessDM, outboundmsg.OwnerCallNoAudioNotice)
	ai.mu.Lock()
	spoken := append([]string(nil), ai.spoken...)
	ai.mu.Unlock()
	if len(spoken) != 2 || spoken[1] != callCantHear {
		t.Errorf("spoke %q, want the greeting and %q", spoken, callCantHear)
	}
	var ended string
	for _, l := range h.auditOf("voice_call") {
		if l["detail"] == "ended" {
			ended, _ = l["outcome"].(string)
		}
	}
	if ended != "failed (no inbound audio), 0 turn(s)" {
		t.Errorf("the call's end in the audit log: %q", ended)
	}
	if n := len(h.auditOf("run_start")); n != 0 {
		t.Errorf("%d runs for a call nobody spoke on", n)
	}
}

// TestOwnerCallFailsBeforeAudio (#679): the owner's call is answered and
// its audio never flows; the owner is told in the chat that it failed.
func TestOwnerCallFailsBeforeAudio(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h := newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.setConnectionKeys()
	owner := &fakeCall{peer: types.NewJID(chat.NormalizeJID(harnessDM), types.DefaultUserServer), endsWith: "server:500"}
	h.b.takeCall(owner)
	h.WaitSent(harnessDM, outboundmsg.OwnerCallFailedNotice("WhatsApp's server ended it (server:500)"))
	if owner.spoke() != 0 {
		t.Errorf("spoke %d times on a call that failed", owner.spoke())
	}
}
