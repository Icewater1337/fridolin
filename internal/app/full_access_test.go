package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Full access takes both keys: the brain in nodes.full_access_brains and
// the node's full-access, offered there and approved. With them, node_exec
// runs what the allowlist does not, a shell included, audited as full
// access; a command that looks risky waits for the owner's yes. Without
// either, the allowlist holds as before: the default stays restricted.
func TestFullAccessTakesTheBrainsTrustAndTheNodesWord(t *testing.T) {
	t.Parallel()
	skipWithoutSh(t)
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8768"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	addr := h.StartNodes()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caps := func(full bool) nodeproto.Capabilities {
		return nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname", Args: []string{}},
			{Name: "find", Args: []string{root, "-name", "stale", "-delete"}, Exact: true}}},
			FS: &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: root, Write: true}}}, Terminal: &nodeproto.WorkstationCap{Roots: []string{root}}, FullAccess: full}
	}
	mini := h.joinExecNode("mac-mini", addr, caps(true))
	laptop := h.joinExecNode("laptop", addr, caps(false))
	h.approveNode(mini)
	h.approveNode(laptop)
	h.WaitFor("both nodes running commands", func() bool { return len(h.b.execNodeViews()) == 2 })
	shell := func(node, script string, more string) string {
		if more != "" {
			more = "," + more
		}
		return execArgs([]string{"/bin/sh", "-c", script}, `"node":"`+node+`"`+more)
	}
	trust := func(brains ...string) {
		if _, err := h.b.cfgs.Update("", func(c *config.File) error { c.Nodes.FullAccessBrains = brains; return nil }); err != nil {
			t.Fatal(err)
		}
	}

	// The default: nobody is trusted, so the node's full-access gives
	// nothing; the allowlist refuses a shell on both nodes.
	reply := h.coreSays("default", step("node_exec", shell("mac-mini", "echo hi", "")), step("node_exec", shell("laptop", "echo hi", "")))
	if strings.Count(reply, "does not run this") != 2 || strings.Contains(reply, `"stdout": "hi\n"`) {
		t.Fatalf("untrusted:\n%s", reply)
	}

	reply = h.coreSays("terminal", step("node_terminal_open", `{"node":"mac-mini"}`), step("node_terminal_close", `{"terminal":1}`))
	if !strings.Contains(reply, "Opened terminal #1") || strings.Contains(reply, "full access") {
		t.Fatalf("an untrusted terminal:\n%s", reply)
	}

	// Another brain trusted: still nothing for core.
	trust("family")
	reply = h.coreSays("other", step("node_exec", shell("mac-mini", "echo hi", "")))
	if !strings.Contains(reply, "mac-mini does not run this") {
		t.Fatalf("another brain trusted:\n%s", reply)
	}

	// core trusted: the node that offers full access runs the shell; the
	// one that does not keeps its allowlist.
	trust("core")
	reply = h.coreSays("trusted", step("node_exec", shell("mac-mini", "echo hi", "")), step("node_exec", shell("laptop", "echo hi", "")))
	if !strings.Contains(reply, `"stdout": "hi\n"`) || !strings.Contains(reply, "laptop does not run this") {
		t.Fatalf("trusted:\n%s", reply)
	}

	// A command an exec rule allows as it is runs without a poll, though
	// the guard would ask for it under full access alone (find -delete).
	stale := filepath.Join(root, "stale")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reply = h.coreSays("allowed cleanup", step("node_exec", execArgs([]string{"find", root, "-name", "stale", "-delete"}, `"node":"mac-mini"`)))
	if !strings.Contains(reply, `"exit_code": 0`) || strings.Contains(reply, "asks you first") {
		t.Fatalf("an allowed find -delete under full access:\n%s", reply)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the allowed cleanup did not run: %v", err)
	}

	// A risky command waits for the owner: no runs nothing, yes runs it.
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	rm := "rm -rf " + victim
	reply = h.coreSaysWhile("delete", func() { h.decidePending("rm -rf", false) }, step("node_exec", shell("mac-mini", rm, `"wait_seconds":60`)))
	if !strings.Contains(reply, "denied: the owner said no") {
		t.Fatalf("denied rm:\n%s", reply)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the no deleted it: %v", err)
	}
	reply = h.coreSaysWhile("delete now", func() { h.decidePending("rm -rf", true) }, step("node_exec", shell("mac-mini", rm, `"wait_seconds":60`)))
	if !strings.Contains(reply, `"exit_code": 0`) || !strings.Contains(reply, `"approval_id": "a`) {
		t.Fatalf("approved rm:\n%s", reply)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatalf("the yes did not delete it: %v", err)
	}

	// A trusted terminal runs without the sandbox; a risky line waits for
	// the owner and is not typed meanwhile; a harmless one goes in.
	reply = h.coreSays("trusted terminal", step("node_terminal_open", `{"node":"mac-mini"}`),
		step("node_terminal_send", `{"terminal":2,"text":"sudo","enter":false}`),
		step("node_terminal_send", `{"terminal":2,"text":" -n true","wait_seconds":1}`),
		step("node_terminal_send", `{"terminal":2,"text":"\u0015echo fine"}`))
	for _, want := range []string{"Opened terminal #2", "sandbox none, full access", "pending: no answer yet", "call node_terminal_send again with the same terminal and text", "Typed into terminal #2"} {
		if !strings.Contains(reply, want) {
			t.Errorf("a trusted terminal lacks %q:\n%s", want, reply)
		}
	}
	var typed []string
	for _, l := range h.auditOf("node_command") {
		typed = append(typed, l["detail"].(string)+" → "+auditText(l["outcome"]))
	}
	if got := strings.Join(typed, "\n"); !strings.Contains(got, "mac-mini [full access]: sudo -n true → approval a") || !strings.Contains(got, "mac-mini [full access]: echo fine") {
		t.Errorf("the terminal's audit lines:\n%s", got)
	}

	// The guard turned off: it runs without asking.
	if _, err := h.b.cfgs.Update("", func(c *config.File) error { c.Nodes.FullAccessSkipApproval = true; return nil }); err != nil {
		t.Fatal(err)
	}
	reply = h.coreSays("unguarded", step("node_exec", shell("mac-mini", "echo sudo-less; true ~/.ssh", "")))
	if !strings.Contains(reply, `"stdout": "sudo-less\n"`) {
		t.Fatalf("unguarded:\n%s", reply)
	}

	// Every full-access command is in the audit log, marked.
	var full int
	for _, l := range h.auditOf("node_exec") {
		if strings.HasPrefix(l["detail"].(string), "mac-mini [full access]: ") {
			full++
		}
	}
	if full != 5 { // echo hi, the allowed find, the denied rm, the approved rm, the unguarded one
		t.Errorf("%d full-access lines, want 5: %v", full, h.auditOf("node_exec"))
	}
}

