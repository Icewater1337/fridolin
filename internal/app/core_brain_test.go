package app

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// coreHarness is a served bridge with a core brain on your DMs and a
// family brain on the group, whose fake CLI plays steps against the
// fridolin MCP server each run is given, when the message says #mcp.
func coreHarness(t *testing.T, steps []testutil.FakeMCPStep) *harness {
	t.Helper()
	data, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	setenv(t, "FRIDOLIN_FAKE_MCP", string(data))
	h := newServedHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "core", Name: "Core", Role: manifest.RoleCore, Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "family", Name: "Family", Routing: manifest.Routing{Groups: []string{harnessGroup}}})
	return h
}

// grant gives the core brain grants in the dashboard, as the Grants
// editor saves them.
func (h *harness) grant(grants map[string][]string) {
	h.t.Helper()
	m := h.Approved()["core"]
	m.Grants = grants
	if w := h.Do("PUT", "/api/brains/core", map[string]any{"manifest": m}); w.Code != http.StatusOK {
		h.t.Fatalf("saving the grants: %d %s", w.Code, w.Body.String())
	}
}

// auditOf is the audit log's lines of kind, oldest first.
func (h *harness) auditOf(kind string) []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, l := range h.auditLines() {
		if l["kind"] == kind {
			out = append(out, l)
		}
	}
	return out
}

// auditLines reads the data dir's audit log of this month.
func (h *harness) auditLines() []map[string]any {
	h.t.Helper()
	f, err := os.Open(filepath.Join(h.DataDir(), auditDirName, time.Now().UTC().Format("2006-01")+".jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			h.t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, line)
	}
	return out
}

// TestCoreDelegatesOnlyWhenGranted is M4's gate: the core brain delegates
// a summary of last week's family chat to family only when granted; without
// the grant the tool is absent; a sub-brain run, and a delegated one, has
// no delegate tool. Every tool list here is what `fridolin mcp --run` hands
// the CLI, asked of the bridge with the run's own token.
func TestCoreDelegatesOnlyWhenGranted(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Tool: ""},
		{Brain: "core", Tool: "delegate", Args: json.RawMessage(`{"brain":"family","task":"Summarise last week's family chat. #mcp"}`)},
		{Brain: "family", Tool: "delegate", Args: json.RawMessage(`{"brain":"core","task":"escalate"}`)},
		{Tool: "read_memory", Args: json.RawMessage(`{"brain":"family","path":"memories/README.md"}`)},
	})

	// Without a grant the core has no delegate tool, and calling it is
	// calling a tool that does not exist.
	reply := h.Answer(inbound{JID: harnessDM, Text: "What did the family talk about? #mcp"})
	if !strings.Contains(reply, "[core:list] read_memory,ask_user,request_approval,check_approval") {
		t.Errorf("the core's tools without a grant:\n%s", reply)
	}
	if !strings.Contains(reply, "[core:delegate] error -32602 unknown tool: delegate") {
		t.Errorf("delegate without a grant:\n%s", reply)
	}
	if !strings.Contains(reply, `[core:read_memory failed] you may not read brain "family"'s notes`) {
		t.Errorf("read_memory of family without a grant:\n%s", reply)
	}

	// A sub-brain's own run: no delegate, no list_brains.
	reply = h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude anything new? #mcp"})
	if !strings.Contains(reply, "[family:list] read_memory,ask_user,request_approval,check_approval") || !strings.Contains(reply, "[family:delegate] error -32602 unknown tool: delegate") {
		t.Errorf("family's tools in its own run:\n%s", reply)
	}

	h.grant(map[string][]string{"family": {"delegate", "read"}})
	reply = h.Answer(inbound{JID: harnessDM, Text: "What did the family talk about? #mcp"})
	if !strings.Contains(reply, "[core:list] delegate,list_brains,read_memory,ask_user,request_approval,check_approval") {
		t.Errorf("the core's tools with the grant:\n%s", reply)
	}
	// family ran the task in its own folder, saw it come from core, and
	// listed its tools in that delegated run: still no delegate.
	familyDir := filepath.Join(h.Home(), "family")
	for _, want := range []string{
		"[core:delegate] echo:" + familyDir + ":core says: Summarise last week's family chat. #mcp",
		"[family:list] read_memory,ask_user,request_approval,check_approval",
		"[family:delegate] error -32602 unknown tool: delegate",
		"[core:read_memory] # ",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("the core's answer lacks %q:\n%s", want, reply)
		}
	}

	// The delegated run's answer went to the core only: the family group
	// got nothing more than its own answer, and no message went to the
	// task's chat.
	for _, m := range h.Outbox() {
		if strings.HasPrefix(m.JID, "delegate:") {
			t.Errorf("a delegated run's reply was sent to %s: %q", m.JID, m.Text)
		}
	}
	if got := h.SentTo(harnessGroup); len(got) != 1 {
		t.Errorf("the family group got %d messages, want its own answer only: %q", len(got), got)
	}

	audit := h.auditOf("delegation")
	if len(audit) != 1 || audit[0]["brain"] != "core" || audit[0]["thread"] != harnessDM || audit[0]["outcome"] != "ok" || !strings.HasPrefix(audit[0]["detail"].(string), "to family: Summarise last week") || audit[0]["ts"] == "" {
		t.Errorf("audit log: %v", audit)
	}
	// The delegated run is in the core run's thread, with its risk.
	var delegatedRun map[string]any
	for _, l := range h.auditOf("run_start") {
		if l["brain"] == "family" && strings.HasPrefix(l["chat"].(string), "delegate:") {
			delegatedRun = l
		}
	}
	if delegatedRun == nil || delegatedRun["thread"] != harnessDM || delegatedRun["risk"] != "write" || !strings.HasPrefix(delegatedRun["detail"].(string), "delegated by run ") {
		t.Errorf("the delegated run's line: %v", delegatedRun)
	}

	// Taking the grant away takes the tool away again.
	h.grant(nil)
	reply = h.Answer(inbound{JID: harnessDM, Text: "and now? #mcp"})
	if !strings.Contains(reply, "[core:list] read_memory,ask_user,request_approval,check_approval") {
		t.Errorf("the core's tools after the grant was taken away:\n%s", reply)
	}
}

