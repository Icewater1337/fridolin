// Package files is what the Files page and the wiki read of the folders
// Fridolin is linked to: each brain's folder, its media inbox and the
// folders nodes lend (docs/FILES.md).
//
// Every read goes through a Root and is held to it: a path is a
// slash-separated path relative to the root, with no "..", and is opened
// through os.Root, which refuses at every step a path that leads out of
// the folder. No symlink is followed, not even one that stays inside: a
// brain can put a symlink anywhere in its folder, and one to a hidden
// place inside the root would get past the rules below as surely as one
// to ~/.ssh.
//
// What a root hides is never listed, opened or searched: every name
// starting with ".", the files a backup always leaves out as secrets, the
// brain's own backup.exclude (its work folders), the paths the brains'
// sandbox denies and Fridolin's own data folder, secrets and config where
// they lie inside a root. A Scope that is not Private (a site role other
// than admin) hides the brain's history, run logs, brain.json and the map of
// WhatsApp numbers to people too.
package files

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The kinds of root.
const (
	KindBrain = "brain"
	KindMedia = "media"
	KindNode  = "node"
)

// Root is one folder the Files page lists.
type Root struct {
	// ID names the root in requests: "brain:<id>", "media:<id>" or
	// "node:<node>:<n>".
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Brain string `json:"brain,omitempty"`
	Node  string `json:"node,omitempty"`
	Label string `json:"label"`
	// Path is the folder: absolute on this machine, or on the node.
	Path string `json:"path"`
	// Location says where the files physically are: "core" or the node's
	// name.
	Location string `json:"location"`
	// Writable: the folder may be changed (write.go): a brain's folder, or
	// a node root granted for writing. A media inbox is read-only.
	Writable bool `json:"writable"`
	// Backup is "on", "off" or "" (a node's folder, which the core does not
	// back up).
	Backup string `json:"backup,omitempty"`
	// Excluded are the brain's backup.exclude lines, which the page hides.
	Excluded []string `json:"excluded,omitempty"`
	// Online is false for a node root whose node is not connected.
	Online bool `json:"online"`

	// patterns are the hidden paths relative to the root, as
	// backup.Excluded matches them; deny are absolute paths hidden where
	// they lie inside the root.
	patterns []string
	deny     []string
	// uploads are a brain root's upload folders (write.go).
	uploads []Upload
}

// BrainRoot is a brain's folder, hiding its backup.exclude and the
// absolute paths in deny.
func BrainRoot(brainID, label, dir string, backupOn bool, exclude, deny []string) Root {
	var unique []string
	for _, x := range exclude {
		if !slices.Contains(unique, x) {
			unique = append(unique, x)
		}
	}
	r := Root{ID: KindBrain + ":" + brainID, Kind: KindBrain, Brain: brainID, Label: label, Path: dir, Location: "core", Writable: true, Online: true, Excluded: unique}
	r.Backup = "off"
	if backupOn {
		r.Backup = "on"
	}
	r.patterns = backup.ExcludePatterns(exclude, false)
	r.deny = denyWithin(dir, deny)
	return r
}

// MediaRoot is a brain's media inbox, the files that came in on WhatsApp
// and through /ingest.
func MediaRoot(brainID, label, dir string, deny []string) Root {
	return Root{
		ID: KindMedia + ":" + brainID, Kind: KindMedia, Brain: brainID, Label: label, Path: dir, Location: "core", Online: true,
		Backup: "on", patterns: backup.ExcludePatterns(nil, false), deny: denyWithin(dir, deny),
	}
}

// NodeRoot is the n-th folder node lends.
func NodeRoot(node, nodeName string, n int, dir string, writable, online bool) Root {
	return Root{
		ID: fmt.Sprintf("%s:%s:%d", KindNode, node, n), Kind: KindNode, Node: node, Label: dir, Path: dir,
		Location: nodeName, Writable: writable, Online: online, patterns: backup.ExcludePatterns(nil, false),
	}
}

