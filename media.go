package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The media inbox. Attachments and transcripts arrive in the media folder
// (media_dir, by default <data_dir>/media), which no brain can read. Each
// brain reads and writes only its own inbox in it, <media_dir>/<brain id>/,
// so a group brain cannot open the files sent to the DM brain, nor replace
// them. A file moves into an inbox once the bridge knows which brain takes
// it (see claimMedia): WhatsApp downloads a file before the message is
// routed, and scripts drop theirs at the top of the media folder before
// they hand it to a brain with /ingest.

// incomingMediaDir is the folder in the media folder where WhatsApp
// attachments are downloaded before their message is routed. Brain ids
// cannot start with a dot, so it is never an inbox, and /ingest refuses
// files in it: those are on their way to the brain of their chat.
const incomingMediaDir = ".incoming"

// mediaInbox is the inbox of brain id in the media folder.
func mediaInbox(mediaDir, id string) string {
	return filepath.Join(mediaDir, id)
}

// mediaInboxesMarker is a folder in the media folder that says every folder
// there is an inbox, or the bridge's own with a dot name. Older versions
// kept other folders there: plaud/ with every brain's Plaud transcripts,
// and folders scripts made for /ingest, such as upload/. An inbox is the
// folder named after its brain, so a brain called plaud would have read
// every brain's transcripts. See retireLegacyMedia. It is a folder, not a
// file: /ingest moves regular files from the top of the media folder into
// a brain's inbox, and a brain that took the marker so would have the next
// inbox move every brain's inbox aside.
const mediaInboxesMarker = ".inboxes"

// legacyMediaDir is where retireLegacyMedia moves the folders and files
// older versions left in the media folder. A dot name is never a brain id, so it never
// becomes an inbox: no brain can read it, and /ingest takes nothing from it.
const legacyMediaDir = ".old"

// legacyMediaMu keeps two first inboxes from moving the same folders.
var legacyMediaMu sync.Mutex

// processStarted is when this process started: a file at the top of the
// media folder older than that was left by an older version (see
// retireLegacyMedia).
var processStarted = time.Now()

// retireLegacyMedia readies the media folder for inboxes, once: unless it
// has the mediaInboxesMarker, every folder (or symlink) in it whose name
// could be a brain id's goes into legacyMediaDir, under a free name, and
// so does every file at its top older than this process; then the folder
// gets the marker. Older versions downloaded every chat's attachments to
// the top, where /ingest would hand any of them to a brain that names it,
// and a document keeps its sender's file name (scan.pdf), which is easy
// to guess. A file a script put there for /ingest once the bridge was
// running stays. It runs before any inbox is opened (see openInbox) and
// when the bridge starts (see NewBridge), so it moves only what older
// versions left, never an inbox: once the marker is there, every folder
// that appears is an inbox. Nothing else runs it: check, say, must leave
// the media folder of a bridge that is still running alone.
func retireLegacyMedia(mediaDir string) error {
	legacyMediaMu.Lock()
	defer legacyMediaMu.Unlock()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return err
	}
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return err
	}
	defer media.Close()
	if fi, err := media.Lstat(mediaInboxesMarker); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s in the media folder is not a folder", mediaInboxesMarker)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, err := fs.ReadDir(media.FS(), ".")
	if err != nil {
		return err
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.Type().IsRegular() {
			fi, err := e.Info()
			if err != nil {
				return err
			}
			if !fi.ModTime().Before(processStarted) {
				continue
			}
		}
		if err := media.Mkdir(legacyMediaDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		for i := 1; ; i++ {
			dst := filepath.Join(legacyMediaDir, mediaNameCandidate(name, i))
			if _, err := media.Lstat(dst); err == nil && i < lastMediaCandidate {
				continue
			}
			if err := media.Rename(name, dst); err != nil {
				return fmt.Errorf("moving %s, which an older version left in the media folder, aside: %w", name, err)
			}
			if e.Type().IsRegular() {
				files++
			} else {
				log.Printf("media: moved %s, which an older version left in the media folder, to %s, where no brain can read it", name, filepath.Join(mediaDir, dst))
			}
			break
		}
	}
	if files > 0 {
		log.Printf("media: moved %d files an older version left at the top of the media folder to %s, where no brain can read them", files, filepath.Join(mediaDir, legacyMediaDir))
	}
	if err := media.Mkdir(mediaInboxesMarker, 0o700); err != nil {
		return err
	}
	return media.WriteFile(filepath.Join(mediaInboxesMarker, "README.txt"),
		[]byte("Every folder next to this one is the media inbox of the brain it is named after.\n"+
			"The folders and files an older version left there were moved into "+legacyMediaDir+"/.\n"), 0o600)
}

