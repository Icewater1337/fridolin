package main

import (
	"context"
	"errors"
	"fmt"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// Outbox delivers brains' replies. WhatsApp does not queue a message
// while the connection is down, and a reply sent then would be lost,
// while the brain's history, which the next run reads, said it went out.
// So a reply goes into bridge.db first and leaves it only once WhatsApp
// took every message of it: the history records it then, and until then
// the brain's runs read it from here (Bridge.undelivered). A failed send
// is tried again with backoff, from the message that failed (the chat
// does not get "[1/3]" twice), and again at once when WhatsApp connects;
// replies still waiting when the bridge stops go out after the next
// start. A reply that could not go out within outboxTTL is given up on,
// since an answer that late would only confuse, and the dashboard says
// so. Replies to one chat go out in order.
type Outbox struct {
	store *store.Store
	// messenger is the bridge's, read at each reply and each send: the
	// bridge wraps it after the outbox is made.
	messenger func() outboundmsg.Link
	// pace is the pause between the messages of a reply, so that they
	// arrive in order; 0 in dev mode.
	pace time.Duration
	// delivered takes each reply that went out in full out of the outbox
	// (Bridge.delivered, which records it in its brain's history too);
	// nil only removes it.
	delivered func(ctx context.Context, r store.OutboxRow) error
	now       func() time.Time

	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Delivery timings. A send is retried after outboxFirstRetry, doubling up
// to outboxMaxRetry, for as long as outboxTTL after the reply was written.
const (
	outboxFirstRetry = 5 * time.Second
	outboxMaxRetry   = 2 * time.Minute
	outboxTTL        = time.Hour
	outboxSendLimit  = 30 * time.Second // one message
	outboxIdle       = time.Minute      // the longest the outbox sleeps
	outboxPace       = 300 * time.Millisecond
	outboxPruneEvery = time.Hour // how often replies given up on are forgotten
)

// newOutbox makes an outbox and starts delivering, the replies a
// previous run of the bridge left first. Stop ends it.
func newOutbox(store *store.Store, messenger func() outboundmsg.Link, pace time.Duration, delivered func(context.Context, store.OutboxRow) error) *Outbox {
	ctx, cancel := context.WithCancel(context.Background())
	o := &Outbox{
		store: store, messenger: messenger, pace: pace, delivered: delivered, now: time.Now,
		wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}
	go o.run(ctx)
	return o
}

// Stop ends delivery and waits for a send in flight to end. What did not
// go out stays in bridge.db for the next start.
func (o *Outbox) Stop() {
	o.once.Do(o.cancel)
	<-o.done
}

// Reply puts what brain answered in chat into the outbox: each [POLL]
// block as a poll, then the rest as text in chunks of at most maxChunk
// characters (see chunks). Once all of it went out, the brain's history
// records what the chat got, in that order: each poll as its question and
// numbered options, as it reads when WhatsApp refuses the poll, then the
// text. The [POLL] markup never reached anyone.
func (o *Outbox) Reply(ctx context.Context, brain, chat, text string, maxChunk int) error {
	polls, remaining := outboundmsg.ExtractPolls(text)
	var parts []store.OutboxPart
	var said []string
	// The polls go before the text that introduces them, as the example
	// prompt promises (see sendPolls).
	for _, p := range polls {
		parts = append(parts, store.OutboxPart{Question: p.Question, Options: p.Options, Max: p.MaxSelectable})
		said = append(said, outboundmsg.PollAsText(p))
	}
	parts = append(parts, o.textParts(chat, remaining, maxChunk)...)
	if remaining != "" {
		said = append(said, remaining)
	}
	return o.add(ctx, store.OutboxRow{Chat: chat, Brain: brain, History: strings.Join(said, "\n\n"), Parts: parts})
}

// Text puts text into the outbox, in chunks of at most maxChunk
// characters, recorded in no history: an alert of the bridge's own.
func (o *Outbox) Text(ctx context.Context, chat, text string, maxChunk int) error {
	return o.add(ctx, store.OutboxRow{Chat: chat, Parts: o.textParts(chat, text, maxChunk)})
}

// Question puts a question brain asks mid-run into the outbox, in chunks
// of at most maxChunk characters. Like a reply, the brain's history
// records it once all of it went out: the answer comes as the chat's next
// message, and the run for it must see what it answers.
func (o *Outbox) Question(ctx context.Context, brain, chat, text string, maxChunk int) error {
	return o.add(ctx, store.OutboxRow{Chat: chat, Brain: brain, History: text, Parts: o.textParts(chat, text, maxChunk)})
}

// textParts is text as the messages it goes to chat as.
func (o *Outbox) textParts(chat, text string, maxChunk int) []store.OutboxPart {
	var parts []store.OutboxPart
	for _, c := range o.chunks(chat, text, maxChunk) {
		parts = append(parts, store.OutboxPart{Text: c})
	}
	return parts
}

// wholeText is a Messenger that shows what it sends to some chats whole,
// however long: siteChatMessenger, whose build chats are on the Build
// page. Text for such a chat is not split into numbered messages.
type wholeText interface {
	TakesWhole(chat string) bool
}

// chunks splits text into the messages it goes to chat as: of at most
// maxChunk characters each, or one for a chat that takes text whole.
func (o *Outbox) chunks(chat, text string, maxChunk int) []string {
	if w, ok := o.messenger().(wholeText); ok && w.TakesWhole(chat) {
		maxChunk = math.MaxInt
	}
	return outboundmsg.Chunk(text, maxChunk)
}

func (o *Outbox) add(ctx context.Context, r store.OutboxRow) error {
	if len(r.Parts) == 0 {
		return nil
	}
	r.CreatedAt = o.now()
	if _, err := o.store.AddOutbox(ctx, r); err != nil {
		return fmt.Errorf("queueing the reply to %s: %w", r.Chat, err)
	}
	o.Wake()
	return nil
}

// Wake makes the outbox look at its replies now.
func (o *Outbox) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *Outbox) run(ctx context.Context) {
	defer close(o.done)
	force := true // what a previous run left is due at once
	offline := false
	var pruned time.Time
	for {
		// Here rather than when the dashboard asks (OutboxSummary), which
		// it does every few seconds.
		if now := o.now(); now.Sub(pruned) >= outboxPruneEvery {
			pruned = now
			if err := o.store.PruneOutbox(ctx, now.Add(-store.OutboxFailedKeep)); err != nil && ctx.Err() == nil {
				log.Printf("outbox: %v", err)
			}
		}
		wait, waiting, failedOffline := o.flush(ctx, force)
		force = false
		offline = (offline || failedOffline) && waiting
		var online <-chan struct{}
		if offline {
			m := o.messenger()
			if isConnected(m) {
				// It connected since a send failed for want of it:
				// everything is due now.
				force, offline = true, false
				continue
			}
			online = m.Connected()
		}
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
		case <-online:
			force, offline = true, false
		case <-time.After(wait):
		}
	}
}

