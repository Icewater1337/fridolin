package main

import (
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// deliveries records what brains hand back to the bridge.
type deliveries struct {
	mu   sync.Mutex
	runs []delivered
}

type delivered struct {
	brain string
	msg   Inbound
	res   RunResult
}

func (d *deliveries) add(br *Brain, m Inbound, res RunResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, delivered{br.ID(), m, res})
}

func (d *deliveries) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.runs)
}

func (d *deliveries) all() []delivered {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]delivered(nil), d.runs...)
}

// queueTestManager builds a manager with inline brains a (DMs) and b (a group)
// running the fake CLI.
func queueTestManager(t *testing.T, sleepMS int, extra string) (*BrainManager, *deliveries, string) {
	t.Helper()
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	claude := fakeClaudeEnv(t, sleepMS)
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+claude+`",
		"brains": [
			{"name": "a", "work_dir": "`+home+`/a", "handles_dms": true},
			{"name": "b", "work_dir": "`+home+`/b", "groups": ["120363000000000000@g.us"]}
		]`+extra+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, cfg)
	d := &deliveries{}
	mgr.SetHooks(BrainHooks{Deliver: d.add})
	t.Cleanup(mgr.Stop)
	return mgr, d, home
}

func dm(text string) Inbound {
	return Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: text}
}

func TestBrainRunsAndDeliversInOrder(t *testing.T) {
	mgr, d, home := queueTestManager(t, 50, "")
	for _, txt := range []string{"one", "two", "three"} {
		if _, _, _, err := mgr.Submit(dm(txt)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, "three runs", func() bool { return d.count() == 3 })
	for i, want := range []string{"one", "two", "three"} {
		r := d.all()[i]
		if r.res.Err != nil {
			t.Fatalf("run %d: %v", i, r.res.Err)
		}
		if !strings.HasSuffix(r.res.Text, "says: "+want) || !strings.Contains(r.res.Text, filepath.Join(home, "a")) {
			t.Errorf("run %d = %q, want an echo of %q from brain a's folder", i, r.res.Text, want)
		}
	}
	hist, _ := os.ReadFile(filepath.Join(home, "a", "conversation-history.jsonl"))
	if strings.Count(string(hist), `"type":"in"`) != 3 || strings.Count(string(hist), `"type":"out"`) != 3 {
		t.Errorf("history:\n%s", hist)
	}
}

func TestBusyReportedAndQueuePosition(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 400, "")
	_, _, busy, _ := mgr.Submit(dm("first"))
	if busy {
		t.Error("an idle brain reported busy")
	}
	waitFor(t, 5*time.Second, "first run to start", func() bool { return mgr.Get("a").Snapshot().Running != nil })
	_, pos, busy, _ := mgr.Submit(dm("second"))
	if !busy || pos != 1 {
		t.Errorf("second: busy=%v pos=%d, want busy at #1", busy, pos)
	}
	waitFor(t, 10*time.Second, "both runs", func() bool { return d.count() == 2 })
}

func TestStopKillsTheRunningProcess(t *testing.T) {
	// The private fork's queue never recorded the current run, so !stop and
	// !reset could not kill it. Regression test.
	mgr, d, _ := queueTestManager(t, 30000, "")
	_, _, _, _ = mgr.Submit(dm("long task"))
	_, _, _, _ = mgr.Submit(dm("queued"))
	br := mgr.Get("a")
	waitFor(t, 5*time.Second, "run with a pid", func() bool {
		rt := br.Snapshot()
		return rt.Running != nil && rt.Running.PID > 0
	})
	start := time.Now()
	if !br.StopCurrent(true) {
		t.Fatal("StopCurrent reported nothing running")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("stopping took too long — the process was not killed")
	}
	waitFor(t, 5*time.Second, "interrupted delivery", func() bool { return d.count() == 1 })
	if r := d.all()[0]; r.res.Err == nil || !strings.Contains(r.res.Err.Error(), "interrupted") {
		t.Errorf("result = %+v, want interrupted", r.res)
	}
	time.Sleep(200 * time.Millisecond)
	if d.count() != 1 || br.Snapshot().Running != nil {
		t.Error("the cleared queue still ran")
	}
}

