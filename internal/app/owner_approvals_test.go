package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
)

// joinNode joins a machine called name with a join code from origin
// (nodes.JoinedFromChat, JoinedFromDashboard, or "" for neither), and
// returns its id.
func (h *harness) joinNode(addr, name, origin string, caps nodeproto.Capabilities) string {
	h.t.Helper()
	code, _, err := h.b.nodes.NewJoinCodeFrom(origin)
	if err != nil {
		h.t.Fatal(err)
	}
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: h.t.TempDir(), Core: addr, Code: code, Name: name,
		Capabilities: caps, AndroidBuild: noAndroidBuild, Version: "test", Logf: h.t.Logf})
	if err != nil {
		h.t.Fatal(err)
	}
	return j.ID
}

// device is node id's device state.
func (h *harness) device(id string) string {
	h.t.Helper()
	for _, v := range h.nodeViews() {
		if v.ID == id {
			return v.Device
		}
	}
	h.t.Fatalf("no node %s", id)
	return ""
}

// TestApproveAlone: a machine that joined with the owner's !join code is
// told in plain words, with no code to type, and !approve alone lets it
// in; then nothing waits. The audit log has it.
func TestApproveAlone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	addr := h.StartNodes()
	id := h.joinNode(addr, "laptop", nodes.JoinedFromChat, nodeproto.Capabilities{Host: true, Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname"}}}})

	alert := h.WaitSent(harnessDM, "wants to join as a node")
	for _, want := range []string{"*laptop*", "• When: ", "• From: this machine itself, 127.0.0.1", "Started: with a join code you asked for with !join",
		"run these programs there when asked: hostname", "Was this you? Reply !approve — or !deny if not.", "_ref " + id + "_"} {
		if !strings.Contains(alert, want) {
			t.Errorf("the alert lacks %q:\n%s", want, alert)
		}
	}
	if strings.Contains(alert, "Unexpected") || strings.Contains(alert, "`!approve") {
		t.Errorf("the alert of an expected join:\n%s", alert)
	}

	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.HasPrefix(got, "✅ *laptop* is approved. It can now:") {
		t.Fatalf("!approve: %q", got)
	}
	if d := h.device(id); d != nodeproto.DeviceApproved {
		t.Errorf("after !approve: %s", d)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); got != outboundmsg.NothingPendingReply {
		t.Errorf("!approve with nothing waiting: %q", got)
	}
	if l := h.auditOf("node_approved"); len(l) != 1 || l[0]["outcome"] != "approved" || l[0]["id"] != id || l[0]["by"] != "15551234567" {
		t.Errorf("audit: %v", l)
	}
	// Told once.
	h.b.alertPendingApprovals(context.Background())
	if n := len(h.SentTo(harnessDM)); n != 3 {
		t.Errorf("%d messages, not the alert and two answers: %q", n, h.SentTo(harnessDM))
	}
}

// TestApproveUnexpected: a machine whose join code came neither from
// !join nor from the dashboard is flagged, and !approve alone asks once
// more; a second !approve within confirmWindow lets it in, one later asks
// again. !deny needs no second word.
func TestApproveUnexpected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	addr := h.StartNodes()
	id := h.joinNode(addr, "stranger", "", nodeproto.Capabilities{Host: true})
	alert := h.WaitSent(harnessDM, "wants to join")
	if !strings.Contains(alert, "⚠️ *Unexpected:* its join code came neither from your !join nor from the dashboard.") {
		t.Errorf("the alert:\n%s", alert)
	}

	ask := h.Answer(inbound{JID: harnessDM, Text: "!approve"})
	if !strings.Contains(ask, "reply !approve again within 2 minutes") {
		t.Fatalf("the first !approve: %q", ask)
	}
	if d := h.device(id); d != nodeproto.DevicePending {
		t.Fatalf("approved on the first !approve: %s", d)
	}
	// Too late: it asks again.
	h.b.approvals.mu.Lock()
	h.b.approvals.confirm.at = time.Now().Add(-confirmWindow - time.Second)
	h.b.approvals.mu.Unlock()
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); got != ask {
		t.Fatalf("!approve after the window: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.HasPrefix(got, "✅ *stranger* is approved") {
		t.Fatalf("the second !approve: %q", got)
	}
	var outcomes []string
	for _, l := range h.auditOf("node_approved") {
		outcomes = append(outcomes, l["outcome"].(string)+" "+l["detail"].(string))
	}
	if strings.Join(outcomes, "|") != "asked to confirm node approve|asked to confirm node approve|approved node approve confirmed" {
		t.Errorf("audit: %q", outcomes)
	}

	// !deny turns one down at once.
	other := h.joinNode(addr, "other", "", nodeproto.Capabilities{Host: true})
	h.WaitSent(harnessDM, "*other*")
	if got := h.Answer(inbound{JID: harnessDM, Text: "!deny"}); got != outboundmsg.NodeRevokedReply("other", other) {
		t.Errorf("!deny: %q", got)
	}
	if d := h.device(other); d != nodeproto.DeviceRevoked {
		t.Errorf("after !deny: %s", d)
	}
}

