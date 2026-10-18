package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
)

// Edits and deletions of WhatsApp messages a brain has received. A message
// deleted for everyone must not be answered, nor stay in the history,
// which brain backups copy to Drive. An edit of a message that waits
// replaces its text; once a run has acted on a message, starting another
// could repeat what it did (book the table twice), so the edit only goes
// into the history for the next run to see.

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
	is := func(e historyEntry) bool {
		return e.ID == c.Target && e.Type == historyIn && e.From == c.Sender &&
			(e.Chat == c.Chat || (c.ChatAlt != "" && e.Chat == c.ChatAlt))
	}
	var n int
	var err error
	switch {
	case c.Kind == changeRevoke:
		n, err = br.rewriteHistory(c.Target, is, func(historyEntry) *historyEntry { return nil })
	case edited != nil:
		n, err = br.rewriteHistory(c.Target, is, func(e historyEntry) *historyEntry {
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
			br.appendHistory(c.Chat, c.Sender, "edited a message to: "+strconv.Quote(c.Text), historyEvent)
		}
		return
	}
	if err != nil {
		log.Printf("brain %s: could not apply the %s of a message in %s to the history: %v", br.id, c.Kind, c.Chat, err)
		return
	}
	if c.Kind == changeRevoke && n > 0 && !waiting {
		br.appendHistory(c.Chat, c.Sender, "deleted a message they had sent", historyEvent)
	}
}

// rewriteHistory rewrites the brain's history file: each line match
// accepts, which must be one of the message id's, is replaced by what
// replace returns for it, or left out when that is nil. Every other line,
// one that does not decode included, stays as it was. It returns how many
// lines matched. The new file is written next to the old one in the
// brain's folder and renamed over it, through the folder's root like
// every other access, so that a crash leaves one or the other.
func (br *Brain) rewriteHistory(id string, match func(historyEntry) bool, replace func(historyEntry) *historyEntry) (int, error) {
	br.historyMu.Lock()
	defer br.historyMu.Unlock()
	in, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer in.Close()
	// A change often names a message this history never got: one in
	// another brain's chat, one the debouncer dropped. Such a file is only
	// read, not copied and synced, for every deletion in an allowed chat
	// holds up incoming messages, which wait for historyMu.
	if found, err := holdsID(in, id); err != nil || !found {
		return 0, err
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmpName := "." + historyFileName + ".tmp-" + hex.EncodeToString(suffix[:])
	out, err := openInFolder(br.root, tmpName, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		out.Close()
		_ = br.root.Remove(tmpName) // gone after the rename
	}()

	n := 0
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 {
			var e historyEntry
			if json.Unmarshal(bytes.TrimSpace(line), &e) == nil && match(e) {
				n++
				repl := replace(e)
				if repl == nil {
					line = nil
				} else if data, err := json.Marshal(repl); err == nil {
					line = append(data, '\n')
				}
			}
			if _, err := w.Write(line); err != nil {
				return 0, err
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return 0, rerr
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	if err := br.root.Rename(tmpName, historyFileName); err != nil {
		return 0, fmt.Errorf("replacing the history: %w", err)
	}
	return n, nil
}

// holdsEntry reports whether the brain's history has a line match
// accepts, which must be one of the message id's. Like rewriteHistory, it
// decodes the lines only of a file that holds id at all.
func (br *Brain) holdsEntry(id string, match func(historyEntry) bool) (bool, error) {
	br.historyMu.Lock()
	defer br.historyMu.Unlock()
	f, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if found, err := holdsID(f, id); err != nil || !found {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		var e historyEntry
		if len(line) > 0 && json.Unmarshal(bytes.TrimSpace(line), &e) == nil && match(e) {
			return true, nil
		}
		if errors.Is(rerr, io.EOF) {
			return false, nil
		}
		if rerr != nil {
			return false, rerr
		}
	}
}

// holdsID reports whether r holds a history line with the message ID id,
// or something that reads like one: the bytes `"id":` and id as JSON,
// which a line's text cannot hold unescaped.
func holdsID(r io.Reader, id string) (bool, error) {
	key, err := json.Marshal(id)
	if err != nil {
		return false, err
	}
	needle := append([]byte(`"id":`), key...)
	buf := make([]byte, 64<<10)
	if len(needle) > len(buf)/2 {
		return true, nil // too long to look for; the rewrite will tell
	}
	kept := 0 // the end of the last read, where the needle may begin
	for {
		n, err := r.Read(buf[kept:])
		if bytes.Contains(buf[:kept+n], needle) {
			return true, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		end := kept + n
		kept = min(end, len(needle)-1)
		copy(buf, buf[end-kept:end])
	}
}
