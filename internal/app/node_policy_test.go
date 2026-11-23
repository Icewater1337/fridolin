package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func strs(s ...string) *[]string { l := append([]string{}, s...); return &l }

// workRule is the owner's example (#760): the work folder is backed up
// only to icebase, and coding on it runs on icebase or mac-mini.
var workRule = manifest.NodeRule{Folder: "work", Storage: strs("icebase"),
	Tasks: map[string][]string{manifest.TaskBackup: {"icebase"}, manifest.TaskCoding: {"icebase", "mac-mini"}}, Repos: []string{"acme-api"}}

func writePolicyBrain(t *testing.T, dir string, rules ...manifest.NodeRule) manifest.Manifest {
	t.Helper()
	m := manifest.Manifest{ID: "personal", Name: "personal", NodePolicy: rules,
		Backup: manifest.Backup{Enabled: true, Targets: map[string]bool{"node:nas": true, "node:ice": true}}}
	data, err := json.Marshal(m.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, manifest.File), string(data))
	return m.Normalized()
}

// TestNodePolicyBackups: a folder the policy keeps to icebase stays out
// of the cloud and of every other node target, and goes to icebase's
// target, named by its node's name or id; the rest of the brain folder
// goes everywhere as before.
func TestNodePolicyBackups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := writePolicyBrain(t, dir, workRule)
	b := brainBackupOf("personal", dir, &m, false)
	if b.Skip != "" || !b.excluded("work/contract.pdf") || b.excluded("memories/a.md") {
		t.Errorf("cloud: skip %q, work excluded %v, memories excluded %v", b.Skip, b.excluded("work/contract.pdf"), b.excluded("memories/a.md"))
	}
	nas, ok := b.toNode("nas", "0123456789ab", "nas-box")
	if !ok || !excludedBy("work/contract.pdf", nas) || excludedBy("memories/a.md", nas) {
		t.Errorf("nas: ok %v, patterns %v", ok, nas)
	}
	ice, ok := b.toNode("ice", "ba9876543210", "icebase")
	if !ok || excludedBy("work/contract.pdf", ice) {
		t.Errorf("icebase takes work: ok %v, patterns %v", ok, ice)
	}
	// Drive is the cloud too.
	if !excludedBy("work/contract.pdf", backup.ExcludePatterns(driveExcludes(t, dir, m), true)) {
		t.Error("the Drive backup took work")
	}
}

func driveExcludes(t *testing.T, dir string, m manifest.Manifest) []string {
	t.Helper()
	b := brainBackup{ID: m.ID, Dir: dir, policies: backupPolicies([]vaultBrainRef{{Dir: dir, Approved: &m}}, HomeDir())}
	all, _, denied := b.policyDenied(manifest.TargetCloud)
	if all {
		return []string{"**"}
	}
	return append(m.Backup.CloudExcludes(), denied...)
}

// TestNodePolicyWholeFolder: a rule on the whole brain folder that keeps
// it off the cloud skips its cloud backup, not just a part.
func TestNodePolicyWholeFolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := writePolicyBrain(t, dir, manifest.NodeRule{Folder: "", Storage: strs("icebase")})
	b := brainBackupOf("personal", dir, &m, false)
	if b.Skip == "" || !b.excluded("memories/a.md") {
		t.Errorf("the whole folder should stay off the cloud: %+v", b)
	}
	if _, ok := b.toNode("nas", "0123456789ab", "nas-box"); ok {
		t.Error("nas got a folder that may live only on icebase")
	}
}

// TestNodePolicyDiskCannotLoosen: a rule the brain wrote into its
// brain.json, not yet approved, can keep more home, never let more out.
func TestNodePolicyDiskCannotLoosen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	approved := writePolicyBrain(t, dir, workRule)
	// On disk the brain lets work/public go to the cloud.
	writePolicyBrain(t, dir, workRule, manifest.NodeRule{Folder: "work/public", Storage: strs("cloud", "icebase"), Tasks: map[string][]string{manifest.TaskBackup: {"cloud"}}})
	b := brainBackupOf("personal", dir, &approved, false)
	if !b.excluded("work/public/x.md") {
		t.Error("an unapproved rule let work/public into the cloud")
	}
	// On disk it keeps memories home: that holds before approval.
	writePolicyBrain(t, dir, workRule, manifest.NodeRule{Folder: "memories", Storage: strs()})
	if b := brainBackupOf("personal", dir, &approved, false); !b.excluded("memories/a.md") {
		t.Error("a stricter rule on disk was ignored")
	}
}

// TestNodePolicyDefaultBacksUpAsBefore: without node_policy, backups
// go where they went.
func TestNodePolicyDefaultBacksUpAsBefore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := writePolicyBrain(t, dir)
	b := brainBackupOf("personal", dir, &m, false)
	if b.Skip != "" || b.excluded("work/contract.pdf") {
		t.Errorf("cloud: %+v", b)
	}
	for _, target := range []string{"nas", "ice"} {
		if p, ok := b.toNode(target); !ok || excludedBy("work/contract.pdf", p) {
			t.Errorf("%s: ok %v, %v", target, ok, p)
		}
	}
}

