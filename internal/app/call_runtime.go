package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/errand"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/voice"
	"github.com/Icewater1337/fridolin/internal/whatsapp"
)

// Voice calls (docs/CALLS.md), with calls.enabled: the owner calls
// Fridolin's number and talks to the brain that gets their direct
// messages, and an errand calls someone Fridolin wrote to before. Both
// are turn by turn: what the other side says, an utterance at a time,
// is transcribed through a connection (OpenAI, else ElevenLabs), and
// what Fridolin answers is read aloud by ElevenLabs. A call from anyone
// else is not taken: it rings on as before.

const (
	// callSpeechModel and callSpeechFormat are how ElevenLabs reads for a
	// call: its fast multilingual model, as the call's own PCM.
	callSpeechModel  = "eleven_flash_v2_5"
	callSpeechFormat = "pcm_16000"
	// maxSpoken is the most of an answer that is read aloud.
	maxSpoken = 1200
	// The owner's calls: how long one lasts at most, and how long a
	// silence ends it.
	ownerCallMax  = 15 * time.Minute
	ownerCallIdle = 2 * time.Minute
	// An errand's calls: how long one lasts at most, how long a silence
	// ends it, and how many calls errands make a day (how long one rings
	// is waits.callRing).
	errandCallMax     = 10 * time.Minute
	errandCallIdle    = 45 * time.Second
	errandCallsPerDay = 5
	// callReadyWait is how long an answered call may take until its
	// audio flows.
	callReadyWait = 15 * time.Second
)

// What Fridolin says on a call besides the answers, aloud: no emoji.
const (
	callOneMoment   = "One moment."
	callGoodbye     = "Goodbye."
	callFailed      = "Sorry, that did not work. Please say it again."
	errandCallLater = "I'll get back to you on WhatsApp. Goodbye."
)

// callPrompt goes ahead of what the owner said on a call, for the brain.
const callPrompt = "(On a WhatsApp voice call: this is what the owner just said, transcribed, and your answer is read aloud to them. Answer briefly, in plain spoken sentences: no lists, markup, emoji or links.)\n\n"

var (
	errCallEnded = errors.New("the call ended")
	errCallIdle  = errors.New("nobody spoke")
)

// callsOn says why no call can be taken or made now: calls are off, or
// a connection they need has no key.
func (b *App) callsOn(cfg *config.Settings) error {
	switch {
	case !b.cfgs.Boot().CallsEnabled || !cfg.Calls.Enabled:
		return errors.New("calls are off: calls.enabled in config.json turns them on, after a restart (docs/CALLS.md)")
	case b.conns == nil || !b.conns.Configured(connections.ElevenLabs):
		return errors.New("calls speak through Fridolin's ElevenLabs connection, which has no key (Integrations → ElevenLabs)")
	case connectionSettingsOf(cfg).Of(connections.ElevenLabs).Voice == "":
		return errors.New("calls need the ElevenLabs connection's voice (Integrations → ElevenLabs → voice)")
	}
	return nil
}

// speak is text read aloud by ElevenLabs, as the call's PCM, paid for by
// brain's connection limit.
func (b *App) speak(ctx context.Context, brain, run, text string) ([]byte, error) {
	cfg := b.cfgs.Get()
	sp, err := b.conns.Speak(ctx, connections.BrainCaller(brain, run, connectionSettingsOf(cfg).Of(connections.ElevenLabs).BrainMonthlyUSD),
		connections.SpeakRequest{Text: text, Model: callSpeechModel, Format: callSpeechFormat})
	return sp.Audio, err
}

// transcribe is what wav says, through OpenAI when it has a key, else
// ElevenLabs.
func (b *App) transcribe(ctx context.Context, brain, run string, wav []byte) (string, error) {
	conn := connections.ElevenLabs
	if b.conns.Configured(connections.OpenAI) {
		conn = connections.OpenAI
	}
	t, err := b.conns.Transcribe(ctx, conn, connections.BrainCaller(brain, run, connectionSettingsOf(b.cfgs.Get()).Of(conn).BrainMonthlyUSD),
		connections.TranscribeRequest{Audio: connections.Audio{Data: wav, Filename: "speech.wav", ContentType: "audio/wav"}})
	return strings.TrimSpace(t.Text), err
}

