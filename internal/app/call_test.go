package app

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/voice"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// fakeCall is a WhatsApp call the tests hold the other end of: they
// speak into it, and see what Fridolin said.
type fakeCall struct {
	peer   types.JID
	pickUp bool // an outgoing call the other side answers

	mu       sync.Mutex
	listen   func([]float32)
	onEnd    func(string)
	onReady  func()
	ready    bool
	ended    bool
	answered bool
	said     [][]byte
}

func (f *fakeCall) ID() string { return "call1" }
func (f *fakeCall) Peer() (types.JID, bool) {
	return f.peer, f.peer.Server == types.DefaultUserServer
}

func (f *fakeCall) Answer() error {
	f.mu.Lock()
	f.answered = true
	f.mu.Unlock()
	f.becomeReady()
	return nil
}

func (f *fakeCall) becomeReady() {
	f.mu.Lock()
	f.ready = true
	fn := f.onReady
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (f *fakeCall) Hangup() error { f.end("hung up"); return nil }

func (f *fakeCall) end(reason string) {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return
	}
	f.ended = true
	fn := f.onEnd
	f.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}

func (f *fakeCall) Listen(fn func([]float32)) { f.mu.Lock(); f.listen = fn; f.mu.Unlock() }
func (f *fakeCall) OnEnd(fn func(string))     { f.mu.Lock(); f.onEnd = fn; f.mu.Unlock() }

func (f *fakeCall) OnReady(fn func()) {
	f.mu.Lock()
	f.onReady = fn
	ready := f.ready || f.pickUp
	f.mu.Unlock()
	if ready {
		f.becomeReady()
	}
}

func (f *fakeCall) Say(pcm []byte) <-chan struct{} {
	f.mu.Lock()
	f.said = append(f.said, pcm)
	f.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return done
}

// speak says something into the call: a second of a tone, then a pause.
func (f *fakeCall) speak() {
	f.mu.Lock()
	fn := f.listen
	f.mu.Unlock()
	frame := func(level float64) []float32 {
		out := make([]float32, voice.SampleRate*60/1000)
		for i := range out {
			out[i] = float32(level * math.Sin(2*math.Pi*220*float64(i)/voice.SampleRate))
		}
		return out
	}
	for range 5 {
		fn(frame(0.001))
	}
	for range 17 {
		fn(frame(0.3))
	}
	for range 20 {
		fn(frame(0.001))
	}
}

func (f *fakeCall) spoke() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.said)
}

func (f *fakeCall) isOver() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

// callsConfig turns calls on, with an ElevenLabs voice.
const callsConfig = `{"calls": {"enabled": true, "greeting": "Fridolin here."}, "connections": {"elevenlabs": {"voice": "v1"}}}`

// TestOwnerCallsFridolin: a call from the owner's number is answered with
// the greeting; what they say is transcribed and goes to the brain that
// gets their direct messages as their own words (chat.SourceCall), whose
// answer is read aloud and not sent to the chat. A stranger's call rings
// on.
func TestOwnerCallsFridolin(t *testing.T) {
	t.Parallel()
	ai := useFakeAI(t)
	h := newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.setConnectionKeys()

	stranger := &fakeCall{peer: types.NewJID("15550001234", types.DefaultUserServer)} // personal-data:ignore (a fictional 555 number)
	h.b.takeCall(stranger)
	if stranger.answered {
		t.Error("a stranger's call was taken")
	}

	owner := &fakeCall{peer: types.NewJID(chat.NormalizeJID(harnessDM), types.DefaultUserServer)}
	done := make(chan struct{})
	go func() { h.b.takeCall(owner); close(done) }()
	h.WaitFor("the greeting", func() bool { return owner.spoke() >= 1 })
	before := len(h.SentTo(harnessDM))
	owner.speak()
	// "One moment.", then the answer.
	h.WaitFor("the answer, read aloud", func() bool { return owner.spoke() >= 3 })
	owner.end("they hung up")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not end")
	}
	calls, _, _ := ai.seen()
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "POST /v1/audio/transcriptions") || strings.Count(joined, "POST /v1/text-to-speech/v1") < 3 {
		t.Errorf("the AI calls: %v", calls)
	}
	if got := h.SentTo(harnessDM); len(got) != before {
		t.Errorf("the call's answer went to the chat too: %q", got[before:])
	}
	steps := map[string]bool{}
	for _, l := range h.auditOf("voice_call") {
		steps[l["detail"].(string)] = true
	}
	if !steps["answered"] || !steps["ended"] {
		t.Errorf("the audit's call lines: %v", h.auditOf("voice_call"))
	}
	runs := h.auditOf("run_start")
	if len(runs) == 0 {
		t.Fatal("no run for what the owner said")
	}
}