func TestInterruptModeSupersedes(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 30000, `, "queue": {"mode": "interrupt"}`)
	_, _, _, _ = mgr.Submit(dm("old"))
	waitFor(t, 5*time.Second, "old run", func() bool { r := mgr.Get("a").Snapshot().Running; return r != nil && r.PID > 0 })
	t.Setenv("WCB_FAKE_SLEEP_MS", "10")
	_, _, _, _ = mgr.Submit(dm("new"))
	waitFor(t, 10*time.Second, "two deliveries", func() bool { return d.count() == 2 })
	runs := d.all()
	if runs[0].res.Err == nil || runs[1].res.Err != nil || !strings.HasSuffix(runs[1].res.Text, "says: new") {
		t.Errorf("runs = %+v", runs)
	}
}

func TestSilentMessagesAreSupersededAndQueueOverflows(t *testing.T) {
	mgr, d, _ := queueTestManager(t, 500, `, "queue": {"max_pending": 2}`)
	g := func(text string, silent bool) Inbound {
		return Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: text, Silent: silent}
	}
	_, _, _, _ = mgr.Submit(g("running", false))
	waitFor(t, 5*time.Second, "run", func() bool { return mgr.Get("b").Snapshot().Running != nil })
	_, _, _, _ = mgr.Submit(g("chatter 1", true))
	_, _, _, _ = mgr.Submit(g("chatter 2", true)) // supersedes chatter 1
	if q := mgr.Get("b").Snapshot().Queue; len(q) != 1 || q[0].Preview != "chatter 2" {
		t.Errorf("queue = %+v, want only the newest silent message", q)
	}
	_, _, _, _ = mgr.Submit(g("question", false)) // supersedes chatter 2 too
	_, _, _, _ = mgr.Submit(g("question 2", false))
	if _, _, _, err := mgr.Submit(g("question 3", false)); err != errQueueFull {
		t.Errorf("err = %v, want queue full", err)
	}
	waitFor(t, 10*time.Second, "runs", func() bool { return d.count() == 3 })
}