// voiceSession is one call, turn by turn: it listens for what the other
// side says, an utterance at a time, and reads Fridolin's words aloud,
// not listening meanwhile.
type voiceSession struct {
	b          *App
	call       whatsapp.Call
	brain, run string

	ended, ready       chan struct{}
	endOnce, readyOnce sync.Once
	heard              chan []float32

	mu       sync.Mutex
	seg      voice.Segmenter
	speaking bool
}

func (b *App) newVoiceSession(call whatsapp.Call, brain, run string) *voiceSession {
	v := &voiceSession{b: b, call: call, brain: brain, run: run, ended: make(chan struct{}), ready: make(chan struct{}), heard: make(chan []float32, 4)}
	call.OnEnd(func(reason string) {
		v.endOnce.Do(func() {
			log.Printf("call %s: ended (%s)", run, reason)
			close(v.ended)
		})
	})
	call.OnReady(func() { v.readyOnce.Do(func() { close(v.ready) }) })
	call.Listen(v.frame)
	return v
}

// frame takes 60 ms of what the other side says.
func (v *voiceSession) frame(f []float32) {
	v.mu.Lock()
	if v.speaking {
		v.mu.Unlock()
		return
	}
	u, ok := v.seg.Feed(f)
	v.mu.Unlock()
	if ok {
		select {
		case v.heard <- u:
		default: // a turn is in flight: what comes meanwhile waits for none
		}
	}
}

// waitReady reports whether the call's audio flows within d.
func (v *voiceSession) waitReady(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-v.ready:
		return true
	case <-v.ended:
	case <-ctx.Done():
	case <-t.C:
	}
	return false
}

