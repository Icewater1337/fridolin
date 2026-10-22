package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// workNode is an approved node offering agent and terminal in root,
// without a sandbox, with the fake agent as its claude.
func workNode(t *testing.T, root string) (*core, *Agent) {
	t.Helper()
	t.Setenv("FAKE_AGENT", "claude")
	c := startCore(t, t.TempDir())
	w := &nodeproto.WorkstationCap{Roots: []string{root}}
	a := joinNode(t, c, nodeproto.Capabilities{Agent: w, Terminal: w})
	a.WorkSandbox = nodeproto.SandboxNone
	a.AgentPaths = map[string]string{nodeproto.ProviderClaude: testExe(t)}
	runAgent(t, a)
	approveAll(t, c, a)
	return c, a
}

func waitSession(t *testing.T, c *core, a *Agent, id string, want ...string) nodeproto.AgentSession {
	t.Helper()
	var s nodeproto.AgentSession
	waitFor(t, "agent session "+id+" to be "+strings.Join(want, " or "), func() bool {
		var err error
		s, err = c.svc.AgentStatus(context.Background(), a.ID(), nodeproto.AgentRef{ID: id})
		return err == nil && slices.Contains(want, s.State)
	})
	return s
}

func TestAgentSessionAsksAndFinishes(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "agent.log")
	t.Setenv("FAKE_AGENT_LOG", log)
	c, a := workNode(t, root)
	n, err := c.svc.Registry().Get(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if ag := n.Effective().Agent; ag == nil || ag.Sandbox != nodeproto.SandboxNone || !slices.Contains(ag.Providers, nodeproto.ProviderClaude) {
		t.Fatalf("agent offered: %+v", ag)
	}
	ctx := context.Background()
	s, err := c.svc.AgentStart(ctx, a.ID(), nodeproto.AgentStartParams{Folder: root, Prompt: "pick a colour", CoreToken: "core-token-for-this-session"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.CoreLogin {
		t.Error("the session does not say it runs with the core's login")
	}
	s = waitSession(t, c, a, s.ID, nodeproto.AgentWaiting)
	if s.Ask != nodeproto.AskQuestion || s.Question != "Which colour?" {
		t.Fatalf("waiting with %q: %q", s.Ask, s.Question)
	}
	kinds := []string{}
	for _, e := range s.Events {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Contains(kinds, "tool") || !slices.Contains(kinds, "question") {
		t.Errorf("events %v", kinds)
	}
	if _, err := c.svc.AgentReply(ctx, a.ID(), nodeproto.AgentReplyParams{ID: s.ID, Text: "blue"}); err != nil {
		t.Fatal(err)
	}
	s = waitSession(t, c, a, s.ID, nodeproto.AgentDone)
	if got, _ := os.ReadFile(filepath.Join(root, "colour.txt")); string(got) != "blue" {
		t.Fatalf("colour.txt holds %q", got)
	}
	if s.Summary != "Wrote blue to colour.txt." || !slices.Contains(s.Changed, "A colour.txt") || s.Turns != 2 {
		t.Fatalf("done: %+v", s)
	}
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "--resume fake-1") || !strings.Contains(lines[0], "--permission-mode acceptEdits") || !strings.HasSuffix(lines[0], "token=true") {
		t.Fatalf("the turns ran as:\n%s", data)
	}
	if _, err := c.svc.AgentReply(ctx, a.ID(), nodeproto.AgentReplyParams{ID: s.ID, Text: "again"}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("a reply to a finished session: %v", err)
	}
}

func TestAgentPermissionIsAskedAsIrreversible(t *testing.T) {
	root := t.TempDir()
	c, a := workNode(t, root)
	ctx := context.Background()
	s, err := c.svc.AgentStart(ctx, a.ID(), nodeproto.AgentStartParams{Folder: root, Prompt: "commit and PUSHIT"})
	if err != nil {
		t.Fatal(err)
	}
	if s.CoreLogin {
		t.Error("a session without use_core_login runs with the core's login")
	}
	s = waitSession(t, c, a, s.ID, nodeproto.AgentWaiting)
	if s.Ask != nodeproto.AskIrreversible || !strings.Contains(s.Question, "Bash(git push origin main)") {
		t.Fatalf("waiting with %q: %q", s.Ask, s.Question)
	}
	if _, err := c.svc.AgentReply(ctx, a.ID(), nodeproto.AgentReplyParams{ID: s.ID, Allow: true}); err != nil {
		t.Fatal(err)
	}
	s = waitSession(t, c, a, s.ID, nodeproto.AgentDone)
	if _, err := os.Stat(filepath.Join(root, "pushed.txt")); err != nil {
		t.Fatalf("allowed, it did not go on: %+v", s)
	}
}

func TestAgentStopAndRevocationEndSessions(t *testing.T) {
	root := t.TempDir()
	c, a := workNode(t, root)
	ctx := context.Background()
	s, err := c.svc.AgentStart(ctx, a.ID(), nodeproto.AgentStartParams{Folder: root, Prompt: "pick a colour"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, c, a, s.ID, nodeproto.AgentWaiting)
	if s, err = c.svc.AgentStop(ctx, a.ID(), s.ID); err != nil || s.State != nodeproto.AgentStopped {
		t.Fatalf("stop: %+v, %v", s, err)
	}
	s2, err := c.svc.AgentStart(ctx, a.ID(), nodeproto.AgentStartParams{Folder: root, Prompt: "pick a colour"})
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, c, a, s2.ID, nodeproto.AgentWaiting)
	if _, err := c.svc.Revoke(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the revocation to stop the session", func() bool {
		a.work.mu.Lock()
		sess := a.work.agents[s2.ID]
		a.work.mu.Unlock()
		return sess.snapshot(0).State == nodeproto.AgentStopped
	})
}

func TestWorkFolderConfinement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{Dir: filepath.Join(base, "state")}
	eff := nodeproto.Capabilities{Agent: &nodeproto.WorkstationCap{Roots: []string{root}}}
	if _, perr := a.confinedWorkFolder(eff, "agent", filepath.Join(root, "sub")); perr != nil {
		t.Fatalf("a folder in the root: %v", perr)
	}
	if _, perr := a.confinedWorkFolder(eff, "terminal", root); perr == nil || perr.Code != nodeproto.CodeDenied {
		t.Fatalf("terminal, which is not approved: %v", perr)
	}
	for _, p := range []string{outside, root + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside", "relative", base} {
		if _, perr := a.confinedWorkFolder(eff, "agent", p); perr == nil {
			t.Errorf("%s was let through", p)
		}
	}
	link := filepath.Join(root, "link")
	symlinkOrSkip(t, outside, link)
	if _, perr := a.confinedWorkFolder(eff, "agent", link); perr == nil || perr.Code != nodeproto.CodeOutsideRoot {
		t.Fatalf("a symlink out of the root: %v", perr)
	}
	// A root that is itself a symlink to elsewhere is the folder it names.
	eff.Agent.Roots = []string{link}
	if _, perr := a.confinedWorkFolder(eff, "agent", link); perr != nil {
		t.Fatalf("a root that is a symlink: %v", perr)
	}
}

