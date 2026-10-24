package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Changing what is in a root (docs/FILES.md, "Changing files"): editing a
// text file, uploading, renaming or moving, and deleting, which moves what
// it deletes into the root's trash. Every change is held to the root as a
// read is: through os.Root, through no symlink, never to or from a hidden
// path. Only a Scope with Write changes anything; one with Uploads may
// add files to the upload folders of those brains.

// Upload is a folder of a brain root that takes uploads (brain.json
// files.uploads), with its subfolders.
type Upload struct {
	Folder  string
	ToBrain bool
}

// The upload destinations UploadTo answers.
const (
	// UploadFolder: the file lands in the folder.
	UploadFolder = "folder"
	// UploadBrain: the file goes to the brain, as one sent on WhatsApp.
	UploadBrain = "brain"
)

// TrashDir is where Trash moves what it deletes, in the root.
const TrashDir = ".fridolin-trash"

// Errors of a change.
var (
	ErrReadOnly = errors.New("this folder is read-only here")
	ErrChanged  = errors.New("the file changed since you opened it; open it again, and make your change there")
	ErrExists   = errors.New("there is already a file or folder of that name")
	ErrTooLarge = errors.New("the file is too large")
	errProtect  = errors.New("Fridolin keeps this file itself; change it on the brain's page")
)

// noWrite are the files of a brain folder nobody changes here: its
// settings, which the Brain page edits and checks, and its history and run
// logs, which the bridge writes.
var noWrite = []string{"brain.json", "conversation-history.jsonl", "claude-stream.log", "claude-stream.1.log"}

// WithUploads returns r with its upload folders.
func (r Root) WithUploads(u []Upload) Root {
	r.uploads = slices.Clone(u)
	return r
}

// uploadRule is the deepest upload folder rel lies in.
func (r Root) uploadRule(rel string) (Upload, bool) {
	var best Upload
	found := false
	for _, u := range r.uploads {
		if (rel == u.Folder || strings.HasPrefix(rel, u.Folder+"/")) && (!found || len(u.Folder) > len(best.Folder)) {
			best, found = u, true
		}
	}
	return best, found
}

// UploadRule says how the folder rel takes uploads by the rule of the
// brain: UploadFolder, UploadBrain, or "" where it has none.
func (r Root) UploadRule(rel string) string {
	u, ok := r.uploadRule(rel)
	switch {
	case !ok:
		return ""
	case u.ToBrain:
		return UploadBrain
	}
	return UploadFolder
}

// CanWrite reports whether s may change rel (cleaned) in r.
func (r Root) CanWrite(rel string, s Scope) error {
	if !s.Write || !r.Writable || r.Kind == KindMedia {
		return ErrReadOnly
	}
	if rel == "" {
		return errors.New("name a file or folder in it")
	}
	if r.Hidden(rel, s) || hiddenAbove(r, rel, s) {
		return ErrHidden
	}
	if r.Kind == KindBrain && slices.Contains(noWrite, rel) {
		return errProtect
	}
	return nil
}

// hiddenAbove reports whether a folder above rel is hidden.
func hiddenAbove(r Root, rel string, s Scope) bool {
	for d := path.Dir(rel); d != "." && d != ""; d = path.Dir(d) {
		if r.Hidden(d, s) {
			return true
		}
	}
	return false
}

// UploadTo says where an upload into the folder rel goes for s:
// UploadFolder, UploadBrain or "" (s may not upload there). With Write, s
// uploads into any folder it may change, and to the brain where the
// folder's rule says so; with Uploads, only into the brain's upload folders.
func (r Root) UploadTo(rel string, s Scope) string {
	if r.Hidden(rel, s) || hiddenAbove(r, rel, s) {
		return ""
	}
	rule := r.UploadRule(rel)
	switch {
	case r.Kind == KindMedia || !r.Writable:
		return ""
	case r.Kind == KindNode:
		if s.Write {
			return UploadFolder
		}
		return ""
	case s.Write:
		if rule == UploadBrain {
			return UploadBrain
		}
		return UploadFolder
	case slices.Contains(s.Uploads, r.Brain):
		return rule
	}
	return ""
}

// SHA is the hex SHA-256 of data: the version of a file an edit starts
// from.
func SHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// locks keeps two changes in one folder from interleaving: a check and the
// change it checked are one step.
var locks sync.Map

