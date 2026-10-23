package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPackageCopy(t *testing.T) {
	root := t.TempDir()
	a := AppData{Local: filepath.Join(root, "Local"), Roaming: filepath.Join(root, "Roaming")}
	cache := filepath.Join(a.Local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	for path, want := range map[string]string{
		filepath.Join(a.Local, "fridolin-node"):           filepath.Join(cache, "Local", "fridolin-node"),
		filepath.Join(a.Local, "a", "b"):                  filepath.Join(cache, "Local", "a", "b"),
		filepath.Join(a.Roaming, "fridolin-node"):         filepath.Join(cache, "Roaming", "fridolin-node"),
		filepath.Join(a.Local, "Packages", "x", "y"):      "",
		filepath.Join(root, "fridolin-node"):              "",
		filepath.Join(root, "Localx", "fridolin-node"):    "",
		filepath.Join(a.Local, "..", "Roaming", "x", "y"): filepath.Join(cache, "Roaming", "x", "y"),
	} {
		got, ok := a.PackageCopy("Claude_pzs8sxrjxfjjc", path)
		if got != want || ok != (want != "") {
			t.Errorf("PackageCopy(%s) = %q, %v; want %q", path, got, ok, want)
		}
	}
	if _, ok := a.PackageCopy("", filepath.Join(a.Local, "fridolin-node")); ok {
		t.Error("no package: a copy")
	}
	if _, ok := (AppData{}).PackageCopy("Claude_pzs8sxrjxfjjc", filepath.Join(a.Local, "fridolin-node")); ok {
		t.Error("no AppData: a copy")
	}
	if runtime.GOOS == "windows" {
		// Windows paths ignore case, and so do the copies.
		got, ok := a.PackageCopy("Claude_pzs8sxrjxfjjc", strings.ToUpper(filepath.Join(a.Local, "fridolin-node")))
		if !ok || !strings.EqualFold(got, filepath.Join(cache, "Local", "fridolin-node")) {
			t.Errorf("upper case: %q, %v", got, ok)
		}
	}
}

func TestPackageCopies(t *testing.T) {
	root := t.TempDir()
	a := AppData{Local: filepath.Join(root, "Local"), Roaming: filepath.Join(root, "Roaming")}
	dir := filepath.Join(a.Local, "fridolin-node")
	if got := a.PackageCopies(dir); got != nil {
		t.Errorf("no Packages folder: %q", got)
	}
	claude := filepath.Join(a.Local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache", "Local", "fridolin-node")
	for _, d := range []string{claude, filepath.Join(a.Local, "Packages", "Other_8wekyb3d8bbwe", "LocalState")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a.Local, "Packages", "stray.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.PackageCopies(dir); len(got) != 1 || got[0] != claude {
		t.Errorf("copies: %q, want %s", got, claude)
	}
	if got := a.PackageCopies(filepath.Join(root, "elsewhere")); got != nil {
		t.Errorf("outside AppData: %q", got)
	}
}

// redirectedFixture is an AppData whose Claude package copy shows the real
// Local folder, as Windows' redirection shows a package's processes their
// copy in place of it: a link from the copy to the real folder. It skips
// the test where no link can be made.
func redirectedFixture(t *testing.T) (AppData, string) {
	t.Helper()
	root := t.TempDir()
	a := AppData{Local: filepath.Join(root, "Local"), Roaming: filepath.Join(root, "Roaming")}
	cache := filepath.Join(a.Local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cache, "Local")
	if err := os.Symlink(a.Local, link); err != nil {
		// Windows lets only some users make symbolic links, and everyone
		// junctions.
		if runtime.GOOS != "windows" || exec.Command("cmd", "/c", "mklink", "/J", link, a.Local).Run() != nil {
			t.Skipf("no links here: %v", err)
		}
	}
	// Removed before the temp dir, which would otherwise be walked round
	// the loop.
	t.Cleanup(func() { os.Remove(link) })
	return a, link
}

func TestWritesRedirected(t *testing.T) {
	root := t.TempDir()
	a := AppData{Local: filepath.Join(root, "Local"), Roaming: filepath.Join(root, "Roaming")}
	dir := filepath.Join(a.Local, "fridolin-node")
	// Another package's folder, which holds nothing of this.
	if err := os.MkdirAll(filepath.Join(a.Local, "Packages", "Other_8wekyb3d8bbwe", "LocalState"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Nothing redirects here: the probe lands in the real folder.
	if to, _, ok := a.WritesRedirected(dir); ok {
		t.Errorf("not redirected: %s", to)
	}
	if _, _, ok := a.WritesRedirected(filepath.Join(root, "elsewhere")); ok {
		t.Error("outside AppData: redirected")
	}
	if _, _, ok := (AppData{}).WritesRedirected(dir); ok {
		t.Error("no AppData: redirected")
	}
	entries, _ := os.ReadDir(a.Local)
	if len(entries) != 1 {
		t.Errorf("the probe stays: %v", entries)
	}

	r, link := redirectedFixture(t)
	to, family, ok := r.WritesRedirected(filepath.Join(r.Local, "fridolin-node"))
	if want := filepath.Join(link, "fridolin-node"); !ok || to != want || family != "Claude_pzs8sxrjxfjjc" {
		t.Errorf("redirected: %q, %q, %v; want %s", to, family, ok, want)
	}
	if entries, _ := os.ReadDir(r.Local); len(entries) != 1 {
		t.Errorf("the probe stays: %v", entries)
	}
}

func TestSeenAsCopy(t *testing.T) {
	// Outside the package: the real file and the package's copy are two.
	root := t.TempDir()
	a := AppData{Local: filepath.Join(root, "Local"), Roaming: filepath.Join(root, "Roaming")}
	file := filepath.Join(a.Local, "fridolin-node", "node.json")
	cp, _ := a.PackageCopy("Claude_pzs8sxrjxfjjc", file)
	for _, p := range []string{file, cp} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c, _, ok := a.SeenAsCopy(file); ok {
		t.Errorf("outside the package: seen as %s", c)
	}
	if _, _, ok := a.SeenAsCopy(filepath.Join(a.Local, "nope")); ok {
		t.Error("a missing file: seen as a copy")
	}
	// Inside: what shows at the real path is the copy.
	r, link := redirectedFixture(t)
	path := filepath.Join(r.Local, "fridolin-node", "node.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, family, ok := r.SeenAsCopy(path)
	if want := filepath.Join(link, "fridolin-node", "node.json"); !ok || c != want || family != "Claude_pzs8sxrjxfjjc" {
		t.Errorf("inside the package: %q, %q, %v; want %s", c, family, ok, want)
	}
}
