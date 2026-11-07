// Package wiki renders a brain's Markdown for the Files page and the
// wiki, and indexes the pages of its wiki/ and memories/ folders: titles,
// links between pages, backlinks and search (docs/FILES.md).
//
// Markdown in a brain folder is untrusted: a brain writes it, often from
// what someone sent it. So it is never turned into HTML. Render parses it
// (CommonMark with GitHub's tables, strikethrough, task lists and
// autolinks, and [[wiki links]]) into a tree of a few element names and
// text (Node), which the dashboard and the site build with DOM calls, so
// nothing in it can become markup or script. Raw HTML in the Markdown
// stays text. A link is a web address (http, https or mailto only), a
// page, a file of the root or an anchor on the page; an image is a file
// of the root, never a web address, so that opening a page loads nothing
// from elsewhere.
package wiki

import (
	"bytes"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/v2"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	east "github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// Node is an element or, with T empty, a text. The element names are
// those of HTML, from a fixed list: p, h1–h6, ul, ol, li, blockquote, pre,
// code, em, strong, del, a, img, table, thead, tbody, tr, th, td, hr, br.
type Node struct {
	T string `json:"t,omitempty"`
	// X is a text node's text.
	X string `json:"x,omitempty"`
	C []Node `json:"c,omitempty"`

	// ID is a heading's anchor.
	ID string `json:"id,omitempty"`
	// Href is a link to the web: http, https or mailto.
	Href string `json:"href,omitempty"`
	// Page is a link to a page of the wiki, or to a Markdown file: its
	// path in the root. Anchor is a heading on it, or on this page when
	// neither Page nor File is set.
	Page   string `json:"page,omitempty"`
	Anchor string `json:"anchor,omitempty"`
	// File is a link to, or an image from, a file of the root.
	File string `json:"file,omitempty"`
	// Missing is a [[link]] to a page that does not exist.
	Missing bool   `json:"missing,omitempty"`
	Alt     string `json:"alt,omitempty"`
	Title   string `json:"title,omitempty"`
	// Lang is a code block's language.
	Lang string `json:"lang,omitempty"`
	// Align is a table cell's alignment: left, center or right.
	Align string `json:"align,omitempty"`
	// Checked is a task list item's box.
	Checked *bool `json:"checked,omitempty"`
	// Start is an ordered list's first number, when not 1.
	Start int `json:"start,omitempty"`
}

// Heading is an entry of a page's table of contents.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	ID    string `json:"id"`
}

// Doc is a rendered page.
type Doc struct {
	// Title is the frontmatter's title, else the first heading of level
	// one; "" when it has neither.
	Title string    `json:"title"`
	Tags  []string  `json:"tags,omitempty"`
	Body  []Node    `json:"body"`
	TOC   []Heading `json:"toc"`
}

// Resolver finds what a [[link]] names: a page, or else a file, as paths
// in the root, from the folder dir of the page that links. Both "" when
// neither exists.
type Resolver interface {
	Resolve(target, dir string) (page, file string)
}

// MaxSource is the most of a file Render takes; the caller cuts it.
const MaxSource = 2 << 20

var md = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList, extension.Linkify),
	goldmark.WithParserOptions(parser.WithInlineParsers(util.Prioritized(wikiLinkParser{}, 199))),
)

// Render renders src, the Markdown file at file (a path in the root), with
// res resolving its [[links]] (nil: none resolve).
func Render(src []byte, file string, res Resolver) Doc {
	meta, body := frontmatter(src)
	r := &renderer{src: body, dir: path.Dir(file), res: res, ids: map[string]int{}}
	if r.dir == "." {
		r.dir = ""
	}
	doc := md.Parser().Parse(text.NewReader(body))
	out := Doc{Title: meta.title, Tags: meta.tags, Body: r.children(doc), TOC: r.toc}
	if out.Body == nil {
		out.Body = []Node{}
	}
	if out.TOC == nil {
		out.TOC = []Heading{}
	}
	if out.Title == "" {
		out.Title = r.firstH1
	}
	return out
}

type renderer struct {
	src     []byte
	dir     string
	res     Resolver
	ids     map[string]int
	toc     []Heading
	firstH1 string
}

func (r *renderer) children(n ast.Node) []Node {
	var out []Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, r.node(c)...)
	}
	return mergeText(out)
}

// mergeText joins neighbouring texts, which goldmark splits at every
// special character.
func mergeText(nodes []Node) []Node {
	var out []Node
	for _, n := range nodes {
		if n.T == "" && len(out) > 0 && out[len(out)-1].T == "" {
			out[len(out)-1].X += n.X
			continue
		}
		out = append(out, n)
	}
	return out
}

func el(t string, c []Node) Node { return Node{T: t, C: c} }
func txt(s string) Node          { return Node{X: s} }