func TestRiskyCommand(t *testing.T) {
	t.Parallel()
	roots := []string{"/Users/you/brains"}
	for _, c := range []struct {
		line  string
		risky bool
	}{
		{"open -a Parsec", false},
		{"ls -la /Users/you", false},
		{"rm notes.txt", false},
		{"rm /Users/you/brains/old.txt", false},
		{"git status && echo done", false},
		{"sudo shutdown -r now", true},
		{"echo hi; sudo -s", true},
		{"rm -rf build", true},
		{"rm -f x", true},
		{"rm --recursive x", true},
		{"rm /Users/you/Documents/tax.pdf", true},
		{"rm ~/x", true},
		{"rm ../x", true},
		{"/bin/sh -c 'cd /tmp && rm -r x'", true},
		{"find . -name '*.log' -delete", true},
		{"ls | xargs rm -f", true},
		{"cat ~/.ssh/id_ed25519", true},
		{"cat ~/.gitlab-token", true},
		{"cat /Users/you/.openai-api-key", true},
		{"cp x ~/.app-secrets/", true},
		{"cat ~/.secret", true},
		{"echo secrets.txt", false},
		{"ls ~/.personal-calendar", true},
		{"cat ~/.gitconfig", true},
		{"cat ~/.git-credentials", true},
		{"gh auth status --hostname x; cat ~/.config/gh/hosts.yml", true},
		{"sqlite3 ~/.fridolin/bridge.db .tables", true},
		{"cat ~/.fridolin/dashboard.token", true},
		{"security find-generic-password -s x", true},
		{"dd if=/dev/zero of=/dev/disk2", true},
		{"diskutil eraseDisk APFS X disk2", true},
		{"diskutil list", false},
	} {
		if got := riskyCommand(c.line, roots); (got != "") != c.risky {
			t.Errorf("%q: %q, want risky %v", c.line, got, c.risky)
		}
	}
}

func auditText(v any) string {
	s, _ := v.(string)
	return s
}