// TestAllowedForReposNeverFallsBack: FrIDE's candidates lose every node
// the policy does not allow, and none comes back when that leaves none.
func TestAllowedForReposNeverFallsBack(t *testing.T) {
	t.Parallel()
	p := manifest.Policy((manifest.Manifest{ID: "personal", NodePolicy: []manifest.NodeRule{workRule}}).FolderRules("/b", "/h"))
	cands := []nodes.View{{Node: &nodes.Node{ID: "aaaaaaaaaaaa", Name: "gpu-box"}}, {Node: &nodes.Node{ID: "bbbbbbbbbbbb", Name: "mac-mini"}}}
	repos := []string{"acme-api"}
	kept, why := allowedForRepos(cands, p.RepoTask(manifest.TaskCoding, repos), repos)
	if len(kept) != 1 || kept[0].Name != "mac-mini" || why == "" {
		t.Errorf("kept %v, why %q", kept, why)
	}
	kept, why = allowedForRepos(cands[:1], p.RepoTask(manifest.TaskCoding, repos), repos)
	if len(kept) != 0 || why == "" {
		t.Errorf("gpu-box alone must not be used: kept %v, why %q", kept, why)
	}
	// Other repositories: no limit.
	if kept, why := allowedForRepos(cands, p.RepoTask(manifest.TaskCoding, []string{"blog"}), []string{"blog"}); len(kept) != 2 || why != "" {
		t.Errorf("an unrestricted repository: kept %v, why %q", kept, why)
	}
}

// TestNodePolicyNodeExec: node_exec refuses a dir whose node policy keeps
// commands off the node, says so, runs nothing and tries no other node;
// a dir outside the rule runs as before.
func TestNodePolicyNodeExec(t *testing.T) {
	t.Parallel()
	skipWithoutSh(t)
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(bin, "work")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8768"}}`)
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true},
		NodePolicy: []manifest.NodeRule{{Folder: work, Tasks: map[string][]string{manifest.TaskExec: {"icebase"}}}}})
	addr := h.StartNodes()
	ran := filepath.Join(bin, "ran.log")
	status := filepath.Join(bin, "status")
	if err := os.WriteFile(status, []byte("#!/bin/sh\necho ran >> \""+ran+"\"\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.approveNode(h.execNode("mac-mini", addr, []nodeproto.ExecRule{{Name: status, Args: []string{}}}))
	h.WaitFor("mac-mini running commands", func() bool { return len(h.b.execNodeViews()) == 1 })

	reply := h.coreSays("status of work", step("node_exec", execArgs([]string{status}, `"dir":"`+filepath.Join(work, "repo")+`"`)))
	if !strings.Contains(reply, "the node policy keeps exec of") || !strings.Contains(reply, "off mac-mini") || !strings.Contains(reply, "only on icebase") {
		t.Fatalf("a denied dir:\n%s", reply)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the command ran on a node the policy denies")
	}
	reply = h.coreSays("status here", step("node_exec", execArgs([]string{status}, `"dir":"`+bin+`"`)))
	if !strings.Contains(reply, `"exit_code": 0`) {
		t.Fatalf("a dir no rule covers:\n%s", reply)
	}
}

// TestNodePolicyFride: FrIDE work in a repository of a restricted folder
// waits while only a node the policy denies offers FrIDE, its chat told
// why, and is never handed to that node; once the owner allows the node
// on the Files page, the queue places it. Work in another repository goes
// at once.
func TestNodePolicyFride(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.frideRetry = 100 * time.Millisecond
	setenv(t, "FRIDOLIN_FAKE_MCP", "[]")
	ff := testutil.StartFakeFride(t, "api", "blog")
	h := newServedHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	dir := h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true},
		NodePolicy: []manifest.NodeRule{{Folder: "work", Tasks: map[string][]string{manifest.TaskCoding: {"icebase"}}, Repos: []string{"api"}}}})
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	addr := h.StartNodes()
	h.fakeFrideNode("laptop", addr, ff)

	reply := h.coreSays("fix the api", step("fride_task", `{"message":"Fix the api","repos":["api"]}`))
	if !strings.Contains(reply, "FrIDE #1 waits") {
		t.Fatalf("restricted work:\n%s", reply)
	}
	h.WaitSent(harnessDM, "⏳ *FrIDE #1* waits")
	if msgs := strings.Join(h.SentTo(harnessDM), "\n"); !strings.Contains(msgs, "node policy allows for coding: only on icebase") {
		t.Errorf("the chat was not told why:\n%s", msgs)
	}
	if got := ff.Replies(1); len(got) != 0 {
		t.Errorf("the denied node got work: %v", got)
	}
	reply = h.coreSays("write a post", step("fride_task", `{"message":"Write a post","repos":["blog"]}`))
	if !strings.Contains(reply, "Handed to FrIDE on laptop as FrIDE #2") {
		t.Fatalf("unrestricted work:\n%s", reply)
	}
	time.Sleep(300 * time.Millisecond) // a few queue retries
	if th, err := h.b.store.GetFrideThread(context.Background(), 1); err != nil || th.Node != "" {
		t.Fatalf("FrIDE #1 went somewhere: %+v %v", th, err)
	}

	// The owner lets coding in work run on the laptop too.
	w := h.Do("PUT", "/api/files/node-policy", map[string]any{"root": "brain:core", "path": "work", "tasks": map[string][]string{"coding": {"icebase", "laptop"}}, "repos": []string{"api"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT node-policy: %d %s", w.Code, w.Body.String())
	}
	h.WaitSent(harnessDM, "🛠️ *FrIDE #1* went to laptop now.")
	w = h.Do("GET", "/api/files/list?root=brain:core&path=work", nil)
	if !strings.Contains(w.Body.String(), `"node_policy"`) || !strings.Contains(w.Body.String(), `"laptop"`) {
		t.Errorf("the Files page's view: %s", w.Body.String())
	}
}