func TestReloadKeepsRunningBrainAndRetiresRemoved(t *testing.T) {
	mgr, d, home := queueTestManager(t, 800, "")
	a := mgr.Get("a")
	_, _, _, _ = mgr.Submit(dm("keep going"))
	_, _, _, _ = mgr.Submit(Inbound{JID: "120363000000000000@g.us", Sender: "15551234567", Text: "doomed"})
	waitFor(t, 5*time.Second, "both running", func() bool {
		return a.Snapshot().Running != nil && mgr.Get("b").Snapshot().Running != nil
	})

	// Concurrent routing while reloading must be race-free (run with -race).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = mgr.BrainFor("120363000000000000@g.us", true)
					_ = mgr.Brains()
				}
			}
		}()
	}

	// Remove b, give a a new model, add c.
	cfg, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+os.Args[0]+`",
		"brain_model": "new-model",
		"brains": [
			{"name": "a", "work_dir": "`+home+`/a", "handles_dms": true},
			{"name": "c", "work_dir": "`+home+`/c", "groups": ["120363000000000000@g.us"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	if mgr.Get("a") != a {
		t.Error("an unchanged brain must keep its *Brain (and its running process)")
	}
	if a.settings.Load().model != "new-model" {
		t.Error("settings were not swapped")
	}
	if br := mgr.BrainFor("120363000000000000@g.us", true); br == nil || br.ID() != "c" {
		t.Error("the group must route to the new brain")
	}
	if mgr.Get("b") != nil {
		t.Error("b should be gone")
	}
	waitFor(t, 10*time.Second, "both deliveries", func() bool { return d.count() == 2 })
	for _, r := range d.all() {
		switch r.brain {
		case "a":
			if r.res.Err != nil {
				t.Errorf("a's run must finish normally, got %v", r.res.Err)
			}
		case "b":
			if r.res.Err == nil {
				t.Error("b's run must be interrupted when b is removed")
			}
		}
	}
	if !strings.Contains(mustProfile(t, mgr.profiler, "a"), filepath.Join(home, "c")) {
		t.Error("a's sandbox profile must deny the new brain c")
	}
}

// TestClaudeArgs: a brain loads only its own folder's Claude Code setup,
// with the folder's .mcp.json passed explicitly and the instructions above
// it excluded, unless inherit_claude_user_config asks for the old
// behaviour. The other settings map to their flags, and the prompt comes
// last.
func TestClaudeArgs(t *testing.T) {
	const mcp = "/brains/a/.mcp.json"
	const settings = `{"claudeMdExcludes":["/brains/CLAUDE.md"]}`
	cases := []struct {
		name                string
		s                   brainSettings
		mcpConfig, settings string
		want, dontWant      []string
	}{
		{"own setup only", brainSettings{}, "", "",
			[]string{"--setting-sources project,local", "--strict-mcp-config"},
			[]string{"--settings", "--mcp-config", "--dangerously-skip-permissions", "--model", "--effort", "--disallowed-tools"}},
		{"with the folder's .mcp.json", brainSettings{}, mcp, "",
			[]string{"--setting-sources project,local", "--strict-mcp-config --mcp-config " + mcp}, nil},
		{"with the instructions above excluded", brainSettings{}, mcp, settings,
			[]string{"--setting-sources project,local", "--settings " + settings, "--mcp-config " + mcp}, nil},
		{"inherit_claude_user_config", brainSettings{inheritUser: true}, mcp, settings,
			nil, []string{"--setting-sources", "--strict-mcp-config", "--settings", "--mcp-config"}},
		{"model, effort, tools, permissions", brainSettings{model: "m", effort: "high", disallowedTools: []string{"WebFetch", "WebSearch"}, dangerously: true}, mcp, settings,
			[]string{"--model m", "--effort high", "--disallowed-tools WebFetch,WebSearch", "--dangerously-skip-permissions"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := claudeArgs(&tc.s, tc.mcpConfig, tc.settings, "hi")
			got := strings.Join(args, " ")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("args %q lack %q", got, w)
				}
			}
			for _, w := range tc.dontWant {
				if strings.Contains(got, w) {
					t.Errorf("args %q have %q", got, w)
				}
			}
			if !strings.HasSuffix(got, " -p hi") {
				t.Errorf("args %q: the prompt must come last", got)
			}
			// --mcp-config takes several files, so the CLI would take
			// anything after the file that is not an option for another.
			if i := slices.Index(args, "--mcp-config"); i >= 0 && !strings.HasPrefix(args[i+2], "-") {
				t.Errorf("args %q: %q follows --mcp-config's file", got, args[i+2])
			}
		})
	}
}

// TestInstructionsAbove: the claudeMdExcludes for a brain name the
// instruction files of every folder above it, up to /, in each spelling the
// CLI may meet them, and your user memory for a brain outside your home
// folder too, but nothing of the brain's own folder.
func TestInstructionsAbove(t *testing.T) {
	mkdir := func(t *testing.T, p string) {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		// layout makes the folders below root and returns the brain's
		// folder and HOME.
		layout func(t *testing.T, root string) (dir, home string)
		// folders above the brain whose four patterns must be there, and
		// patterns that must not, below root.
		above, without []string
	}{
		{"a brain in the home folder", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "home", "brains", "a"))
			return filepath.Join(root, "home", "brains", "a"), filepath.Join(root, "home")
		}, []string{"home/brains", "home", ""},
			[]string{"home/brains/a/CLAUDE.md", "home/brains/a/CLAUDE.local.md", "home/brains/a/.claude/CLAUDE.md", "home/brains/a/.claude/rules/**"}},
		{"a brain outside the home folder", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "home"))
			mkdir(t, filepath.Join(root, "elsewhere", "a"))
			return filepath.Join(root, "elsewhere", "a"), filepath.Join(root, "home")
		}, []string{"elsewhere", ""},
			// Only the user memory of the home folder: no brain loads the rest.
			[]string{"home/CLAUDE.md", "home/CLAUDE.local.md"}},
		{"a brains folder linked from another volume", func(t *testing.T, root string) (string, string) {
			mkdir(t, filepath.Join(root, "volume", "brains", "a"))
			mkdir(t, filepath.Join(root, "home"))
			if err := os.Symlink(filepath.Join(root, "volume", "brains"), filepath.Join(root, "home", "brains")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(root, "home", "brains", "a"), filepath.Join(root, "home")
		}, []string{"home/brains", "home", "volume/brains", "volume", ""}, nil},
		{"folders with glob characters", func(t *testing.T, root string) (string, string) {
			dir := filepath.Join(root, "home", "notes (old) [1] {x,y}", "*?!+@|", "a")
			mkdir(t, dir)
			return dir, filepath.Join(root, "home")
		}, []string{"home/notes (old) [1] {x,y}/*?!+@|", "home/notes (old) [1] {x,y}", "home", ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalDir(t.TempDir())
			dir, home := tc.layout(t, root)
			got := instructionsAbove(dir, home)
			for _, p := range got {
				if !strings.HasPrefix(p, "/") {
					t.Errorf("relative pattern %q", p)
				}
			}
			for _, f := range tc.above {
				d := globEscape(filepath.Join(root, f))
				for _, want := range []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", ".claude/rules/**"} {
					if want = filepath.Join(d, want); !slices.Contains(got, want) {
						t.Errorf("patterns lack %q:\n%s", want, strings.Join(got, "\n"))
					}
				}
			}
			for _, f := range []string{"/CLAUDE.md", "/.claude/rules/**", filepath.Join(globEscape(home), ".claude", "CLAUDE.md"), filepath.Join(globEscape(home), ".claude", "rules", "**")} {
				if !slices.Contains(got, f) {
					t.Errorf("patterns lack %q", f)
				}
			}
			for _, f := range tc.without {
				if p := filepath.Join(globEscape(root), f); slices.Contains(got, p) {
					t.Errorf("patterns have %q", p)
				}
			}
		})
	}
}

// TestGlobEscape: a path becomes a picomatch pattern that matches it alone.
// Checked against CLI 2.1.302 with a parent folder named after each case:
// its CLAUDE.md files and rules stayed out with the escaped pattern, and
// without it "{x,y}" and "@(y)" let the rules through. The characters in
// the last case it matched unescaped.
func TestGlobEscape(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/Users/you/brains", "/Users/you/brains"},
		{"/Users/you/notes (old)", "/Users/you/notes [(]old[)]"},
		{"/x/[a]{b,c}", "/x/[[]a[]][{]b,c[}]"},
		{"/x/b]x/b[x", "/x/b[]]x/b[[]x"},
		{"/x/*?!+@|", "/x/[*][?][!][+][@][|]"},
		{"/x/!(y)+(y)@(y)", "/x/[!][(]y[)][+][(]y[)][@][(]y[)]"},
		{"/x/^$.#%&~,=;'\"` é", "/x/^$.#%&~,=;'\"` é"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := globEscape(tc.path); got != tc.want {
				t.Errorf("globEscape(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestBrainRunsWithItsOwnClaudeSetup: a brain's run gets the flags that
// keep your Claude Code setup out, the instructions above its folder
// excluded, and its folder's .mcp.json only when the CLI would start with
// it (see TestMCPConfigOf). inherit_claude_user_config drops them all
// again.
func TestBrainRunsWithItsOwnClaudeSetup(t *testing.T) {
	write := func(body string) func(t *testing.T, p string) {
		return func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	isolated := "--setting-sources project,local --strict-mcp-config"
	cases := []struct {
		name, extra    string
		mcp            func(t *testing.T, p string) // lays out the brain's .mcp.json at p
		want, dontWant []string
	}{
		{"no .mcp.json", "", nil, []string{isolated + " --settings <settings>"}, []string{"--mcp-config"}},
		{"a .mcp.json", "", write(`{"mcpServers": {}}`), []string{isolated + " --settings <settings> --mcp-config <mcp>"}, nil},
		{"a .mcp.json that is not JSON", "", write(`{"mcpServers": `), []string{isolated}, []string{"--mcp-config"}},
		{"a symlinked .mcp.json", "", func(t *testing.T, p string) {
			target := filepath.Join(t.TempDir(), "servers.json")
			write(`{"mcpServers": {}}`)(t, target)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}, []string{isolated}, []string{"--mcp-config"}},
		{"a .mcp.json folder", "", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{isolated}, []string{"--mcp-config"}},
		{"a .mcp.json with a server turned off", "", func(t *testing.T, p string) {
			write(`{"mcpServers": {"keep": {"command": "k"}, "off": {"command": "o"}}}`)(t, p)
			if err := os.Mkdir(filepath.Join(filepath.Dir(p), ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(`{"disabledMcpjsonServers": ["off"]}`)(t, filepath.Join(filepath.Dir(p), ".claude", "settings.local.json"))
		}, []string{isolated + ` --settings <settings> --mcp-config {"mcpServers":{"keep":{"command":"k"}}}`}, []string{"<mcp>"}},
		{"inherit_claude_user_config", `, "inherit_claude_user_config": true`, write(`{"mcpServers": {}}`),
			nil, []string{"--setting-sources", "--strict-mcp-config", "--settings", "--mcp-config"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 0, tc.extra)
			argsLog := filepath.Join(home, "args.log")
			t.Setenv("WCB_FAKE_ARGS_LOG", argsLog)
			mcp := filepath.Join(home, "a", ".mcp.json")
			if tc.mcp != nil {
				tc.mcp(t, mcp)
			}
			if _, _, _, err := mgr.Submit(dm("hello")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the run", func() bool { return d.count() == 1 })
			if err := d.all()[0].res.Err; err != nil {
				t.Fatalf("run: %v", err)
			}
			data, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			fill := strings.NewReplacer("<mcp>", mcp, "<settings>", excludeInstructionsAbove(filepath.Join(home, "a"))).Replace
			for _, w := range tc.want {
				if w = fill(w); !strings.Contains(got, w) {
					t.Errorf("args %q lack %q", got, w)
				}
			}
			for _, w := range tc.dontWant {
				if w = fill(w); strings.Contains(got, w) {
					t.Errorf("args %q have %q", got, w)
				}
			}
		})
	}
}

// TestMCPConfigOf: the brain's .mcp.json is passed only when the CLI starts
// with it as an --mcp-config file, which it does not for anything but a
// JSON object with an "mcpServers" object (checked against CLI 2.1.302),
// and the bridge log says why it left one out. The bridge reads the file
// outside the sandbox, so it follows no symlink and reads no FIFO, folder
// or huge file.
func TestMCPConfigOf(t *testing.T) {
	write := func(body string) func(t *testing.T, p string) {
		return func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name    string
		mcp     func(t *testing.T, p string) // lays out the .mcp.json at p
		pass    bool
		wantLog string // in the log line when it is left out
	}{
		{"no .mcp.json", func(*testing.T, string) {}, false, ""},
		{"no servers", write(`{"mcpServers": {}}`), true, ""},
		// The CLI starts anyway and reports the server as failed.
		{"broken servers", write(`{"mcpServers": {"x": 5, "y": [], "z": {"type": "bogus"}}}`), true, ""},
		{"other keys too", write(`{"mcpServers": {}, "other": 1}`), true, ""},
		{"byte order mark", write("\uFEFF" + `{"mcpServers": {}}`), true, ""},
		{"not JSON", write(`{"mcpServers": `), false, "not a JSON object"},
		{"trailing text", write(`{"mcpServers": {}} x`), false, "not a JSON object"},
		{"empty object", write(`{}`), false, `no "mcpServers" object`},
		{"array", write(`[]`), false, "not a JSON object"},
		{"null", write(`null`), false, "not a JSON object"},
		{"mcpServers an array", write(`{"mcpServers": []}`), false, "mcpServers is not an object"},
		{"mcpServers null", write(`{"mcpServers": null}`), false, `no "mcpServers" object`},
		{"misspelt key", write(`{"mcpServer": {}}`), false, `no "mcpServers" object`},
		{"key in other case", write(`{"mcpservers": {}}`), false, `no "mcpServers" object`},
		// Like the CLI, the last of two equal keys wins.
		{"key given twice", write(`{"mcpServers": {}, "mcpServers": null}`), false, `no "mcpServers" object`},
		{"symlink", func(t *testing.T, p string) {
			target := filepath.Join(t.TempDir(), "servers.json")
			write(`{"mcpServers": {}}`)(t, target)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}, false, "symlink"},
		{"folder", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, false, "not a regular file"},
		// Opening it to read would block until something writes to it.
		{"FIFO", func(t *testing.T, p string) {
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		}, false, "not a regular file"},
		{"huge", write(`{"mcpServers": {}, "pad": "` + strings.Repeat("x", maxBrainFile) + `"}`), false, "larger than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := NewLogRing(10)
			prev := log.Writer()
			log.SetOutput(ring)
			t.Cleanup(func() { log.SetOutput(prev) })

			dir := t.TempDir()
			p := filepath.Join(dir, ".mcp.json")
			tc.mcp(t, p)
			done := make(chan string, 1)
			go func() { done <- mcpConfigOf("a", dir) }()
			var got string
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("mcpConfigOf hangs")
			}
			if want := map[bool]string{true: p}[tc.pass]; got != want {
				t.Errorf("mcpConfigOf = %q, want %q", got, want)
			}
			lines, _ := ring.Since(0, 10)
			var logged []string
			for _, l := range lines {
				logged = append(logged, l.Text)
			}
			if tc.wantLog == "" {
				if len(logged) > 0 {
					t.Errorf("unexpected log %q", logged)
				}
				return
			}
			// The test's name is in p, so only the reason after it counts.
			_, reason, ok := strings.Cut(strings.Join(logged, "\n"), "brain a: leaving out "+p+": ")
			if len(logged) != 1 || !ok || !strings.Contains(reason, tc.wantLog) {
				t.Errorf("log = %q, want one line leaving out %s for %q", logged, p, tc.wantLog)
			}
		})
	}
}

// TestMCPConfigOfDropsServersTurnedOff: a server the brain folder's
// settings turn off with disabledMcpjsonServers stays off, as it did for a
// project .mcp.json, although --mcp-config would start it: the bridge
// passes the rest as JSON, or nothing when no server is left. A settings
// file it cannot read leaves out every server, since nothing can tell
// which ones it turns off. enabledMcpjsonServers changes nothing.
func TestMCPConfigOfDropsServersTurnedOff(t *testing.T) {
	const servers = `{"mcpServers": {"keep": {"command": "k", "args": ["<&>"]}, "off": {"command": "o"}}, "other": 1}`
	cases := []struct {
		name           string
		project, local string   // the .claude/settings.json and settings.local.json; "" for none
		want           []string // the servers passed as JSON; nil for the file itself
		leftOut        string   // in the log line when the file is left out
	}{
		{"no settings", "", "", nil, ""},
		{"nothing turned off", `{"permissions": {}}`, "", nil, ""},
		{"an unknown server turned off", `{"disabledMcpjsonServers": ["gone"]}`, "", nil, ""},
		{"turned off in settings.json", `{"disabledMcpjsonServers": ["off"]}`, "", []string{"keep"}, ""},
		{"turned off in settings.local.json", "", `{"disabledMcpjsonServers": ["off"]}`, []string{"keep"}, ""},
		{"enabled elsewhere", `{"disabledMcpjsonServers": ["off"]}`, `{"enabledMcpjsonServers": ["off"]}`, []string{"keep"}, ""},
		{"only enabledMcpjsonServers", `{"enabledMcpjsonServers": ["keep"]}`, "", nil, ""},
		{"all turned off", `{"disabledMcpjsonServers": ["off"]}`, `{"disabledMcpjsonServers": ["keep"]}`, []string{}, ""},
		{"list is null", `{"disabledMcpjsonServers": null}`, "", nil, ""},
		{"settings not JSON", `{"disabledMcpjsonServers": ["off"],}`, "", []string{}, "cannot tell which servers"},
		{"list not a list", `{"disabledMcpjsonServers": "off"}`, "", []string{}, "cannot tell which servers"},
		{"settings not an object", "", `[]`, []string{}, "cannot tell which servers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := NewLogRing(10)
			prev := log.Writer()
			log.SetOutput(ring)
			t.Cleanup(func() { log.SetOutput(prev) })

			dir := t.TempDir()
			p := filepath.Join(dir, ".mcp.json")
			for path, body := range map[string]string{
				p: servers,
				filepath.Join(dir, ".claude", "settings.json"):       tc.project,
				filepath.Join(dir, ".claude", "settings.local.json"): tc.local,
			} {
				if body == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := mcpConfigOf("a", dir)
			lines, _ := ring.Since(0, 10)
			switch {
			case tc.want == nil:
				if got != p {
					t.Errorf("mcpConfigOf = %q, want the file %q", got, p)
				}
			case len(tc.want) == 0:
				if got != "" {
					t.Errorf("mcpConfigOf = %q, want nothing", got)
				}
			default:
				var cfg struct {
					MCPServers map[string]json.RawMessage `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(got), &cfg); err != nil {
					t.Fatalf("mcpConfigOf = %q, want JSON: %v", got, err)
				}
				if names := slices.Sorted(maps.Keys(cfg.MCPServers)); !slices.Equal(names, tc.want) {
					t.Errorf("servers passed = %q, want %q", names, tc.want)
				}
				// A kept server is passed as it was written.
				var kept, orig any
				_ = json.Unmarshal(cfg.MCPServers["keep"], &kept)
				_ = json.Unmarshal([]byte(`{"command": "k", "args": ["<&>"]}`), &orig)
				if !reflect.DeepEqual(kept, orig) {
					t.Errorf("keep = %s, want it unchanged", cfg.MCPServers["keep"])
				}
			}
			var logged []string
			for _, l := range lines {
				logged = append(logged, l.Text)
			}
			if tc.leftOut == "" && len(logged) > 0 {
				t.Errorf("unexpected log %q", logged)
			}
			if tc.leftOut != "" && (len(logged) != 1 || !strings.Contains(logged[0], "leaving out "+p+": "+tc.leftOut)) {
				t.Errorf("log = %q, want one line leaving out %s for %q", logged, p, tc.leftOut)
			}
		})
	}
}

func TestBuildPromptListsAllAttachments(t *testing.T) {
	br := &Brain{id: "a", workDir: t.TempDir()}
	br.historyFile = filepath.Join(br.workDir, "h.jsonl")
	p := br.buildPrompt(&brainSettings{}, Inbound{JID: "x@s.whatsapp.net", Sender: "1", Text: "file these", Media: []string{"/m/1.jpg", "/m/2.pdf"}})
	if !strings.Contains(p, "2 files were shared together") || !strings.Contains(p, "- /m/2.pdf") {
		t.Errorf("prompt:\n%s", p)
	}
	if got := historyTextWithMedia("hi", []string{"/m/1.jpg"}); got != "hi\n[attachment: /m/1.jpg]" {
		t.Errorf("history text = %q", got)
	}
}

func TestOTPMarkersConfigurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"out","from":"a","text":"Please send the Examplebank code"}`+"\n"), 0o600)
	if recentOutboundLooksLikeOTPRequest(path, "a") {
		t.Error("default markers should not match a bank name")
	}
	if !recentOutboundLooksLikeOTPRequest(path, "a", "Examplebank") {
		t.Error("a configured marker should arm the guard")
	}
}

func TestParseStreamReportsErrorText(t *testing.T) {
	br := &Brain{id: "a", workDir: t.TempDir()}
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"Failed to authenticate. API Error: 401"}]}}
{"type":"result","is_error":true}
`
	res := br.parseStream("x", strings.NewReader(stream))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "Failed to authenticate") {
		t.Errorf("err = %v, want the CLI's own message", res.Err)
	}
}

func TestStopKillsProcessesClaudeStarted(t *testing.T) {
	// A command Claude started keeps the output pipe open; stopping must kill
	// the whole process group, not just the CLI, or the stop hangs.
	mgr, d, _ := queueTestManager(t, 30000, "")
	t.Setenv("WCB_FAKE_CHILD", "1")
	_, _, _, _ = mgr.Submit(dm("long task with a child"))
	br := mgr.Get("a")
	waitFor(t, 5*time.Second, "run with a pid", func() bool {
		rt := br.Snapshot()
		return rt.Running != nil && rt.Running.PID > 0
	})
	time.Sleep(200 * time.Millisecond) // let the child start
	start := time.Now()
	br.StopCurrent(false)
	waitFor(t, 4*time.Second, "interrupted delivery", func() bool { return d.count() == 1 })
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("stop took %v — the child kept the run alive", el)
	}
}
