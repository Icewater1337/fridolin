package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// brainFolder is a brain folder with something of every kind a root
// hides, and a secret next to it, outside.
func brainFolder(t *testing.T) (dir, outside, data string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "brain")
	outside = filepath.Join(base, "secret.txt")
	write(t, base, "secret.txt", "outside")
	for _, f := range []string{
		"notes.md", "wiki/index.md", "wiki/people/alex.md", "docs/report.pdf",
		".env", ".git/config", "work/big.bin", "build.log", "keys/id_ed25519",
		"brain.json", "conversation-history.jsonl", "claude-stream.log",
		"memories/README.md", "memories/people/whatsapp-identity-map.md",
		"fridolin-data/bridge.db", "denied/x.txt",
	} {
		write(t, dir, f, "x")
	}
	data = filepath.Join(dir, "fridolin-data")
	return dir, outside, data
}

func names(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

var owner = Scope{Private: true, All: true, Media: true, Nodes: true, By: "dashboard"}

func TestListHidesWhatARootHides(t *testing.T) {
	dir, _, data := brainFolder(t)
	r := BrainRoot("main", "Main", dir, true, []string{"/work/", "*.log"}, []string{data, filepath.Join(dir, "denied"), "/elsewhere"})
	got, _, err := r.List("", owner)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs", "keys", "memories", "wiki", "brain.json", "conversation-history.jsonl", "notes.md"}
	if !slices.Equal(names(got), want) {
		t.Errorf("owner sees %v, want %v", names(got), want)
	}
	keys, _, err := r.List("keys", owner)
	if err != nil || len(keys) != 0 {
		t.Errorf("keys/: %v %v, want the private key hidden", names(keys), err)
	}

	member := Scope{Brains: []string{"main"}, By: "site:sam"}
	got, _, err = r.List("", member)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"docs", "keys", "memories", "wiki", "notes.md"}
	if !slices.Equal(names(got), want) {
		t.Errorf("a member sees %v, want %v", names(got), want)
	}
	mem, _, _ := r.List("memories", member)
	if !slices.Equal(names(mem), []string{"people"}) && !slices.Equal(names(mem), []string{"people", "README.md"}) {
		t.Errorf("memories/: %v", names(mem))
	}
	people, _, _ := r.List("memories/people", member)
	if len(people) != 0 {
		t.Errorf("a member sees the identity map: %v", names(people))
	}
}

func TestPathsThatLeaveTheRootAreRefused(t *testing.T) {
	dir, outside, data := brainFolder(t)
	r := BrainRoot("main", "Main", dir, true, []string{"/work/"}, []string{data})
	for _, rel := range []string{
		"../secret.txt", "wiki/../../secret.txt", "..", "a/../../secret.txt",
		`..\secret.txt`, "notes.md\x00", "\xff", outside,
		".env", "wiki/../.env", ".git/config", "work/big.bin", "fridolin-data/bridge.db",
	} {
		if f, _, err := r.Open(rel, owner); err == nil {
			f.Close()
			t.Errorf("Open(%q) opened it", rel)
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q): %v, want it not found", rel, err)
		}
		if _, _, err := r.List(rel, owner); err == nil {
			t.Errorf("List(%q) listed it", rel)
		}
	}
	// An absolute path is a path inside the root.
	if _, _, err := r.List("/wiki", owner); err != nil {
		t.Errorf("List(/wiki): %v", err)
	}
	f, fi, err := r.Open("wiki/people/alex.md", owner)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := io.ReadAll(f); string(b) != "x" || fi.Size() != 1 {
		t.Errorf("read %q", b)
	}
}

func TestSymlinksAreNeverFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir, outside, _ := brainFolder(t)
	r := BrainRoot("main", "Main", dir, true, []string{"/work/"}, nil)
	links := map[string]string{
		"out.txt":     outside,
		"outdir":      filepath.Dir(outside),
		"inwork":      filepath.Join(dir, "work"),
		"env":         filepath.Join(dir, ".env"),
		"wiki/notes":  filepath.Join(dir, "notes.md"),
		"wiki/linked": filepath.Join(dir, "docs"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name := range links {
		if f, _, err := r.Open(name, owner); err == nil {
			f.Close()
			t.Errorf("Open(%q) followed the symlink", name)
		}
		if _, _, err := r.List(name, owner); err == nil {
			t.Errorf("List(%q) followed the symlink", name)
		}
	}
	for _, rel := range []string{"outdir/secret.txt", "inwork/big.bin", "wiki/linked/report.pdf"} {
		if f, _, err := r.Open(rel, owner); err == nil {
			f.Close()
			t.Errorf("Open(%q) went through a symlinked folder", rel)
		}
	}
	got, _, _ := r.List("", owner)
	for _, n := range names(got) {
		if _, ok := links[n]; ok {
			t.Errorf("the listing shows the symlink %s", n)
		}
	}
	hits, _, err := r.Search("", "secret", owner)
	if err != nil || len(hits) != 0 {
		t.Errorf("Search went through a symlink: %v %v", hits, err)
	}
}

func TestSearchFindsNamesAndSkipsHidden(t *testing.T) {
	dir, _, data := brainFolder(t)
	r := BrainRoot("main", "Main", dir, true, []string{"/work/"}, []string{data})
	write(t, dir, "work/alex-notes.md", "x")
	write(t, dir, ".hidden/alex.md", "x")
	hits, truncated, err := r.Search("", "ALEX", owner)
	if err != nil || truncated {
		t.Fatal(err, truncated)
	}
	if len(hits) != 1 || hits[0].Path != "wiki/people/alex.md" || hits[0].Kind != FileMarkdown {
		t.Errorf("hits %+v", hits)
	}
	if _, _, err := r.Search("../", "x", owner); err == nil {
		t.Error("a search above the root ran")
	}
	if _, _, err := r.Search("", " ", owner); err == nil {
		t.Error("an empty search ran")
	}
}

func TestScopeAllows(t *testing.T) {
	brain := BrainRoot("acme", "Acme", t.TempDir(), true, nil, nil)
	media := MediaRoot("acme", "Acme", t.TempDir(), nil)
	node := NodeRoot("n1", "box", 0, "/srv", false, true)
	member := Scope{Brains: []string{"acme"}}
	if !member.Allows(brain) || member.Allows(media) || member.Allows(node) {
		t.Error("a member reads its brain's folder only")
	}
	if (Scope{Brains: []string{"other"}}).Allows(brain) {
		t.Error("a role reads another brain")
	}
	if !owner.Allows(brain) || !owner.Allows(media) || !owner.Allows(node) {
		t.Error("the owner reads everything")
	}
}

func TestMediaInTheDataDirIsNotHiddenByIt(t *testing.T) {
	data := t.TempDir()
	write(t, data, "media/main/a.jpg", "x")
	r := MediaRoot("main", "Main", filepath.Join(data, "media", "main"), []string{data})
	got, _, err := r.List("", owner)
	if err != nil || !slices.Equal(names(got), []string{"a.jpg"}) {
		t.Errorf("%v %v", names(got), err)
	}
}

func TestKindsAndTypes(t *testing.T) {
	for name, want := range map[string][2]string{
		"a.md":       {FileMarkdown, "text/plain; charset=utf-8"},
		"page.html":  {FileText, "text/plain; charset=utf-8"},
		"x.svg":      {FileImage, "image/svg+xml"},
		"x.PDF":      {FilePDF, "application/pdf"},
		"song.m4a":   {FileAudio, "audio/mp4"},
		"clip.mov":   {FileVideo, "video/quicktime"},
		"tool.exe":   {FileOther, "application/octet-stream"},
		"Makefile":   {FileText, "text/plain; charset=utf-8"},
		"data.jsonl": {FileText, "text/plain; charset=utf-8"},
	} {
		if k, ct := KindOf(name), ContentType(name); k != want[0] || ct != want[1] {
			t.Errorf("%s: %s %s, want %v", name, k, ct, want)
		}
	}
	if d := Disposition("tool.exe", false); d != `attachment; filename=tool.exe` {
		t.Errorf("binary: %s", d)
	}
	if d := Disposition("Bericht ü.pdf", false); d != `inline; filename*=utf-8''Bericht%20%C3%BC.pdf` {
		t.Errorf("pdf: %s", d)
	}
	if !LooksLikeText([]byte("hällo\xc3")) || LooksLikeText([]byte("a\x00b")) {
		t.Error("LooksLikeText")
	}
}
