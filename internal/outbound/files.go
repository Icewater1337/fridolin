package outbound

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Shelf keeps the bridge's copies of the files replies carry (a brain's
// reply_file) until the reply went out or was given up on: the brain may
// change or remove its own file meanwhile, and a voice note is a
// converted copy anyway. Each copy is a blob, a file of its own in Dir
// named by a random name and the file's extension.
type Shelf struct{ Dir string }

// errFileGone is a file part whose blob is not there: nothing to retry.
var errFileGone = errors.New("the file to send is gone")

// errBlobName refuses a blob name that is not one Create made.
var errBlobName = errors.New("not a blob of the outbox's")

// Create makes a new, empty blob ext (".pdf", say) and returns it open
// for writing, with its name.
func (s Shelf) Create(ext string) (*os.File, string, error) {
	if s.Dir == "" {
		return nil, "", errors.New("the outbox keeps no files")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, "", err
	}
	name := strings.ToLower(rand.Text()) + cleanExt(ext)
	f, err := os.OpenFile(filepath.Join(s.Dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", err
	}
	return f, name, nil
}

// cleanExt is ext if it is a plain one, lower case, else "".
func cleanExt(ext string) string {
	ext = strings.ToLower(ext)
	if len(ext) < 2 || len(ext) > 10 || ext[0] != '.' {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

// Path is where the blob name is.
func (s Shelf) Path(name string) (string, error) {
	if s.Dir == "" || name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%w: %q", errBlobName, name)
	}
	return filepath.Join(s.Dir, name), nil
}

// Remove takes the blob name away.
func (s Shelf) Remove(name string) {
	p, err := s.Path(name)
	if err != nil {
		return
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("outbox: removing the file %s: %v", name, err)
	}
}

// Prune takes away the blobs made before before: a reply that old was
// given up on, and its blob only stayed because the bridge stopped
// before it could take it away.
func (s Shelf) Prune(before time.Time) {
	if s.Dir == "" {
		return
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && fi.ModTime().Before(before) {
			s.Remove(e.Name())
		}
	}
}

// KeepFiles makes the outbox keep the files replies carry in s. Call it
// before Start; without it, File refuses.
func (o *Outbox) KeepFiles(s Shelf) { o.shelf = s }

// Shelf is where the outbox keeps the files replies carry.
func (o *Outbox) Shelf() Shelf { return o.shelf }

// File puts a file brain sends into chat into the outbox: f, a blob
// of the outbox's Shelf, with caption. Once it went out, the brain's
// history records it as a line that names the file, then the caption.
// The blob goes once the reply went out or was given up on.
func (o *Outbox) File(ctx context.Context, brain, chat string, f store.OutboxFile, caption string) error {
	if _, err := o.shelf.Path(f.Blob); err != nil {
		return err
	}
	said := "[sent " + f.Kind + ": " + f.Name + "]"
	if caption != "" {
		said += "\n" + caption
	}
	return o.add(ctx, store.OutboxRow{Chat: chat, Brain: brain, History: said, Parts: []store.OutboxPart{{Text: caption, File: &f}}})
}

// outboxFileLimit bounds the sending of one file, its upload included.
const outboxFileLimit = 5 * time.Minute

// sendFile sends a file part.
func (o *Outbox) sendFile(ctx context.Context, m Link, r *store.OutboxRow, p store.OutboxPart) error {
	path, err := o.shelf.Path(p.File.Blob)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		// Gone: a retry will not bring it back.
		return fmt.Errorf("%w: %w", errFileGone, err)
	}
	log.Printf("Sending %s %s to %s", p.File.Kind, p.File.Name, r.Chat)
	return m.SendFile(ctx, r.Chat, chat.File{Path: path, Name: p.File.Name, Mime: p.File.Mime, Kind: p.File.Kind, Caption: p.Text, Seconds: p.File.Seconds})
}

// dropFiles takes the blobs of r away: it went out, or was given up on.
func (o *Outbox) dropFiles(r store.OutboxRow) {
	for _, p := range r.Parts {
		if p.File != nil {
			o.shelf.Remove(p.File.Blob)
		}
	}
}
