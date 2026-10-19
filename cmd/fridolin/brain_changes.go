package main

import (
	"log"
	"slices"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/history"
)

// Edits and deletions of WhatsApp messages a brain has received. A message
// deleted for everyone must not be answered, nor stay in the history,
// which brain backups copy to Drive. An edit of a message that waits
// replaces its text; once a run has acted on a message, starting another
// could repeat what it did (book the table twice), so the edit only goes
// into the history for the next run to see. An answer a run finished
// before the deletion, which waits in the outbox for the connection, still
// goes out: the outbox does not know which message it answers.

// Change applies an edit or a deletion to the brain's queue: a waiting
// message gets the new text or is dropped, and a run for a message that
// was deleted is stopped, so it sends nothing. It reports whether the
// message was still waiting, which no run has read yet, and for an edit
// of one, the message as edited.
func (br *Brain) Change(c messageChange) (waiting bool, edited *Inbound) {
	br.mu.Lock()
	var gone []Inbound
	for i, p := range br.pending {
		if !p.changes(c) {
			continue
		}
		waiting = true
		if next, _, keep := p.applyChange(c); keep {
			br.pending[i] = next
			if c.Kind == changeEdit {
				if j := slices.IndexFunc(next.ownParts(), c.names); j >= 0 {
					part := next.ownParts()[j]
					edited = &part
				}
			}
		} else {
			gone = append(gone, p)
			br.pending = append(br.pending[:i], br.pending[i+1:]...)
		}
		break
	}
	var stop *brainRun
	if cur := br.current; !waiting && cur != nil && cur.msg.changes(c) {
		// A run cannot take back what it read; one whose whole message
		// was deleted is stopped. Part of an album deleted, it goes on.
		if _, _, keep := cur.msg.applyChange(c); c.Kind == changeRevoke && !keep {
			stop = cur
		}
	}
	br.mu.Unlock()
	dropped(gone)
	if stop != nil {
		// Not waiting for it to end: this runs on whatsmeow's event
		// goroutine, and a stopped run sends nothing anyway.
		log.Printf("brain %s: the message in %s it was answering was deleted; stopping the run", br.id, c.Chat)
		br.interruptRun(stop)
	}
	return waiting, edited
}

// recordChange writes c into the brain's history. A deletion takes the
// message's line out. An edit of a message still waiting, edited, replaces
// its line, since that is what the run will read; after a run read it,
// the edit is added as an event instead, after the line the run acted on,
// so only when the history holds that line.
// A message deleted after a run read it (not waiting) leaves an event too,
// so the next run knows why it no longer sees what it answered.
func (br *Brain) recordChange(c messageChange, waiting bool, edited *Inbound) {
	is := func(e history.Entry) bool {
		return e.ID == c.Target && e.Type == history.In && e.From == c.Sender &&
			(e.Chat == c.Chat || (c.ChatAlt != "" && e.Chat == c.ChatAlt))
	}
	var n int
	var err error
	switch {
	case c.Kind == changeRevoke:
		n, err = br.rewriteHistory(c.Target, is, func(history.Entry) *history.Entry { return nil })
	case edited != nil:
		n, err = br.rewriteHistory(c.Target, is, func(e history.Entry) *history.Entry {
			e.Text = historyTextWithMedia(edited.textWithQuote(), edited.Media)
			return &e
		})
	default:
		// Noted only after the line of the message a run read. One the
		// history never held stays out: a one-time code the OTP guard
		// caught, which its sender corrects, say, or a message the
		// debouncer dropped.
		held, err := br.holdsEntry(c.Target, is)
		if err != nil {
			log.Printf("brain %s: could not look for the edited message in %s in the history: %v", br.id, c.Chat, err)
		}
		if held {
			br.appendHistory(c.Chat, c.Sender, "edited a message to: "+strconv.Quote(c.Text), history.Event)
		}
		return
	}
	if err != nil {
		log.Printf("brain %s: could not apply the %s of a message in %s to the history: %v", br.id, c.Kind, c.Chat, err)
		return
	}
	if c.Kind == changeRevoke && n > 0 && !waiting {
		br.appendHistory(c.Chat, c.Sender, "deleted a message they had sent", history.Event)
	}
}

// rewriteHistory serializes file rewrites with append and outbox delivery.
func (br *Brain) rewriteHistory(id string, match func(history.Entry) bool, replace func(history.Entry) *history.Entry) (int, error) {
	br.historyMu.Lock()
	defer br.historyMu.Unlock()
	return history.Rewrite(br.root, id, match, replace)
}

func (br *Brain) holdsEntry(id string, match func(history.Entry) bool) (bool, error) {
	br.historyMu.Lock()
	defer br.historyMu.Unlock()
	return history.HoldsEntry(br.root, id, match)
}
