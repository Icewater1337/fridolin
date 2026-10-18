package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The media inbox. Attachments and transcripts arrive in the media folder
// (media_dir, by default <data_dir>/media), which no brain can read. Each
// brain reads and writes only its own inbox in it, <media_dir>/<brain id>/,
// so a group brain cannot open the files sent to the DM brain, nor replace
// them. A file moves into an inbox once the bridge knows which brain takes
// it (see claimMedia): WhatsApp downloads a file before the message is
// routed, and scripts drop theirs at the top of the media folder before
// they hand it to a brain with /ingest.

// IncomingDir is the folder in the media folder where WhatsApp
// attachments are downloaded before their message is routed. Brain ids
// cannot start with a dot, so it is never an inbox, and /ingest refuses
// files in it: those are on their way to the brain of their chat.
const IncomingDir = ".incoming"

// Path is the inbox of brain id in the media folder.
func (in Inbox) Path(id string) string {
	mediaDir := in.Dir
	return filepath.Join(mediaDir, id)
}

// InboxesMarker is a folder in the media folder that says every folder
// there is an inbox, or the bridge's own with a dot name. Older versions
// kept other folders there: plaud/ with every brain's Plaud transcripts,
// and folders scripts made for /ingest, such as upload/. An inbox is the
// folder named after its brain, so a brain called plaud would have read
// every brain's transcripts. See retireLegacyMedia. It is a folder, not a
// file: /ingest moves regular files from the top of the media folder into
// a brain's inbox, and a brain that took the marker so would have the next
// inbox move every brain's inbox aside.
const InboxesMarker = ".inboxes"

// LegacyDir is where retireLegacyMedia moves the folders and files
// older versions left in the media folder. A dot name is never a brain id, so it never
// becomes an inbox: no brain can read it, and /ingest takes nothing from it.
const LegacyDir = ".old"

// Migration shares the startup cutoff and lock across inboxes. Its
// owner constructs it once when the process starts, before accepting files.
type Migration struct {
	Started time.Time
	mu      sync.Mutex
}

// Inbox confines attachments to Dir. WorkingDir resolves relative inputs
// without reading the process's working directory inside the media layer.
// Migration must be shared by every inbox of this process.
type Inbox struct {
	Dir        string
	WorkingDir string
	Migration  *Migration
}

// absolute preserves the old path resolution, with the working directory
// supplied by the caller rather than read from process state.
func (in Inbox) absolute(p string) string {
	if p == "" || filepath.IsAbs(p) || in.WorkingDir == "" {
		return p
	}
	return filepath.Join(in.WorkingDir, p)
}

// validInboxID repeats the folder-name constraint at the storage boundary:
// media cannot trust a caller to have validated an id as a manifest first.
var validInboxID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// StuckFile, in the InboxesMarker folder, names what
// retireLegacyMedia could not move aside, one name a line: none of them
// becomes an inbox while it is there (see openInbox).
const StuckFile = "stuck"

// ErrNotTheInbox is why a brain gets no inbox: what is at its inbox's path
// is something else, which the brain's profile, naming the inbox by its
// path, would open to it.
var ErrNotTheInbox = errors.New("not the brain's inbox")

// RetireLegacy readies the media folder for inboxes, once: unless it
// has the InboxesMarker, every folder (or symlink) in it whose name
// could be a brain id's goes into LegacyDir, under a free name, and
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
//
// What it cannot move (a folder without write permission, an immutable
// file) stays where it is, logged and listed in the marker's
// StuckFile, and it goes on with the rest. One entry like that used
// to fail every call, and with it every brain's every run.
func (in Inbox) RetireLegacy() error {
	mediaDir := in.Dir
	in.Migration.mu.Lock()
	defer in.Migration.mu.Unlock()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return err
	}
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return err
	}
	defer media.Close()
	if fi, err := media.Lstat(InboxesMarker); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s in the media folder is not a folder", InboxesMarker)
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
	var stuck []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dst, err := in.moveLegacyMedia(media, e)
		switch {
		case err != nil:
			log.Printf("media: cannot move %s, which an older version left in the media folder, aside (%v); it stays in %s, and no brain named %s gets an inbox while it is there: move it out by hand", name, err, mediaDir, name)
			stuck = append(stuck, name)
		case dst == "":
		case e.Type().IsRegular():
			files++
		default:
			log.Printf("media: moved %s, which an older version left in the media folder, to %s, where no brain can read it", name, filepath.Join(mediaDir, dst))
		}
	}
	if files > 0 {
		log.Printf("media: moved %d files an older version left at the top of the media folder to %s, where no brain can read them", files, filepath.Join(mediaDir, LegacyDir))
	}
	// The marker appears whole, with the list of what stayed in it, so
	// that a bridge stopped in between neither redoes the move nor makes
	// an inbox of what stayed.
	tmp := InboxesMarker + ".new"
	if err := media.RemoveAll(tmp); err != nil {
		return err
	}
	if err := media.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	if err := media.WriteFile(filepath.Join(tmp, "README.txt"),
		[]byte("Every folder next to this one is the media inbox of the brain it is named after.\n"+
			"The folders and files an older version left there were moved into "+LegacyDir+"/,\n"+
			"except those named in "+StuckFile+", which could not be moved.\n"), 0o600); err != nil {
		return err
	}
	if len(stuck) > 0 {
		if err := media.WriteFile(filepath.Join(tmp, StuckFile), []byte(strings.Join(stuck, "\n")+"\n"), 0o600); err != nil {
			return err
		}
	}
	return media.Rename(tmp, InboxesMarker)
}