// TestLateDelegationAnswerComesToTheCoresChat: past its wait, delegate
// returns, and the sub-brain's answer comes to the core brain as a new
// message in the chat its run was for, which it answers there.
func TestLateDelegationAnswerComesToTheCoresChat(t *testing.T) {
	t.Parallel()
	release := filepath.Join(t.TempDir(), "release")
	setenv(t, "FRIDOLIN_FAKE_WAIT_FILE", release)
	setenv(t, "FRIDOLIN_FAKE_WAIT_BRAIN", "family")
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Brain: "core", Tool: "delegate", Args: json.RawMessage(`{"brain":"family","task":"Find the photos","timeout_seconds":1}`)},
	})
	h.grant(map[string][]string{"family": {"delegate"}})

	reply := h.Answer(inbound{JID: harnessDM, Text: "Find the photos #mcp"})
	if !strings.Contains(reply, "[core:delegate] family is still working on it after 1s") {
		t.Fatalf("the core's answer after the wait:\n%s", reply)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	late := h.WaitSent(harnessDM, "The answer of brain family")
	if !strings.Contains(late, "echo:"+filepath.Join(h.Home(), "core")+":family says: The answer of brain family to the task you delegated to it") || !strings.Contains(late, "core says: Find the photos") {
		t.Errorf("the core's answer to the late result:\n%s", late)
	}
	h.WaitFor("the audit line", func() bool { return len(h.auditOf("delegation")) == 1 })
	if a := h.auditOf("delegation")[0]; a["outcome"] != "ok, after the wait" {
		t.Errorf("audit: %v", a)
	}
}

// TestMCPAPIKnowsTheRunByItsToken: the MCP routes answer only a run's own
// token, for the run it was made for, and compute the tools from the
// brain's approved manifest, whatever a request says.
func TestMCPAPIKnowsTheRunByItsToken(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	h.grant(map[string][]string{"family": {"delegate"}})
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "core", ID: "r1", Chat: harnessDM})
	defer revoke()
	call := func(method, path, body, run, token string) (int, string) {
		w := h.Script(method, path, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Fridolin-Run", run)
		})
		return w.Code, w.Body.String()
	}
	if code, body := call("GET", "/brain/mcp/tools", "", "r1", tok); code != 200 || !strings.Contains(body, `"name":"delegate"`) || !strings.Contains(body, `"enum":["family"]`) {
		t.Errorf("tools: %d %s", code, body)
	}
	if code, body := call("GET", "/brain/mcp/tools", "", "r2", tok); code != http.StatusPreconditionFailed {
		t.Errorf("another run's id: %d %s", code, body)
	}
	if code, _ := call("GET", "/brain/mcp/tools", "", "r1", h.readToken("scripts.token")); code != http.StatusUnauthorized {
		t.Errorf("the scripts token: %d", code)
	}
	if code, body := call("POST", "/brain/mcp/call", `{"name":"vault_get","arguments":{}}`, "r1", tok); code != http.StatusNotFound {
		t.Errorf("a tool there is none of: %d %s", code, body)
	}
	if code, body := call("POST", "/brain/mcp/call", `{"name":"delegate","arguments":{"brain":"core","task":"x"}}`, "r1", tok); code != 200 || !strings.Contains(body, `"isError":true`) || !strings.Contains(body, `you may not delegate to \"core\"`) {
		t.Errorf("delegating to a brain not granted: %d %s", code, body)
	}
	if code, body := call("POST", "/brain/mcp/call", `{"name":"ask_user","arguments":{"question":"?","wait_seconds":1}}`, "r1", tok); code != 200 || !strings.Contains(body, "pending: no answer yet after 1s") {
		t.Errorf("ask_user: %d %s", code, body)
	}

	// A delegated run of the core, were there one, gets no delegate.
	sub, revokeSub := h.b.runTokens.mintRun(runInfo{Brain: "core", ID: "r3", Chat: "delegate:r0", Source: "delegate"})
	defer revokeSub()
	if code, body := call("GET", "/brain/mcp/tools", "", "r3", sub); code != 200 || strings.Contains(body, `"delegate"`) || strings.Contains(body, "list_brains") {
		t.Errorf("a delegated run's tools: %d %s", code, body)
	}
}

