package wiki

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/files"
)

// The folders of a brain whose Markdown files are its wiki's pages.
const (
	SectionWiki     = "wiki"
	SectionMemories = "memories"
)

// Sections in the order the wiki shows them.
var Sections = []string{SectionWiki, SectionMemories}

// Bounds of an index.
const (
	MaxPages     = 3000
	maxPageBytes = 1 << 20
	// fresh is how long an index is reused before the folders are walked
	// again; files that did not change are not read again either way.
	fresh = 3 * time.Second
)

// Page is one Markdown file of a brain's wiki/ or memories/ folder.
type Page struct {
	// Path is the file's path in the brain folder, "wiki/people/alex.md".
	Path string `json:"path"`
	// Name is the path in its section without ".md": "people/alex".
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Section string    `json:"section"`
	ModTime time.Time `json:"mod_time"`
	Size    int64     `json:"size"`
}

// Index is the pages of one brain for one scope.
type Index struct {
	Root  files.Root
	Pages []Page
	// Home is the wiki's front page ("wiki/index.md" or the like), "" if
	// it has none.
	Home string
	// Truncated: the wiki has more than MaxPages pages.
	Truncated bool

	scope  files.Scope
	byPath map[string]*entry
}

type entry struct {
	page  Page
	text  string   // the Markdown, without frontmatter
	links []string // the pages it links to (resolved)
}

// cached is a file read once, kept while it has the same size and time.
type cached struct {
	mod   time.Time
	size  int64
	title string
	text  string
	raw   []string // link targets as written
}

// Manager builds and caches the indexes of the brains' wikis.
type Manager struct {
	mu      sync.Mutex
	files   map[string]*cached // absolute path → content
	indexes map[string]*Index  // root id + scope → index
	built   map[string]time.Time
	now     func() time.Time
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{files: map[string]*cached{}, indexes: map[string]*Index{}, built: map[string]time.Time{}, now: time.Now}
}

// homeNames are the files a wiki's front page may be, first found wins.
var homeNames = []string{"index.md", "_index.md", "README.md", "readme.md", "home.md", "Home.md"}

