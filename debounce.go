package main

import (
	"sync"
	"time"
)

// Debouncer merges messages that one person sends in quick succession in one
// chat — five photos, or a file followed by "what is this?" — into a single
// brain run. A batch is flushed once the chat has been quiet for the quiet
// period, and never later than the max wait after its first message.
type Debouncer struct {
	quiet   func() time.Duration
	maxWait func() time.Duration
	flush   func(Inbound)

	mu      sync.Mutex
	batches map[string]*pendingBatch
}

type pendingBatch struct {
	msg     Inbound
	started time.Time
	timer   *time.Timer
}

// NewDebouncer creates a debouncer. quiet and maxWait are read for every
// message, so configuration changes apply immediately.
func NewDebouncer(quiet, maxWait func() time.Duration, flush func(Inbound)) *Debouncer {
	return &Debouncer{quiet: quiet, maxWait: maxWait, flush: flush, batches: map[string]*pendingBatch{}}
}

// batchKey includes the target brain: two hand-offs for different brains that
// answer in the same chat must not merge into one run in one brain.
func batchKey(m Inbound) string { return m.Brain + "|" + m.JID + "|" + m.Sender }

// Add queues m, merging it into the sender's open batch for the chat.
func (d *Debouncer) Add(m Inbound) {
	quiet := d.quiet()
	if quiet <= 0 {
		d.flush(m)
		return
	}
	key := batchKey(m)
	now := time.Now()

	d.mu.Lock()
	b, ok := d.batches[key]
	if ok {
		b.msg = b.msg.merge(m)
		b.timer.Stop()
	} else {
		b = &pendingBatch{msg: m, started: now}
		d.batches[key] = b
	}
	wait := quiet
	if limit := d.maxWait(); limit > 0 {
		if left := b.started.Add(limit).Sub(now); left < wait {
			wait = left
		}
	}
	if wait <= 0 {
		delete(d.batches, key)
		msg := b.msg
		d.mu.Unlock()
		d.flush(msg)
		return
	}
	b.timer = time.AfterFunc(wait, func() { d.fire(key, b) })
	d.mu.Unlock()
}

func (d *Debouncer) fire(key string, b *pendingBatch) {
	d.mu.Lock()
	if d.batches[key] != b {
		d.mu.Unlock()
		return
	}
	delete(d.batches, key)
	msg := b.msg
	d.mu.Unlock()
	d.flush(msg)
}

// Change applies an edit or a deletion to the open batch that holds the
// message it changes, and reports whether one did. A batch left empty by
// a deletion is dropped: nothing of it reaches a brain.
func (d *Debouncer) Change(c messageChange) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, b := range d.batches {
		if !b.msg.changes(c) {
			continue
		}
		msg, _, keep := b.msg.applyChange(c)
		if !keep {
			b.timer.Stop()
			delete(d.batches, k)
			return true
		}
		b.msg = msg
		return true
	}
	return false
}

// Drop discards every open batch for a chat (after !reset or !stop).
func (d *Debouncer) Drop(jid string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, b := range d.batches {
		if b.msg.JID == jid {
			b.timer.Stop()
			delete(d.batches, k)
		}
	}
}

// DropAll discards every open batch (on shutdown).
func (d *Debouncer) DropAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, b := range d.batches {
		b.timer.Stop()
		delete(d.batches, k)
	}
}

// Pending returns how many batches are waiting.
func (d *Debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.batches)
}
