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

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// registerPostMessage gives every run a tool that takes an irreversible
// step, as a tool that posts in a group would, behind the policy.
func (h *harness) registerPostMessage() {
	h.b.mcpTools.Register(mcp.Entry[mcpCaller]{
		Name:     "post_message",
		Describe: func(mcpCaller) mcp.Tool { return mcp.Tool{Description: "test", InputSchema: schema(nil)} },
		Call: h.b.guarded(skill.RiskIrreversible, func(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error) {
			return mcp.TextResult("posted"), nil
		}),
	})
}

// approvalPolls are the approval polls the bridge sent.
func (h *harness) approvalPolls() []sent {
	var out []sent
	for _, m := range h.Outbox() {
		if m.ID != "" && strings.HasPrefix(m.Text, outboundmsg.ApprovalQuestion("")) {
			out = append(out, m)
		}
	}
	return out
}

// setApprovals saves brain id's approvals setting in the dashboard.
func (h *harness) setApprovals(id, mode string) {
	h.t.Helper()
	m := h.Approved()[id]
	m.Approvals = mode
	if w := h.Do("PUT", "/api/brains/"+id, map[string]any{"manifest": m}); w.Code != http.StatusOK {
		h.t.Fatalf("saving approvals %s: %d %s", mode, w.Code, w.Body.String())
	}
}

// TestDelegationCarriesTheApproval: the owner said yes once, in the core
// run, to the step a delegated task holds; the delegated run is told so,
// takes the step with that approval_id, and nobody asks the owner a
// second time.
func TestDelegationCarriesTheApproval(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Brain: "core", Tool: "request_approval", Args: json.RawMessage(`{"action":"Post \"Hello\" in the family group","details":"Hello","wait_seconds":30}`)},
		{Brain: "core", When: "approved", Tool: "delegate", Args: json.RawMessage(`{"brain":"family","task":"Post Hello in the family group. #mcp","approval_id":"` + testutil.FakeApprovalID + `","timeout_seconds":60}`)},
		{Brain: "family", Tool: "post_message", Args: json.RawMessage(`{"approval_id":"` + testutil.FakeApprovalID + `"}`)},
		// A step it was not handed still needs an approval of its own.
		{Brain: "family", Tool: "post_message", Args: json.RawMessage(`{}`)},
	})
	h.registerPostMessage()
	h.grant(map[string][]string{"family": {"delegate"}})

	reply := h.answerAfter(inbound{JID: harnessDM, Text: "Have family post Hello in its group #mcp"}, func() {
		poll := h.waitPoll(harnessDM, `Post "Hello"`)
		h.vote(poll.ID, "15551234567", outboundmsg.ApprovalYes)
	})
	for _, want := range []string{
		"[core:request_approval] approved: the owner said yes",
		"The owner approved this step already, before the task was handed to you (approval_id ",
		"[family:post_message] posted",
		"[family:post_message failed] this tool takes an irreversible step: call request_approval first",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("the core's answer lacks %q:\n%s", want, reply)
		}
	}
	if polls := h.approvalPolls(); len(polls) != 1 {
		t.Errorf("the owner was asked %d times, want once: %+v", len(polls), polls)
	}
	req := h.auditOf("approval_requested")
	if len(req) != 1 || req[0]["brain"] != "core" {
		t.Fatalf("approval_requested: %v", req)
	}
	id := req[0]["id"].(string)
	if d := h.auditOf("delegation"); len(d) != 1 || !strings.HasSuffix(d[0]["detail"].(string), "(approval "+id+")") {
		t.Errorf("delegation: %v", d)
	}
	var delegated map[string]any
	for _, l := range h.auditOf("run_start") {
		if l["brain"] == "family" {
			delegated = l
		}
	}
	if delegated == nil || !strings.HasSuffix(delegated["detail"].(string), "with approval "+id) {
		t.Errorf("the delegated run's line: %v", delegated)
	}
	a, err := h.b.store.ApprovalByID(context.Background(), id)
	if err != nil || !a.Used {
		t.Errorf("the handed approval, once the delegated run took the step: %+v %v", a, err)
	}
}