// denyWithin keeps the paths of deny that lie inside dir, but not dir
// itself or a folder above it: the media inbox is in the data folder, which
// is hidden in a brain folder that holds it.
func denyWithin(dir string, deny []string) []string {
	dir = fsx.Canonical(dir)
	var out []string
	for _, d := range deny {
		if d == "" {
			continue
		}
		d = fsx.Canonical(d)
		if d != dir && fsx.Within(d, dir) {
			out = append(out, d)
		}
	}
	return out
}

// Scope is who reads: the owner on the dashboard, or a site account.
type Scope struct {
	// Private sees all of a brain but what is hidden for everyone: the
	// dashboard and a site admin.
	Private bool
	// Brains are the brains whose roots it may read; nil with All.
	Brains []string
	All    bool
	// Media and Nodes: the dashboard's only.
	Media, Nodes bool
	// Write may change what it reads where a root is writable (write.go):
	// the dashboard and a site admin.
	Write bool
	// Uploads are the brains into whose upload folders it may upload: a
	// site role given uploads.
	Uploads []string
	// By is who it is, for the audit log: "dashboard" or "site:<account>".
	By string
}

// Allows reports whether s may read r.
func (s Scope) Allows(r Root) bool {
	switch r.Kind {
	case KindBrain:
		return s.All || slices.Contains(s.Brains, r.Brain)
	case KindMedia:
		return s.Media && (s.All || slices.Contains(s.Brains, r.Brain))
	case KindNode:
		return s.Nodes
	}
	return false
}

// privateOnly are the paths of a brain folder only a Private scope sees:
// its settings (which name chats and numbers), its history and run logs,
// and the map of WhatsApp numbers to people.
var privateOnly = []string{"brain.json", "conversation-history.jsonl", "claude-stream.log", "claude-stream.1.log", "memories/people/whatsapp-identity-map.md"}

// ErrHidden is the answer for a path the root hides or that does not stay
// in it: the same as for one that does not exist, so that it tells nothing.
var ErrHidden = fmt.Errorf("no such file or folder: %w", fs.ErrNotExist)

// Clean checks rel, a slash-separated path relative to a root, and returns
// it cleaned, "" for the root itself. It refuses "..", a backslash, a NUL
// and a path that is not UTF-8.
func Clean(rel string) (string, error) {
	if !utf8.ValidString(rel) || strings.ContainsAny(rel, "\\\x00") {
		return "", ErrHidden
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == ".." {
			return "", ErrHidden
		}
	}
	return strings.Trim(path.Clean("/"+rel), "/"), nil
}

