package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// execNode joins a node named name whose exec allows rules, runs it,
// approves it, and returns its id.
func (h *harness) execNode(name, addr string, rules []nodeproto.ExecRule) string {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	dir := t.TempDir()
	caps := nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: rules}, Host: true}
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: addr, Code: jc.Code, Name: name, Capabilities: caps, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery = t.Logf, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(name+" offering exec", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && agent.Connected() && h.b.nodes.Online(j.ID) && n.Declared.Exec != nil
	})
	return j.ID
}

// coreSaysWhile has the core brain play steps for a message in your DM,
// runs during once the bridge took it, and returns the core brain's
// answer, as coreSays does.
func (h *harness) coreSaysWhile(text string, during func(), steps ...testutil.FakeMCPStep) string {
	h.t.Helper()
	data, _ := json.Marshal(steps)
	setenv(h.t, "FRIDOLIN_FAKE_MCP", string(data))
	before := len(h.SentTo(harnessDM))
	h.Inject(inbound{JID: harnessDM, Text: text + " " + testutil.FakeMCPTrigger})
	during()
	var got string
	testutil.WaitFor(h.t, 60*time.Second, "the core brain's answer to "+text, func() bool {
		for _, m := range h.SentTo(harnessDM)[before:] {
			if strings.Contains(m, "[core:") {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

// decidePending waits for the one pending approval whose action has
// substr, decides it on the dashboard, and returns its id.
func (h *harness) decidePending(substr string, approve bool) string {
	h.t.Helper()
	var id string
	h.WaitFor("an approval of "+substr, func() bool {
		pending, _ := h.approvals()
		for _, a := range pending {
			if strings.Contains(a["action"].(string), substr) {
				id = a["id"].(string)
				return true
			}
		}
		return false
	})
	decision := "deny"
	if approve {
		decision = "approve"
	}
	if w := h.Do("POST", "/api/approvals/"+id, map[string]string{"decision": decision}); w.Code != http.StatusOK {
		h.t.Fatalf("deciding %s: %d %s", id, w.Code, w.Body.String())
	}
	return id
}

var approvalIDIn = regexp.MustCompile(`approval_id (a[0-9A-Za-z_-]+)`)

// execArgs is node_exec's arguments for argv, with more fields.
func execArgs(argv []string, more string) string {
	data, _ := json.Marshal(argv)
	if more != "" {
		more = "," + more
	}
	return `{"argv":` + string(data) + more + `}`
}

// The core brain runs commands on a node through node_exec (VISION step
// 4): the tools appear only on the core brain and only once a node's exec
// is approved; node_exec_rules says what runs and what asks; a free
// command runs at once; a shell and what no rule allows are refused; a
// command whose rule asks waits for the owner's yes in a poll, runs on
// yes and not on no; unanswered it answers pending, and the approval_id
// runs it once, for exactly that command; everything is audited, secrets
// masked; a lockdown refuses.
func TestNodeExecThroughNodes(t *testing.T) {
	t.Parallel()
	skipWithoutSh(t)
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8768"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	addr := h.StartNodes()

	// The node's programs: scripts that say what they ran.
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(bin, "ran.log")
	script := func(name, body string) string {
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	status := script("status", `echo "status: all good"`)
	brew := script("brew", `[ "$1" = --version ] || echo "$*" >> "`+ran+`"; echo "brew $*"`)
	say := script("say", `echo "said $#"`)
	id := h.execNode("mac-mini", addr, []nodeproto.ExecRule{
		{Name: status, Args: []string{}},
		{Name: brew, Args: []string{"upgrade", "update"}, Approve: true},
		{Name: brew, Args: []string{"--version"}},
		{Name: say},
	})
	ranLog := func() string { b, _ := os.ReadFile(ran); return string(b) }

	// Not approved yet: no tool.
	if reply := h.coreSays("look", step("node_exec", execArgs([]string{status}, ""))); !strings.Contains(reply, "unknown tool: node_exec") {
		t.Fatalf("node_exec before approval:\n%s", reply)
	}
	h.approveNode(id)
	h.WaitFor("mac-mini running commands", func() bool { return len(h.b.execNodeViews()) == 1 })

	// A sub-brain never has them.
	setenv(t, "FRIDOLIN_FAKE_MCP", `[{"brain":"family"}]`)
	if reply := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude update the mac #mcp"}); !strings.Contains(reply, "[family:list]") || strings.Contains(reply, "node_exec") {
		t.Errorf("family's tools:\n%s", reply)
	}

	// What runs, and what asks.
	reply := h.coreSays("rules", step("node_exec_rules", `{}`))
	for _, want := range []string{"mac-mini (", status + " (no arguments)\n", brew + " upgrade | update  [asks the owner each time]", brew + " --version\n", say + " (any arguments)"} {
		if !strings.Contains(reply, want) {
			t.Errorf("node_exec_rules lacks %q:\n%s", want, reply)
		}
	}

	// A node online that runs no commands is named, so that it does not
	// read as offline, and node_list lists every node as it is now.
	ice := h.fakeHostNode("ice", addr, 65536, 40000, 6144)
	reply = h.coreSays("rules", step("node_exec_rules", `{}`), step("node_list", `{}`))
	for _, want := range []string{"Online, but running no commands for the core: ice", "2 nodes, 2 online now:", "- ice (", "id " + ice.ID + "): online", "FrIDE instances: none yet", "- mac-mini (", "may: run these programs there when asked"} {
		if !strings.Contains(reply, want) {
			t.Errorf("node_exec_rules and node_list lack %q:\n%s", want, reply)
		}
	}

	// A command that does not ask runs at once; so does one a rule
	// without approve allows beside one with it.
	reply = h.coreSays("status", step("node_exec", execArgs([]string{status}, "")), step("node_exec", execArgs([]string{brew, "--version"}, `"node":"MAC-MINI"`)))
	if !strings.Contains(reply, `status: all good`) || !strings.Contains(reply, `brew --version`) || strings.Count(reply, `"exit_code": 0`) != 2 {
		t.Fatalf("free commands:\n%s", reply)
	}
	if pending, history := h.approvals(); len(pending)+len(history) != 0 {
		t.Errorf("a free command asked: %v %v", pending, history)
	}

	// A shell, and what no rule allows, are refused, with what the node
	// allows.
	reply = h.coreSays("evil", step("node_exec", execArgs([]string{"sh", "-c", "id"}, "")), step("node_exec", execArgs([]string{brew, "uninstall", "x"}, "")),
		step("node_exec", execArgs([]string{status}, `"node":"laptop"`)))
	if strings.Count(reply, "mac-mini does not run this") != 2 || !strings.Contains(reply, "sh is a shell") || !strings.Contains(reply, `there is no node "laptop"`) {
		t.Fatalf("refused commands:\n%s", reply)
	}

	// A command that asks: the poll goes to the owner, and yes runs it.
	upgrade := "Run `" + brew + " upgrade` on mac-mini"
	reply = h.coreSaysWhile("upgrade the mac", func() {
		poll := h.waitPoll(harnessDM, upgrade)
		if poll.Text != outboundmsg.ApprovalQuestion(upgrade) {
			t.Errorf("the poll: %q", poll.Text)
		}
		h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	}, step("node_exec", execArgs([]string{brew, "upgrade"}, `"wait_seconds":60`)))
	if !strings.Contains(reply, `"exit_code": 0`) || !strings.Contains(reply, `"stdout": "brew upgrade\n"`) || !strings.Contains(reply, `"approval_id": "a`) {
		t.Fatalf("approved upgrade:\n%s", reply)
	}
	if got := ranLog(); got != "upgrade\n" {
		t.Errorf("ran: %q", got)
	}

	// No: it does not run.
	reply = h.coreSaysWhile("update brew", func() { h.decidePending("update` on mac-mini", false) },
		step("node_exec", execArgs([]string{brew, "update"}, `"wait_seconds":60`)))
	if !strings.Contains(reply, "[core:node_exec] denied: the owner said no") {
		t.Fatalf("denied update:\n%s", reply)
	}
	if got := ranLog(); got != "upgrade\n" {
		t.Errorf("ran after the no: %q", got)
	}

	// Unanswered: pending with the approval_id. The yes then goes to the
	// brain as a message, and the id runs the command, once.
	reply = h.coreSays("upgrade later", step("node_exec", execArgs([]string{brew, "upgrade"}, `"wait_seconds":1`)))
	m := approvalIDIn.FindStringSubmatch(reply)
	if !strings.Contains(reply, "[core:node_exec] pending: no answer yet after 1s") || !strings.Contains(reply, "call node_exec again with the same node, argv and dir") || m == nil {
		t.Fatalf("pending:\n%s", reply)
	}
	later := m[1]
	if got := h.decidePending(upgrade, true); got != later {
		t.Fatalf("decided %s, want %s", got, later)
	}
	h.WaitSent(harnessDM, "fridolin says: The owner approved")
	withID := `"approval_id":"` + later + `"`
	reply = h.coreSays("go", step("node_exec", execArgs([]string{brew, "update"}, withID)), step("node_exec", execArgs([]string{brew, "upgrade"}, withID)),
		step("node_exec", execArgs([]string{brew, "upgrade"}, withID)))
	if !strings.Contains(reply, "is for \""+upgrade+"\", not for this command on this node") || strings.Count(reply, `"exit_code": 0`) != 1 || !strings.Contains(reply, "was used already") {
		t.Fatalf("with approval_id:\n%s", reply)
	}
	if got := ranLog(); got != "upgrade\nupgrade\n" {
		t.Errorf("ran with the approval_id: %q", got)
	}
	// An approval of something else, or none at all, runs nothing.
	reply = h.coreSays("sneak", step("request_approval", `{"action":"Check the weather","wait_seconds":1}`), step("node_exec", execArgs([]string{brew, "upgrade"}, `"approval_id":"$APPROVAL_ID"`)),
		step("node_exec", execArgs([]string{brew, "upgrade"}, `"approval_id":"a-nothing"`)))
	if !strings.Contains(reply, `is for "Check the weather"`) || !strings.Contains(reply, `there is no approval "a-nothing" of yours`) {
		t.Fatalf("other approvals:\n%s", reply)
	}
	if got := ranLog(); got != "upgrade\nupgrade\n" {
		t.Errorf("ran with another approval: %q", got)
	}

	// The audit log has each command, its secrets masked, and who said
	// yes.
	reply = h.coreSays("say", step("node_exec", execArgs([]string{say, "token=abcdefghijklmnopqrstuvwxyz0123456789"}, "")))
	if !strings.Contains(reply, `"stdout": "said 1\n"`) {
		t.Fatalf("say:\n%s", reply)
	}
	lines := h.auditOf("node_exec")
	var outcomes []string
	for _, l := range lines {
		outcomes = append(outcomes, l["outcome"].(string))
		if strings.Contains(l["detail"].(string), "abcdefghijklmnop") {
			t.Errorf("a secret in the audit log: %v", l)
		}
	}
	want := []string{"exit 0", "exit 0", "exit 0", "denied", "pending", "refused", "exit 0", "refused", "refused", "refused", "exit 0"}
	if len(outcomes) != len(want) {
		t.Fatalf("node_exec lines: %q, want %q", outcomes, want)
	}
	for i, w := range want {
		if !strings.HasPrefix(outcomes[i], w) {
			t.Errorf("line %d: %q, want %q", i, outcomes[i], w)
		}
	}
	// (Scrub takes the test's long temp path for a token.)
	if l, d := lines[2], lines[2]["detail"].(string); l["by"] != "15551234567" || !strings.HasPrefix(d, "mac-mini: ") || !strings.HasSuffix(d, " upgrade") || l["brain"] != "core" || !strings.HasPrefix(l["id"].(string), "a") {
		t.Errorf("the approved upgrade's line: %v", l)
	}
	if l := lines[6]; l["by"] != "dashboard" || l["id"] != later {
		t.Errorf("the line of the upgrade with the approval_id: %v", l)
	}

	// A lockdown that starts while a command waits for the yes: the yes
	// runs nothing.
	reply = h.coreSaysWhile("upgrade once more", func() {
		h.WaitFor("the approval of the upgrade", func() bool {
			pending, _ := h.approvals()
			return len(pending) > 0 && strings.Contains(pending[len(pending)-1]["action"].(string), upgrade)
		})
		if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
			t.Fatal(err)
		}
		h.decidePending(upgrade, true)
	}, step("node_exec", execArgs([]string{brew, "upgrade"}, `"wait_seconds":60`)))
	if !strings.Contains(reply, "[core:node_exec failed] the bridge is in lockdown") {
		t.Fatalf("a yes in a lockdown:\n%s", reply)
	}
	if got := ranLog(); got != "upgrade\nupgrade\n" {
		t.Errorf("ran in a lockdown: %q", got)
	}
	if l := h.auditOf("node_exec"); l[len(l)-1]["outcome"] != "refused: lockdown" {
		t.Errorf("the lockdown's line: %v", l[len(l)-1])
	}

	// In a lockdown nothing runs; the rules still read.
	reply = h.coreSays("locked", step("node_exec", execArgs([]string{status}, "")), step("node_exec_rules", `{"node":"mac-mini"}`))
	if !strings.Contains(reply, "[core:node_exec failed] the bridge is in lockdown") || !strings.Contains(reply, "[core:node_exec_rules] ") || !strings.Contains(reply, status+" (no arguments)") {
		t.Fatalf("in a lockdown:\n%s", reply)
	}
}

// The approval names the command whole, or, when the poll could not show
// all of it, in short, never cut; the draft has all of it.
func TestNodeExecApprovalText(t *testing.T) {
	t.Parallel()
	v := nodes.View{Node: &nodes.Node{ID: "n1", Name: "mac-mini"}}
	action, details := execApproval(v, []string{"brew", "upgrade", "--greedy"}, "")
	if action != "Run `brew upgrade --greedy` on mac-mini" {
		t.Errorf("action %q", action)
	}
	if details != "On mac-mini (node n1): brew upgrade --greedy\nargv: [\"brew\",\"upgrade\",\"--greedy\"]\nThe rule on mac-mini that allows it asks you each time." {
		t.Errorf("details %q", details)
	}
	action, details = execApproval(v, []string{"/Users/you/bin/backup.sh", "two words", "it's", ""}, "/Users/you/Backups")
	if action != "Run `/Users/you/bin/backup.sh 'two words' 'it'\\''s' ''` in /Users/you/Backups on mac-mini" || !strings.Contains(details, "\nin: /Users/you/Backups\n") {
		t.Errorf("quoted, in a folder: %q\n%s", action, details)
	}
	long := []string{"/Users/you/bin/backup.sh"}
	for range 20 {
		long = append(long, "--exclude=/Users/you/Library/Caches")
	}
	action, details = execApproval(v, long, "")
	if len(action) > maxExecAction || action != "Run a long backup.sh command (20 arguments) on mac-mini: Show me the draft has all of it" {
		t.Errorf("a long command: %q", action)
	}
	if !strings.Contains(details, commandLine(long)) {
		t.Errorf("the draft lacks the command:\n%s", details)
	}
	if got := outboundmsg.ApprovalQuestion(action); strings.HasSuffix(got, "…") {
		t.Errorf("the poll cuts it: %q", got)
	}
}

// node_list says each node's state as it is now: online, offline since
// when, or waiting for the owner; a node shut out is left out.
func TestNodeListText(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	seen := now.Add(-3*time.Hour - 20*time.Minute)
	host := &nodeproto.FrideHostCap{Ready: true, NoLogin: "no Claude token", Instances: []nodeproto.FrideInstance{{Name: "fridolin", State: nodeproto.FrideInstanceRunning}}}
	views := []nodes.View{
		{Node: &nodes.Node{ID: "a1", Name: "ice", OS: "windows", Device: nodeproto.DeviceApproved}, Online: true, Effective: nodeproto.Capabilities{Host: true, FrideHost: host}},
		{Node: &nodes.Node{ID: "b2", Name: "laptop", OS: "darwin", Device: nodeproto.DeviceApproved, LastSeen: &seen}, Effective: nodeproto.Capabilities{Notify: true}},
		{Node: &nodes.Node{ID: "c3", Name: "new", OS: "linux", Device: nodeproto.DevicePending}},
		{Node: &nodes.Node{ID: "d4", Name: "old", OS: "linux", Device: nodeproto.DeviceRevoked}},
	}
	got := nodeListText(views, now)
	for _, want := range []string{"3 nodes, 1 online now:", "- ice (windows, id a1): online\n  may: report its system details (OS, disk, memory); run FrIDE instances",
		"FrIDE instances: fridolin running (no Claude token of its own)", "- laptop (darwin, id b2): offline, last seen 3h20m0s ago", "- new (linux, id c3): offline, never seen; waits for the owner to approve it"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old") {
		t.Errorf("a node shut out is listed:\n%s", got)
	}
	if got := nodeListText(nil, now); got != "The core has no nodes." {
		t.Errorf("no nodes: %q", got)
	}
}
