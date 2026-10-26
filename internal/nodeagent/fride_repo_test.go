package nodeagent

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestCheckRepoName(t *testing.T) {
	for _, ok := range []string{"api", "my-repo", "repo_2", "v1.2", "A"} {
		if err := checkRepoName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", ".git", "-x", "a/b", `a\b`, "../x", "a b", "x\n", strings.Repeat("a", 101), "ä"} {
		if checkRepoName(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestCheckCloneURL(t *testing.T) {
	for _, ok := range []string{
		"https://github.com/acme/api.git", "https://github.com/acme/api",
		"ssh://git@github.com/acme/api.git", "git@github.com:acme/api.git",
	} {
		if err := checkCloneURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/tmp/repo", "./repo", "file:///etc", "http://example.com/a.git", "git://example.com/a.git",
		"ext::sh -c touch% /tmp/x", "-uhack", "--upload-pack=touch /tmp/x", "https://", "https://github.com",
		// Credentials in a URL, put together so the personal-data check does
		// not read them as an address.
		"https://token" + "@github.com/acme/api.git", "https://u:p" + "@github.com/acme/api.git", "ssh://u:p" + "@host/a.git",
		"https://github.com/a b", "C:\\repo", "fd::7",
	} {
		if checkCloneURL(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

// gitIn runs git in dir for a test.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoTestEnv gives git an identity and no other configuration than a
// rewrite of https://example.com/acme/api.git to a local repository
// (which a clone may not name itself), and puts a fake gh first in the
// PATH. It returns the file gh writes its working folder and arguments to.
func repoTestEnv(t *testing.T) (ghLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	src := t.TempDir()
	tmp := t.TempDir()
	gitcfg := filepath.Join(tmp, "gitconfig")
	cfg := "[user]\n\tname = Test\n\temail = you@example.com\n[init]\n\tdefaultBranch = main\n" +
		"[url \"" + src + "\"]\n\tinsteadOf = https://example.com/acme/api.git\n" +
		"[url \"" + filepath.Join(tmp, "missing") + "\"]\n\tinsteadOf = https://example.com/acme/gone.git\n"
	if err := os.WriteFile(gitcfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitcfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitIn(t, src, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", "main.go")
	gitIn(t, src, "commit", "--quiet", "-m", "first")

	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ghLog = filepath.Join(tmp, "gh.log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> '" + ghLog + "'\n" +
		"[ \"$3\" = broken ] && { echo 'GraphQL: Name already exists on this account' >&2; exit 1; }\n" +
		"echo \"https://github.com/you/$3\"\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ghLog
}

// Adding repositories to FrIDE through the node: a clone and a new one
// land in FrIDE's base folder, FrIDE rescans and the node offers them;
// names, URLs and folders that would leave the base are refused, and a
// failed clone leaves nothing behind.
func TestFrideRepoAdd(t *testing.T) {
	ghLog := repoTestEnv(t)
	c := startCore(t, t.TempDir())
	ff := testutil.StartFakeFride(t, "web")
	repos, _ := filepath.EvalSymlinks(t.TempDir())
	other, _ := filepath.EvalSymlinks(t.TempDir())
	ff.SetBaseFolders(repos)
	a := joinNode(t, c, nodeproto.Capabilities{Host: true})
	a.FrideCheckEvery = 50 * time.Millisecond
	ctx := context.Background()
	if _, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: ff.URL, Code: ff.NewCode(), Base: filepath.Join(repos, "nope")}); err == nil {
		t.Fatal("paired with a base folder that does not exist")
	}
	if _, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: ff.URL, Code: ff.NewCode()}); err != nil {
		t.Fatal(err)
	}
	runAgent(t, a)
	waitFor(t, "fride declared", func() bool {
		n, err := c.svc.Registry().Get(a.ID())
		return err == nil && n.Declared.Fride != nil
	})
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the approval to arrive", func() bool { return a.State().Approved.Fride != nil })

	// No base in node.json: FrIDE's only base folder.
	got, err := c.svc.FrideRepoClone(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "api", URL: "https://example.com/acme/api.git"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != filepath.Join(repos, "api") || !got.Rescanned || !slices.Contains(got.Repos, "api") {
		t.Fatalf("fride.repo_clone: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(repos, "api", "main.go")); err != nil {
		t.Fatalf("the clone: %v", err)
	}
	waitFor(t, "api offered", func() bool {
		n, err := c.svc.Registry().Get(a.ID())
		return err == nil && n.Declared.Fride != nil && slices.Contains(n.Declared.Fride.Repos, "api")
	})

	for _, p := range []nodeproto.FrideRepoParams{
		{Name: "api", URL: "https://example.com/acme/api.git"},
		{Name: "../escape", URL: "https://example.com/acme/api.git"},
		{Name: "x", URL: "file:///etc"},
		{Name: "x", URL: "--upload-pack=touch /tmp/x"},
	} {
		if _, err := c.svc.FrideRepoClone(ctx, a.ID(), p); !isCode(err, nodeproto.CodeBadRequest) {
			t.Errorf("clone %+v: %v", p, err)
		}
	}
	if _, err := c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "x", URL: "https://example.com/acme/api.git"}); !isCode(err, nodeproto.CodeBadRequest) {
		t.Errorf("a new repository with a URL: %v", err)
	}
	if _, err := c.svc.FrideRepoClone(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "gone", URL: "https://example.com/acme/gone.git"}); !isCode(err, nodeproto.CodeFailed) {
		t.Errorf("a clone that fails: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repos, "gone")); !os.IsNotExist(err) {
		t.Errorf("a failed clone left its folder: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Dir(repos)); slices.ContainsFunc(left, func(e os.DirEntry) bool { return e.Name() == "escape" }) {
		t.Error("a clone left the base folder")
	}

	// A new repository, private on GitHub by default.
	got, err = c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "tool", GitHub: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.GitHubURL != "https://github.com/you/tool" || got.Warning != "" || !slices.Contains(got.Repos, "tool") {
		t.Fatalf("fride.repo_new: %+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(repos, "tool", "README.md")); err != nil || string(data) != "# tool\n" {
		t.Fatalf("README: %q %v", data, err)
	}
	cmd := exec.Command("git", "log", "--format=%s")
	cmd.Dir = filepath.Join(repos, "tool")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "Initial commit" {
		t.Fatalf("the initial commit: %q %v", out, err)
	}
	// Public when asked; gh failing leaves the repository with a warning.
	if _, err := c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "site", GitHub: true, Public: true}); err != nil {
		t.Fatal(err)
	}
	got, err = c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "broken", GitHub: true})
	if err != nil || !strings.Contains(got.Warning, "not on GitHub") || !strings.Contains(got.Warning, "Name already exists") {
		t.Fatalf("gh failing: %+v %v", got, err)
	}
	// Without github, gh is not run.
	if _, err := c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "local"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ghLog)
	want := repos + "/tool repo create tool --private --source . --remote origin --push\n" +
		repos + "/site repo create site --public --source . --remote origin --push\n" +
		repos + "/broken repo create broken --private --source . --remote origin --push\n"
	if string(data) != want {
		t.Errorf("gh ran:\n%s\nwant:\n%s", data, want)
	}

	// A base FrIDE does not scan is refused, as are several base folders
	// without one named.
	//
	// Pairing again gives FrIDE a new token, and the old one stops working.
	// A check the agent began before may ask FrIDE with the old one: it is
	// refused, the node stops offering fride until its next check, and the
	// core refuses fride calls until it hears the offer again (#273). A
	// repository FrIDE reads only after the pairing marks the node's first
	// offer since. The core takes a node's offers in order: once it has
	// that repository, no older offer can follow.
	if _, err := PairFride(ctx, FridePairOptions{Dir: a.Dir, URL: ff.URL, Code: ff.NewCode(), Base: other}); err != nil {
		t.Fatal(err)
	}
	byHand := filepath.Join(repos, "notes")
	if err := os.Mkdir(byHand, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, byHand, "init", "--quiet")
	// What Re-check in FrIDE's settings does.
	if err := frideDo(ctx, ff.URL, "", http.MethodPost, "/api/config", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new base read, and the offer since the pairing at the core", func() bool {
		n, err := c.svc.Registry().Get(a.ID())
		return err == nil && n.Declared.Fride != nil && slices.Contains(n.Declared.Fride.Repos, "notes") && a.frideBase() == other
	})
	if _, err := c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "elsewhere"}); err == nil || !strings.Contains(err.Error(), "FrIDE does not scan") {
		t.Fatalf("a base FrIDE does not scan: %v", err)
	}
	ff.SetBaseFolders(repos, other)
	got, err = c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "elsewhere"})
	if err != nil || got.Path != filepath.Join(other, "elsewhere") {
		t.Fatalf("a new repository in the named base: %+v %v", got, err)
	}
	cfg, _ := LoadConfig(a.Dir)
	cfg.Fride.Base = ""
	if err := SaveConfig(a.Dir, cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the base dropped", func() bool { return a.frideBase() == "" })
	if _, err := c.svc.FrideRepoNew(ctx, a.ID(), nodeproto.FrideRepoParams{Name: "nowhere"}); err == nil || !strings.Contains(err.Error(), "several base folders") {
		t.Fatalf("several base folders: %v", err)
	}
}

// frideBase is the base the agent read last.
func (a *Agent) frideBase() string {
	a.fride.mu.Lock()
	defer a.fride.mu.Unlock()
	if a.fride.cfg == nil {
		return "-"
	}
	return a.fride.cfg.Base
}