// TestGrantsWrittenByABrainWaitForApproval: a core run that writes itself
// grants into its brain.json, as a manipulated run could, gets no tool
// from them: the Brains page shows the change, and only your approval
// there makes it count.
func TestGrantsWrittenByABrainWaitForApproval(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "core", ID: "r1", Chat: harnessDM})
	defer revoke()
	tools := func() string {
		w := h.Script("GET", "/brain/mcp/tools", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) })
		return w.Body.String()
	}
	edited := h.Approved()["core"]
	edited.Grants = map[string][]string{"family": {"delegate"}}
	data, err := json.MarshalIndent(edited, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.Home(), "core", manifest.File), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tools(), `"delegate"`) {
		t.Fatal("grants the brain wrote itself took effect without approval")
	}
	h.WaitFor("the change on disk to show", func() bool {
		for _, is := range h.Issues() {
			if is.ID == "core" && is.Kind == "drift" {
				return true
			}
		}
		return false
	})
	if w := h.Do("POST", "/api/brains/core/approve", map[string]any{"manifest": edited}); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the approved grant to give the tool", func() bool { return strings.Contains(tools(), `"delegate"`) })
}

// TestRunMCPArgsForEachProvider: Claude gets the run's --mcp-config file,
// Codex the same server as -c settings that pass it the two variables
// leading to the bridge, and a wait long enough for a delegated task.
func TestRunMCPArgsForEachProvider(t *testing.T) {
	t.Parallel()
	access := runAccess{MCPConfig: "/data/run-tokens/core/run-r1.mcp.json", MCPCommand: []string{"/opt/fridolin", "mcp", "--run", "r1"}}
	if got := strings.Join(runMCPArgs("claude", access), " "); got != "--mcp-config /data/run-tokens/core/run-r1.mcp.json --allowed-tools mcp__fridolin" {
		t.Errorf("claude: %s", got)
	}
	want := `-c mcp_servers.fridolin.command="/opt/fridolin" -c mcp_servers.fridolin.args=["mcp","--run","r1"] -c mcp_servers.fridolin.env_vars=["FRIDOLIN_API","FRIDOLIN_BRAIN_TOKEN_FILE"] -c mcp_servers.fridolin.tool_timeout_sec=660`
	if got := strings.Join(runMCPArgs("codex", access), " "); got != want {
		t.Errorf("codex:\n%s\nwant\n%s", got, want)
	}
	if runMCPArgs("claude", runAccess{}) != nil || runMCPArgs("codex", runAccess{}) != nil {
		t.Error("a run without a brain API gets no MCP server")
	}
}

func TestReadMemoryStaysInMemoriesAndSkills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"memories/a.md", "memories/sub/b.md", "memories/raw.txt", "skills/s/SKILL.md", "brain.json", "conversation-history.jsonl"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Links a brain could plant to lead the bridge out of memories/.
	if err := os.Symlink("..", filepath.Join(dir, "memories", "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../brain.json", filepath.Join(dir, "memories", "brain.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "skills", "all")); err != nil {
		t.Fatal(err)
	}
	read := func(p string) (string, error) {
		rel, err := memoryPath(p)
		if err != nil {
			return "", err
		}
		return readMemory(dir, rel)
	}
	for _, tc := range []struct {
		path, want, err string
	}{
		{"memories/a.md", "content of memories/a.md", ""},
		{"memories/sub/b.md", "content of memories/sub/b.md", ""},
		{"skills/s/SKILL.md", "content of skills/s/SKILL.md", ""},
		{"", "memories/a.md\nmemories/sub/", ""},
		{"skills/", "skills/s/", ""},
		{"memories/raw.txt", "", "only the markdown files"},
		{"brain.json", "", "outside memories/ and skills/"},
		{"conversation-history.jsonl", "", "outside memories/ and skills/"},
		{"memories/../brain.json", "", "outside memories/ and skills/"},
		{"../x/memories/a.md", "", "not a path inside"},
		{"/etc/passwd", "", "not a path inside"},
		{"memories/up/brain.json", "", "symlink"},
		{"memories/brain.md", "", "symlink"},
		{"skills/all/conversation-history.jsonl", "", "symlink"},
		{"memories/none.md", "", "no such file"},
	} {
		got, err := read(tc.path)
		switch {
		case tc.err == "" && (err != nil || got != tc.want):
			t.Errorf("%q: %q, %v; want %q", tc.path, got, err, tc.want)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: %q, %v; want an error saying %q", tc.path, got, err, tc.err)
		}
	}
}
