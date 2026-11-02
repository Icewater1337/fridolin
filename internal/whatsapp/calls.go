package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Voice calls (docs/CALLS.md). With Options.Calls the client carries
// meowcaller's call stack, which must be put onto each whatsmeow client
// before it connects: it answers or places 1:1 audio calls and moves
// their audio as 16 kHz mono PCM. What a call is for is the bridge's to
// decide (Options.OnCall); a call nobody takes rings on as before.

// ErrCallsOff is a call placed while calls are off.
var ErrCallsOff = errors.New("calls are off (calls.enabled)")

// Call is a WhatsApp voice call the bridge takes part in.
type Call interface {
	// ID is the call's id.
	ID() string
	// Peer is the other side as the bridge knows people: the JID of their
	// phone number where WhatsApp told it, else their LID (number false).
	Peer() (jid types.JID, number bool)
	// Answer takes an incoming call; Hangup ends a call either way.
	Answer() error
	Hangup() error
	// Listen hands fn each 60 ms frame of what the other side says.
	Listen(fn func(frame []float32))
	// Say plays pcm (signed 16-bit little-endian, mono, 16 kHz) and
	// closes the channel it returns once all of it played.
	Say(pcm []byte) <-chan struct{}
	// OnReady runs fn once audio flows; OnEnd once the call ended, with
	// why. Each takes one fn.
	OnReady(fn func())
	OnEnd(fn func(reason string))
}

// meowCall is a Call of meowcaller's.
type meowCall struct {
	c      *meowcaller.Call
	peer   types.JID
	number bool
}

func (m *meowCall) ID() string                      { return m.c.ID() }
func (m *meowCall) Peer() (types.JID, bool)         { return m.peer, m.number }
func (m *meowCall) Answer() error                   { return m.c.Answer() }
func (m *meowCall) Hangup() error                   { return m.c.Hangup() }
func (m *meowCall) Listen(fn func(frame []float32)) { m.c.Receive(meowcaller.SinkFunc(fn)) }
func (m *meowCall) OnReady(fn func())               { m.c.OnReady(fn) }
func (m *meowCall) OnEnd(fn func(reason string))    { m.c.OnEnd(fn) }

func (m *meowCall) Say(pcm []byte) <-chan struct{} {
	done := make(chan struct{})
	var once sync.Once
	p := meowcaller.NewPlayer()
	p.OnFinish(func() { once.Do(func() { close(done) }) })
	m.c.Subscribe(p)
	p.Play(meowcaller.PCMStream(io.NopCloser(bytes.NewReader(pcm))))
	return done
}

// callLog passes what meowcaller warns about on to the bridge's log.
type callLog struct{}

func (callLog) Write(p []byte) (int, error) {
	log.Printf("calls: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// attachCalls puts the call stack onto cli, which has not connected yet.
func (wa *Client) attachCalls(cli *whatsmeow.Client) *meowcaller.Client {
	mc := meowcaller.NewClient(cli, meowcaller.WithLogger(zerolog.New(callLog{}).Level(zerolog.WarnLevel)))
	mc.OnIncomingCall(wa.incomingCall)
	return mc
}

// incomingCall hands a call to the bridge, with its caller as the
// bridge knows people.
func (wa *Client) incomingCall(c *meowcaller.Call) {
	peer := c.Peer()
	jid := wa.identity.Canonical(wa.storeCtx(), wa.LIDStore(), peer, types.EmptyJID)
	call := &meowCall{c: c, peer: jid, number: jid.Server == types.DefaultUserServer}
	if wa.onCall == nil {
		return
	}
	go wa.onCall(call)
}

// PlaceCall calls number (digits with the country code) and returns the
// call once its offer went out; audio flows once they answer (OnReady).
func (wa *Client) PlaceCall(ctx context.Context, number string) (Call, error) {
	wa.mu.Lock()
	mc := wa.calls
	wa.mu.Unlock()
	if mc == nil {
		return nil, ErrCallsOff
	}
	jid := types.NewJID(number, types.DefaultUserServer)
	c, err := mc.Call(ctx, jid.String())
	if err != nil {
		return nil, err
	}
	return &meowCall{c: c, peer: jid, number: true}, nil
}