// TestErrandCallsSomeone: contact_person with call: true calls someone
// Fridolin wrote to before. It opens with its introduction, that it is an
// AI, and the message; what they say goes to the errand's run, told it is
// a call, whose answers are read aloud; done ends the call and the errand.
// Someone Fridolin never wrote to is not called, and a call nobody picks
// up ends the errand.
func TestErrandCallsSomeone(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	runs := &errandRuns{}
	optionsOf(t).hooks.errandRun = runs.run
	optionsOf(t).waits.callRing = 200 * time.Millisecond
	var mu sync.Mutex
	var placed []*fakeCall
	pickUp := true
	optionsOf(t).hooks.placeCall = func(_ context.Context, number string) (whatsapp.Call, error) {
		mu.Lock()
		defer mu.Unlock()
		c := &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: pickUp}
		placed = append(placed, c)
		return c, nil
	}
	h := newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}, Errands: &manifest.Errands{OwnerName: "Sam"}})
	h.setConnectionKeys()
	ctx := context.Background()
	call := dinner
	call.Call = true
	if _, err := h.b.openErrand(ctx, h.ownerRun(), call); err == nil || !strings.Contains(err.Error(), "wrote to before is called") {
		t.Fatalf("calling someone never written to: %v", err)
	}
	_ = h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactKnown, UpdatedAt: time.Now()})
	runs.say(`{"action":"reply","text":"Thursday at seven then?"}`, `{"action":"done","text":"Great, see you then!","summary":"Dinner on Thursday at 7."}`)
	out, err := h.b.openErrand(ctx, h.ownerRun(), call)
	if err != nil || !strings.Contains(out, "calling Alex now") {
		t.Fatalf("contact_person call: %q %v", out, err)
	}
	h.WaitFor("the call", func() bool { mu.Lock(); defer mu.Unlock(); return len(placed) == 1 })
	c := placed[0]
	h.WaitFor("the opener", func() bool { return c.spoke() >= 1 })
	c.speak()
	h.WaitFor("the reply", func() bool { return c.spoke() >= 2 })
	c.speak()
	h.WaitFor("the end of the call", c.isOver)
	e := h.onlyErrand()
	h.WaitSent(harnessDM, outboundmsg.ErrandDoneNotice("Alex", e.ID, "Dinner on Thursday at 7.", false))
	if e = h.errandOf(e.ID); e.State != store.ErrandDone || e.Mode != store.ErrandByCall || e.Sent != 3 {
		t.Errorf("the errand: %+v", e)
	}
	lines, _ := h.b.store.ErrandLines(ctx, e.ID)
	if len(lines) != 5 || !strings.HasPrefix(lines[0].Text, "Hi, this is Fridolin, Sam's assistant. I'm an AI assistant, and I keep notes of this call. When would dinner") ||
		lines[1].Sender != errand.FromThem || lines[1].Text != "transcribed by openai" || lines[4].Text != "Great, see you then!" {
		t.Errorf("the conversation: %+v", lines)
	}
	for _, d := range runs.handed() {
		if !d.Call {
			t.Errorf("a run was not told it is on a call: %+v", d)
		}
	}
	if got := h.SentTo(errandJID); len(got) != 0 {
		t.Errorf("messages went to them during a call errand: %q", got)
	}

	// Nobody picks up: the errand ends, and the owner hears it.
	mu.Lock()
	pickUp = false
	mu.Unlock()
	if _, err := h.b.openErrand(ctx, h.ownerRun(), call); err != nil {
		t.Fatal(err)
	}
	var second store.Errand
	h.WaitFor("the errand that was not picked up", func() bool {
		list, _ := h.b.store.RecentErrands(ctx, "core", 10)
		second = list[0]
		return len(list) == 2 && second.State == store.ErrandExpired
	})
	h.WaitSent(harnessDM, outboundmsg.ErrandNoAnswerNotice("Alex", second.ID))
}

// TestCallsOffRefuseCalls: without calls.enabled no errand calls, and
// the owner's call is not taken.
func TestCallsOffRefuseCalls(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h, _ := errandHarness(t)
	h.setConnectionKeys()
	ctx := context.Background()
	_ = h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactKnown, UpdatedAt: time.Now()})
	call := dinner
	call.Call = true
	if _, err := h.b.openErrand(ctx, h.ownerRun(), call); err == nil || !strings.Contains(err.Error(), "calls are off") {
		t.Errorf("an errand call with calls off: %v", err)
	}
	owner := &fakeCall{peer: types.NewJID(chat.NormalizeJID(harnessDM), types.DefaultUserServer)}
	h.b.takeCall(owner)
	if owner.answered {
		t.Error("the owner's call was taken with calls off")
	}
}