func TestCheckWorkRoot(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })
	projects := filepath.Join(home, "Projects")
	ssh := filepath.Join(home, ".ssh", "keys")
	secrets := filepath.Join(home, ".my-secrets")
	for _, d := range []string{projects, ssh, secrets} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(home, "state")
	if err := CheckWorkRoot(projects, false, state); err != nil {
		t.Fatalf("a folder in the home folder: %v", err)
	}
	for _, c := range []struct {
		root      string
		allowHome bool
	}{
		{home, false}, {filepath.Dir(home), true}, {ssh, true}, {secrets, false}, {state, false}, {filepath.Join(state, "x"), false}, {"relative", false},
	} {
		if err := CheckWorkRoot(c.root, c.allowHome, state); err == nil {
			t.Errorf("CheckWorkRoot(%s, %v) let it through", c.root, c.allowHome)
		}
	}
	if err := CheckWorkRoot(home, true, ""); err != nil {
		t.Errorf("the home folder with allow_home: %v", err)
	}
	vol := "/"
	if runtime.GOOS == "windows" {
		vol = `C:\`
	}
	if err := CheckWorkRoot(vol, true, ""); err == nil {
		t.Error("a whole drive was let through")
	}
}

func TestWorkstationGrowthWaitsForApproval(t *testing.T) {
	root, more := t.TempDir(), t.TempDir()
	c, a := workNode(t, root)
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	// The owner adds a folder: the new one waits, the old one serves.
	decl := a.declared()
	decl.Terminal = &nodeproto.WorkstationCap{Roots: []string{root, more}, Sandbox: nodeproto.SandboxNone}
	n, err := c.svc.Registry().Declare(a.ID(), decl)
	if err != nil {
		t.Fatal(err)
	}
	if n.Commands() != nodeproto.CommandsPending {
		t.Fatalf("a grown terminal is %s", n.Commands())
	}
	eff := n.Effective()
	if !eff.Terminal.AllowsFolder(root) || eff.Terminal.AllowsFolder(more) {
		t.Fatalf("while it waits: %+v", eff.Terminal)
	}
	// So does allow_home.
	approved := nodeproto.Capabilities{Agent: &nodeproto.WorkstationCap{Roots: []string{root}}}
	if nodeproto.Covers(approved, nodeproto.Capabilities{Agent: &nodeproto.WorkstationCap{Roots: []string{root}, AllowHome: true}}) {
		t.Error("allow_home was covered without being approved")
	}
}

func TestTerminalEcho(t *testing.T) {
	root := t.TempDir()
	c, a := workNode(t, root)
	if runtime.GOOS == "windows" {
		a.TerminalShell = []string{os.Getenv("COMSPEC")}
		if a.TerminalShell[0] == "" {
			a.TerminalShell = []string{`C:\Windows\System32\cmd.exe`}
		}
	} else {
		a.TerminalShell = []string{"/bin/sh"}
	}
	ctx := context.Background()
	term, err := c.svc.TerminalOpen(ctx, a.ID(), nodeproto.TerminalOpenParams{Folder: root, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	line, want := "echo fridolin-$((40+2))\n", "fridolin-42"
	if runtime.GOOS == "windows" {
		line, want = "echo fridolin-%OS%\r\n", "fridolin-Windows_NT"
	}
	if _, err := c.svc.TerminalSend(ctx, a.ID(), nodeproto.TerminalSendParams{ID: term.ID, Data: line}); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	var off int64
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(got.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in the terminal's output:\n%q", want, got.String())
		}
		out, err := c.svc.TerminalRead(ctx, a.ID(), nodeproto.TerminalReadParams{ID: term.ID, Offset: off, WaitMS: 2000})
		if err != nil {
			t.Fatal(err)
		}
		got.Write(out.Data)
		off = out.Next
	}
	if _, err := c.svc.TerminalSend(ctx, a.ID(), nodeproto.TerminalSendParams{ID: term.ID, Data: "exit\r\n"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the shell to exit", func() bool {
		out, err := c.svc.TerminalRead(ctx, a.ID(), nodeproto.TerminalReadParams{ID: term.ID, Offset: off, WaitMS: 500})
		if err == nil {
			off = out.Next
		}
		return err == nil && out.Closed
	})
	list, err := c.svc.TerminalList(ctx, a.ID())
	if err != nil || len(list.Terminals) != 1 || !list.Terminals[0].Closed {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if _, err := c.svc.TerminalOpen(ctx, a.ID(), nodeproto.TerminalOpenParams{Folder: t.TempDir()}); !isCode(err, nodeproto.CodeOutsideRoot) {
		t.Fatalf("a terminal outside the roots: %v", err)
	}
}

func TestTerminalIdleTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by TestTerminalEcho there")
	}
	root := t.TempDir()
	c, a := workNode(t, root)
	a.TerminalShell = []string{"/bin/sh"}
	a.TerminalIdle = 300 * time.Millisecond
	term, err := c.svc.TerminalOpen(context.Background(), a.ID(), nodeproto.TerminalOpenParams{Folder: root})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the idle terminal to close", func() bool {
		tm, perr := a.terminalByID(term.ID)
		return perr == nil && tm.isClosed()
	})
}

func TestTerminalOutputBufferDrops(t *testing.T) {
	tm := &terminal{id: "x", wake: make(chan struct{})}
	tm.buf = []byte(strings.Repeat("a", 10))
	tm.total = 100
	out := tm.outputLocked(5)
	if !out.Dropped || out.Offset != 90 || out.Next != 100 || len(out.Data) != 10 {
		t.Fatalf("%+v", out)
	}
	if out := tm.outputLocked(200); out.Offset != 100 || len(out.Data) != 0 {
		t.Fatalf("past the end: %+v", out)
	}
}

func TestLastMarker(t *testing.T) {
	for text, want := range map[string]string{
		"Done.\nQUESTION: Which one?":           nodeproto.AskQuestion,
		"**IRREVERSIBLE: git push origin**":     nodeproto.AskIrreversible,
		"I changed it. No question for you.":    "",
		"QUESTION:\n":                           "",
		"question: lower case works too, right": nodeproto.AskQuestion,
	} {
		if got, _ := lastMarker(text); got != want {
			t.Errorf("lastMarker(%q) = %q, want %q", text, got, want)
		}
	}
}

// A sandbox that cannot start is refused, never skipped.
func TestWrapFailsClosed(t *testing.T) {
	plan := newSandboxPlan(t.TempDir(), t.TempDir(), "", "")
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "linux" {
		if _, err := plan.wrap(nodeproto.SandboxBwrap, []string{"/bin/true"}); err == nil && !bwrapInToolDirs() {
			t.Error("bwrap is not there, and wrap ran without it")
		}
	}
	if _, err := plan.wrap("chroot", []string{"/bin/true"}); err == nil {
		t.Error("an unknown sandbox was accepted")
	}
	if argv, err := plan.wrap(nodeproto.SandboxNone, []string{"/bin/true"}); err != nil || len(argv) != 1 {
		t.Errorf("none: %v %v", argv, err)
	}
}

// bwrapInToolDirs reports whether LookTool finds bwrap outside the PATH.
func bwrapInToolDirs() bool {
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		if _, err := os.Stat(filepath.Join(d, "bwrap")); err == nil {
			return true
		}
	}
	return false
}

func TestSandboxKeepsWritesInTheFolder(t *testing.T) {
	sb := detectSandbox()
	if sb == nodeproto.SandboxNone {
		t.Skip("no sandbox on this machine")
	}
	base := t.TempDir()
	folder, outside, tmp := filepath.Join(base, "folder"), filepath.Join(base, "outside"), filepath.Join(base, "tmp")
	for _, d := range []string{folder, outside, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plan := newSandboxPlan(folder, tmp, "", "")
	run := func(file string) error {
		argv, err := plan.wrap(sb, []string{"/bin/sh", "-c", "echo x > \"$1\"", "sh", file})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = folder
		return cmd.Run()
	}
	if err := run(filepath.Join(folder, "in.txt")); err != nil {
		t.Fatalf("writing in the folder: %v", err)
	}
	if err := run(filepath.Join(outside, "out.txt")); err == nil {
		t.Fatal("the sandbox let a write outside the folder through")
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Fatal("out.txt was written")
	}
	if sb == nodeproto.SandboxExec {
		// Claude Code's Bash tool keeps its folder in /tmp/claude-<hex>-cwd.
		cwd := fmt.Sprintf("/tmp/claude-%x-cwd", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(cwd) })
		if err := run(cwd); err != nil {
			t.Errorf("writing Claude Code's cwd file: %v", err)
		}
		other := fmt.Sprintf("/tmp/claude-%x-other", time.Now().UnixNano())
		if err := run(other); err == nil {
			_ = os.Remove(other)
			t.Error("the sandbox let another write into /tmp through")
		}
	}
}
