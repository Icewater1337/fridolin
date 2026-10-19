package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"io"
	"log"
	"os"
	"slices"
	"time"
)

// Reading a brain's history back from its end. The file holds every message
// of every chat the brain ever had and only grows, while what the bridge
// wants of it is recent: the last messages of one chat for a run's prompt,
// a code request in the last minutes for the OTP guard, the last lines for
// the dashboard's feed. So it is read backwards, a block at a time, until
// the reader has what it wants or a budget of bytes runs out, never from
// the start.

// historyBlock is how much of the history the reader reads at a time.
const historyBlock = 64 << 10

// MaxLine is the longest history line the reader takes. A longer
// one, a pasted book say, is skipped and the reading goes on before it: a
// run's prompt would not hold it anyway, and one line must not stop the
// history from being read, as it once froze every chat's preface at the
// line before it.
var MaxLine = 4 << 20

// scanHistoryBackward calls visit with each line of r, a history file of
// size bytes, newest first, until visit returns false, the start of the
// file is reached or more than budget bytes were read (0 for no limit).
// Blank lines are left out and lines longer than maxHistoryLine skipped; it
// reports how many were. visit must not keep the line, whose bytes the
// reader reuses.
func scanHistoryBackward(r io.ReaderAt, size, budget int64, visit func(line []byte) bool) (skipped int, err error) {
	var (
		buf = make([]byte, historyBlock)
		// tail is the end of the line being read, a part for each block
		// it crosses, newest first: what comes after the last newline
		// found so far, in front of which the rest of it lies further
		// back. The parts are joined once, when the line's start is
		// found: joined block by block, a line of a few MB would be
		// copied again for each of its blocks. tailLen is their length.
		tail    [][]byte
		tailLen int
		// joined holds the last line made of parts, reused for the next.
		joined []byte
		// long is set while the line being read is longer than
		// maxHistoryLine; its bytes are dropped until it ends.
		long bool
	)
	// line hands the line made of head and tail to visit, or skips it.
	line := func(head []byte) bool {
		n := len(head) + tailLen
		parts := tail
		tail, tailLen = nil, 0
		if long || n > MaxLine {
			long = false
			skipped++
			return true
		}
		l := head
		if len(parts) > 0 {
			joined = append(slices.Grow(joined[:0], n), head...)
			for i := len(parts) - 1; i >= 0; i-- {
				joined = append(joined, parts[i]...)
			}
			l = joined
		}
		if l = bytes.TrimSpace(l); len(l) == 0 {
			return true
		}
		return visit(l)
	}
	end := size
	for end > 0 {
		if budget > 0 && size-end >= budget {
			// The budget is spent: the line read so far is cut off and
			// left out.
			return skipped, nil
		}
		start := max(0, end-historyBlock)
		chunk := buf[:end-start]
		if _, err := r.ReadAt(chunk, start); err != nil && !errors.Is(err, io.EOF) {
			return skipped, err
		}
		for {
			i := bytes.LastIndexByte(chunk, '\n')
			if i < 0 {
				break
			}
			if !line(chunk[i+1:]) {
				return skipped, nil
			}
			chunk = chunk[:i]
		}
		// What is left of the block is the start of a line that begins
		// further back, or at the start of the file.
		switch {
		case long:
		case tailLen+len(chunk) > MaxLine:
			long, tail, tailLen = true, nil, 0
		case len(chunk) > 0:
			tail, tailLen = append(tail, slices.Clone(chunk)), tailLen+len(chunk)
		}
		end = start
	}
	line(nil) // the first line of the file
	return skipped, nil
}

