package fsx

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")
	// The folder is made, private, when it is missing.
	if err := WriteFileAtomic(path, []byte("one"), 0o600, true); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("folder: %v, %v; want mode 0700", st.Mode().Perm(), err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a first write keeps a .bak: %v", err)
	}
	steps := []struct {
		name, data string
		perm       os.FileMode
		backup     bool
		bak        string // "" for none
	}{
		{"backup keeps the previous version", "two", 0o600, true, "one"},
		{"the same bytes again keep the .bak", "two", 0o600, true, "one"},
		{"no backup leaves the .bak alone", "three", 0o644, false, "one"},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if err := WriteFileAtomic(path, []byte(s.data), s.perm, s.backup); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != s.data {
				t.Errorf("file = %q, %v; want %q", got, err, s.data)
			}
			if st, err := os.Stat(path); err != nil || st.Mode().Perm() != s.perm {
				t.Errorf("mode = %v, %v; want %v", st.Mode().Perm(), err, s.perm)
			}
			if bak, err := os.ReadFile(path + ".bak"); err != nil || string(bak) != s.bak {
				t.Errorf(".bak = %q, %v; want %q", bak, err, s.bak)
			}
		})
	}
	// Nothing is left of the temp files.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("the folder holds %v, want the file and its .bak", entries)
	}
}

func TestExpandHome(t *testing.T) {
	const home = "/Users/you"
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/", home},
		{"~/brains/notes", home + "/brains/notes"},
		{"~other/x", "~other/x"}, // another user's home is not expanded
		{"/abs/~/x", "/abs/~/x"},
		{"rel/x", "rel/x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExpandHome(c.in, home); got != c.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonical(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	// The temp folder itself may be behind a symlink (/var on macOS).
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"a symlink resolves", filepath.Join(dir, "link", "sub"), filepath.Join(realDir, "real", "sub")},
		{"cleaned", filepath.Join(dir, "real") + "/./sub/", filepath.Join(realDir, "real", "sub")},
		{"a missing path resolves the part that exists", dir + "/missing//x/..", filepath.Join(realDir, "missing")},
		{"a missing path below a symlink", filepath.Join(dir, "link", "new", "deeper"), filepath.Join(realDir, "real", "new", "deeper")},
		{"a relative missing path stays as written, cleaned", "missing//x/..", "missing"},
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, struct{ name, in, want string }{"macOS /tmp", "/tmp", "/private/tmp"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Canonical(c.in); got != c.want {
				t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestResolve: the sandbox matches real paths, so every profile path is
// resolved through symlinks, including the parts that do not exist yet.
func TestResolve(t *testing.T) {
	root := Canonical(t.TempDir())
	for _, d := range []string{"real/sub", "other"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"link":       filepath.Join(root, "real"),
		"deep":       filepath.Join(root, "real", "sub"),
		"rel":        "real",
		"real/up":    "../other",
		"chain":      "link",
		"dangling":   filepath.Join(root, "nowhere", "sub"),
		"loop1":      "loop2",
		"loop2":      "loop1",
		"other/back": "../link/sub",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ name, in, want string }{
		{"real path", "real/sub", "real/sub"},
		{"missing tail", "real/missing/deeper", "real/missing/deeper"},
		{"symlinked parent", "link/sub", "real/sub"},
		{"missing tail under a symlink", "link/missing/deeper", "real/missing/deeper"},
		{"the symlink itself", "link", "real"},
		{"relative symlink", "rel/sub", "real/sub"},
		{"relative symlink with ..", "real/up", "other"},
		{"symlink to a symlink", "chain/sub", "real/sub"},
		{"dangling symlink", "dangling/f", "nowhere/sub/f"},
		{".. after a symlink is physical", "deep/../x", "real/x"},
		{".. inside the missing part", "real/missing/../sub", "real/sub"},
		{"symlink inside a symlink target", "other/back", "real/sub"},
		{"symlink loop is left alone", "loop1/x", "loop1/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Not filepath.Join: it would apply ".." before Resolve sees it.
			if got, want := Resolve(root+"/"+tc.in), filepath.Join(root, tc.want); got != want {
				t.Errorf("Resolve(%s) = %s, want %s", tc.in, got, want)
			}
		})
	}
	t.Run("temp dir spelling", func(t *testing.T) {
		// On macOS t.TempDir() is under /var, a symlink to /private/var.
		dir := t.TempDir()
		if got, want := Resolve(dir), Canonical(dir); got != want {
			t.Errorf("Resolve(%s) = %s, want %s", dir, got, want)
		}
	})
}

func TestWithin(t *testing.T) {
	cases := []struct {
		p, dir string
		want   bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b/..c", "/a/b", true}, // a folder called ..c is inside
		{"/a/bc", "/a/b", false},   // a sibling that shares the prefix
		{"/a", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
		{"/x/y", "/a/b", false},
		{"/a/b", "a/b", false}, // one relative and one absolute path
	}
	for _, c := range cases {
		if got := Within(c.p, c.dir); got != c.want {
			t.Errorf("Within(%q, %q) = %v, want %v", c.p, c.dir, got, c.want)
		}
	}
}

func TestTilde(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Paths arrive canonical; the home folder is resolved to compare.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, p, home, want string }{
		{"the home folder", realHome, home, "~"},
		{"inside it", filepath.Join(realHome, "notes"), home, "~/notes"},
		{"outside it", "/somewhere/else", home, "/somewhere/else"},
		{"a sibling that shares the prefix", realHome + "x", home, realHome + "x"},
		{"no home folder", filepath.Join(realHome, "notes"), "", filepath.Join(realHome, "notes")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Tilde(c.p, c.home); got != c.want {
				t.Errorf("Tilde(%q, %q) = %q, want %q", c.p, c.home, got, c.want)
			}
		})
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		p    string
		want bool
	}{
		{file, true},
		{dir, true},
		{filepath.Join(dir, "missing"), false},
		{filepath.Join(dir, "dangling"), false}, // a symlink is followed
	}
	for _, c := range cases {
		if got := Exists(c.p); got != c.want {
			t.Errorf("Exists(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}