func lockRoot(dir string) func() {
	v, _ := locks.LoadOrStore(dir, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// openWrite opens r for a change of rel by s.
func (r Root) openWrite(rel string, s Scope) (*os.Root, func(), error) {
	if err := r.CanWrite(rel, s); err != nil {
		return nil, nil, err
	}
	root, err := r.open()
	if err != nil {
		return nil, nil, err
	}
	unlock := lockRoot(r.Path)
	return root, func() { unlock(); root.Close() }, nil
}

// folder checks that rel (cleaned) is a folder of root reached through no
// symlink and nothing hidden.
func (r Root) folder(root *os.Root, rel string, s Scope) error {
	fi, err := r.walk(root, rel, s)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("not a folder")
	}
	return nil
}

// makeFolders creates the folders of rel that are missing, each checked as
// it goes.
func (r Root) makeFolders(root *os.Root, rel string, s Scope) error {
	if rel == "" || rel == "." {
		return nil
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		p := strings.Join(segs[:i+1], "/")
		if r.Hidden(p, s) {
			return ErrHidden
		}
		fi, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(p, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return ErrHidden
		}
	}
	return nil
}

// WriteFile replaces the file rel of r with data, or, with sha "", creates
// it and the folders above it that are missing. sha is the SHA of the
// content the change started from: a file that changed meanwhile is
// ErrChanged, one created meanwhile ErrExists. The file is replaced whole,
// through a new file beside it, so that a reader never sees half of it.
func (r Root) WriteFile(rel string, data []byte, sha string, s Scope) (fs.FileInfo, error) {
	rel, err := Clean(rel)
	if err != nil {
		return nil, err
	}
	root, done, err := r.openWrite(rel, s)
	if err != nil {
		return nil, err
	}
	defer done()
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	if sha == "" {
		if err := r.makeFolders(root, dir, s); err != nil {
			return nil, err
		}
	} else if err := r.folder(root, dir, s); err != nil {
		return nil, err
	}
	perm := fs.FileMode(0o644)
	fi, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if sha != "" {
			return nil, ErrChanged
		}
	case err != nil:
		return nil, err
	case sha == "":
		return nil, ErrExists
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular():
		return nil, ErrHidden
	default:
		perm = fi.Mode().Perm()
		f, err := root.Open(rel)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		if hex.EncodeToString(h.Sum(nil)) != sha {
			return nil, ErrChanged
		}
	}
	if err := replace(root, rel, data, perm); err != nil {
		return nil, err
	}
	return root.Lstat(rel)
}

// replace writes data to a new hidden file beside rel and renames it over
// rel.
func replace(root *os.Root, rel string, data []byte, perm fs.FileMode) error {
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+".fridolin-"+randomSuffix()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SafeName is the name an uploaded file keeps: its last path element,
// without control characters and leading dots, at most 200 bytes;
// "upload" when nothing is left.
func SafeName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f || c == utf8.RuneError {
			return -1
		}
		return c
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, "."))
	for len(name) > 200 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	if name == "" {
		return "upload"
	}
	return name
}

// numbered is name with " (n)" before its extension.
func numbered(name string, n int) string {
	if n < 2 {
		return name
	}
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return strings.TrimSuffix(name, ext) + " (" + strconv.Itoa(n) + ")" + ext
}

// Create writes body, at most max bytes, as a new file name in the folder
// dir of r: an upload that lands in the folder (UploadTo says
// UploadFolder). A name that is taken gets " (2)", " (3)" and so on, so
// that an upload never replaces a file. It returns the new file's path.
func (r Root) Create(dir, name string, body io.Reader, max int64, s Scope) (string, int64, error) {
	dir, err := Clean(dir)
	if err != nil {
		return "", 0, err
	}
	if r.UploadTo(dir, s) != UploadFolder {
		return "", 0, ErrReadOnly
	}
	if r.Kind == KindNode {
		return "", 0, errors.New("a node's folder is written through the node")
	}
	root, err := r.open()
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	defer lockRoot(r.Path)()
	if err := r.folder(root, dir, s); err != nil {
		return "", 0, err
	}
	name = SafeName(name)
	for n := 1; n <= 1000; n++ {
		rel := path.Join(dir, numbered(name, n))
		if r.Hidden(rel, s) || r.Kind == KindBrain && slices.Contains(noWrite, rel) {
			return "", 0, fmt.Errorf("%s: %w", name, errUnshown)
		}
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", 0, err
		}
		size, err := copyAtMost(f, body, max)
		if err != nil {
			_ = root.Remove(rel)
			return "", 0, err
		}
		return rel, size, nil
	}
	return "", 0, ErrExists
}

