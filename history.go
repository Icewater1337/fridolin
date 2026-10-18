package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"slices"
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

// maxHistoryLine is the longest history line the reader takes. A longer
// one, a pasted book say, is skipped and the reading goes on before it: a
// run's prompt would not hold it anyway, and one line must not stop the
// history from being read, as it once froze every chat's preface at the
// line before it.
var maxHistoryLine = 4 << 20

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
		if long || n > maxHistoryLine {
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
		case tailLen+len(chunk) > maxHistoryLine:
			long, tail, tailLen = true, nil, 0
		case len(chunk) > 0:
			tail, tailLen = append(tail, slices.Clone(chunk)), tailLen+len(chunk)
		}
		end = start
	}
	line(nil) // the first line of the file
	return skipped, nil
}

// scanHistory runs scanHistoryBackward over the brain's history, through
// its folder (see openInFolder). It returns false when the history cannot
// be read: there is none yet, or it is no regular file in the folder.
func (br *Brain) scanHistory(budget int64, visit func(line []byte) bool) bool {
	f, err := openInFolder(br.root, historyFileName, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	skipped, err := scanHistoryBackward(f, st.Size(), budget, visit)
	if skipped > 0 {
		log.Printf("brain %s: skipped %d history lines longer than %d KiB", br.id, skipped, maxHistoryLine>>10)
	}
	if err != nil {
		log.Printf("brain %s: reading its history: %v", br.id, err)
	}
	return true
}

// mentionsChat reports whether line, a history line as written, may be
// one of the chats': a quick look for one of their JIDs before the line is
// decoded, which most lines of a busy brain's other chats fail.
func mentionsChat(line []byte, chats []string) bool {
	for _, c := range chats {
		if bytes.Contains(line, []byte(c)) {
			return true
		}
	}
	return false
}

// recentHistory returns the last n entries of the brain's history, oldest
// first, from no further back than budget bytes: the dashboard's feed. A
// reset marker is no message and stays out.
func (br *Brain) recentHistory(n int, budget int64) []historyEntry {
	var out []historyEntry
	br.scanHistory(budget, func(line []byte) bool {
		var e historyEntry
		if json.Unmarshal(line, &e) == nil && e.Type != historyReset {
			out = append(out, e)
		}
		return len(out) < n
	})
	slices.Reverse(out)
	return out
}