// TestApproveExpired: a machine that waited longer than approvalFreshFor
// is not approved by !approve alone without a second one.
func TestApproveExpired(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	addr := h.StartNodes()
	id := h.joinNode(addr, "laptop", nodes.JoinedFromDashboard, nodeproto.Capabilities{Host: true})
	if alert := h.WaitSent(harnessDM, "wants to join"); !strings.Contains(alert, "from the dashboard") {
		t.Errorf("the alert:\n%s", alert)
	}
	// It joined two days ago.
	path := filepath.Join(h.DataDir(), "nodes", nodes.RegistryFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, n := range file["nodes"].([]any) {
		n.(map[string]any)["joined_at"] = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	}
	data, _ = json.Marshal(file)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.Contains(got, "longer than approvals are good for") || !strings.Contains(got, "within 2 minutes") {
		t.Fatalf("!approve of an old one: %q", got)
	}
	if d := h.device(id); d != nodeproto.DevicePending {
		t.Fatalf("approved silently: %s", d)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.HasPrefix(got, "✅ *laptop* is approved") {
		t.Errorf("the second !approve: %q", got)
	}
}

// TestApproveSeveral: with several waiting, !approve sends a poll with
// one plain option each; only the owner's vote counts, and it approves
// that one. A second vote in the same poll is out of date. `!approve
// <ref>` still names one.
func TestApproveSeveral(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	addr := h.StartNodes()
	laptop := h.joinNode(addr, "laptop", nodes.JoinedFromChat, nodeproto.Capabilities{Host: true})
	h.WaitSent(harnessDM, "*laptop*")
	mini := h.joinNode(addr, "mini", nodes.JoinedFromChat, nodeproto.Capabilities{Host: true})
	h.WaitSent(harnessDM, "*mini*")
	pi := h.joinNode(addr, "pi", nodes.JoinedFromChat, nodeproto.Capabilities{Host: true})
	h.WaitSent(harnessDM, "*pi*")

	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); got != outboundmsg.ApprovalPickNotice(true) {
		t.Fatalf("!approve with three waiting: %q", got)
	}
	poll := h.waitPoll(harnessDM, "Which one do you approve?")
	if len(poll.Poll) != 3 || poll.Poll[0] != "laptop (Linux) joins as a node" && !strings.HasSuffix(poll.Poll[0], "joins as a node") {
		t.Fatalf("the poll: %+v", poll)
	}
	option := ""
	for _, o := range poll.Poll {
		if strings.HasPrefix(o, "mini ") {
			option = o
		}
	}
	n := len(h.SentTo(harnessDM))
	h.vote(poll.ID, strangerNumber, option)
	h.vote(poll.ID, "15551234567", option)
	got := h.WaitSent(harnessDM, "is approved")
	if !strings.HasPrefix(got, "✅ *mini* is approved") || len(h.SentTo(harnessDM)) != n+1 {
		t.Fatalf("the vote: %q (%q)", got, h.SentTo(harnessDM)[n:])
	}
	if h.device(mini) != nodeproto.DeviceApproved || h.device(laptop) != nodeproto.DevicePending || h.device(pi) != nodeproto.DevicePending {
		t.Errorf("after the vote: %+v", h.nodeViews())
	}
	h.vote(poll.ID, "15551234567", poll.Poll[0])
	h.WaitSent(harnessDM, outboundmsg.ApprovalPollStale)
	if l := h.auditOf("owner_approval"); len(l) < 3 || l[1]["outcome"] != "ignored: not the owner" {
		t.Errorf("audit of the votes: %v", l)
	}

	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve " + pi}); !strings.HasPrefix(got, "✅ *pi* is approved") {
		t.Errorf("!approve <ref>: %q", got)
	}
	// One left: !deny alone takes it.
	if got := h.Answer(inbound{JID: harnessDM, Text: "!revoke"}); got != outboundmsg.NodeRevokedReply("laptop", laptop) {
		t.Errorf("!revoke alone: %q", got)
	}
}