// Hidden reports whether rel (cleaned) is hidden in r for s.
func (r Root) Hidden(rel string, s Scope) bool {
	if rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, seg := range segs {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	if backup.Excluded(rel, r.patterns) {
		return true
	}
	if r.Kind == KindBrain && !s.Private {
		for _, p := range privateOnly {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
	}
	if len(r.deny) > 0 {
		abs := filepath.Join(fsx.Canonical(r.Path), filepath.FromSlash(rel))
		for _, d := range r.deny {
			if fsx.Within(abs, d) {
				return true
			}
		}
	}
	return false
}

// Entry is one file or folder.
type Entry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	// Kind is how the page opens a file (KindOf); "" for a folder.
	Kind string `json:"kind,omitempty"`
}

// MaxEntries bounds a listing.
const MaxEntries = 5000

// open opens r's folder as an os.Root. A root that is not there is an
// error worth showing, unlike a path inside it.
func (r Root) open() (*os.Root, error) {
	if r.Kind == KindNode {
		return nil, errors.New("a node's folder is read through the node")
	}
	return os.OpenRoot(r.Path)
}

// walk checks every folder from the root down to rel: none hidden, none a
// symlink. It returns rel's own Lstat.
func (r Root) walk(root *os.Root, rel string, s Scope) (fs.FileInfo, error) {
	if r.Hidden(rel, s) {
		return nil, ErrHidden
	}
	if rel == "" {
		return root.Lstat(".")
	}
	segs := strings.Split(rel, "/")
	var fi fs.FileInfo
	for i := range segs {
		p := strings.Join(segs[:i+1], "/")
		var err error
		if fi, err = root.Lstat(p); err != nil {
			return nil, ErrHidden
		}
		if fi.Mode()&fs.ModeSymlink != 0 || (i < len(segs)-1 && !fi.IsDir()) {
			return nil, ErrHidden
		}
	}
	return fi, nil
}

// List lists the folder rel of r for s, folders first, then by name.
// Hidden entries, symlinks and anything that is neither a regular file
// nor a folder are left out. truncated says there were more than
// MaxEntries.
func (r Root) List(rel string, s Scope) (entries []Entry, truncated bool, err error) {
	rel, err = Clean(rel)
	if err != nil {
		return nil, false, err
	}
	root, err := r.open()
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	fi, err := r.walk(root, rel, s)
	if err != nil {
		return nil, false, err
	}
	if !fi.IsDir() {
		return nil, false, errors.New("not a folder")
	}
	name := rel
	if name == "" {
		name = "."
	}
	dir, err := root.Open(name)
	if err != nil {
		return nil, false, ErrHidden
	}
	defer dir.Close()
	des, err := dir.ReadDir(-1)
	if err != nil {
		return nil, false, err
	}
	entries = []Entry{}
	for _, de := range des {
		child := path.Join(rel, de.Name())
		if de.Type()&fs.ModeSymlink != 0 || r.Hidden(child, s) || !utf8.ValidString(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		if len(entries) == MaxEntries {
			truncated = true
			break
		}
		e := Entry{Name: de.Name(), Dir: info.IsDir(), ModTime: info.ModTime().UTC()}
		if !e.Dir {
			e.Size, e.Kind = info.Size(), KindOf(de.Name())
		}
		entries = append(entries, e)
	}
	SortEntries(entries)
	return entries, truncated, nil
}

// SortEntries puts folders first, then sorts by name, ignoring case.
func SortEntries(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// Open opens the file rel of r for s: a regular file, through no symlink,
// and not hidden. The caller closes it.
func (r Root) Open(rel string, s Scope) (*os.File, fs.FileInfo, error) {
	rel, err := Clean(rel)
	if err != nil {
		return nil, nil, err
	}
	if rel == "" {
		return nil, nil, errors.New("not a file")
	}
	root, err := r.open()
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if _, err := r.walk(root, rel, s); err != nil {
		return nil, nil, err
	}
	f, err := fsx.OpenInFolder(root, rel, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fsx.ErrSymlink) || errors.Is(err, fs.ErrNotExist) {
			return nil, nil, ErrHidden
		}
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// Hit is a file or folder a search found.
type Hit struct {
	Path string `json:"path"`
	Entry
}

// Search bounds.
const (
	MaxHits    = 200
	maxVisited = 50_000
	searchTime = 5 * time.Second
)

// Search finds the files and folders below rel whose name contains q,
// ignoring case. It skips what Hidden hides and never enters a symlink.
// truncated says it stopped early: at MaxHits, after visiting too many
// entries, or after a few seconds.
func (r Root) Search(rel, q string, s Scope) (hits []Hit, truncated bool, err error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil, false, errors.New("q is required: part of a file name")
	}
	rel, err = Clean(rel)
	if err != nil {
		return nil, false, err
	}
	root, err := r.open()
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	fi, err := r.walk(root, rel, s)
	if err != nil {
		return nil, false, err
	}
	if !fi.IsDir() {
		return nil, false, errors.New("not a folder")
	}
	start := rel
	if start == "" {
		start = "."
	}
	deadline := time.Now().Add(searchTime)
	visited := 0
	hits = []Hit{}
	errStop := errors.New("stop")
	err = fs.WalkDir(root.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == start {
				return err
			}
			return nil
		}
		if p == start {
			return nil
		}
		visited++
		if visited > maxVisited || len(hits) >= MaxHits || (visited%256 == 0 && time.Now().After(deadline)) {
			truncated = true
			return errStop
		}
		if d.Type()&fs.ModeSymlink != 0 || r.Hidden(p, s) || !utf8.ValidString(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.Contains(strings.ToLower(d.Name()), q) {
			return nil
		}
		info, err := d.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil
		}
		h := Hit{Path: p, Entry: Entry{Name: d.Name(), Dir: info.IsDir(), ModTime: info.ModTime().UTC()}}
		if !h.Dir {
			h.Size, h.Kind = info.Size(), KindOf(d.Name())
		}
		hits = append(hits, h)
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return hits, truncated, err
}

// The kinds of file, as the page opens them.
const (
	FileMarkdown = "markdown"
	FileText     = "text"
	FileImage    = "image"
	FilePDF      = "pdf"
	FileAudio    = "audio"
	FileVideo    = "video"
	FileOther    = "other"
)

var kindByExt = map[string]string{
	".md": FileMarkdown, ".markdown": FileMarkdown,
	".png": FileImage, ".jpg": FileImage, ".jpeg": FileImage, ".gif": FileImage, ".webp": FileImage, ".avif": FileImage, ".bmp": FileImage, ".ico": FileImage, ".svg": FileImage,
	".pdf": FilePDF,
	".mp3": FileAudio, ".m4a": FileAudio, ".wav": FileAudio, ".ogg": FileAudio, ".oga": FileAudio, ".opus": FileAudio, ".flac": FileAudio, ".aac": FileAudio,
	".mp4": FileVideo, ".m4v": FileVideo, ".webm": FileVideo, ".mov": FileVideo,
}

// textExts are files shown as text.
var textExts = []string{
	".txt", ".text", ".log", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf", ".xml",
	".html", ".htm", ".css", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".go", ".py", ".rb", ".rs", ".java", ".kt", ".swift", ".c", ".h",
	".cc", ".cpp", ".hpp", ".cs", ".php", ".sh", ".bash", ".zsh", ".fish", ".ps1", ".sql", ".r", ".lua", ".pl", ".vue", ".svelte",
	".tex", ".bib", ".rst", ".org", ".adoc", ".srt", ".vtt", ".diff", ".patch", ".gitignore", ".mod", ".sum", ".lock",
}

// KindOf is how a file of this name is opened.
func KindOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if k, ok := kindByExt[ext]; ok {
		return k
	}
	if slices.Contains(textExts, ext) || strings.EqualFold(name, "Makefile") || strings.EqualFold(name, "Dockerfile") || strings.EqualFold(name, "README") || strings.EqualFold(name, "LICENSE") {
		return FileText
	}
	return FileOther
}

// mediaTypes are the types a file of a kind is served as, by extension.
var mediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".bmp": "image/bmp", ".ico": "image/x-icon", ".svg": "image/svg+xml", ".pdf": "application/pdf",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".flac": "audio/flac", ".aac": "audio/aac", ".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
}

// ContentType is the type a file is served as: what the browser shows
// inline for images, PDF, audio and video, text/plain for text (HTML
// included, which is never run), and application/octet-stream for the
// rest, which only downloads.
func ContentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := mediaTypes[ext]; ok {
		return t
	}
	switch KindOf(name) {
	case FileText, FileMarkdown:
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// Disposition is the Content-Disposition for name: inline unless download.
func Disposition(name string, download bool) string {
	kind := "inline"
	if download || ContentType(name) == "application/octet-stream" {
		kind = "attachment"
	}
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

// LooksLikeText reports whether data, the start of a file, is text: UTF-8
// with no NUL byte (a character cut at the end is fine).
func LooksLikeText(data []byte) bool {
	if slices.Contains(data, 0) {
		return false
	}
	for i := 0; i < 4 && len(data) > 0 && !utf8.Valid(data); i++ {
		data = data[:len(data)-1]
	}
	return utf8.Valid(data)
}