// callMidWay starts a call errand to Alex, lets it pick up and open, and
// returns the harness, the errand and the call, with the runs for later.
func callMidWay(t *testing.T) (*harness, *errandRuns, store.Errand, *fakeCall) {
	t.Helper()
	runs := &errandRuns{}
	optionsOf(t).hooks.errandRun = runs.run
	optionsOf(t).waits.errandGather = 10 * time.Millisecond
	optionsOf(t).waits.callRing = 200 * time.Millisecond
	var mu sync.Mutex
	var placed *fakeCall
	optionsOf(t).hooks.placeCall = func(_ context.Context, number string) (whatsapp.Call, error) {
		mu.Lock()
		defer mu.Unlock()
		placed = &fakeCall{peer: types.NewJID(number, types.DefaultUserServer), pickUp: true}
		return placed, nil
	}
	h := newServedHarness(t, callsConfig)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}, Errands: &manifest.Errands{OwnerName: "Sam"}})
	h.setConnectionKeys()
	ctx := context.Background()
	_ = h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactKnown, UpdatedAt: time.Now()})
	call := dinner
	call.Call = true
	if _, err := h.b.openErrand(ctx, h.ownerRun(), call); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the call", func() bool { mu.Lock(); defer mu.Unlock(); return placed != nil })
	c := placed
	h.WaitFor("the opener", func() bool { return c.spoke() >= 1 })
	return h, runs, h.onlyErrand(), c
}

// TestCallEndsWithTheirLineUnanswered: a WhatsApp line from the person
// while the call listens, and a call that ends, get a turn by message.
func TestCallEndsWithTheirLineUnanswered(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h, runs, e, c := callMidWay(t)
	runs.say(`{"action":"reply","text":"Sure, I will write instead."}`)
	h.theyWrite("can't talk now, text me")
	c.end("hung up")
	h.WaitFor("the answer by message", func() bool { return len(h.SentTo(errandJID)) == 1 })
	if got := h.SentTo(errandJID); got[0] != "Sure, I will write instead." {
		t.Errorf("sent: %q", got)
	}
	if e = h.errandOf(e.ID); e.State != store.ErrandOpen {
		t.Errorf("the errand: %+v", e)
	}
}

// TestCallLimitAfterTheirLine: the call ends on the message limit right
// after they spoke; a turn by message follows instead of nothing.
func TestCallLimitAfterTheirLine(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h, _, e, c := callMidWay(t)
	ctx := context.Background()
	for i := 0; i < errand.MaxMessages; i++ {
		_ = h.b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromYou, Text: "x", At: time.Now()}, true)
	}
	c.speak()
	h.WaitFor("the turn after the call", func() bool { return h.errandOf(e.ID).State != store.ErrandOpen })
}

// TestCallEndsOnOwnerQuestionAsksOnce: the run asked the owner and
// nothing new came: no turn, so the owner is not asked again.
func TestCallEndsOnOwnerQuestionAsksOnce(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	h, runs, e, c := callMidWay(t)
	runs.say(`{"action":"ask_owner","question":"Which day?"}`)
	c.speak()
	h.WaitFor("the call over", c.isOver)
	time.Sleep(200 * time.Millisecond)
	if n := len(runs.handed()); n != 1 {
		t.Errorf("runs after the question: %d", n)
	}
	if e = h.errandOf(e.ID); e.State != store.ErrandOpen {
		t.Errorf("the errand: %+v", e)
	}
}

// TestCallSilenceAfterAStopSaysNothing (#581): the errand is stopped while
// the person stays silent; at the silence's end the call ends without
// saying Fridolin will get back to them, and the owner gets no notice.
func TestCallSilenceAfterAStopSaysNothing(t *testing.T) {
	t.Parallel()
	useFakeAI(t)
	optionsOf(t).waits.errandCallIdle = 2 * time.Second // long enough to stop within it
	h, _, e, c := callMidWay(t)
	if err := h.b.stopErrand(context.Background(), "core", e.ID); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the call over", c.isOver)
	if n := c.spoke(); n != 1 {
		t.Errorf("spoke %d times, want the opener alone", n)
	}
	if got := h.SentTo(harnessDM); len(got) != 0 {
		t.Errorf("to the owner: %q", got)
	}
}

// TestCallSilenceOnAnOpenErrandSaysLater: a silence on an errand still open
// ends the call with the promise to go on by message, and the owner is told.
func TestCallSilenceOnAnOpenErrandSaysLater(t *testing.T) {
	t.Parallel()
	ai := useFakeAI(t)
	optionsOf(t).waits.errandCallIdle = 100 * time.Millisecond
	h, _, e, c := callMidWay(t)
	h.WaitFor("the call over", c.isOver)
	ai.mu.Lock()
	spoken := append([]string(nil), ai.spoken...)
	ai.mu.Unlock()
	if len(spoken) != 2 || spoken[1] != errandCallLater {
		t.Errorf("spoke %q, want the opener and %q", spoken, errandCallLater)
	}
	h.WaitSent(harnessDM, outboundmsg.ErrandCallEndedNotice("Alex", e.ID))
	if e = h.errandOf(e.ID); e.State != store.ErrandOpen {
		t.Errorf("the errand: %+v", e)
	}
}