// TestHandOverTakesOnlyTheOwnersApproval: delegate's approval_id must be
// an approved request of the core brain's own, not used yet; one only the
// core's approvals: auto gave goes to no brain that asks.
func TestHandOverTakesOnlyTheOwnersApproval(t *testing.T) {
	t.Parallel()
	h := coreHarness(t, nil)
	ctx := context.Background()
	now := time.Now()
	add := func(id, brain, status, by string) {
		t.Helper()
		a := store.Approval{ID: id, Kind: approvalKind, Brain: brain, Notify: brain, Action: "Post Hello", Risk: "irreversible", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := h.b.store.AddApproval(ctx, a); err != nil {
			t.Fatal(err)
		}
		if status != store.ApprovalPending {
			if _, err := h.b.store.DecideApproval(ctx, id, status, "", by, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("aowner", "core", store.ApprovalApproved, "15551234567")
	add("aauto", "core", store.ApprovalApproved, autoApprovedBy)
	add("adenied", "core", store.ApprovalDenied, "15551234567")
	add("apending", "core", store.ApprovalPending, "")
	add("afamily", "family", store.ApprovalApproved, "15551234567")
	c := mcpCaller{run: runInfo{Brain: "core"}}

	if a, msg, err := h.b.handOver(ctx, c, "aowner", "family"); err != nil || msg != "" || a.ID != "aowner" {
		t.Errorf("the owner's yes: %+v %q %v", a, msg, err)
	}
	if a, msg, err := h.b.handOver(ctx, c, "aauto", "family"); err != nil || msg != "" || a.ID != "" {
		t.Errorf("an auto approval went to a brain that asks: %+v %q %v", a, msg, err)
	}
	for id, want := range map[string]string{
		"adenied":  "is denied, not approved",
		"apending": "is pending, not approved",
		"afamily":  "there is no approval",
		"anone":    "there is no approval",
	} {
		if _, msg, err := h.b.handOver(ctx, c, id, "family"); err != nil || !strings.Contains(msg, want) {
			t.Errorf("%s: %q %v, want %q", id, msg, err, want)
		}
	}
	if _, err := h.b.store.UseApproval(ctx, "aowner"); err != nil {
		t.Fatal(err)
	}
	if _, msg, _ := h.b.handOver(ctx, c, "aowner", "family"); !strings.Contains(msg, "was used already") {
		t.Errorf("a used approval: %q", msg)
	}

	// To a brain that approves by itself too, an auto approval goes.
	h.setApprovals("family", manifest.ApprovalsAuto)
	if a, msg, err := h.b.handOver(ctx, c, "aauto", "family"); err != nil || msg != "" || a.ID != "aauto" {
		t.Errorf("an auto approval to an auto brain: %+v %q %v", a, msg, err)
	}
}

// TestAutoApprovals: with approvals: auto, request_approval answers
// approved at once, without a poll, recorded as approved by "auto" in
// the history and the audit log, and the guarded tool takes its id. A
// lockdown and policy.max_pending_irreversible still hold.
func TestAutoApprovals(t *testing.T) {
	t.Parallel()
	h := policyHarness(t, "", nil)
	h.registerPostMessage()
	h.setApprovals("taxes", manifest.ApprovalsAuto)
	answer := func(text string) string {
		reply := h.Answer(inbound{JID: harnessDM, Text: text})
		for !strings.HasPrefix(reply, "echo:") {
			reply = h.WaitSent(harnessDM, text)
		}
		return reply
	}

	reply := answer("Steuer, bitte #mcp")
	if !strings.Contains(reply, "[taxes:request_approval] approved: this brain acts without asking (approvals: auto)") || !strings.Contains(reply, "[taxes:write] submitted.txt") {
		t.Errorf("the run's answer:\n%s", reply)
	}
	if _, err := os.Stat(filepath.Join(h.Home(), "taxes", "submitted.txt")); err != nil {
		t.Errorf("the step was not taken: %v", err)
	}
	if polls := h.approvalPolls(); len(polls) != 0 {
		t.Errorf("auto asked the owner: %+v", polls)
	}
	pending, history := h.approvals()
	if len(pending) != 0 || len(history) != 1 || history[0]["status"] != store.ApprovalApproved || history[0]["decided_by"] != autoApprovedBy || history[0]["step"] != "submit" {
		t.Errorf("approvals: pending %v, history %v", pending, history)
	}
	id := history[0]["id"].(string)
	if req := h.auditOf("approval_requested"); len(req) != 1 || req[0]["id"] != id || req[0]["detail"] != "Submit the 2025 tax return through ELSTER (step submit)" {
		t.Errorf("approval_requested: %v", req)
	}
	if d := h.auditOf("approval_decided"); len(d) != 1 || d[0]["by"] != autoApprovedBy || d[0]["outcome"] != store.ApprovalApproved {
		t.Errorf("approval_decided: %v", d)
	}
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "taxes", ID: "r1", Chat: harnessDM})
	defer revoke()
	w := h.Script("POST", "/brain/mcp/call", `{"name":"post_message","arguments":{"approval_id":"`+id+`"}}`, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Fridolin-Run", "r1")
	})
	if !strings.Contains(w.Body.String(), `"posted"`) {
		t.Errorf("the guarded tool with the auto approval: %s", w.Body.String())
	}

	// policy.max_pending_irreversible: auto adds none of its own, but
	// does not go past those that wait.
	now := time.Now()
	for _, pid := range []string{"ap1", "ap2", "ap3"} {
		if err := h.b.store.AddApproval(context.Background(), store.Approval{ID: pid, Kind: approvalKind, Brain: "taxes", Notify: "taxes", Action: "x", Risk: "irreversible", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if reply := answer("Steuer again #mcp"); !strings.Contains(reply, "[taxes:request_approval failed] 3 irreversible actions wait for approval already") {
		t.Errorf("past max_pending_irreversible:\n%s", reply)
	}
	for _, pid := range []string{"ap1", "ap2", "ap3"} {
		h.b.decide(context.Background(), pid, store.ApprovalDenied, "", "dashboard", callerAnswers)
	}

	// A lockdown: nothing is approved.
	if got := h.Answer(inbound{JID: harnessDM, Text: "!lockdown"}); got != outboundmsg.LockdownReply(true, false) {
		t.Fatalf("!lockdown: %q", got)
	}
	if reply := answer("what is due? #mcp"); !strings.Contains(reply, "[taxes:request_approval failed] the bridge is in lockdown") {
		t.Errorf("auto in a lockdown:\n%s", reply)
	}
}

// TestAutoApprovesOnlyAboveRead: a run held to read, by its skill or a
// lockdown, asks the owner even when its brain approves by itself.
func TestAutoApprovesOnlyAboveRead(t *testing.T) {
	t.Parallel()
	auto := manifest.Manifest{ID: "a", Name: "A", Approvals: manifest.ApprovalsAuto}
	for _, tc := range []struct {
		m    manifest.Manifest
		risk skill.Risk
		want bool
	}{
		{auto, skill.RiskWrite, true},
		{auto, skill.RiskIrreversible, true},
		{auto, skill.RiskRead, false},
		{manifest.Manifest{ID: "a", Name: "A"}, skill.RiskWrite, false},
		{manifest.Manifest{ID: "a", Name: "A", Approvals: manifest.ApprovalsAsk}, skill.RiskIrreversible, false},
	} {
		c := mcpCaller{run: runInfo{Brain: "a", Policy: &runPolicy{Risk: tc.risk}}, manifest: tc.m}
		if got := c.autoApproves(); got != tc.want {
			t.Errorf("approvals %q at %s: %v, want %v", tc.m.Approvals, tc.risk, got, tc.want)
		}
	}
}
