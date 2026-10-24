package nodeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func hasCoreRule(c nodeproto.Capabilities, name string) bool {
	return c.Exec != nil && slices.ContainsFunc(c.Exec.Allow, func(r nodeproto.ExecRule) bool { return r.Core && r.Name == name })
}

func TestProgramsFromTheCore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test programs are shell scripts")
	}
	c := startCore(t, t.TempDir())
	accepted, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	ok := filepath.Join(accepted, "ok.sh")
	writeScript(t, ok, `echo "ok $*"`)
	escape := filepath.Join(outside, "escape.sh")
	writeScript(t, escape, `echo escaped`)
	link := filepath.Join(accepted, "link.sh")
	if err := os.Symlink(escape, link); err != nil {
		t.Fatal(err)
	}
	shLink := filepath.Join(accepted, "run")
	if err := os.Symlink("/bin/sh", shLink); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash", "python3", "node"} {
		writeScript(t, filepath.Join(accepted, name), "true")
	}
	plain := filepath.Join(accepted, "notes.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := joinNode(t, c, nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{}, AcceptFromCore: []string{accepted}}})
	runAgent(t, a)
	waitFor(t, "the agent to connect", func() bool { return c.svc.Online(a.ID()) })
	ctx := context.Background()
	// Not before the owner approved that the node accepts programs.
	if _, err := c.svc.SetPrograms(ctx, a.ID(), []nodeproto.ExecProgram{{Name: ok}}); err == nil {
		t.Fatal("a list reached a node whose device is pending")
	}
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "approved commands", func() bool { return a.State().Commands == nodeproto.CommandsApproved })

	progs := []nodeproto.ExecProgram{
		{Name: ok, Args: []string{"daily"}, Agents: []string{"personal/daily"}},
		{Name: escape},
		{Name: link},
		{Name: shLink, Args: []string{"-c", "id"}},
		{Name: filepath.Join(accepted, "bash")},
		{Name: filepath.Join(accepted, "python3")},
		{Name: filepath.Join(accepted, "node")},
		{Name: plain},
		{Name: filepath.Join(accepted, "missing.sh")},
		{Name: filepath.Join(accepted, "..", filepath.Base(outside), "escape.sh")},
		{Name: "/bin/sh", Args: []string{"-c", "id"}},
	}
	st, err := c.svc.SetPrograms(ctx, a.ID(), progs)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Accepted) != 1 || st.Accepted[0].Name != ok {
		t.Fatalf("accepted %+v, want only %s; refused %+v", st.Accepted, ok, st.Refused)
	}
	if len(st.Refused) != len(progs)-1 {
		t.Fatalf("refused %d, want %d: %+v", len(st.Refused), len(progs)-1, st.Refused)
	}
	for _, r := range st.Refused {
		if r.Why == "" {
			t.Errorf("%s refused without a reason", r.Name)
		}
	}
	waitFor(t, "the program declared and approved", func() bool {
		return hasCoreRule(a.State().Approved, ok) && a.State().Commands == nodeproto.CommandsApproved
	})
	n, _ := c.svc.Registry().Get(a.ID())
	if hasCoreRule(n.Approved, escape) || hasCoreRule(n.Effective(), link) {
		t.Fatalf("the core approved what the node refused: %+v", n.Approved.Exec)
	}

	// Exactly the arguments approved.
	res, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{ok, "daily"}})
	if err != nil || strings.TrimSpace(res.Stdout) != "ok daily" {
		t.Fatalf("the approved program: %+v, %v", res, err)
	}
	for _, argv := range [][]string{{ok}, {ok, "daily", "more"}, {ok, "weekly"}, {escape}, {link}} {
		if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: argv}); !isCode(err, nodeproto.CodeDenied) {
			t.Errorf("exec %v: %v, want denied", argv, err)
		}
	}
	// Kept across a restart of the agent.
	data, err := os.ReadFile(filepath.Join(a.Dir, ExecProgramsFile))
	if err != nil || !strings.Contains(string(data), "personal/daily") {
		t.Fatalf("%s: %s, %v", ExecProgramsFile, data, err)
	}

	// The program turns into a symlink out of the folder: the node checks
	// again before it runs it.
	if err := os.Remove(ok); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, ok); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{ok, "daily"}}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("a program that became a symlink out: %v", err)
	}
	if err := os.Remove(ok); err != nil {
		t.Fatal(err)
	}
	writeScript(t, ok, `echo "ok $*"`)

	// The agent deleted: the list without it removes the program.
	if _, err := c.svc.SetPrograms(ctx, a.ID(), nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the program removed", func() bool { return !hasCoreRule(a.declared(), ok) && !hasCoreRule(a.State().Approved, ok) })
	if n, _ := c.svc.Registry().Get(a.ID()); hasCoreRule(n.Approved, ok) {
		t.Fatal("the core still approves the removed program")
	}
	if _, err := c.svc.Exec(ctx, a.ID(), nodeproto.ExecParams{Argv: []string{ok, "daily"}}); !isCode(err, nodeproto.CodeDenied) {
		t.Fatalf("the removed program: %v", err)
	}
}