// moveLegacyMedia moves the entry e at the top of the media folder into
// LegacyDir under a free name and returns that name, or "" for a file
// it leaves where it is, one newer than this process.
func (in Inbox) moveLegacyMedia(media *os.Root, e fs.DirEntry) (string, error) {
	name := e.Name()
	if e.Type().IsRegular() {
		fi, err := e.Info()
		if err != nil {
			return "", err
		}
		if !fi.ModTime().Before(in.Migration.Started) {
			return "", nil
		}
	}
	if err := media.Mkdir(LegacyDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	for i := 1; ; i++ {
		dst := filepath.Join(LegacyDir, mediaNameCandidate(name, i))
		if _, err := media.Lstat(dst); err == nil && i < lastMediaCandidate {
			continue
		}
		if err := media.Rename(name, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
}

// stuckMedia reports whether retireLegacyMedia left name in the media
// folder because it could not move it, and that is still there. A name
// whose entry is gone is taken off the list.
func stuckMedia(media *os.Root, name string) (bool, error) {
	list := filepath.Join(InboxesMarker, StuckFile)
	data, err := media.ReadFile(list)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	names := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !slices.Contains(names, name) {
		return false, nil
	}
	if _, err := media.Lstat(name); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	names = slices.DeleteFunc(names, func(n string) bool { return n == name || n == "" })
	if len(names) == 0 {
		return false, media.Remove(list)
	}
	return false, media.WriteFile(list, []byte(strings.Join(names, "\n")+"\n"), 0o600)
}

// Open opens brain id's inbox in the media folder as an os.Root,
// creating it first if needed. The inbox is the brain's to write, but not
// the folder itself: the sandbox keeps a brain from renaming or replacing
// it (with a symlink to another brain's folder, say), and the media folder
// above it is the bridge's alone. The bridge still checks that it is a real
// folder before it puts anything there, and works inside it only through
// the root, which refuses a path that a symlink the brain planted would
// lead out of the inbox. What is at the inbox's path and is no inbox, a
// file or what an older version left there, fails with ErrNotTheInbox.
func (in Inbox) Open(id string) (*os.Root, error) {
	mediaDir := in.Dir
	if !validInboxID.MatchString(id) {
		// The id names a folder; anything else could name another one.
		return nil, fmt.Errorf("brain id %q cannot name an inbox", id)
	}
	// A folder an older version left under this name is not the brain's.
	if err := in.RetireLegacy(); err != nil {
		return nil, fmt.Errorf("readying the media folder for inboxes: %w", err)
	}
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return nil, err
	}
	defer media.Close()
	in.Migration.mu.Lock()
	stuck, err := stuckMedia(media, id)
	in.Migration.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if stuck {
		return nil, fmt.Errorf("%w: %s in the media folder %s was left there by an older version and could not be moved aside; move it out of the folder by hand", ErrNotTheInbox, id, mediaDir)
	}
	if err := media.Mkdir(id, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("creating the inbox of brain %s: %w", id, err)
	}
	if fi, err := media.Lstat(id); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%w: the inbox of brain %s is not a folder (%v)", ErrNotTheInbox, id, fi.Mode().Type())
	}
	return media.OpenRoot(id)
}

// mediaRel returns p relative to the media folder, by the folder as
// configured or by its real path, and whether p lies inside it. The folder
// itself does not count.
func (in Inbox) relative(p string) (string, bool) {
	mediaDir := in.Dir
	p = filepath.Clean(in.absolute(p))
	for _, base := range []string{filepath.Clean(in.absolute(mediaDir)), fsx.Canonical(mediaDir)} {
		if base == "" || !fsx.Within(p, base) {
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

// errNotInOwnInbox is what ownInboxFile answers for any p that is not a
// regular file in the brain's inbox, whatever the reason, so that the
// answer says nothing of what is outside the inbox.
var errNotInOwnInbox = errors.New("not a file in the brain's own inbox")

// OwnFile returns p, a regular file in brain id's inbox, by the media
// folder's real path, for /ingest called with that brain's own token. The
// brain may put symlinks in its inbox, and one that leads out of it, to a
// file left at the top of the media folder for another brain, say, must
// not hand that file in: /ingest would move it into the inbox for the
// brain to read. So p is looked up through the inbox's root, which refuses
// a symlink that leads out of the inbox without looking at what it leads
// to, and p's own ".." are taken away first, by its clean form: whether
// what they lead to is there or not, the answer is errNotInOwnInbox. The
// path returned is not resolved further; a link in it leads, as checked,
// to a file in the inbox, and the sandbox has the last word on it anyway.
func (in Inbox) OwnFile(id, p string) (string, error) {
	mediaDir := in.Dir
	rel, ok := in.relative(p)
	if !filepath.IsAbs(p) || !ok || mediaOwner(rel) != id {
		return "", errNotInOwnInbox
	}
	inner := rel[len(id)+1:]
	media, err := os.OpenRoot(mediaDir)
	if err != nil {
		return "", errNotInOwnInbox
	}
	defer media.Close()
	if fi, err := media.Lstat(id); err != nil || !fi.IsDir() {
		return "", errNotInOwnInbox
	}
	inbox, err := media.OpenRoot(id)
	if err != nil {
		return "", errNotInOwnInbox
	}
	defer inbox.Close()
	if fi, err := inbox.Stat(inner); err != nil || !fi.Mode().IsRegular() {
		return "", errNotInOwnInbox
	}
	return filepath.Join(fsx.Canonical(mediaDir), id, inner), nil
}

// Claim moves each file in paths that lies in the media folder, but
// not in brain id's inbox, into that inbox, and returns where each file it
// moved went, by the path it had (see Inbound.withMediaMoved). It runs
// once the brain is known (see Brain.Submit), so a message that lands on
// another brain than its chat's (after a config change, say) takes its
// files with it. A file it cannot move keeps its path, and the
// log says why; the brain cannot read it there under the sandbox, but
// nothing is lost. A name already taken in the inbox gets "-2", "-3" and
// so on, as in placeMedia, so nothing there is replaced.
func (in Inbox) Claim(id string, paths []string) map[string]string {
	mediaDir := in.Dir
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
		rel, ok := in.relative(p)
		if !ok || mediaOwner(rel) == id {
			continue
		}
		if inbox == nil {
			var err error
			if inbox, err = in.Open(id); err != nil {
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
		moved[p] = filepath.Join(in.Path(id), dst)
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

// Write writes data to rel inside brain id's inbox, replacing what
// is there, and returns the file's path. Like fsx.WriteFileAtomic it writes a
// temp file first and renames it into place, so the brain never reads half
// a file; unlike it, every step goes through the inbox's root, because the
// brain can replace any folder inside its inbox with a symlink.
func (in Inbox) Write(id, rel string, data []byte) (string, error) {
	inbox, err := in.Open(id)
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
	return filepath.Join(in.Path(id), rel), nil
}

// Resolve checks the paths handed to /ingest. A script may hand in files at
// the top or in this brain's inbox; a brain token can name only its own files.
// Errors preserve the HTTP contract without coupling storage to HTTP.
func (in Inbox) Resolve(id string, requested []string, ownOnly bool) ([]string, error) {
	media := fsx.Canonical(in.Dir)
	var paths []string
	for _, p := range requested {
		// A brain writes no part of the media folder but its own inbox, so
		// a file anywhere else, at its top too, was left there by someone
		// else for some brain: by a script or the site, between its write
		// and its /ingest, or for good after an /ingest that failed. A
		// brain's own token takes only what is in its inbox already, and
		// is told so in one answer for every file it may not hand in, so
		// that the answer says nothing of whether a name it guessed is
		// there. That holds for a symlink it put in its inbox too: one
		// that leads out of the inbox is refused unfollowed (see
		// ownInboxFile).
		if ownOnly {
			f, err := in.OwnFile(id, p)
			if err != nil {
				return nil, fmt.Errorf("brain %s hands in only files in its own inbox, %s", id, in.Path(id))
			}
			paths = append(paths, f)
			continue
		}
		resolved, err := filepath.EvalSymlinks(p)
		if !filepath.IsAbs(p) || err != nil || !fsx.Within(resolved, media) {
			return nil, errors.New("every path must be an existing file inside the media directory")
		}
		if st, err := os.Stat(resolved); err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("not a regular file: " + filepath.Base(p))
		}
		// The brain takes the file into its inbox (see claimMedia), and a
		// brain can ask /ingest for that itself, so only a file handed in
		// for it may move: one at the top of the media folder, where
		// scripts drop their files, or one already in its own inbox. Every
		// folder below the media folder holds files for a brain, which
		// would then read them: another brain's inbox, the inbox left by
		// a brain that was removed or is left out (whose id may come
		// back), .old/ with the folders older versions left there, every
		// brain's Plaud transcripts among them (see retireLegacyMedia), and
		// .incoming/ with attachments on their way to the brain of their
		// chat.
		rel, _ := in.relative(resolved)
		if owner := mediaOwner(rel); owner != "" && owner != id {
			return nil, fmt.Errorf("not a file for brain %s: %s is in %s/ in the media directory; /ingest takes files at its top or in the brain's own inbox", id, filepath.Base(p), owner)
		}
		paths = append(paths, resolved)
	}

	return paths, nil
}
