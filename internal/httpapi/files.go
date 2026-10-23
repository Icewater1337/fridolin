package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/wiki"
)

// FilesAPI is the Files page and the wiki (docs/FILES.md): the same
// handlers serve the dashboard, which reads every root, and the site's
// /files and /wiki through the site bridge (site_bridge.go), which reads
// the brains a site account's role may see.
type FilesAPI struct {
	// Roots lists every root, node roots with whether their node is online.
	Roots func() []files.Root
	Wiki  *wiki.Manager
	// Nodes reads node roots; nil where there are no nodes.
	Nodes NodeFiles
	// Audit records a view.
	Audit func(FileAudit)
	// Tickets open the dashboard's /files/raw (files_ticket.go).
	Tickets *Tickets
}

// NodeFiles reads the folders nodes lend.
type NodeFiles interface {
	List(ctx context.Context, node, dir string) ([]files.Entry, error)
	// Read reads at most max bytes of file; size is the whole file's.
	Read(ctx context.Context, node, file string, max int64) (data []byte, size int64, truncated bool, err error)
}

// FileAudit is one view for the audit log.
type FileAudit struct {
	By, Brain, Root, Action, Path, Outcome string
}

// The most of a file a view returns, as text and as Markdown to render,
// and of a node's file at all.
const (
	maxViewText     = 512 << 10
	maxNodeFileOpen = 16 << 20
)

// FilesRootsResponse is GET /api/files/roots.
type FilesRootsResponse struct {
	Roots []files.Root `json:"roots"`
}

// FilesListResponse is a folder.
type FilesListResponse struct {
	Root      string        `json:"root"`
	Path      string        `json:"path"`
	Entries   []files.Entry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

// FilesSearchResponse is what a search by name found.
type FilesSearchResponse struct {
	Hits      []files.Hit `json:"hits"`
	Truncated bool        `json:"truncated"`
}

// FileViewResponse is a file as the page shows it: its text, its
// Markdown rendered, or only what it is (an image, a PDF, audio, video
// or another file, which the page loads from /raw).
type FileViewResponse struct {
	Root    string    `json:"root"`
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Kind    string    `json:"kind"`
	Text    *string   `json:"text,omitempty"`
	Doc     *wiki.Doc `json:"doc,omitempty"`
	// Truncated: Text or Doc has only the start of the file.
	Truncated bool `json:"truncated,omitempty"`
}

// WikiBrain is one brain on the wiki's front page.
type WikiBrain struct {
	Brain string `json:"brain"`
	Label string `json:"label"`
	Pages int    `json:"pages"`
	Home  string `json:"home"`
}

// WikiBrainsResponse is GET /api/wiki.
type WikiBrainsResponse struct {
	Brains []WikiBrain `json:"brains"`
}

// WikiPagesResponse is a brain's pages.
type WikiPagesResponse struct {
	Brain     string      `json:"brain"`
	Label     string      `json:"label"`
	Home      string      `json:"home"`
	Pages     []wiki.Page `json:"pages"`
	Truncated bool        `json:"truncated"`
}

// WikiPageResponse is a page, rendered, with the pages that link to it.
type WikiPageResponse struct {
	Brain     string      `json:"brain"`
	Page      wiki.Page   `json:"page"`
	Doc       wiki.Doc    `json:"doc"`
	Backlinks []wiki.Page `json:"backlinks"`
}

// WikiSearchResponse is what a search of the wikis found.
type WikiSearchResponse struct {
	Hits []wiki.Hit `json:"hits"`
}

// Scope is who reads.
type Scope = files.Scope

// scoped is a handler for one reader.
type scoped func(w http.ResponseWriter, r *http.Request, s files.Scope)

// Owner is the dashboard's scope: everything.
var Owner = files.Scope{Private: true, All: true, Media: true, Nodes: true, By: "dashboard"}

// Register adds the dashboard's routes.
func (a *FilesAPI) Register(mux *http.ServeMux) {
	own := func(h scoped) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, Owner) }
	}
	mux.HandleFunc("GET /api/files/roots", own(a.roots))
	mux.HandleFunc("GET /api/files/list", own(a.list))
	mux.HandleFunc("GET /api/files/search", own(a.search))
	mux.HandleFunc("GET /api/files/view", own(a.view))
	mux.HandleFunc("GET /api/files/raw", own(a.raw))
	mux.HandleFunc("GET /api/files/ticket", own(a.ticket))
	mux.HandleFunc("GET /api/wiki", own(a.wikiBrains))
	mux.HandleFunc("GET /api/wiki/search", own(a.wikiSearch))
	mux.HandleFunc("GET /api/wiki/{brain}/pages", own(a.wikiPages))
	mux.HandleFunc("GET /api/wiki/{brain}/page", own(a.wikiPage))
}