func TestProgramsListMustBeSignedAndNew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test programs are shell scripts")
	}
	accepted, _ := filepath.EvalSymlinks(t.TempDir())
	ok := filepath.Join(accepted, "ok.sh")
	writeScript(t, ok, "true")
	pub, core, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	if err := writeCoreKey(dir, pub); err != nil {
		t.Fatal(err)
	}
	key, err := NewKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Dir: dir, Key: key, Config: Config{Core: "x", Name: "n", Capabilities: nodeproto.Capabilities{
		Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{}, AcceptFromCore: []string{accepted}},
	}}, Logf: t.Logf}
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Commands: nodeproto.CommandsApproved, Approved: a.declared()})
	list := []nodeproto.ExecProgram{{Name: ok, Args: []string{}}}
	call := func(signer ed25519.PrivateKey, node string, seq int64) *nodeproto.Error {
		p := nodeproto.ExecProgramsParams{Node: node, Seq: seq, Programs: list, Signature: nodeproto.SignExecPrograms(signer, node, seq, list)}
		raw, _ := json.Marshal(p)
		return a.handle(context.Background(), nodeproto.Request{Method: nodeproto.MethodExecPrograms, Params: raw}).Error
	}
	if e := call(other, a.ID(), 10); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("a list signed with another key: %v", e)
	}
	if e := call(core, "someone-else", 10); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("a list for another node: %v", e)
	}
	if e := call(core, a.ID(), 10); e != nil {
		t.Fatalf("the core's list: %v", e)
	}
	if !hasCoreRule(a.declared(), ok) {
		t.Fatal("the program is not declared")
	}
	if e := call(core, a.ID(), 10); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("the same list again: %v", e)
	}
	if e := call(core, a.ID(), 9); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("an older list: %v", e)
	}
	// A list that is tampered with on the way.
	p := nodeproto.ExecProgramsParams{Node: a.ID(), Seq: 11, Programs: list, Signature: nodeproto.SignExecPrograms(core, a.ID(), 11, list)}
	p.Programs = []nodeproto.ExecProgram{{Name: ok, Args: []string{"other"}}}
	raw, _ := json.Marshal(p)
	if e := a.handle(context.Background(), nodeproto.Request{Method: nodeproto.MethodExecPrograms, Params: raw}).Error; e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("a tampered list: %v", e)
	}

	// Taken again by an agent that starts anew, and dropped once node.json
	// no longer accepts the folder.
	b := &Agent{Dir: dir, Key: key, Config: a.Config, Logf: t.Logf}
	if !hasCoreRule(b.declared(), ok) {
		t.Fatal("a new agent lost the program")
	}
	cfg := a.Config
	cfg.Capabilities.Exec = &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{}, AcceptFromCore: []string{t.TempDir()}}
	c := &Agent{Dir: dir, Key: key, Config: cfg, Logf: t.Logf}
	if hasCoreRule(c.declared(), ok) {
		t.Fatal("a program outside the folders node.json accepts now is still declared")
	}
}

func TestAcceptFromCoreFolders(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })
	good := filepath.Join(home, "projects")
	cfg := func(folders ...string) Config {
		return Config{Core: "x:1", Name: "n", Capabilities: nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{}, AcceptFromCore: folders}}}
	}
	if err := cfg(good).Check(); err != nil {
		t.Fatal(err)
	}
	bad := []string{"/", home, "/usr/bin", "/bin", filepath.Join(home, ".ssh"), "relative/dir"}
	if runtime.GOOS == "windows" {
		bad = []string{`C:\`, home}
	}
	for _, f := range bad {
		if err := cfg(f).Check(); err == nil {
			t.Errorf("accept_from_core %s passed", f)
		}
	}
	c := cfg(good)
	c.Capabilities.Exec.Allow = []nodeproto.ExecRule{{Name: filepath.Join(good, "x"), Args: []string{}, Exact: true, Core: true}}
	if err := c.Check(); err == nil {
		t.Error("node.json may not mark a rule as the core's")
	}
}