// TestApproveBrainFiles: a skill and a scheduled agent a brain wrote into
// its folder are told the same way, flagged as not made in the
// dashboard, and approved with !approve alone twice; !deny leaves one as
// it is and stops asking.
func TestApproveBrainFiles(t *testing.T) {
	t.Parallel()
	fakeRuns(t)
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "taxes", Name: "Taxes", Routing: manifest.Routing{HandlesDMs: true}})
	if err := os.MkdirAll(filepath.Join(dir, "skills", "taxes-de"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "taxes-de", "SKILL.md"), []byte(taxesSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	h.b.alertPendingApprovals(context.Background())
	alert := h.WaitSent(harnessDM, "has a new skill")
	for _, want := range []string{"*Taxes* has a new skill, *taxes-de*", "start when a message says: steuer", "stop for your yes before: submit", "⚠️ *Unexpected:* it was written into the brain's folder"} {
		if !strings.Contains(alert, want) {
			t.Errorf("the skill's alert lacks %q:\n%s", want, alert)
		}
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.Contains(got, "within 2 minutes") {
		t.Fatalf("the first !approve: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.HasPrefix(got, "✅ Approved: the skill *taxes-de* of Taxes.") {
		t.Fatalf("the second !approve: %q", got)
	}
	if s := h.skill("taxes", "skills/taxes-de/SKILL.md"); s.Status != skillApproved {
		t.Errorf("the skill after !approve: %s", s.Status)
	}
	if l := h.auditOf("skill_approved"); len(l) != 1 || l[0]["by"] != "15551234567" {
		t.Errorf("audit: %v", l)
	}

	writeAgentFile(t, dir, "triage", `{"when": "daily 03:00", "prompt": "Triage the issues.", "risk": "irreversible"}`)
	h.b.alertPendingApprovals(context.Background())
	alert = h.WaitSent(harnessDM, "new scheduled agent")
	for _, want := range []string{"• Runs: daily 03:00, on its own", "• Does: its prompt", "take irreversible steps", "_ref taxes/triage_"} {
		if !strings.Contains(alert, want) {
			t.Errorf("the agent's alert lacks %q:\n%s", want, alert)
		}
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!deny"}); !strings.HasPrefix(got, "🚫 Turned down: the scheduled agent *triage* of Taxes.") {
		t.Fatalf("!deny: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); got != outboundmsg.NothingPendingReply {
		t.Errorf("!approve after !deny: %q", got)
	}
	if s := h.Approved()["taxes"].Schedules; len(s) != 0 {
		t.Errorf("approved schedules after !deny: %+v", s)
	}
	// A change asks again; !approve <ref> approves it as it is now.
	writeAgentFile(t, dir, "triage", `{"when": "daily 04:00", "prompt": "Triage the issues."}`)
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve taxes/triage"}); !strings.HasPrefix(got, "✅ Approved: the scheduled agent *triage*") {
		t.Fatalf("!approve <ref>: %q", got)
	}
	if s := h.Approved()["taxes"].Schedules; len(s) != 1 || s[0].When != "daily 04:00" {
		t.Errorf("approved schedules: %+v", s)
	}
}

// TestApproveRunRequest: a run's request_approval, which has its own
// poll, is answered by !approve alone too, and its brain hears it.
func TestApproveRunRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	now := time.Now()
	a := store.Approval{ID: "atest", Kind: approvalKind, Brain: "main", Notify: "main", Thread: harnessDM, Chat: harnessDM,
		Action: "Send the offer to the client", Risk: "irreversible", Status: store.ApprovalPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := h.b.store.AddApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve"}); !strings.HasPrefix(got, "✅ Approved: “Send the offer to the client”.") {
		t.Fatalf("!approve: %q", got)
	}
	got, err := h.b.store.ApprovalByID(context.Background(), "atest")
	if err != nil || got.Status != store.ApprovalApproved || got.DecidedBy != "15551234567" {
		t.Errorf("the approval: %+v %v", got, err)
	}
}