// SiteHandler serves the same reads to the site process, for the account
// scope finds in the request (site_bridge.go); a request it refuses gets
// its error.
func (a *FilesAPI) SiteHandler(scope func(*http.Request) (files.Scope, error)) http.Handler {
	mux := http.NewServeMux()
	as := func(h scoped) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			s, err := scope(r)
			if err != nil {
				WriteErr(w, http.StatusUnauthorized, err.Error())
				return
			}
			h(w, r, s)
		}
	}
	mux.HandleFunc("GET /site-api/files/roots", as(a.roots))
	mux.HandleFunc("GET /site-api/files/list", as(a.list))
	mux.HandleFunc("GET /site-api/files/search", as(a.search))
	mux.HandleFunc("GET /site-api/files/view", as(a.view))
	mux.HandleFunc("GET /site-api/files/raw", as(a.raw))
	mux.HandleFunc("GET /site-api/wiki", as(a.wikiBrains))
	mux.HandleFunc("GET /site-api/wiki/search", as(a.wikiSearch))
	mux.HandleFunc("GET /site-api/wiki/{brain}/pages", as(a.wikiPages))
	mux.HandleFunc("GET /site-api/wiki/{brain}/page", as(a.wikiPage))
	mux.HandleFunc("/site-api/", func(w http.ResponseWriter, r *http.Request) {
		WriteErr(w, http.StatusNotFound, "no such call: "+r.Method+" "+r.URL.Path)
	})
	return mux
}

func (a *FilesAPI) allowed(s files.Scope) []files.Root {
	out := []files.Root{}
	for _, r := range a.Roots() {
		if s.Allows(r) {
			out = append(out, r)
		}
	}
	return out
}

// root finds the root a request names. One the scope may not read is
// not found, as one that does not exist.
func (a *FilesAPI) root(id string, s files.Scope) (files.Root, bool) {
	for _, r := range a.allowed(s) {
		if r.ID == id {
			return r, true
		}
	}
	return files.Root{}, false
}

// siteReader reports whether s is a site account, whose every read is audited;
// the dashboard's folder listings and searches are not.
func siteReader(s files.Scope) bool { return strings.HasPrefix(s.By, "site:") }

func (a *FilesAPI) audit(s files.Scope, root files.Root, action, p string, err error) {
	if a.Audit == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "refused: " + err.Error()
	}
	a.Audit(FileAudit{By: s.By, Brain: root.Brain, Root: root.ID, Action: action, Path: p, Outcome: outcome})
}

// writeFilesErr answers err: a path that is hidden, leaves the root or is
// not there is 404 alike.
func writeFilesErr(w http.ResponseWriter, err error) {
	var status int
	switch {
	case errors.Is(err, fs.ErrNotExist):
		WriteErr(w, http.StatusNotFound, "no such file or folder")
		return
	case errors.Is(err, errNodeOffline):
		status = http.StatusServiceUnavailable
	case errors.Is(err, errTooLarge):
		status = http.StatusRequestEntityTooLarge
	default:
		status = http.StatusBadRequest
	}
	WriteErr(w, status, err.Error())
}

var (
	errNodeOffline = errors.New("the node that lends this folder is offline")
	errTooLarge    = errors.New("this file is larger than 16 MiB, the most a node hands over at once; download it on the node")
	errNoRoot      = errors.New("no such folder")
)

func (a *FilesAPI) roots(w http.ResponseWriter, r *http.Request, s files.Scope) {
	roots := a.allowed(s)
	if siteReader(s) {
		// Where a folder is on this machine is the owner's business.
		for i := range roots {
			roots[i].Path, roots[i].Excluded = "", nil
		}
	}
	WriteJSON(w, http.StatusOK, FilesRootsResponse{Roots: roots})
}