// lines is a block's text, its lines joined.
func (r *renderer) lines(n ast.Node) string {
	var b strings.Builder
	l := n.Lines()
	for i := range l.Len() {
		seg := l.At(i)
		b.Write(seg.Value(r.src))
	}
	return b.String()
}

func (r *renderer) node(n ast.Node) []Node {
	switch n := n.(type) {
	case *ast.Paragraph:
		return []Node{el("p", r.children(n))}
	case *ast.TextBlock:
		return r.children(n)
	case *ast.Heading:
		c := r.children(n)
		t := strings.TrimSpace(plain(c))
		id := r.slug(t)
		r.toc = append(r.toc, Heading{Level: n.Level, Text: t, ID: id})
		if n.Level == 1 && r.firstH1 == "" {
			r.firstH1 = t
		}
		h := el("h"+strconv.Itoa(n.Level), c)
		h.ID = id
		return []Node{h}
	case *ast.ThematicBreak:
		return []Node{{T: "hr"}}
	case *ast.Blockquote:
		return []Node{el("blockquote", r.children(n))}
	case *ast.List:
		if n.IsOrdered() {
			l := el("ol", r.children(n))
			if n.Start != 1 {
				l.Start = n.Start
			}
			return []Node{l}
		}
		return []Node{el("ul", r.children(n))}
	case *ast.ListItem:
		li := el("li", nil)
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			// A tight list's items hold a TextBlock: no paragraph.
			if tb, ok := c.(*ast.TextBlock); ok {
				if box, ok := tb.FirstChild().(*east.TaskCheckBox); ok {
					checked := box.IsChecked
					li.Checked = &checked
				}
			}
			if p, ok := c.(*ast.Paragraph); ok {
				if box, ok := p.FirstChild().(*east.TaskCheckBox); ok {
					checked := box.IsChecked
					li.Checked = &checked
				}
			}
			li.C = append(li.C, r.node(c)...)
		}
		li.C = mergeText(li.C)
		return []Node{li}
	case *ast.CodeBlock:
		return []Node{{T: "pre", C: []Node{el("code", []Node{txt(r.lines(n))})}}}
	case *ast.FencedCodeBlock:
		pre := Node{T: "pre", C: []Node{el("code", []Node{txt(r.lines(n))})}}
		pre.Lang = cleanLang(string(n.Language(r.src)))
		return []Node{pre}
	case *ast.HTMLBlock:
		// Raw HTML is shown as the text it is.
		s := r.lines(n)
		if n.HasClosure() {
			s += string(n.ClosureLine.Value(r.src))
		}
		return []Node{el("p", []Node{txt(strings.TrimRight(s, "\n"))})}
	case *ast.Text:
		out := []Node{txt(string(n.Value(r.src)))}
		switch {
		case n.HardLineBreak():
			out = append(out, Node{T: "br"})
		case n.SoftLineBreak():
			out = append(out, txt("\n"))
		}
		return out
	case *ast.String:
		return []Node{txt(string(n.Value))}
	case *ast.CodeSpan:
		var b strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				b.Write(c.Value(r.src))
			case *ast.String:
				b.Write(c.Value)
			}
		}
		return []Node{el("code", []Node{txt(b.String())})}
	case *ast.Emphasis:
		if n.Level >= 2 {
			return []Node{el("strong", r.children(n))}
		}
		return []Node{el("em", r.children(n))}
	case *east.Strikethrough:
		return []Node{el("del", r.children(n))}
	case *ast.Link:
		a := r.link(string(n.Destination))
		a.C = r.children(n)
		a.Title = string(n.Title)
		if a.T == "" {
			return a.C // a link that is not allowed keeps its text
		}
		return []Node{a}
	case *ast.AutoLink:
		label := string(n.Label(r.src))
		u := string(n.URL(r.src))
		if n.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(u), "mailto:") {
			u = "mailto:" + u
		}
		if href := webHref(u); href != "" {
			return []Node{{T: "a", Href: href, C: []Node{txt(label)}}}
		}
		return []Node{txt(label)}
	case *ast.Image:
		return []Node{r.image(string(n.Destination), plain(r.children(n)), string(n.Title))}
	case *ast.RawHTML:
		var b strings.Builder
		for i := range n.Segments.Len() {
			seg := n.Segments.At(i)
			b.Write(seg.Value(r.src))
		}
		return []Node{txt(b.String())}
	case *east.Table:
		t := el("table", nil)
		var body []Node
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *east.TableHeader:
				t.C = append(t.C, el("thead", []Node{el("tr", r.cells(c, "th"))}))
			case *east.TableRow:
				body = append(body, el("tr", r.cells(c, "td")))
			}
		}
		if len(body) > 0 {
			t.C = append(t.C, el("tbody", body))
		}
		return []Node{t}
	case *east.TaskCheckBox:
		return nil // the item's Checked says it
	case *wikiLink:
		return []Node{r.wiki(n)}
	}
	// Anything else (a footnote, say, which is not turned on) keeps its
	// children's text.
	return r.children(n)
}