// say reads text aloud and returns once it played.
func (v *voiceSession) say(ctx context.Context, text string) error {
	text = voice.Speakable(text, maxSpoken)
	if text == "" {
		return nil
	}
	select {
	case <-v.ended:
		return errCallEnded
	default:
	}
	pcm, err := v.b.speak(ctx, v.brain, v.run, text)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.speaking = true
	v.seg.Reset()
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.speaking = false
		v.seg.Reset()
		v.mu.Unlock()
		// What came while Fridolin spoke is its own voice or the line's.
		for len(v.heard) > 0 {
			<-v.heard
		}
	}()
	played := v.call.Say(pcm)
	length := time.Duration(len(pcm)/2) * time.Second / voice.SampleRate
	t := time.NewTimer(length + 5*time.Second)
	defer t.Stop()
	select {
	case <-played:
	case <-t.C:
	case <-v.ended:
		return errCallEnded
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// listen returns what the other side says next, transcribed: errCallIdle
// after idle without a word, errCallEnded once the call is over.
func (v *voiceSession) listen(ctx context.Context, idle time.Duration) (string, error) {
	t := time.NewTimer(idle)
	defer t.Stop()
	for {
		select {
		case u := <-v.heard:
			text, err := v.b.transcribe(ctx, v.brain, v.run, voice.WAV(u))
			if err != nil {
				return "", fmt.Errorf("transcribing: %w", err)
			}
			if text != "" {
				return text, nil
			}
		case <-t.C:
			return "", errCallIdle
		case <-v.ended:
			return "", errCallEnded
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// auditCall writes one step of call id with number to the audit log.
func (b *App) auditCall(brain, id, number, detail, outcome string) {
	b.audit.Append(audit.Entry{Kind: audit.VoiceCall, Brain: brain, Run: "call:" + id, ID: id, Chat: logx.MaskJID(number, "", ""), Detail: detail, Outcome: outcome})
}

// takeCall takes a call to Fridolin's number: the owner's (a number in
// allowed_numbers) is answered and goes to the brain that gets their
// direct messages; anyone else's rings on, untouched.
func (b *App) takeCall(call whatsapp.Call) {
	cfg := b.cfgs.Get()
	peer, number := call.Peer()
	who := logx.MaskJID(peer.User, "", peer.Server)
	if !number || !b.isOwner(peer.User) {
		log.Printf("call from %s: not taken (not in allowed_numbers)", who)
		return
	}
	if err := b.callsOn(cfg); err != nil {
		log.Printf("call from %s: not taken: %v", who, err)
		return
	}
	jid := chat.DMJID(peer.User)
	br := b.brainMgr.BrainFor(jid, false)
	if br == nil {
		log.Printf("call from %s: not taken: no brain gets direct messages", who)
		return
	}
	id := "c" + randomToken(7)
	if err := call.Answer(); err != nil {
		log.Printf("call from %s: answering: %v", who, err)
		return
	}
	b.auditCall(br.ID(), id, peer.User, "answered", "the owner called")
	defer func() { _ = call.Hangup() }()
	v := b.newVoiceSession(call, br.ID(), "call:"+id)
	ctx, cancel := context.WithTimeout(context.Background(), ownerCallMax)
	defer cancel()
	turns := 0
	defer func() { b.auditCall(br.ID(), id, peer.User, "ended", fmt.Sprintf("%d turn(s)", turns)) }()
	if !v.waitReady(ctx, callReadyWait) {
		return
	}
	if err := v.say(ctx, cfg.Calls.GreetingOrDefault()); err != nil {
		log.Printf("call %s: greeting: %v", id, err)
		return
	}
	for {
		text, err := v.listen(ctx, ownerCallIdle)
		switch {
		case errors.Is(err, errCallIdle), errors.Is(err, context.DeadlineExceeded):
			_ = v.say(context.Background(), callGoodbye)
			return
		case errors.Is(err, errCallEnded), errors.Is(err, context.Canceled):
			return
		case err != nil:
			log.Printf("call %s: %v", id, err)
			if v.say(ctx, callFailed) != nil {
				return
			}
			continue
		}
		turns++
		answer, err := b.askByVoice(br, jid, peer.User, text)
		if err != nil {
			log.Printf("call %s: the brain did not take it: %v", id, err)
			if v.say(ctx, callFailed) != nil {
				return
			}
			continue
		}
		if v.say(ctx, callOneMoment) != nil {
			return
		}
		var reply string
		select {
		case reply = <-answer:
		case <-v.ended:
			return
		case <-ctx.Done():
			_ = v.say(context.Background(), callGoodbye)
			return
		}
		if reply == "" {
			reply = callFailed
		}
		if v.say(ctx, reply) != nil {
			return
		}
	}
}

// askByVoice hands br what the owner said on a call, as their own words
// (chat.SourceCall), and returns the channel its answer comes on: "" for
// a run that failed or did not answer. The answer is read aloud, not
// sent to the chat.
func (b *App) askByVoice(br *brain.Brain, jid, sender, text string) (<-chan string, error) {
	out := make(chan string, 1)
	send := func(s string) {
		select {
		case out <- s:
		default:
		}
	}
	m := brain.Inbound{Inbound: chat.Inbound{JID: jid, Brain: br.ID(), Sender: sender, Text: callPrompt + text, Source: chat.SourceCall, At: time.Now(),
		Answer: func(t string) string { send(t); return t }, AnswerOnly: true,
		Done: func(error) { send("") }, GitHub: br.Manifest().GitHub}}
	_, _, _, err := b.brainMgr.Submit(m)
	return out, err
}

// placeCallTo calls number for an errand.
func (b *App) placeCallTo(ctx context.Context, number string) (whatsapp.Call, error) {
	if b.core.lost.Load() {
		return nil, errors.New("this bridge lost the core lease")
	}
	if b.hooks.placeCall != nil {
		return b.hooks.placeCall(ctx, number)
	}
	if b.placeCall == nil {
		return nil, whatsapp.ErrCallsOff
	}
	return b.placeCall(ctx, number)
}

// errandCallsReady says why errand e cannot call now; nil when it can.
func (b *App) errandCallsReady(ctx context.Context, cfg *config.Settings) error {
	if err := b.callsOn(cfg); err != nil {
		return err
	}
	if b.hooks.placeCall == nil && b.placeCall == nil {
		return errors.New("calls need WhatsApp: this bridge runs without it")
	}
	n, err := b.store.ErrandCallsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if n >= errandCallsPerDay {
		return fmt.Errorf("errands made %d calls in the last day, the most they make: write instead, or wait", n)
	}
	return nil
}

// callErrand calls errand e's person and carries the errand by voice:
// Fridolin opens with its introduction, that it is an AI, and the
// errand's first message, and each thing they say goes to the errand's
// run, whose answer is read aloud. A question for the owner, the end, an
// abort, a silence or the call's time ends the call; an errand that has
// not ended goes on by message.
func (b *App) callErrand(e store.Errand, s manifest.Errands) {
	b.errands.setCalling(e.ID, true)
	defer b.errands.setCalling(e.ID, false)
	cfg := b.cfgs.Get()
	ring := b.waits.callRing
	if ring <= 0 {
		ring = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), ring+errandCallMax+time.Minute)
	defer cancel()
	call, err := b.placeCallTo(ctx, e.Contact)
	if err != nil {
		log.Printf("errand %s: calling: %v", e.ID, err)
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandExpired, "the call could not be placed: "+err.Error(), time.Now()); ended {
			b.auditErrand(e, "ended", store.ErrandExpired+": the call could not be placed")
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandFailedNotice(e.Name, e.ID, "the call could not be placed: "+err.Error()))
		}
		return
	}
	defer func() { _ = call.Hangup() }()
	v := b.newVoiceSession(call, e.Brain, "errand:"+e.ID)
	if !v.waitReady(ctx, ring) {
		if ended, _ := b.store.EndErrand(ctx, e.ID, store.ErrandExpired, "they did not pick up", time.Now()); ended {
			b.auditErrand(e, "ended", store.ErrandExpired+": they did not pick up")
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandNoAnswerNotice(e.Name, e.ID))
		}
		return
	}
	b.auditErrand(e, "called", "they picked up")
	owner := strings.TrimSpace(s.OwnerName)
	say := func(text string) error {
		if err := v.say(ctx, text); err != nil {
			return err
		}
		if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromYou, Text: text, At: time.Now()}, true); err != nil {
			log.Printf("errand %s: recording what was said: %v", e.ID, err)
		}
		b.auditErrand(e, "said", fmt.Sprintf("%d characters", len([]rune(text))))
		return nil
	}
	// The chat's words, read aloud without their emoji.
	ch := errandChannel{say: say, abort: strings.TrimLeftFunc(outboundmsg.ErrandAbortMessage(owner), func(r rune) bool { return !unicode.IsLetter(r) })}
	opener := errand.Intro(s.OwnerName, s.Intro) + " " + cfg.Calls.NoticeOrDefault() + " " + e.Message
	if err := say(opener); err != nil {
		log.Printf("errand %s: opening the call: %v", e.ID, err)
		return
	}
	until := time.Now().Add(errandCallMax)
	goesOn := true
	for goesOn {
		text, err := v.listen(ctx, errandCallIdle)
		if err != nil {
			if errors.Is(err, errCallIdle) {
				_ = v.say(ctx, errandCallLater)
			}
			break
		}
		if err := b.store.AddErrandLine(ctx, e.ID, store.ErrandLine{Sender: errand.FromThem, Text: text, At: time.Now()}, false); err != nil {
			log.Printf("errand %s: recording what they said: %v", e.ID, err)
			break
		}
		b.auditErrand(e, "answered", fmt.Sprintf("%d characters, on the call", len([]rune(text))))
		if e, err = b.store.GetErrand(ctx, e.ID); err != nil || e.State != store.ErrandOpen {
			break
		}
		if e.Sent >= errand.MaxMessages || time.Now().After(until) {
			_ = v.say(ctx, errandCallLater)
			break
		}
		lines, err := b.store.ErrandLines(ctx, e.ID)
		if err != nil {
			break
		}
		ans, err := b.errandAsk(ctx, e, s, lines, true)
		if err != nil {
			log.Printf("errand %s: %v", e.ID, err)
			_ = v.say(ctx, errandCallLater)
			b.tellErrandOwner(ctx, e, outboundmsg.ErrandFailedNotice(e.Name, e.ID, err.Error()))
			return
		}
		goesOn = b.errandAct(ctx, e, ans, lines, ch)
		if !goesOn && ans.Action == errand.AskOwner {
			// The owner has the question; the call has done what it can.
			_ = v.say(ctx, errandCallLater)
			return
		}
	}
	if cur, err := b.store.GetErrand(ctx, e.ID); err == nil && cur.State == store.ErrandOpen {
		b.tellErrandOwner(ctx, cur, outboundmsg.ErrandCallEndedNotice(e.Name, e.ID))
	}
}