// openInbox opens brain id's inbox in the media folder as an os.Root,
// creating it first if needed. The inbox is the brain's to write, but not
// the folder itself: the sandbox keeps a brain from renaming or replacing
// it (with a symlink to another brain's folder, say), and the media folder
// above it is the bridge's alone. The bridge still checks that it is a real
// folder before it puts anything there, and works inside it only through
// the root, which refuses a path that a symlink the brain planted would
// lead out of the inbox.
func openInbox(mediaDir, id string) (*os.Root, error) {
	if !brainIDRe.MatchString(id) {
		// The id names a folder; anything else could name another one.
		return nil, fmt.Errorf("brain id %q cannot name an inbox", id)
	}
	// A folder an older version left under this name is not the brain's.
	if err := retireLegacyMedia(mediaDir); err != nil {
		return nil, fmt.Errorf("readying the media folder for inboxes: %w", err)
	}
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return nil, err
	}
	defer media.Close()
	if err := media.Mkdir(id, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("creating the inbox of brain %s: %w", id, err)
	}
	if fi, err := media.Lstat(id); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("the inbox of brain %s is not a folder (%v)", id, fi.Mode().Type())
	}
	return media.OpenRoot(id)
}

// mediaRel returns p relative to the media folder, by the folder as
// configured or by its real path, and whether p lies inside it. The folder
// itself does not count.
func mediaRel(mediaDir, p string) (string, bool) {
	p = filepath.Clean(absPath(p))
	for _, base := range []string{filepath.Clean(absPath(mediaDir)), canonicalDir(mediaDir)} {
		if base == "" || !isWithin(p, base) {
			continue
		}
		if rel, err := filepath.Rel(base, p); err == nil && rel != "." {
			return rel, true
		}
	}
	return "", false
}

// mediaOwner is the first folder of rel (a path relative to the media
// folder), the inbox or bridge folder the file is in, or "" for a file at
// the top of the media folder.
func mediaOwner(rel string) string {
	first, rest, found := strings.Cut(rel, string(filepath.Separator))
	if !found || rest == "" {
		return ""
	}
	return first
}

// claimMedia moves each file in paths that lies in the media folder, but
// not in brain id's inbox, into that inbox, and returns where each file it
// moved went, by the path it had (see Inbound.withMediaMoved). It runs
// once the brain is known (see Brain.Submit), so a message that lands on
// another brain than its chat's (after a config change, say) takes its
// files with it. A file it cannot move keeps its path, and the
// log says why; the brain cannot read it there under the sandbox, but
// nothing is lost. A name already taken in the inbox gets "-2", "-3" and
// so on, as in placeMedia, so nothing there is replaced.
func claimMedia(mediaDir, id string, paths []string) map[string]string {
	if mediaDir == "" || len(paths) == 0 {
		return nil
	}
	moved := map[string]string{}
	var media, inbox *os.Root
	defer func() {
		if media != nil {
			media.Close()
		}
		if inbox != nil {
			inbox.Close()
		}
	}()
	for _, p := range paths {
		rel, ok := mediaRel(mediaDir, p)
		if !ok || mediaOwner(rel) == id {
			continue
		}
		if inbox == nil {
			var err error
			if inbox, err = openInbox(mediaDir, id); err != nil {
				log.Printf("brain %s: cannot move its files into its inbox: %v", id, err)
				return moved
			}
			if media, err = os.OpenRoot(mediaDir); err != nil {
				log.Printf("brain %s: cannot move its files into its inbox: %v", id, err)
				return moved
			}
		}
		dst, err := moveIntoInbox(media, inbox, id, rel)
		if err != nil {
			log.Printf("brain %s: cannot move %s into its inbox: %v", id, filepath.Base(rel), err)
			continue
		}
		moved[p] = filepath.Join(mediaInbox(mediaDir, id), dst)
	}
	return moved
}

// moveIntoInbox moves the regular file rel (relative to the media folder)
// into brain id's inbox under a free name, and returns that name. Both
// steps go through the media folder's root: the file is claimed under its
// new name with O_EXCL, which fails on anything already there, a symlink
// included, and the rename then replaces only that entry, never what a
// link put in its place points to.
func moveIntoInbox(media, inbox *os.Root, id, rel string) (string, error) {
	fi, err := media.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	name := sanitizeMediaName(filepath.Base(rel))
	for i := 1; ; i++ {
		cand := mediaNameCandidate(name, i)
		f, err := inbox.OpenFile(cand, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) && i < lastMediaCandidate {
			continue
		}
		if err != nil {
			return "", err
		}
		f.Close()
		if err := media.Rename(rel, filepath.Join(id, cand)); err != nil {
			_ = inbox.Remove(cand)
			return "", err
		}
		return cand, nil
	}
}

// writeInboxFile writes data to rel inside brain id's inbox, replacing what
// is there, and returns the file's path. Like writeFileAtomic it writes a
// temp file first and renames it into place, so the brain never reads half
// a file; unlike it, every step goes through the inbox's root, because the
// brain can replace any folder inside its inbox with a symlink.
func writeInboxFile(mediaDir, id, rel string, data []byte) (string, error) {
	inbox, err := openInbox(mediaDir, id)
	if err != nil {
		return "", err
	}
	defer inbox.Close()
	dir := filepath.Dir(rel)
	if dir != "." {
		if err := inbox.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmp := filepath.Join(dir, "."+filepath.Base(rel)+"."+hex.EncodeToString(suffix[:])+".tmp")
	f, err := inbox.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer inbox.Remove(tmp) // a failed write's; a finished one was renamed
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := inbox.Rename(tmp, rel); err != nil {
		return "", err
	}
	return filepath.Join(mediaInbox(mediaDir, id), rel), nil
}
