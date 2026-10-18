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
	"os"
)

// rewriteHistory rewrites the brain's history file: each line match
// accepts, which must be one of the message id's, is replaced by what
// replace returns for it, or left out when that is nil. Every other line,
// one that does not decode included, stays as it was. It returns how many
// lines matched. The new file is written next to the old one in the
// brain's folder and renamed over it, through the folder's root like
// every other access, so that a crash leaves one or the other.
func rewriteHistory(root *os.Root, id string, match func(historyEntry) bool, replace func(historyEntry) *historyEntry) (int, error) {
	in, err := openInFolder(root, historyFileName, os.O_RDONLY, 0)
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
	out, err := openInFolder(root, tmpName, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		out.Close()
		_ = root.Remove(tmpName) // gone after the rename
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
	if err := root.Rename(tmpName, historyFileName); err != nil {
		return 0, fmt.Errorf("replacing the history: %w", err)
	}
	return n, nil
}

// holdsEntry reports whether the brain's history has a line match
// accepts, which must be one of the message id's. Like rewriteHistory, it
// decodes the lines only of a file that holds id at all.
func holdsEntry(root *os.Root, id string, match func(historyEntry) bool) (bool, error) {
	f, err := openInFolder(root, historyFileName, os.O_RDONLY, 0)
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
