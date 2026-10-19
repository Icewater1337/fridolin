package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// TestGitConfigChain: every file of your git configuration is protected, so
// the profile must find all of them. It used to read ~/.gitconfig only,
// follow includes one level deep, resolve a relative include against HOME
// rather than the including file's folder, and keep an inline comment in
// the path ("path = extra ; comment" named the file "extra ; comment"), so
// the real file was neither protected nor hidden. ~/.config/git/config
// counts as well.
func TestGitConfigChain(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	files := map[string]string{
		".gitconfig": "[user]\n\tname = You\n" +
			"[include]\n\tpath = .gitconfig-extra ; a comment\n" +
			"[includeIf \"gitdir:~/work/\"]\n\tPath = \"~/work/git config\" # quoted, with a space\n" +
			"[include] path = conf.d/inline\n" +
			"[alias]\n\tpath = not-an-include\n",
		".gitconfig-extra":   "[include]\n\tpath = nested/one\n",
		"nested/one":         "[include]\n\tpath = two ; relative to nested/\n",
		"nested/two":         "[include]\n\tpath = ../.gitconfig\n", // a cycle back to the start
		"work/git config":    "",
		"conf.d/inline":      "",
		".config/git/config": "[include]\n\tpath = ~/xdg-extra\n",
		"xdg-extra":          "",
		"not-an-include":     "",
	}
	for name, content := range files {
		writeTestFile(t, filepath.Join(home, name), content)
	}
	got := gitConfigChain(home)
	want := []string{".gitconfig", ".gitconfig-extra", "nested/one", "nested/two", "work/git config", "conf.d/inline", ".config/git/config", "xdg-extra"}
	for i, w := range want {
		want[i] = filepath.Join(home, w)
	}
	if !slices.Equal(got, want) {
		t.Errorf("gitConfigChain:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	t.Run("depth", func(t *testing.T) {
		home := fsx.Canonical(t.TempDir())
		writeTestFile(t, filepath.Join(home, ".gitconfig"), "[include]\npath = c1\n")
		for i := 1; i <= maxGitIncludeDepth+3; i++ {
			writeTestFile(t, filepath.Join(home, fmt.Sprintf("c%d", i)), fmt.Sprintf("[include]\npath = c%d\n", i+1))
		}
		if got := gitConfigChain(home); !slices.Contains(got, filepath.Join(home, fmt.Sprintf("c%d", maxGitIncludeDepth))) ||
			slices.Contains(got, filepath.Join(home, fmt.Sprintf("c%d", maxGitIncludeDepth+2))) {
			t.Errorf("gitConfigChain = %q, want includes followed %d levels deep", got, maxGitIncludeDepth)
		}
	})
}

// TestGitConfigValue reads values as git does.
func TestGitConfigValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{" extra", "extra"},
		{" extra ; comment", "extra"},
		{" extra# comment", "extra"},
		{` "with ; inside" ; comment`, "with ; inside"},
		{` "~/a b"`, "~/a b"},
		{` "  padded  "`, "  padded  "},
		{` a\"b`, `a"b`},
		{` a\\b`, `a\b`},
		{" ;only a comment", ""},
	}
	for _, c := range cases {
		if got := gitConfigValue(c.in); got != c.want {
			t.Errorf("gitConfigValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSiteSandboxProfileOrder: the last matching rule wins, so what no
// module may be given comes after what the modules declare, and the site's
// own files after that.
func TestSiteSandboxProfileOrder(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".fridolin")
	in := SitePolicy{
		Home: home, DataDir: data,
		Shell: filepath.Join(data, "site", "shell"), Deploy: filepath.Join(data, "site", "deploys", "abc"),
		Tmp: filepath.Join(data, "site", "tmp"), Reads: []string{filepath.Join(data, "site", "users.json")},
		Modules: []SiteModule{{ID: "m", Dir: filepath.Join(data, "site", "deploys", "abc", "modules", "m"), Files: []FileCapability{
			{Name: "keys", Path: filepath.Join(home, ".ssh"), Access: "read", Folder: true},
			{Name: "db", Path: filepath.Join(home, "data", "m.db"), Access: "write"},
			{Name: "secrets", Path: filepath.Join(data, "site", "secrets"), Access: "read", Folder: true},
		}}},
	}
	profile, err := SiteProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	at := func(rule string) int {
		t.Helper()
		i := strings.LastIndex(profile, rule)
		if i < 0 {
			t.Fatalf("no %s in\n%s", rule, profile)
		}
		return i
	}
	q := func(p string) string { s, _ := textx.SBPLString(p); return s }
	sshAllow := at(`(allow file-read* (subpath ` + q(filepath.Join(home, ".ssh")) + `))`)
	sshDeny := at(`(deny file-read* (subpath ` + q(filepath.Join(home, ".ssh")) + `))`)
	secretsAllow := at(`(allow file-read* (subpath ` + q(filepath.Join(data, "site", "secrets")) + `))`)
	dataDeny := at(`(deny file-read* (subpath ` + q(data) + `))`)
	shellAllow := at(`(allow file-read* (subpath ` + q(in.Shell) + `))`)
	if !(sshAllow < sshDeny && secretsAllow < dataDeny && dataDeny < shellAllow) {
		t.Errorf("rules out of order: ssh allow %d deny %d, secrets allow %d, data deny %d, shell allow %d", sshAllow, sshDeny, secretsAllow, dataDeny, shellAllow)
	}
	db := filepath.Join(home, "data", "m.db")
	at(`(allow file-read* file-write* (regex "^` + strings.ReplaceAll(regexp.QuoteMeta(db), `\`, `\\`) + `(-journal|-wal|-shm)?$"))`)
	at(`(deny file-write-create (require-all (literal ` + q(db) + `) (vnode-type SYMLINK)))`)
	if strings.Contains(profile, "allow file-write* (subpath "+q(in.Shell)) {
		t.Error("the shell is writable")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
