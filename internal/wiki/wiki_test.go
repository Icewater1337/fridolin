package wiki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/files"
)

func write(t testing.TB, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// walk calls fn on every node of nodes.
func walk(nodes []Node, fn func(Node)) {
	for _, n := range nodes {
		fn(n)
		walk(n.C, fn)
	}
}

var allowedTags = []string{"", "p", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "del", "a", "img", "table", "thead", "tbody", "tr", "th", "td", "hr", "br"}

// checkSafe fails on an element outside the list, a link to anything but
// the web (http, https, mailto), a page, a file or an anchor, and an
// image from anywhere but a file.
func checkSafe(t testing.TB, doc Doc) {
	t.Helper()
	walk(doc.Body, func(n Node) {
		if !slices.Contains(allowedTags, n.T) {
			t.Errorf("element %q", n.T)
		}
		if n.Href != "" {
			low := strings.ToLower(n.Href)
			if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") && !strings.HasPrefix(low, "mailto:") {
				t.Errorf("href %q", n.Href)
			}
		}
		if n.T == "img" && (n.Href != "" || n.File == "") {
			t.Errorf("image %+v", n)
		}
		for _, p := range []string{n.File, n.Page} {
			if strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
				t.Errorf("path %q leaves the root", p)
			}
		}
	})
}