func (r *renderer) cells(row ast.Node, tag string) []Node {
	var out []Node
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		cell := el(tag, r.children(c))
		if tc, ok := c.(*east.TableCell); ok {
			switch tc.Alignment {
			case east.AlignLeft:
				cell.Align = "left"
			case east.AlignCenter:
				cell.Align = "center"
			case east.AlignRight:
				cell.Align = "right"
			case east.AlignNone:
				// No align attribute.
			}
		}
		out = append(out, cell)
	}
	return out
}

// plain is the text of nodes, without markup.
func plain(nodes []Node) string {
	var b strings.Builder
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if n.T == "" {
				b.WriteString(n.X)
			} else if n.T == "img" {
				b.WriteString(n.Alt)
			}
			walk(n.C)
		}
	}
	walk(nodes)
	return b.String()
}

// slug is a heading's anchor: its letters and digits, lowercased, words
// joined by "-", unique on the page.
func (r *renderer) slug(s string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			dash = true
		}
		if b.Len() > 80 {
			break
		}
	}
	id := b.String()
	if id == "" {
		id = "section"
	}
	n := r.ids[id]
	r.ids[id] = n + 1
	if n > 0 {
		id += "-" + strconv.Itoa(n)
	}
	return id
}

var langRE = regexp.MustCompile(`^[A-Za-z0-9+#._-]{1,24}$`)

func cleanLang(s string) string {
	s = strings.TrimSpace(s)
	if langRE.MatchString(s) {
		return strings.ToLower(s)
	}
	return ""
}

// webHref is u when it is a web address the page may link to: http,
// https or mailto, without control characters. "" otherwise.
func webHref(u string) string {
	u = strings.TrimSpace(u)
	if strings.ContainsFunc(u, unicode.IsControl) {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	switch strings.ToLower(p.Scheme) {
	case "http", "https":
		if p.Host == "" {
			return ""
		}
		return p.String()
	case "mailto":
		return p.String()
	}
	return ""
}

// inRoot resolves a relative link from the page's folder (or, starting
// with "/", from the root) to a path in the root, or reports false when it
// leaves the root.
func (r *renderer) inRoot(rel string) (string, bool) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if !strings.HasPrefix(rel, "/") {
		rel = path.Join(r.dir, rel)
	}
	c := path.Clean(strings.TrimLeft(rel, "/"))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	if c == "." {
		c = ""
	}
	return c, true
}

// link is a Markdown link's target as an element; T is "" when the link
// may not be followed.
func (r *renderer) link(dest string) Node {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return Node{}
	}
	if strings.HasPrefix(dest, "#") {
		return Node{T: "a", Anchor: strings.TrimPrefix(dest, "#")}
	}
	if href := webHref(dest); href != "" {
		return Node{T: "a", Href: href}
	}
	if u, err := url.Parse(dest); err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(dest, "//") {
		return Node{} // javascript:, data:, file:, a host without a scheme
	}
	target, anchor, _ := strings.Cut(dest, "#")
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	target, _, _ = strings.Cut(target, "?")
	p, ok := r.inRoot(target)
	if !ok || p == "" {
		return Node{}
	}
	if strings.EqualFold(path.Ext(p), ".md") || strings.EqualFold(path.Ext(p), ".markdown") {
		return Node{T: "a", Page: p, Anchor: anchor}
	}
	return Node{T: "a", File: p}
}

// image is an image of the root; a web address or one outside the root
// stays its alt text, with a link to the web address.
func (r *renderer) image(dest, alt, title string) Node {
	dest = strings.TrimSpace(dest)
	if href := webHref(dest); href != "" {
		label := alt
		if label == "" {
			label = href
		}
		return Node{T: "a", Href: href, C: []Node{txt(label)}}
	}
	if u, err := url.Parse(dest); err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(dest, "//") {
		return txt(alt)
	}
	if unescaped, err := url.PathUnescape(dest); err == nil {
		dest = unescaped
	}
	p, ok := r.inRoot(dest)
	if !ok || p == "" {
		return txt(alt)
	}
	return Node{T: "img", File: p, Alt: alt, Title: title}
}