// nodePath is rel inside a node's root, in the node's own spelling.
func nodePath(root files.Root, rel string) string {
	if rel == "" {
		return root.Path
	}
	sep := "/"
	if strings.Contains(root.Path, `\`) && !strings.HasPrefix(root.Path, "/") {
		sep = `\`
		rel = strings.ReplaceAll(rel, "/", `\`)
	}
	return strings.TrimSuffix(root.Path, sep) + sep + rel
}

func (a *FilesAPI) nodeList(ctx context.Context, root files.Root, rel string, s files.Scope) ([]files.Entry, error) {
	if !root.Online {
		return nil, errNodeOffline
	}
	if a.Nodes == nil {
		return nil, errNoRoot
	}
	if root.Hidden(rel, s) {
		return nil, files.ErrHidden
	}
	got, err := a.Nodes.List(ctx, root.Node, nodePath(root, rel))
	if err != nil {
		return nil, err
	}
	out := []files.Entry{}
	for _, e := range got {
		if !root.Hidden(path.Join(rel, e.Name), s) {
			out = append(out, e)
		}
	}
	files.SortEntries(out)
	return out, nil
}

func (a *FilesAPI) list(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	rel, err := files.Clean(q.Get("path"))
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	var entries []files.Entry
	var truncated bool
	if root.Kind == files.KindNode {
		entries, err = a.nodeList(r.Context(), root, rel, s)
	} else {
		entries, truncated, err = root.List(rel, s)
	}
	if siteReader(s) {
		a.audit(s, root, "list", rel, err)
	}
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, FilesListResponse{Root: root.ID, Path: rel, Entries: entries, Truncated: truncated})
}

func (a *FilesAPI) search(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	if root.Kind == files.KindNode {
		WriteErr(w, http.StatusBadRequest, "a node's folder cannot be searched yet; open its folders instead")
		return
	}
	hits, truncated, err := root.Search(q.Get("path"), q.Get("q"), s)
	if siteReader(s) {
		a.audit(s, root, "search", q.Get("path")+" ?"+q.Get("q"), err)
	}
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, FilesSearchResponse{Hits: hits, Truncated: truncated})
}

// opened is a file, from this machine or a node.
type opened struct {
	name    string
	size    int64
	mod     time.Time
	content io.ReadSeeker
	close   func()
}

// open opens the file a request names, or answers why it cannot.
func (a *FilesAPI) open(ctx context.Context, root files.Root, rel string, s files.Scope, max int64) (*opened, error) {
	rel, err := files.Clean(rel)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, errors.New("name a file, not the folder")
	}
	if root.Kind != files.KindNode {
		f, fi, err := root.Open(rel, s)
		if err != nil {
			return nil, err
		}
		return &opened{name: path.Base(rel), size: fi.Size(), mod: fi.ModTime(), content: f, close: func() { f.Close() }}, nil
	}
	if !root.Online {
		return nil, errNodeOffline
	}
	if a.Nodes == nil {
		return nil, errNoRoot
	}
	if root.Hidden(rel, s) {
		return nil, files.ErrHidden
	}
	data, size, truncated, err := a.Nodes.Read(ctx, root.Node, nodePath(root, rel), max)
	if err != nil {
		return nil, err
	}
	if truncated && max >= maxNodeFileOpen {
		return nil, errTooLarge
	}
	return &opened{name: path.Base(rel), size: size, content: bytes.NewReader(data), close: func() {}}, nil
}

func (a *FilesAPI) view(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	rel, err := files.Clean(q.Get("path"))
	if err != nil {
		a.audit(s, root, "view", q.Get("path"), err)
		writeFilesErr(w, err)
		return
	}
	kind := files.KindOf(rel)
	max := int64(maxViewText)
	if kind == files.FileMarkdown {
		max = wiki.MaxSource
	}
	f, err := a.open(r.Context(), root, rel, s, max)
	a.audit(s, root, "view", rel, err)
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	defer f.close()
	out := FileViewResponse{Root: root.ID, Path: rel, Name: f.name, Size: f.size, ModTime: f.mod.UTC(), Kind: kind}
	if kind == files.FileOther || kind == files.FileText || kind == files.FileMarkdown {
		data, err := io.ReadAll(io.LimitReader(f.content, max))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		out.Truncated = int64(len(data)) < f.size
		switch {
		case kind == files.FileMarkdown:
			var res wiki.Resolver
			if root.Kind == files.KindBrain && a.Wiki != nil {
				if ix, err := a.Wiki.Index(root, s); err == nil {
					res = ix
				}
			}
			doc := wiki.Render(data, rel, res)
			out.Doc = &doc
		case kind == files.FileText || files.LooksLikeText(data[:min(len(data), 8192)]):
			out.Kind = files.FileText
			text := string(data)
			out.Text = &text
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// rawCSP keeps a file served from the bridge's own address from running
// anything: an SVG's script or an HTML file opened directly. A PDF gets no
// sandbox, which would stop the browser's own viewer.
const (
	rawCSP    = "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; sandbox"
	rawPDFCSP = "default-src 'none'"
)

func (a *FilesAPI) raw(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	rel, err := files.Clean(q.Get("path"))
	if err != nil {
		a.audit(s, root, "open", q.Get("path"), err)
		writeFilesErr(w, err)
		return
	}
	download := q.Get("download") == "1"
	f, err := a.open(r.Context(), root, rel, s, maxNodeFileOpen)
	action := "open"
	if download {
		action = "download"
	}
	// A range request is the browser streaming audio or video it already
	// opened: one entry for the file, not one for each piece.
	if r.Header.Get("Range") == "" || err != nil {
		a.audit(s, root, action, rel, err)
	}
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	defer f.close()
	h := w.Header()
	ct := files.ContentType(f.name)
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", files.Disposition(f.name, download))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	if ct == "application/pdf" {
		h.Set("Content-Security-Policy", rawPDFCSP)
	} else {
		h.Set("Content-Security-Policy", rawCSP)
	}
	http.ServeContent(w, r, "", f.mod, f.content)
}

// wikiRoot is a brain's folder, as a wiki.
func (a *FilesAPI) wikiRoot(brain string, s files.Scope) (files.Root, bool) {
	return a.root(files.KindBrain+":"+brain, s)
}

func (a *FilesAPI) wikiBrains(w http.ResponseWriter, r *http.Request, s files.Scope) {
	out := []WikiBrain{}
	for _, root := range a.allowed(s) {
		if root.Kind != files.KindBrain {
			continue
		}
		ix, err := a.Wiki.Index(root, s)
		if err != nil || len(ix.Pages) == 0 {
			continue
		}
		out = append(out, WikiBrain{Brain: root.Brain, Label: root.Label, Pages: len(ix.Pages), Home: ix.Home})
	}
	WriteJSON(w, http.StatusOK, WikiBrainsResponse{Brains: out})
}

func (a *FilesAPI) wikiPages(w http.ResponseWriter, r *http.Request, s files.Scope) {
	root, ok := a.wikiRoot(r.PathValue("brain"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, "no such brain")
		return
	}
	ix, err := a.Wiki.Index(root, s)
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, WikiPagesResponse{Brain: root.Brain, Label: root.Label, Home: ix.Home, Pages: ix.Pages, Truncated: ix.Truncated})
}

func (a *FilesAPI) wikiPage(w http.ResponseWriter, r *http.Request, s files.Scope) {
	root, ok := a.wikiRoot(r.PathValue("brain"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, "no such brain")
		return
	}
	ix, err := a.Wiki.Index(root, s)
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	p, err := files.Clean(r.URL.Query().Get("path"))
	if err == nil && p == "" {
		p = ix.Home
	}
	page, ok := ix.Page(p)
	if err == nil && !ok {
		err = files.ErrHidden
	}
	a.audit(s, root, "wiki", p, err)
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	doc, err := ix.Render(p)
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, WikiPageResponse{Brain: root.Brain, Page: page, Doc: doc, Backlinks: ix.Backlinks(p)})
}

func (a *FilesAPI) wikiSearch(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		WriteErr(w, http.StatusBadRequest, "q is required: what to search for")
		return
	}
	brain := r.URL.Query().Get("brain")
	hits := []wiki.Hit{}
	for _, root := range a.allowed(s) {
		if root.Kind != files.KindBrain || (brain != "" && root.Brain != brain) {
			continue
		}
		ix, err := a.Wiki.Index(root, s)
		if err != nil {
			continue
		}
		hits = append(hits, ix.Search(q)...)
		if siteReader(s) {
			a.audit(s, root, "wiki-search", q, nil)
		}
	}
	if len(hits) > wiki.MaxHits {
		hits = hits[:wiki.MaxHits]
	}
	WriteJSON(w, http.StatusOK, WikiSearchResponse{Hits: hits})
}