func TestRenderIsSafe(t *testing.T) {
	src := `# Title <script>alert(1)</script>

<script>alert(2)</script>
<img src=x onerror=alert(3)>

[js](javascript:alert(4)) [data](data:text/html,<b>x</b>) [vb](  JaVaScRiPt:alert(5)) [proto](//evil.example.com/x)
[up](../../../etc/passwd) [abs](/notes.md) [ok](https://example.com/a?b=c) [mail](mailto:you@example.com)
![remote](https://evil.example.com/track.png) ![local](img/a.png) ![up](../../x.png) ![data](data:image/png;base64,AAAA)
<a href="javascript:alert(6)">x</a> www.example.com
`
	doc := Render([]byte(src), "wiki/page.md", nil)
	checkSafe(t, doc)
	data, _ := json.Marshal(doc)
	s := string(data)
	var scriptText bool
	walk(doc.Body, func(n Node) { scriptText = scriptText || n.T == "" && n.X == "<script>alert(2)</script>" })
	if !scriptText {
		t.Errorf("raw HTML is not kept as text: %s", s)
	}
	for _, want := range []string{`"href":"https://example.com/a?b=c"`, `"href":"mailto:you@example.com"`, `"t":"img","file":"wiki/img/a.png"`, `"page":"notes.md"`, `"href":"http://www.example.com"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	for _, bad := range []string{"javascript", "data:", "evil.example.com/x", "etc/passwd", `"file":"../`} {
		if strings.Contains(strings.ToLower(strings.ReplaceAll(s, `<`, "<")), "href\":\""+bad) || strings.Contains(s, `"file":"`+bad) {
			t.Errorf("%s survived: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"href":"https://evil.example.com/track.png"`) || strings.Contains(s, `"t":"img","href"`) {
		t.Errorf("a remote image must be a link, never loaded: %s", s)
	}
}

// TestRenderMissingWikiLinks: a [[link]] to no page keeps its path,
// cleaned; one that names no path in the root is only its label (#328).
func TestRenderMissingWikiLinks(t *testing.T) {
	doc := Render([]byte("[[nowhere]] [[a/../b/]] [[/]] [[../up|Up]] [[x/../../y]]"), "wiki/p.md", nil)
	checkSafe(t, doc)
	data, _ := json.Marshal(doc)
	s := string(data)
	for _, want := range []string{
		`{"t":"a","c":[{"x":"nowhere"}],"page":"nowhere","missing":true}`,
		`{"t":"a","c":[{"x":"a/../b/"}],"page":"b","missing":true}`,
		`{"t":"a","c":[{"x":"/"}]}`,
		`{"t":"a","c":[{"x":"Up"}]}`,
		`{"t":"a","c":[{"x":"x/../../y"}]}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestRenderStructure(t *testing.T) {
	src := "---\ntitle: \"Alex\"\ntags: [people, team]\n---\n# Heading one\n\nText with **bold**, *em*, ~~gone~~ and `code`.\n\n## Same\n## Same\n\n- [x] done\n- [ ] open\n\n3. three\n4. four\n\n| a | b |\n|:-|-:|\n| 1 | 2 |\n\n```go\nfunc x() {}\n```\n\n> quoted\n\n---\n"
	doc := Render([]byte(src), "wiki/alex.md", nil)
	checkSafe(t, doc)
	if doc.Title != "Alex" || !slices.Equal(doc.Tags, []string{"people", "team"}) {
		t.Errorf("title %q tags %v", doc.Title, doc.Tags)
	}
	ids := []string{}
	for _, h := range doc.TOC {
		ids = append(ids, h.ID)
	}
	if !slices.Equal(ids, []string{"heading-one", "same", "same-1"}) {
		t.Errorf("toc %v", ids)
	}
	data, _ := json.Marshal(doc)
	s := string(data)
	for _, want := range []string{`"t":"strong"`, `"t":"em"`, `"t":"del"`, `"checked":true`, `"checked":false`, `"start":3`, `"align":"left"`, `"align":"right"`, `"lang":"go"`, `"t":"blockquote"`, `"t":"hr"`, `"x":"func x() {}\n"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if d := Render([]byte("no heading"), "a.md", nil); d.Title != "" {
		t.Errorf("title %q", d.Title)
	}
}

// brainWiki is a brain folder with a small wiki, a memory note, a
// document and things the wiki must not show.
func brainWiki(t *testing.T) files.Root {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "wiki/_index.md", "# Wiki Index\n\n- [[acme]] and [[people/alex|Alex]]\n- [[nowhere]]\n- [[documents/plan]]\n")
	write(t, dir, "wiki/acme.md", "---\ntitle: Acme AG\ntags: [company]\n---\nThe company. See [Alex](people/alex.md#role) and ![[logo.png]].\n")
	write(t, dir, "wiki/people/alex.md", "# Alex\n\n## Role\nWorks at [[acme]]. Fundraising lead.\n")
	write(t, dir, "wiki/logo.png", "png")
	write(t, dir, "wiki/work/secret.md", "# Secret\nfundraising numbers")
	write(t, dir, "wiki/.drafts/x.md", "# Draft")
	write(t, dir, "memories/README.md", "# Memory\nAbout fundraising.")
	write(t, dir, "memories/people/whatsapp-identity-map.md", "# Map\n15551234567")
	write(t, dir, "documents/plan.pdf", "%PDF")
	return files.BrainRoot("main", "Main", dir, true, []string{"/wiki/work/"}, nil)
}

var owner = files.Scope{Private: true, All: true, By: "dashboard"}

func TestIndexResolvesLinksAndBacklinks(t *testing.T) {
	root := brainWiki(t)
	ix, err := NewManager().Index(root, owner)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range ix.Pages {
		paths = append(paths, p.Path)
	}
	want := []string{"wiki/_index.md", "wiki/acme.md", "wiki/people/alex.md", "memories/people/whatsapp-identity-map.md", "memories/README.md"}
	if !slices.Equal(paths, want) {
		t.Errorf("pages %v, want %v", paths, want)
	}
	if ix.Home != "wiki/_index.md" {
		t.Errorf("home %q", ix.Home)
	}
	if pg, _ := ix.Page("wiki/acme.md"); pg.Title != "Acme AG" {
		t.Errorf("title %q", pg.Title)
	}

	doc, err := ix.Render("wiki/_index.md")
	if err != nil {
		t.Fatal(err)
	}
	checkSafe(t, doc)
	data, _ := json.Marshal(doc)
	s := string(data)
	for _, want := range []string{
		`{"t":"a","c":[{"x":"acme"}],"page":"wiki/acme.md"}`,
		`{"t":"a","c":[{"x":"Alex"}],"page":"wiki/people/alex.md"}`,
		`{"t":"a","c":[{"x":"nowhere"}],"page":"nowhere","missing":true}`,
		`{"t":"a","c":[{"x":"documents/plan"}],"file":"documents/plan.pdf"}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	doc, _ = ix.Render("wiki/acme.md")
	if doc.Title != "Acme AG" || !slices.Equal(doc.Tags, []string{"company"}) {
		t.Errorf("the frontmatter is lost: %q %v", doc.Title, doc.Tags)
	}
	data, _ = json.Marshal(doc)
	if s := string(data); !strings.Contains(s, `"page":"wiki/people/alex.md","anchor":"role"`) || !strings.Contains(s, `{"t":"img","file":"wiki/logo.png","alt":"logo.png"}`) {
		t.Errorf("acme: %s", s)
	}

	var back []string
	for _, p := range ix.Backlinks("wiki/acme.md") {
		back = append(back, p.Path)
	}
	if !slices.Equal(back, []string{"wiki/people/alex.md", "wiki/_index.md"}) {
		t.Errorf("backlinks %v", back)
	}

	hits := ix.Search("FUNDRAISING")
	var found []string
	for _, h := range hits {
		found = append(found, h.Path)
	}
	slices.Sort(found)
	if !slices.Equal(found, []string{"memories/README.md", "wiki/people/alex.md"}) {
		t.Errorf("search found %v", found)
	}
	if len(ix.Search("fundraising lead")) != 1 || len(ix.Search("   ")) != 0 {
		t.Error("every word must match")
	}
}

func TestIndexForASiteRole(t *testing.T) {
	root := brainWiki(t)
	ix, err := NewManager().Index(root, files.Scope{Brains: []string{"main"}, By: "site:sam"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ix.Pages {
		if strings.Contains(p.Path, "identity-map") {
			t.Errorf("a role sees %s", p.Path)
		}
	}
	if _, err := ix.Render("memories/people/whatsapp-identity-map.md"); err == nil {
		t.Error("rendered a hidden page")
	}
}

func TestIndexIsReusedAndRefreshed(t *testing.T) {
	root := brainWiki(t)
	m := NewManager()
	now := time.Now()
	m.now = func() time.Time { return now }
	a, _ := m.Index(root, owner)
	write(t, root.Path, "wiki/new.md", "# New")
	if b, _ := m.Index(root, owner); b != a {
		t.Error("a fresh index was built again")
	}
	now = now.Add(time.Minute)
	b, _ := m.Index(root, owner)
	if _, ok := b.Page("wiki/new.md"); !ok {
		t.Error("the new page is missing after the index went stale")
	}
}

func FuzzRender(f *testing.F) {
	for _, s := range []string{
		"# x\n[[a|b]] ![[c.png]] [d](e.md#f) ![g](h.png)",
		"---\ntitle: t\ntags:\n  - a\n---\n| a |\n|-|\n| [x](javascript:y) |",
		"<script>x</script>[a](//b) [[",
		"- [ ] a\n  1. b\n> c\n```\nd\n```",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<16 {
			return
		}
		checkSafe(t, Render([]byte(src), "wiki/p.md", nil))
	})
}