// wiki is a [[link]] or an ![[embed]].
func (r *renderer) wiki(n *wikiLink) Node {
	target := strings.TrimSpace(string(n.Target))
	label := strings.TrimSpace(string(n.Alias))
	target, anchor, _ := strings.Cut(target, "#")
	target = strings.TrimSpace(target)
	if label == "" {
		label = target
		if label == "" {
			label = anchor
		} else if anchor != "" {
			label += " › " + anchor
		}
	}
	if target == "" {
		return Node{T: "a", Anchor: r.slugOf(anchor), C: []Node{txt(label)}}
	}
	var page, file string
	if r.res != nil {
		page, file = r.res.Resolve(target, r.dir)
	}
	switch {
	case page != "" && !n.Embed:
		return Node{T: "a", Page: page, Anchor: r.slugOf(anchor), C: []Node{txt(label)}}
	case page != "":
		// No pages inside pages: an embedded page is a link to it.
		return Node{T: "a", Page: page, C: []Node{txt(label)}}
	case file != "" && n.Embed && isImage(file):
		return Node{T: "img", File: file, Alt: label}
	case file != "":
		return Node{T: "a", File: file, C: []Node{txt(label)}}
	}
	// A page that does not exist yet keeps its path, cleaned, to be told.
	// A target that names no path in the root ([[/]], [[../x]]) can never
	// be a page: it is only its label.
	missing := cleanTarget(target)
	if missing != "" {
		missing = path.Clean(missing)
	}
	if missing == "" || missing == "." || missing == ".." || strings.HasPrefix(missing, "../") {
		return Node{T: "a", C: []Node{txt(label)}}
	}
	return Node{T: "a", Page: missing, Missing: true, C: []Node{txt(label)}}
}

// slugOf is the anchor a heading of this text gets.
func (r *renderer) slugOf(heading string) string {
	if heading == "" {
		return ""
	}
	return (&renderer{ids: map[string]int{}}).slug(heading)
}

func isImage(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".svg":
		return true
	}
	return false
}

// ── [[wiki links]] ──────────────────────────────────────────────────────

var kindWikiLink = ast.NewNodeKind("WikiLink")

// wikiLink is [[target|alias]] or, with Embed, ![[target|alias]].
type wikiLink struct {
	ast.BaseInline
	Target, Alias []byte
	Embed         bool
}

func (n *wikiLink) Kind() ast.NodeKind { return kindWikiLink }
func (n *wikiLink) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"Target": string(n.Target)}, nil)
}

// wikiLinkParser reads [[…]] on one line before the link parser, which
// would take it for a link's text in brackets.
type wikiLinkParser struct{}

func (wikiLinkParser) Trigger() []byte { return []byte{'!', '['} }

func (wikiLinkParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	embed := false
	if bytes.HasPrefix(line, []byte("![[")) {
		embed, line = true, line[1:]
	}
	if !bytes.HasPrefix(line, []byte("[[")) {
		return nil
	}
	end := bytes.Index(line[2:], []byte("]]"))
	if end < 0 {
		return nil
	}
	inner := line[2 : 2+end]
	if len(bytes.TrimSpace(inner)) == 0 || bytes.ContainsAny(inner, "[]\n") {
		return nil
	}
	n := &wikiLink{Embed: embed}
	n.Target, n.Alias, _ = bytes.Cut(inner, []byte("|"))
	consumed := 2 + end + 2
	if embed {
		consumed++
	}
	block.Advance(consumed)
	return n
}

// ── frontmatter ─────────────────────────────────────────────────────────

type meta struct {
	title string
	tags  []string
}

// frontmatter splits a YAML frontmatter block (between "---" lines at the
// very start) from src and reads its title and tags; nothing else of it
// is shown.
func frontmatter(src []byte) (meta, []byte) {
	var m meta
	rest, ok := bytes.CutPrefix(src, []byte("---\n"))
	if !ok {
		if rest, ok = bytes.CutPrefix(src, []byte("---\r\n")); !ok {
			return m, src
		}
	}
	var block []byte
	for off := 0; off <= len(rest); {
		nl := bytes.IndexByte(rest[off:], '\n')
		line := rest[off:]
		next := len(rest)
		if nl >= 0 {
			line, next = rest[off:off+nl], off+nl+1
		}
		if t := bytes.TrimRight(line, "\r \t"); bytes.Equal(t, []byte("---")) || bytes.Equal(t, []byte("...")) {
			block = rest[:off]
			src = rest[next:]
			ok = true
			break
		}
		ok = false
		if nl < 0 {
			break
		}
		off = next
	}
	if !ok {
		return meta{}, src
	}
	var inTags bool
	for raw := range strings.SplitSeq(string(block), "\n") {
		line := strings.TrimRight(raw, "\r")
		if inTags {
			if t, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
				m.tags = append(m.tags, unquote(t))
				continue
			}
			inTags = false
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "title":
			m.title = unquote(v)
		case "tags":
			if v == "" {
				inTags = true
				continue
			}
			v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
			for t := range strings.SplitSeq(v, ",") {
				if t = unquote(t); t != "" {
					m.tags = append(m.tags, t)
				}
			}
		}
	}
	return m, src
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}