// Index returns root's index for s, walking its wiki/ and memories/
// folders again when the last walk is more than a few seconds old.
func (m *Manager) Index(root files.Root, s files.Scope) (*Index, error) {
	key := root.ID + "\x00" + root.Path + "\x00" + scopeKey(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ix, ok := m.indexes[key]; ok && m.now().Sub(m.built[key]) < fresh {
		return ix, nil
	}
	ix, err := m.build(root, s)
	if err != nil {
		return nil, err
	}
	m.indexes[key], m.built[key] = ix, m.now()
	return ix, nil
}

func scopeKey(s files.Scope) string {
	if s.Private {
		return "private"
	}
	return "shared"
}

func (m *Manager) build(root files.Root, s files.Scope) (*Index, error) {
	osRoot, err := os.OpenRoot(root.Path)
	if err != nil {
		return nil, err
	}
	defer osRoot.Close()
	ix := &Index{Root: root, scope: s, byPath: map[string]*entry{}, Pages: []Page{}}
	for _, sec := range Sections {
		if root.Hidden(sec, s) {
			continue
		}
		if fi, err := osRoot.Lstat(sec); err != nil || !fi.IsDir() {
			continue
		}
		err := fs.WalkDir(osRoot.FS(), sec, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 || root.Hidden(p, s) || !utf8.ValidString(p) {
				if d.IsDir() && p != sec {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !strings.EqualFold(path.Ext(p), ".md") {
				return nil
			}
			if len(ix.Pages) >= MaxPages {
				ix.Truncated = true
				return fs.SkipAll
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			c := m.read(root, osRoot, p, info, s)
			if c == nil {
				return nil
			}
			name := strings.TrimSuffix(strings.TrimPrefix(p, sec+"/"), path.Ext(p))
			pg := Page{Path: p, Name: name, Title: c.title, Section: sec, ModTime: info.ModTime().UTC(), Size: info.Size()}
			if pg.Title == "" {
				pg.Title = path.Base(name)
			}
			ix.Pages = append(ix.Pages, pg)
			ix.byPath[p] = &entry{page: pg, text: c.text}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(ix.Pages, func(i, j int) bool {
		a, b := ix.Pages[i], ix.Pages[j]
		if a.Section != b.Section {
			return slices.Index(Sections, a.Section) < slices.Index(Sections, b.Section)
		}
		return strings.ToLower(a.Path) < strings.ToLower(b.Path)
	})
	for _, n := range homeNames {
		if _, ok := ix.byPath[SectionWiki+"/"+n]; ok {
			ix.Home = SectionWiki + "/" + n
			break
		}
	}
	// Links, resolved now that every page is known.
	for p, e := range ix.byPath {
		c := m.files[absKey(root, p)]
		if c == nil {
			continue
		}
		dir := path.Dir(p)
		seen := map[string]bool{}
		for _, t := range c.raw {
			var target string
			if after, ok := strings.CutPrefix(t, "md:"); ok {
				target, _ = (&renderer{dir: dir}).inRoot(after)
			} else {
				target = ix.resolvePage(t, dir, true)
			}
			if target != "" && target != p && !seen[target] {
				if _, ok := ix.byPath[target]; ok {
					seen[target] = true
					e.links = append(e.links, target)
				}
			}
		}
	}
	m.forget()
	return ix, nil
}

func absKey(root files.Root, p string) string { return root.Path + "\x00" + p }

// forget drops cached files beyond a bound; they are read again when needed.
func (m *Manager) forget() {
	const maxCached = 4 * MaxPages
	if len(m.files) <= maxCached {
		return
	}
	for k := range m.files {
		delete(m.files, k)
		if len(m.files) <= maxCached/2 {
			return
		}
	}
}

var (
	wikiLinkRE = regexp.MustCompile(`!?\[\[([^\[\]\n|#]*)(?:#[^\[\]\n|]*)?(?:\|[^\[\]\n]*)?\]\]`)
	mdLinkRE   = regexp.MustCompile(`\]\(([^)\s]+?\.(?i:md|markdown))(?:#[^)\s]*)?(?:\s+"[^"]*")?\)`)
	h1RE       = regexp.MustCompile(`(?m)^#[ \t]+(.+?)[ \t#]*$`)
)

// read reads page p of root, or returns its cached copy when its size and
// time are the same.
func (m *Manager) read(root files.Root, osRoot *os.Root, p string, info fs.FileInfo, s files.Scope) *cached {
	k := absKey(root, p)
	if c, ok := m.files[k]; ok && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
		return c
	}
	f, _, err := root.Open(p, s)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPageBytes))
	if err != nil {
		return nil
	}
	mt, body := frontmatter(data)
	c := &cached{mod: info.ModTime(), size: info.Size(), title: mt.title, text: string(body)}
	if c.title == "" {
		if h := h1RE.FindSubmatch(body); h != nil {
			c.title = strings.TrimSpace(stripInline(string(h[1])))
		}
	}
	for _, l := range wikiLinkRE.FindAllStringSubmatch(c.text, 200) {
		if t := strings.TrimSpace(l[1]); t != "" {
			c.raw = append(c.raw, t)
		}
	}
	for _, l := range mdLinkRE.FindAllStringSubmatch(c.text, 200) {
		if !strings.Contains(l[1], ":") {
			c.raw = append(c.raw, "md:"+l[1])
		}
	}
	m.files[k] = c
	return c
}

// Page returns the page at p, false if p is no page of the index.
func (ix *Index) Page(p string) (Page, bool) {
	e, ok := ix.byPath[p]
	if !ok {
		return Page{}, false
	}
	return e.page, true
}

// Backlinks are the pages that link to p.
func (ix *Index) Backlinks(p string) []Page {
	out := []Page{}
	for _, e := range ix.byPath {
		if slices.Contains(e.links, p) {
			out = append(out, e.page)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// Resolve finds what [[target]] names from the folder dir, as Obsidian
// does: a page by its path from dir, from wiki/ or from the brain folder,
// else the page of that name anywhere (the shortest path if several),
// else a file by those paths, with or without its extension.
func (ix *Index) Resolve(target, dir string) (page, file string) {
	if p := ix.resolvePage(target, dir, false); p != "" {
		return p, ""
	}
	t := cleanTarget(target)
	if t == "" {
		return "", ""
	}
	r := &renderer{}
	for _, b := range []string{dir, SectionWiki, ""} {
		r.dir = b
		p, ok := r.inRoot(t)
		if !ok || p == "" {
			continue
		}
		if f := ix.findFile(p); f != "" {
			return "", f
		}
	}
	return ix.resolvePage(target, dir, true), ""
}

func cleanTarget(target string) string {
	t := strings.Trim(strings.ReplaceAll(strings.TrimSpace(target), "\\", "/"), "/")
	if strings.Contains(t, "\x00") {
		return ""
	}
	return t
}

// resolvePage is the page target names: by path, or by its name alone
// when it is a bare name or, with anyName, whatever it is.
func (ix *Index) resolvePage(target, dir string, anyName bool) string {
	t := cleanTarget(target)
	if t == "" {
		return ""
	}
	stem := strings.TrimSuffix(t, ".md")
	r := &renderer{}
	for _, b := range []string{dir, SectionWiki, ""} {
		r.dir = b
		if p, ok := r.inRoot(stem + ".md"); ok {
			if _, ok := ix.byPath[p]; ok {
				return p
			}
		}
	}
	if strings.Contains(stem, "/") && !anyName {
		return ""
	}
	base := strings.ToLower(path.Base(stem))
	best := ""
	for p := range ix.byPath {
		if strings.ToLower(strings.TrimSuffix(path.Base(p), path.Ext(p))) == base && (best == "" || len(p) < len(best) || len(p) == len(best) && p < best) {
			best = p
		}
	}
	return best
}

// findFile is p if it is a file of the root the scope may open, or a file
// in p's folder named p plus an extension.
func (ix *Index) findFile(p string) string {
	if ix.Root.Hidden(p, ix.scope) {
		return ""
	}
	if f, fi, err := ix.Root.Open(p, ix.scope); err == nil {
		f.Close()
		if fi.Mode().IsRegular() {
			return p
		}
	}
	dir, name := path.Dir(p), path.Base(p)
	if dir == "." {
		dir = ""
	}
	entries, _, err := ix.Root.List(dir, ix.scope)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.Dir && strings.TrimSuffix(e.Name, path.Ext(e.Name)) == name {
			return path.Join(dir, e.Name)
		}
	}
	return ""
}

// Render renders the page at p.
func (ix *Index) Render(p string) (Doc, error) {
	e, ok := ix.byPath[p]
	if !ok {
		return Doc{}, files.ErrHidden
	}
	doc := Render([]byte(e.text), p, ix)
	if doc.Title == "" {
		doc.Title = e.page.Title
	}
	return doc, nil
}

// Hit is a page a search found.
type Hit struct {
	Brain   string `json:"brain"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Section string `json:"section"`
	Snippet string `json:"snippet"`
	score   int
}

// MaxHits bounds a search's answer.
const MaxHits = 50

// Search finds the pages that hold every word of q, in their title or
// text, ignoring case; pages with the words in the title come first.
func (ix *Index) Search(q string) []Hit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return []Hit{}
	}
	var out []Hit
	for _, pg := range ix.Pages {
		e := ix.byPath[pg.Path]
		title, body := strings.ToLower(pg.Title+" "+pg.Name), strings.ToLower(e.text)
		score := 0
		for _, w := range words {
			inTitle, inBody := strings.Count(title, w), strings.Count(body, w)
			if inTitle+inBody == 0 {
				score = -1
				break
			}
			score += 10*inTitle + min(inBody, 20)
		}
		if score < 0 {
			continue
		}
		out = append(out, Hit{Brain: ix.Root.Brain, Path: pg.Path, Title: pg.Title, Section: pg.Section, Snippet: snippet(e.text, words[0]), score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > MaxHits {
		out = out[:MaxHits]
	}
	if out == nil {
		out = []Hit{}
	}
	return out
}

// snippet is a line of text around the first place word is, without most
// Markdown punctuation.
func snippet(text, word string) string {
	plain := stripInline(text)
	low := strings.ToLower(plain)
	i := strings.Index(low, word)
	if i < 0 {
		i = 0
	}
	start, end := max(0, i-70), min(len(plain), i+len(word)+110)
	for start > 0 && !utf8.RuneStart(plain[start]) {
		start--
	}
	for end < len(plain) && !utf8.RuneStart(plain[end]) {
		end++
	}
	s := strings.Join(strings.Fields(plain[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(plain) {
		s += "…"
	}
	return s
}

var (
	wikiLabelRE  = regexp.MustCompile(`!?\[\[(?:[^\[\]|\n]*\|)?([^\[\]\n]*)\]\]`)
	mdLinkTextRE = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^)\n]*\)`)
	inlineMarkRE = regexp.MustCompile("[*_`#>|]+")
)

// stripInline is Markdown as plain text, roughly: a link as its text.
func stripInline(s string) string {
	s = wikiLabelRE.ReplaceAllString(s, "$1")
	s = mdLinkTextRE.ReplaceAllString(s, "$1")
	return inlineMarkRE.ReplaceAllString(s, "")
}

// ErrNoWiki is returned for a brain without wiki or memories pages.
var ErrNoWiki = errors.New("this brain has no wiki/ or memories/ pages")