// Scan hands visit the entries of the brain's history, newest
// first, as scanHistoryBackward reads them back from its end, until visit
// returns false: through the folder (see fsx.OpenInFolder), and none when the
// history cannot be read, as when there is none yet. Only the lines want
// takes, as written, are decoded, a quick look that most lines of a busy
// brain's other chats fail; nil takes them all. A line that is no history
// entry is skipped, and how many were is logged, as are lines too long to
// take: a brain can write anything into its own history.
func Scan(root *os.Root, brainID string, budget int64, want func(line []byte) bool, visit func(e Entry) bool) {
	f, err := fsx.OpenInFolder(root, FileName, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	broken := 0
	long, err := scanHistoryBackward(f, st.Size(), budget, func(line []byte) bool {
		if want != nil && !want(line) {
			return true
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil {
			broken++
			return true
		}
		return visit(e)
	})
	if long > 0 {
		log.Printf("brain %s: skipped %d history lines longer than %d KiB", brainID, long, MaxLine>>10)
	}
	if broken > 0 {
		log.Printf("brain %s: skipped %d history lines that are not JSON", brainID, broken)
	}
	if err != nil {
		log.Printf("brain %s: reading its history: %v", brainID, err)
	}
}

// mentionsChat returns a want for scanHistory that takes the lines that
// may be one of the chats': those that hold one of their JIDs. The JIDs
// are made bytes once, not for each of the many lines it looks at.
func mentionsChat(chats []string) func(line []byte) bool {
	jids := make([][]byte, len(chats))
	for i, c := range chats {
		jids[i] = []byte(c)
	}
	return func(line []byte) bool {
		for _, j := range jids {
			if bytes.Contains(line, j) {
				return true
			}
		}
		return false
	}
}

// Tail returns the last n entries of the brain's history, oldest
// first, from no further back than budget bytes: the dashboard's feed. A
// reset marker is no message and stays out.
func Tail(root *os.Root, brainID string, n int, budget int64) []Entry {
	var out []Entry
	Scan(root, brainID, budget, nil, func(e Entry) bool {
		if e.Type != Reset {
			out = append(out, e)
		}
		return len(out) < n
	})
	slices.Reverse(out)
	return out
}

// Entry is one line in a brain's conversation-history.jsonl file.
// Brains' own scripts read the file too, so docs/BRAINS.md and
// docs/schemas/history-line.schema.json describe the line: fields are
// added, never renamed or removed.
type Entry struct {
	TS   string `json:"ts"`
	From string `json:"from"`
	Chat string `json:"chat"` // JID of the chat (DM participant or group)
	Text string `json:"text"`
	Type string `json:"type"` // In, Out, Event or Reset
	// Via marks an "out" line that came in through /send: "brain" (From is
	// the brain that sent it), "script" (the scripts token) or "api" (no
	// token). A brain's own replies have none.
	Via string `json:"via,omitempty"`
	// ID is the WhatsApp message's ID for a message from WhatsApp, which an
	// edit or a deletion of it names (see recordChange).
	ID string `json:"id,omitempty"`
	// OTPRequest is the id /send answered a request for a one-time code
	// with, on the "out" line of that request (see otpRequest).
	OTPRequest string `json:"otp_request,omitempty"`
}

// The history entry types. They are written to every brain's history file,
// which `fridolin history` and a brain's own scripts read, so their values
// must not change.
const (
	In  = "in"  // a message to the brain
	Out = "out" // a message the brain sent, or one sent on its behalf
	// Event is something that happened in a chat without a message,
	// such as a poll vote. Its From is who did it, and its text says what,
	// after their name.
	Event = "event"
	// Reset marks where !reset started a chat afresh. Its From is
	// who sent it, and it has no text. A run's prompt shows only the
	// chat's lines after the newest one; it is no message, so neither the
	// dashboard's feed nor `fridolin history` shows it.
	Reset = "reset"
)

// FileName is the brain's conversation history, in its folder.
const FileName = "conversation-history.jsonl"

// SameLine reports whether e is the history line l: equal, or for a
// message from WhatsApp, whose line an edit rewrites, equal but for the
// text.
func (l Entry) SameLine(e Entry) bool {
	if l.ID != "" {
		e.Text = l.Text
	}
	return e == l
}

// ForChat returns the entries of the chat that goes by chats in the
// brain's history, oldest first. It reads them back from the end of the
// file, each newest first to enough, until enough reports that it has
// what it needs, or it reaches the chat's newest reset marker (see
// Reset), or budget bytes are read, or the file's start is
// reached. A DM's lines may be under its LID or its number, and both are
// the one chat. It returns none when the history cannot be read (see
// openInFolder).
func ForChat(root *os.Root, brainID string, chats []string, budget int64, enough func(Entry) bool) []Entry {
	var entries []Entry
	Scan(root, brainID, budget, mentionsChat(chats), func(e Entry) bool {
		if !slices.Contains(chats, e.Chat) {
			return true
		}
		if e.Type == Reset {
			return false
		}
		entries = append(entries, e)
		return !enough(e)
	})
	slices.Reverse(entries)
	return entries
}

// Append appends entry to the history file, stamped with the
// time now unless it has a time, and returns it as it reads back, which
// the caller can identify: invalid UTF-8 in its text is written as U+FFFD.
// The caller owns serialization with other history writes.
func Append(root *os.Root, entry Entry) (Entry, error) {
	if entry.TS == "" {
		entry.TS = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return entry, err
	}
	var written Entry
	if json.Unmarshal(data, &written) == nil {
		entry = written
	}
	// Through the folder: appending through a symlink would let a brain
	// write to files its sandbox denies it, another brain's among them.
	f, err := fsx.OpenInFolder(root, FileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return entry, err
	}
	_, _ = f.Write(append(data, '\n'))
	f.Close()
	return entry, nil
}
