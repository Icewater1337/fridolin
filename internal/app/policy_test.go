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

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/toolcli"
)

// taxesSkill is an irreversible skill whose submit step stops for a yes.
const taxesSkill = `---
name: taxes-de
description: File the German tax return through ELSTER.
metadata:
  fridolin:
    risk: irreversible
    vault: ["documents/taxes/**"]
    approve: [submit]
    triggers: [steuer]
---
# Taxes

1. Draft the return.
2. submit: request_approval, then submit it.
`

// A number the bridge does not allow, and so not the owner's.
const strangerNumber = "15559876543"

// policyHarness is a served bridge with brain taxes on your DMs, which
// has the taxes-de skill approved, and whose fake CLI plays steps: ask
// for approval to submit, and submit (write submitted.txt) only when the
// answer is approved.
func policyHarness(t *testing.T, cfgJSON string, steps []testutil.FakeMCPStep) *harness {
	t.Helper()
	if steps == nil {
		steps = []testutil.FakeMCPStep{
			{Tool: "request_approval", Args: json.RawMessage(`{"action":"Submit the 2025 tax return through ELSTER","details":"Refund: 312 EUR","step":"submit","wait_seconds":30}`)},
			{When: "approved", Write: "submitted.txt"},
		}
	}
	data, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRIDOLIN_FAKE_MCP", string(data))
	h := newServedHarness(t, cfgJSON)
	dir := h.AddBrain(manifest.Manifest{ID: "taxes", Name: "Taxes", Routing: manifest.Routing{HandlesDMs: true}})
	if err := os.MkdirAll(filepath.Join(dir, "skills", "taxes-de"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "taxes-de", "SKILL.md"), []byte(taxesSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	s := h.skill("taxes", "skills/taxes-de/SKILL.md")
	if s.Status != skillNew || s.Effective.Risk != skill.RiskRead {
		t.Fatalf("a skill the owner has not approved: %+v", s)
	}
	if w := h.Do("POST", "/api/brains/taxes/skills/approve", map[string]string{"path": s.Path, "hash": s.Hash}); w.Code != http.StatusOK {
		t.Fatalf("approving the skill: %d %s", w.Code, w.Body.String())
	}
	return h
}

// skill is brain id's skill rel as GET /api/brains/{id}/skills has it.
func (h *harness) skill(id, rel string) brainSkill {
	h.t.Helper()
	var out struct {
		Skills []brainSkill `json:"skills"`
	}
	w := h.Do("GET", "/api/brains/"+id+"/skills", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("skills: %d %s", w.Code, w.Body.String())
	}
	for _, s := range out.Skills {
		if s.Path == rel {
			return s
		}
	}
	h.t.Fatalf("no skill %s in %s", rel, w.Body.String())
	return brainSkill{}
}

// waitPoll waits for a poll to jid whose question has substr and
// returns it.
func (h *harness) waitPoll(jid, substr string) sent {
	h.t.Helper()
	var got sent
	h.WaitFor("a poll in "+jid, func() bool {
		for _, m := range h.Outbox() {
			if m.JID == jid && m.ID != "" && strings.Contains(m.Text, substr) {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

// vote votes in poll as voter, through the dev chat, as a vote from
// WhatsApp arrives.
func (h *harness) vote(poll, voter, option string) {
	h.t.Helper()
	if w := h.Do("POST", "/api/dev/vote", map[string]any{"poll": poll, "voter": voter, "options": []string{option}}); w.Code != http.StatusAccepted {
		h.t.Fatalf("vote: %d %s", w.Code, w.Body.String())
	}
}

// approvals is GET /api/approvals.
func (h *harness) approvals() (pending, history []map[string]any) {
	h.t.Helper()
	var out struct {
		Pending []map[string]any `json:"pending"`
		History []map[string]any `json:"history"`
	}
	w := h.Do("GET", "/api/approvals", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		h.t.Fatalf("approvals: %d %s", w.Code, w.Body.String())
	}
	return out.Pending, out.History
}

// answerAfter injects m, runs during() once the bridge took it, and
// returns the next message to m's chat that is not a poll.
func (h *harness) answerAfter(m inbound, during func()) string {
	h.t.Helper()
	n := len(h.SentTo(m.JID))
	h.Inject(m)
	during()
	var got string
	h.WaitFor("the answer in "+m.JID, func() bool {
		for _, s := range h.Outbox()[n:] {
			if s.JID == m.JID && s.ID == "" && strings.HasPrefix(s.Text, "echo:") {
				got = s.Text
				return true
			}
		}
		return false
	})
	return got
}

// TestIrreversibleStepWaitsForApproval is M5's gate: an irreversible
// skill step calls request_approval, the poll arrives in the owner's
// chat, a vote from a number that is not the owner's counts for nothing,
// the owner's No ends the run with the step not taken, and the audit
// log has the request and the votes; no vote in time counts as no.
func TestIrreversibleStepWaitsForApproval(t *testing.T) {
	h := policyHarness(t, "", nil)
	dir := filepath.Join(h.Home(), "taxes")

	var poll sent
	reply := h.answerAfter(inbound{JID: harnessDM, Text: "Bitte mach meine Steuer #mcp"}, func() {
		poll = h.waitPoll(harnessDM, "Submit the 2025 tax return")
		if poll.Text != outboundmsg.ApprovalQuestion("Submit the 2025 tax return through ELSTER") ||
			strings.Join(poll.Poll, "|") != outboundmsg.ApprovalYes+"|"+outboundmsg.ApprovalNo+"|"+outboundmsg.ApprovalDraft {
			t.Errorf("the poll: %+v", poll)
		}
		// Someone else's yes counts for nothing.
		h.vote(poll.ID, strangerNumber, outboundmsg.ApprovalYes)
		if pending, _ := h.approvals(); len(pending) != 1 || pending[0]["status"] != "pending" {
			t.Errorf("after a stranger's vote: %v", pending)
		}
		// The draft, on request; the poll stays open.
		h.vote(poll.ID, "15551234567", outboundmsg.ApprovalDraft)
		h.WaitSent(harnessDM, "Refund: 312 EUR")
		h.vote(poll.ID, "15551234567", outboundmsg.ApprovalNo)
	})
	if !strings.Contains(reply, "[taxes:request_approval] denied: the owner said no") || !strings.Contains(reply, "[taxes:skipped submitted.txt]") {
		t.Errorf("the run's answer:\n%s", reply)
	}
	if _, err := os.Stat(filepath.Join(dir, "submitted.txt")); !os.IsNotExist(err) {
		t.Errorf("the step was taken: %v", err)
	}

	start := h.auditOf("run_start")
	if len(start) != 1 || start[0]["skill"] != "taxes-de" || start[0]["risk"] != "irreversible" || start[0]["thread"] != harnessDM {
		t.Errorf("run_start: %v", start)
	}
	req := h.auditOf("approval_requested")
	if len(req) != 1 || req[0]["detail"] != "Submit the 2025 tax return through ELSTER (step submit)" || req[0]["thread"] != harnessDM || req[0]["risk"] != "irreversible" || req[0]["skill"] != "taxes-de" {
		t.Fatalf("approval_requested: %v", req)
	}
	id := req[0]["id"]
	votes := h.auditOf("approval_vote")
	if len(votes) != 3 ||
		votes[0]["by"] != strangerNumber || votes[0]["outcome"] != "ignored: not an allowed number" ||
		votes[1]["outcome"] != "draft shown" ||
		votes[2]["by"] != "15551234567" || votes[2]["outcome"] != "denied" || votes[2]["id"] != id {
		t.Errorf("approval_vote: %v", votes)
	}
	if d := h.auditOf("approval_decided"); len(d) != 1 || d[0]["outcome"] != "denied" || d[0]["id"] != id {
		t.Errorf("approval_decided: %v", d)
	}
	if _, history := h.approvals(); len(history) != 1 || history[0]["status"] != "denied" || history[0]["decided_by"] != "15551234567" {
		t.Errorf("history: %v", history)
	}

	// No vote within the timeout counts as no.
	defer func(old func(*App) time.Duration) { approvalTimeout = old }(approvalTimeout)
	approvalTimeout = func(*App) time.Duration { return time.Second }
	reply = h.Answer(inbound{JID: harnessDM, Text: "Steuer again #mcp"})
	for !strings.HasPrefix(reply, "echo:") {
		reply = h.WaitSent(harnessDM, "Steuer again")
	}
	if !strings.Contains(reply, "denied: no answer in time, which counts as no") || !strings.Contains(reply, "[taxes:skipped submitted.txt]") {
		t.Errorf("after the timeout:\n%s", reply)
	}
	if _, err := os.Stat(filepath.Join(dir, "submitted.txt")); !os.IsNotExist(err) {
		t.Errorf("the step was taken: %v", err)
	}
	if d := h.auditOf("approval_decided"); len(d) != 2 || d[1]["outcome"] != "expired" || d[1]["by"] != "timeout" {
		t.Errorf("approval_decided: %v", d)
	}
}

// TestApprovedStepGoesAhead: the owner's yes, here from the dashboard,
// lets the step through, and the thread view shows the run.
func TestApprovedStepGoesAhead(t *testing.T) {
	h := policyHarness(t, "", nil)
	reply := h.answerAfter(inbound{JID: harnessDM, Text: "Steuer, bitte #mcp"}, func() {
		h.waitPoll(harnessDM, "Submit")
		pending, _ := h.approvals()
		if len(pending) != 1 {
			t.Fatalf("pending: %v", pending)
		}
		if w := h.Do("POST", "/api/approvals/"+pending[0]["id"].(string), map[string]string{"decision": "approve"}); w.Code != http.StatusOK {
			t.Fatalf("approve: %d %s", w.Code, w.Body.String())
		}
	})
	if !strings.Contains(reply, "[taxes:request_approval] approved") || !strings.Contains(reply, "[taxes:write] submitted.txt") {
		t.Errorf("the run's answer:\n%s", reply)
	}
	if _, err := os.Stat(filepath.Join(h.Home(), "taxes", "submitted.txt")); err != nil {
		t.Errorf("the approved step was not taken: %v", err)
	}
	if v := h.auditOf("approval_vote"); len(v) != 1 || v[0]["by"] != "dashboard" || v[0]["outcome"] != "approved" {
		t.Errorf("the dashboard's vote: %v", v)
	}
	var threads []threadView
	if w := h.Do("GET", "/api/threads", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &threads) != nil {
		t.Fatalf("threads: %d %s", w.Code, w.Body.String())
	}
	if len(threads) != 1 || threads[0].Thread != harnessDM || len(threads[0].Runs) != 1 || threads[0].Runs[0].Skill != "taxes-de" || threads[0].Runs[0].Outcome != "ok" {
		t.Errorf("threads: %+v", threads)
	}
	var lines []map[string]any
	if w := h.Do("GET", "/api/audit?kind=approval_vote", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lines) != nil || len(lines) != 1 {
		t.Errorf("audit filtered: %d %s", w.Code, w.Body.String())
	}
}

// TestLateApprovalReachesTheBrain: an answer that comes once the run
// stopped waiting, after a restart even, goes to the brain as a new
// message in the chat, which it answers there.
func TestLateApprovalReachesTheBrain(t *testing.T) {
	h := policyHarness(t, "", []testutil.FakeMCPStep{
		{Tool: "request_approval", Args: json.RawMessage(`{"action":"Send the offer to the client","wait_seconds":1}`)},
	})
	reply := h.Answer(inbound{JID: harnessDM, Text: "send the offer #mcp"})
	for !strings.HasPrefix(reply, "echo:") {
		reply = h.WaitSent(harnessDM, "send the offer")
	}
	if !strings.Contains(reply, "pending: no answer yet after 1s") {
		t.Fatalf("the run's answer:\n%s", reply)
	}
	h.Restart()
	pending, _ := h.approvals()
	if len(pending) != 1 {
		t.Fatalf("a restart lost the pending approval: %v", pending)
	}
	poll := h.waitPoll(harnessDM, "Send the offer")
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	late := h.WaitSent(harnessDM, "fridolin says: The owner approved")
	if !strings.Contains(late, `"Send the offer to the client"`) {
		t.Errorf("the brain's answer to the late approval:\n%s", late)
	}
}

// TestAskUser: ask_user with options is a poll whose vote is the answer;
// without, the owner's next message in the chat is, and goes no further.
func TestAskUser(t *testing.T) {
	h := policyHarness(t, "", []testutil.FakeMCPStep{
		{Tool: "ask_user", Args: json.RawMessage(`{"question":"Which account?","options":["Giro","Savings"],"wait_seconds":30}`)},
		{Tool: "ask_user", Args: json.RawMessage(`{"question":"What is the reference?","wait_seconds":30}`)},
	})
	reply := h.answerAfter(inbound{JID: harnessDM, Text: "pay it #mcp"}, func() {
		poll := h.waitPoll(harnessDM, "Which account?")
		h.vote(poll.ID, "15551234567", "Savings")
		h.WaitSent(harnessDM, "What is the reference?")
		h.Inject(inbound{JID: harnessDM, Text: "INV-42"})
	})
	if !strings.Contains(reply, "[taxes:ask_user] answered: Savings") || !strings.Contains(reply, "[taxes:ask_user] answered: INV-42") {
		t.Errorf("the run's answer:\n%s", reply)
	}
	// The typed answer started no run of its own.
	time.Sleep(200 * time.Millisecond)
	for _, s := range h.SentTo(harnessDM) {
		if strings.Contains(s, "says: INV-42") {
			t.Errorf("the answer went to the brain as a message too: %q", s)
		}
	}
}

// TestLockdown: !lockdown from the owner pauses every run but read-only
// ones, which may ask nothing; a skill's run that needs more is refused;
// !lockdown from anyone else is refused; the dashboard unlocks.
func TestLockdown(t *testing.T) {
	h := policyHarness(t, `{"group_members": {"120363000000000000@g.us": ["*"]}}`, nil)
	if got := h.Answer(inbound{JID: harnessGroup, Sender: strangerNumber, Text: "@claude !lockdown"}); got != outboundmsg.LockdownOwnerOnly {
		t.Errorf("a stranger's !lockdown: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!lockdown"}); got != outboundmsg.LockdownReply(true, false) {
		t.Errorf("!lockdown: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "Steuer #mcp"}); got != outboundmsg.LockdownRefusedNotice {
		t.Errorf("a skill run in lockdown: %q", got)
	}
	got := h.Answer(inbound{JID: harnessDM, Text: "what is due? #mcp"})
	if !strings.Contains(got, "[taxes:request_approval failed] the bridge is in lockdown") {
		t.Errorf("a read-only run in lockdown:\n%s", got)
	}
	if r := h.auditOf("run_refused"); len(r) != 1 || !strings.HasPrefix(r[0]["outcome"].(string), "lockdown") {
		t.Errorf("run_refused: %v", r)
	}
	if s := h.auditOf("run_start"); s[len(s)-1]["risk"] != "read" {
		t.Errorf("the run in lockdown: %v", s[len(s)-1])
	}
	// The lockdown survives a restart.
	h.Restart()
	var st map[string]any
	if w := h.Do("GET", "/api/policy", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil || st["lockdown"] != true || st["lockdown_by"] != "15551234567" {
		t.Errorf("policy after a restart: %s", w.Body.String())
	}
	if w := h.Do("POST", "/api/policy/lockdown", map[string]bool{"on": false}); w.Code != http.StatusOK {
		t.Fatalf("unlock: %d %s", w.Code, w.Body.String())
	}
	if l := h.auditOf("unlock"); len(l) != 1 || l[0]["by"] != "dashboard" {
		t.Errorf("unlock line: %v", l)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!unlock"}); got != outboundmsg.LockdownReply(false, true) {
		t.Errorf("!unlock when unlocked: %q", got)
	}
}

// TestRateLimit: a chat starts at most policy.max_runs_per_hour runs.
func TestRateLimit(t *testing.T) {
	h := policyHarness(t, `{"policy": {"max_runs_per_hour": 2}}`, []testutil.FakeMCPStep{})
	for i := range 2 {
		if got := h.Answer(inbound{JID: harnessDM, Text: "hi"}); !strings.HasPrefix(got, "echo:") {
			t.Fatalf("run %d: %q", i, got)
		}
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "hi"}); got != outboundmsg.RateLimitedNotice(2) {
		t.Errorf("the third run: %q", got)
	}
}

// TestIrreversibleToolNeedsApproval: a tool registered as irreversible
// takes an approval_id of an approved request of the same brain, once.
func TestIrreversibleToolNeedsApproval(t *testing.T) {
	h := policyHarness(t, "", []testutil.FakeMCPStep{})
	h.b.mcpTools.Register(mcp.Entry[mcpCaller]{
		Name:     "submit_form",
		Describe: func(mcpCaller) mcp.Tool { return mcp.Tool{Description: "test", InputSchema: schema(nil)} },
		Call: h.b.guarded(skill.RiskIrreversible, func(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error) {
			return mcp.TextResult("submitted"), nil
		}),
	})
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "taxes", ID: "r1", Chat: harnessDM})
	defer revoke()
	call := func(body string) string {
		w := h.Script("POST", "/brain/mcp/call", body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Set("X-Fridolin-Run", "r1")
		})
		return w.Body.String()
	}
	if got := call(`{"name":"submit_form","arguments":{}}`); !strings.Contains(got, "call request_approval first") {
		t.Errorf("without an approval: %s", got)
	}
	done := make(chan string, 1)
	go func() {
		done <- call(`{"name":"request_approval","arguments":{"action":"Submit the form","wait_seconds":30}}`)
	}()
	poll := h.waitPoll(harnessDM, "Submit the form")
	h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	got := <-done
	if !strings.Contains(got, "approved") {
		t.Fatalf("request_approval: %s", got)
	}
	_, history := h.approvals()
	id := history[0]["id"].(string)
	if got := call(`{"name":"submit_form","arguments":{"approval_id":"` + id + `"}}`); !strings.Contains(got, `"submitted"`) {
		t.Errorf("with the approval: %s", got)
	}
	if got := call(`{"name":"submit_form","arguments":{"approval_id":"` + id + `"}}`); !strings.Contains(got, "was used already") {
		t.Errorf("the approval a second time: %s", got)
	}
}

// TestStarterSkillsHaveFrontmatter: the skills a new brain gets read as
// the portable format, so they apply as declared (skillStarter).
func TestStarterSkillsHaveFrontmatter(t *testing.T) {
	for _, s := range brainTemplates.Skills {
		got, err := skill.Parse("skills/"+s.Name, []byte(s.Content))
		if err != nil || !got.Frontmatter || got.Description == "" {
			t.Errorf("%s: %v %+v", s.Name, err, got)
		}
		if _, ok := starterPolicies["skills/"+s.Name]; !ok {
			t.Errorf("%s is not a starter policy", s.Name)
		}
	}
}

// TestSkillPolicyFromTheDashboard: the owner's edit writes the policy
// into the file and approves it; a brain's later edit leaves the
// approved policy in force until approved again.
func TestSkillPolicyFromTheDashboard(t *testing.T) {
	h := policyHarness(t, "", []testutil.FakeMCPStep{})
	dir := filepath.Join(h.Home(), "taxes")
	// A starter skill applies as the bridge wrote it.
	if s := h.skill("taxes", "skills/transcription.md"); s.Status != skillStarter || s.Effective.Risk != skill.RiskWrite {
		t.Errorf("a starter skill: %+v", s)
	}
	w := h.Do("PUT", "/api/brains/taxes/skills/policy", map[string]any{"path": "skills/transcription.md", "risk": "read", "triggers": []string{"transcribe"}})
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "skills", "transcription.md"))
	if !strings.Contains(string(data), "risk: read") || !strings.Contains(string(data), "# Skill: Transcription") {
		t.Errorf("the file:\n%s", data)
	}
	if s := h.skill("taxes", "skills/transcription.md"); s.Status != skillApproved || s.Effective.Risk != skill.RiskRead {
		t.Errorf("after the edit: %+v", s)
	}
	// The brain raises its own skill's risk: the approved policy stays.
	if err := os.WriteFile(filepath.Join(dir, "skills", "transcription.md"), []byte(strings.Replace(string(data), "risk: read", "risk: irreversible", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := h.skill("taxes", "skills/transcription.md"); s.Status != skillChanged || s.Effective.Risk != skill.RiskRead || s.Policy.Risk != skill.RiskIrreversible {
		t.Errorf("after the brain's edit: %+v", s)
	}
	// Approving a policy that changed since it was reviewed is refused.
	if w := h.Do("POST", "/api/brains/taxes/skills/approve", map[string]string{"path": "skills/transcription.md", "hash": "stale"}); w.Code != http.StatusConflict {
		t.Errorf("a stale approval: %d %s", w.Code, w.Body.String())
	}
}

// TestLockdownCoversIntentsAndNodes: what M5's policy calls writing
// beyond its own tools. A lockdown refuses setting a standing intent,
// also to a run that answers the owner, and the core's node calls that
// act (exec, fs.write) while reading a node still goes; a run whose
// budget is read may not set an intent either.
func TestLockdownCoversIntentsAndNodes(t *testing.T) {
	tb, url := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	ask := chat.Inbound{JID: apiTestDM, Sender: "15551234567", Source: chat.SourceWhatsApp, Text: "remind me"}
	owner, revokeOwner := tb.runTokens.mintFor("a", ask)
	defer revokeOwner()
	readRun := runInfoFor("a", ask)
	readRun.Policy = &runPolicy{ID: "r-read", Brain: "a", Risk: skill.RiskRead}
	reader, revokeReader := tb.runTokens.mintRun(readRun)
	defer revokeReader()

	if _, err := tb.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if code, doc := brainTool(t, toolcli.Intent, url, owner, "add", "--when", "daily 08:00", "water the plants"); code == 0 || !strings.Contains(doc["error"].(string), "lockdown") {
		t.Errorf("an intent set in a lockdown: %d %v", code, doc)
	}
	call := func(method string) (int, string) {
		w := tb.do("POST", "/api/nodes/n1/call", map[string]any{"method": method, "params": map[string]any{}})
		return w.Code, w.Body.String()
	}
	for _, m := range []string{"exec", "fs.write"} {
		if code, body := call(m); code != http.StatusForbidden || !strings.Contains(body, "lockdown") {
			t.Errorf("%s in a lockdown: %d %s", m, code, body)
		}
	}
	if _, body := call("host.info"); strings.Contains(body, "lockdown") {
		t.Errorf("host.info in a lockdown: %s", body)
	}

	if _, err := tb.setLockdown(context.Background(), false, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if code, doc := brainTool(t, toolcli.Intent, url, reader, "add", "--when", "daily 08:00", "water the plants"); code == 0 || !strings.Contains(doc["error"].(string), "risk level is read") {
		t.Errorf("an intent set by a read run: %d %v", code, doc)
	}
	if code, doc := brainTool(t, toolcli.Intent, url, owner, "add", "--when", "daily 08:00", "water the plants"); code != 0 {
		t.Errorf("the owner's intent once unlocked: %d %v", code, doc)
	}
	if _, body := call("exec"); strings.Contains(body, "lockdown") {
		t.Errorf("exec once unlocked: %s", body)
	}
}