// flush sends the replies that are due, all that wait with force. It
// returns how long until the next one is, whether any still waits, and
// whether a send failed while the messenger could not send.
func (o *Outbox) flush(ctx context.Context, force bool) (wait time.Duration, waiting, offline bool) {
	wait = outboxIdle
	rows, err := o.store.PendingOutbox(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("outbox: %v", err)
		}
		return wait, false, false
	}
	blocked := map[string]bool{} // chats with an earlier reply still waiting
	for _, r := range rows {
		if ctx.Err() != nil {
			return wait, true, offline
		}
		now := o.now()
		if blocked[r.Chat] {
			waiting = true
			continue
		}
		if now.Sub(r.CreatedAt) > outboxTTL {
			o.giveUp(ctx, r, fmt.Sprintf("not delivered within %v: %s", outboxTTL, r.LastError))
			continue
		}
		if !force && r.NextTryAt.After(now) {
			blocked[r.Chat], waiting = true, true
			wait = min(wait, r.NextTryAt.Sub(now))
			continue
		}
		err := o.deliver(ctx, &r)
		switch {
		case err == nil:
			continue
		case ctx.Err() != nil:
			return wait, true, offline
		case errors.Is(err, outboundmsg.ErrRefusedRecipient) || errors.Is(err, errNotAChat):
			o.giveUp(ctx, r, err.Error())
			continue
		case errors.Is(err, errNotRecorded):
			// All of it went out; only its row stayed. The chat's later
			// replies wait, so that the history records them in order.
			log.Printf("outbox: %v", err)
			blocked[r.Chat], waiting = true, true
			wait = min(wait, outboxFirstRetry)
			continue
		}
		blocked[r.Chat], waiting = true, true
		offline = offline || !isConnected(o.messenger())
		r.Attempts++
		delay := outboxDelay(r.Attempts)
		log.Printf("outbox: sending to %s failed (attempt %d), trying again in %v: %v", r.Chat, r.Attempts, delay, err)
		if err := o.store.OutboxRetry(ctx, r.ID, r.Attempts, now.Add(delay), err.Error()); err != nil && ctx.Err() == nil {
			log.Printf("outbox: %v", err)
		}
		wait = min(wait, delay)
	}
	return wait, waiting, offline
}