var errUnshown = errors.New("Fridolin never shows a file of this name (a secret, or what the brain's backup leaves out), so it cannot be uploaded here")

// copyAtMost copies body into f and closes it; more than max bytes is
// ErrTooLarge.
func copyAtMost(f *os.File, body io.Reader, max int64) (int64, error) {
	n, err := io.Copy(f, io.LimitReader(body, max+1))
	if err == nil && n > max {
		err = ErrTooLarge
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// CreateIn writes body, at most max bytes, as a new file name in the
// folder dir (a media inbox), numbered as Create does. It returns the new
// file's absolute path.
func CreateIn(dir, name string, body io.Reader, max int64) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	name = SafeName(name)
	for n := 1; n <= 1000; n++ {
		rel := numbered(name, n)
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", 0, err
		}
		size, err := copyAtMost(f, body, max)
		if err != nil {
			_ = root.Remove(rel)
			return "", 0, err
		}
		return path.Join(dir, rel), size, nil
	}
	return "", 0, ErrExists
}

// maxScan bounds the entries a move or delete looks through in a folder.
const maxScan = 100_000

// holdsHidden reports whether the folder rel holds anything hidden (or
// more than maxScan entries, which counts as hidden): moving it would
// show it, or delete what was never shown.
func (r Root) holdsHidden(root *os.Root, rel string, s Scope) bool {
	n := 0
	found := false
	_ = fs.WalkDir(root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			found = true
			return fs.SkipAll
		}
		n++
		if n > maxScan || r.Hidden(p, s) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// source checks the file or folder rel that a move or delete takes: there,
// reached through no symlink, and, a folder, holding nothing hidden.
func (r Root) source(root *os.Root, rel string, s Scope) (fs.FileInfo, error) {
	fi, err := r.walk(root, rel, s)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		if r.holdsHidden(root, rel, s) {
			return nil, errors.New("this folder holds files Fridolin does not show here (secrets, or what the backup leaves out); move or delete it on the computer itself")
		}
	} else if !fi.Mode().IsRegular() {
		return nil, ErrHidden
	}
	return fi, nil
}

// Move renames or moves the file or folder from to to, both in r. to must
// not exist; its folder must.
func (r Root) Move(from, to string, s Scope) error {
	from, err := Clean(from)
	if err != nil {
		return err
	}
	if to, err = Clean(to); err != nil {
		return err
	}
	if from == to {
		return errors.New("that is where it is already")
	}
	if strings.HasPrefix(to, from+"/") {
		return errors.New("a folder cannot move into itself")
	}
	if err := r.CanWrite(to, s); err != nil {
		return err
	}
	root, done, err := r.openWrite(from, s)
	if err != nil {
		return err
	}
	defer done()
	if _, err := r.source(root, from, s); err != nil {
		return err
	}
	dir := path.Dir(to)
	if dir == "." {
		dir = ""
	}
	if err := r.folder(root, dir, s); err != nil {
		return err
	}
	if _, err := root.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return ErrExists
		}
		return err
	}
	return root.Rename(from, to)
}

// Trash deletes the file or folder rel of r: it moves it into the root's
// trash, TrashDir/<time>/rel, where it stays until you empty the trash
// yourself. It returns where it went.
func (r Root) Trash(rel string, s Scope, now time.Time) (string, error) {
	rel, err := Clean(rel)
	if err != nil {
		return "", err
	}
	root, done, err := r.openWrite(rel, s)
	if err != nil {
		return "", err
	}
	defer done()
	if _, err := r.source(root, rel, s); err != nil {
		return "", err
	}
	stamp := now.UTC().Format("20060102-150405")
	for n := 1; n <= 100; n++ {
		at := stamp
		if n > 1 {
			at += "-" + strconv.Itoa(n)
		}
		dst := path.Join(TrashDir, at, rel)
		if _, err := root.Lstat(dst); err == nil {
			continue
		}
		if err := root.MkdirAll(path.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if fi, err := root.Lstat(TrashDir); err != nil || fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return "", errors.New("the folder's trash is not a folder")
		}
		return dst, root.Rename(rel, dst)
	}
	return "", ErrExists
}

// IsRepo reports whether r's folder is a git repository of its own: it
// holds .git.
func (r Root) IsRepo() bool {
	if r.Kind != KindBrain {
		return false
	}
	root, err := r.open()
	if err != nil {
		return false
	}
	defer root.Close()
	fi, err := root.Lstat(".git")
	return err == nil && fi.Mode()&fs.ModeSymlink == 0
}