// outboxDelay is how long to wait after the given number of failed
// attempts: outboxFirstRetry, doubling up to outboxMaxRetry.
func outboxDelay(attempts int) time.Duration {
	d := outboxFirstRetry
	for i := 1; i < attempts && d < outboxMaxRetry; i++ {
		d *= 2
	}
	return min(d, outboxMaxRetry)
}

// deliver sends what is left of r, saving its progress after each part.
func (o *Outbox) deliver(ctx context.Context, r *store.OutboxRow) error {
	m := o.messenger()
	for i := r.NextPart; i < len(r.Parts); i++ {
		if i > r.NextPart && o.pace > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(o.pace):
			}
		}
		if err := o.send(ctx, m, r, r.Parts[i]); err != nil {
			return err
		}
		r.NextPart = i + 1
		if err := o.store.OutboxSent(ctx, r.ID, r.NextPart); err != nil {
			// Sent, but a retry would send it again: better than losing
			// the rest, which giving up would.
			log.Printf("outbox: recording a sent message to %s: %v", r.Chat, err)
		}
	}
	if err := o.finish(ctx, *r); err != nil {
		return fmt.Errorf("%w: a reply to %s: %w", errNotRecorded, r.Chat, err)
	}
	return nil
}

// errNotRecorded is a reply that went out in full but could not be taken
// out of the outbox. It is tried again soon, which sends nothing more and
// records it once.
var errNotRecorded = errors.New("delivered, but not taken out of the outbox")

// finish takes r, which went out in full, out of the outbox.
func (o *Outbox) finish(ctx context.Context, r store.OutboxRow) error {
	if o.delivered != nil {
		return o.delivered(ctx, r)
	}
	return o.store.OutboxDelivered(ctx, r.ID)
}

// send sends one part. A poll WhatsApp refuses while connected goes out
// as a numbered list instead; while it is not, the poll waits like text.
func (o *Outbox) send(ctx context.Context, m outboundmsg.Link, r *store.OutboxRow, p store.OutboxPart) error {
	ctx, cancel := context.WithTimeout(ctx, outboxSendLimit)
	defer cancel()
	if !p.IsPoll() {
		return m.SendMessage(ctx, r.Chat, p.Text)
	}
	poll := outboundmsg.Poll{Question: p.Question, Options: p.Options, MaxSelectable: p.Max}
	log.Printf("Sending poll to %s: %q (%d options)", r.Chat, p.Question, len(p.Options))
	msgID, err := m.SendPoll(ctx, r.Chat, p.Question, p.Options, p.Max)
	if err != nil {
		if !isConnected(m) || ctx.Err() != nil || errors.Is(err, outboundmsg.ErrRefusedRecipient) {
			return err
		}
		log.Printf("failed to send poll to %s: %v — falling back to text", r.Chat, err)
		return m.SendMessage(ctx, r.Chat, outboundmsg.PollAsText(poll))
	}
	// Kept so the votes can be matched back to its options and reach the
	// brain that sent it.
	if err := o.store.SavePoll(msgID, r.Chat, r.Brain, p.Question, p.Options); err != nil {
		log.Printf("failed to save poll %s to DB: %v", msgID, err)
	}
	return nil
}

// giveUp stops trying r, logs why and keeps the fact for the dashboard.
func (o *Outbox) giveUp(ctx context.Context, r store.OutboxRow, why string) {
	log.Printf("outbox: giving up on a reply to %s after %d attempts: %s", r.Chat, r.Attempts, why)
	if err := o.store.OutboxGiveUp(ctx, r.ID, why, o.now()); err != nil && ctx.Err() == nil {
		log.Printf("outbox: %v", err)
	}
}

// isConnected reports whether m can send now.
func isConnected(m outboundmsg.Link) bool {
	select {
	case <-m.Connected():
		return true
	default:
		return false
	}
}
