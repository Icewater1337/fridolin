package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// ownerNumber is the owner's number in the harness (harnessDM).
const ownerNumber = "15551234567"

// moveCorePoll sends !movecore target from the owner and waits for the
// report and the poll it ends with, both sent after the command.
func (h *harness) moveCorePoll(src, dst string) (report string, poll sent) {
	h.t.Helper()
	n := len(h.Outbox())
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore " + dst}); got != outboundmsg.CoreMoveChecking(dst) {
		h.t.Fatalf("!movecore %s: %q", dst, got)
	}
	h.WaitFor("the report and the poll", func() bool {
		for _, m := range h.Outbox()[n:] {
			switch {
			case m.JID != harnessDM:
			case m.ID != "" && strings.Contains(m.Text, outboundmsg.CoreMoveQuestion(src, dst)):
				poll = m
			case strings.Contains(m.Text, "*Move the core from "+src+" to "+dst+"?*"):
				report = m.Text
			}
		}
		return report != "" && poll.ID != ""
	})
	return report, poll
}

// noMove asserts that no move runs, and none started.
func (h *harness) noMove(m moveHosts) {
	h.t.Helper()
	if id := h.b.nodes.MoveActive(); id != "" {
		h.t.Errorf("a move runs: %s", id)
	}
	if moves := h.b.nodes.Moves(); len(moves) != 0 {
		h.t.Errorf("moves started: %+v", moves)
	}
	if running, _, _ := m.srcCore.state(); !running {
		h.t.Error("mini stopped the core")
	}
}

// TestMoveCoreFromChat is #187 slice 3 end to end: !movecore alone lists
// where the core can go; !movecore icebase answers with the report and a
// poll in which a stranger's vote counts for nothing; the owner's "Move
// now" runs the move on the fake hosts and their bucket, the owner hears
// that the core stops and that it runs on icebase, and is asked whether
// mini keeps the secrets. Then the way back the same way, after which
// icebase forgets the secrets the first move brought it.
func TestMoveCoreFromChat(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)

	list := h.Answer(inbound{JID: harnessDM, Text: "!movecore"})
	if !strings.HasPrefix(list, "🚚 The core runs on *mini*.") || !strings.Contains(list, "*icebase* — ready: `!movecore icebase`") {
		t.Errorf("!movecore alone: %q", list)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore mini"}); got != outboundmsg.CoreMoveChecking("mini") {
		t.Errorf("!movecore mini: %q", got)
	}
	h.WaitSent(harnessDM, outboundmsg.CoreMoveHereAlready("mini"))
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore nowhere"}); got != outboundmsg.NodeUnknownReply("nowhere") {
		t.Errorf("!movecore nowhere: %q", got)
	}

	report, poll := h.moveCorePoll("mini", "icebase")
	for _, want := range []string{"id " + m.dst, "Offline: about", "*Not working on icebase:*", "Apple-only work", "within 30 minutes", "`!movecore mini` moves it back"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if len(poll.Poll) != 2 || poll.Poll[0] != outboundmsg.CoreMoveNow || poll.Poll[1] != outboundmsg.CoreMoveCancel {
		t.Errorf("the poll's options: %q", poll.Poll)
	}
	// One at a time.
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore icebase"}); got != outboundmsg.CoreMoveRunningNotice {
		t.Errorf("a second !movecore: %q", got)
	}
	// The dashboard cannot answer it.
	pending, _ := h.approvals()
	if len(pending) != 1 || pending[0]["kind"] != questionKind {
		t.Fatalf("pending: %+v", pending)
	}
	if w := h.Do("POST", "/api/approvals/"+pending[0]["id"].(string), map[string]any{"approve": true}); w.Code == http.StatusOK {
		t.Errorf("the dashboard approved the move: %s", w.Body.String())
	}
	// A stranger's vote counts for nothing.
	h.vote(poll.ID, strangerNumber, outboundmsg.CoreMoveNow)
	h.WaitFor("the stranger's vote in the audit log", func() bool {
		for _, e := range h.auditOf("approval_vote") {
			if e["outcome"] == "ignored: not an allowed number" {
				return true
			}
		}
		return false
	})
	h.noMove(m)

	h.vote(poll.ID, ownerNumber, outboundmsg.CoreMoveNow)
	h.WaitSent(harnessDM, "Moving the core from *mini* to *icebase*")
	h.WaitSent(harnessDM, "Stopping the core on *mini* now")
	done := h.WaitSent(harnessDM, "Running on *icebase* now")
	if !strings.Contains(done, "Apple-only work") || !strings.Contains(done, "`!movecore mini` moves the core back") {
		t.Errorf("the done message: %q", done)
	}
	if running, data, _ := m.dstCore.state(); !running || data != "mini's data" {
		t.Errorf("icebase: running %v on %q", running, data)
	}
	approved := h.auditOf("core_move_approved")
	if len(approved) != 1 || !strings.HasPrefix(approved[0]["outcome"].(string), "started m-") || approved[0]["by"] != ownerNumber {
		t.Errorf("core_move_approved: %+v", approved)
	}

	keep := h.waitPoll(harnessDM, "Keep the host secrets on mini?")
	h.vote(keep.ID, ownerNumber, outboundmsg.CoreSecretsKeep("mini"))
	h.WaitSent(harnessDM, outboundmsg.CoreSecretsKept("mini"))
	if v, err := m.dstStore.Get(hostsecrets.ClaudeOAuthToken); err != nil || v != m.secret {
		t.Fatalf("icebase did not get the secret: %v", err)
	}

	// The way back, by chat too.
	h.pinBack(m)
	_, back := h.moveCorePoll("icebase", "mini")
	h.vote(back.ID, ownerNumber, outboundmsg.CoreMoveNow)
	h.WaitSent(harnessDM, "Running on *mini* now")
	if running, data, _ := m.srcCore.state(); !running || data != "icebase's data" {
		t.Errorf("mini after the way back: running %v on %q", running, data)
	}
	forget := h.waitPoll(harnessDM, "Keep the host secrets on icebase?")
	h.vote(forget.ID, ownerNumber, outboundmsg.CoreSecretsForget("icebase"))
	h.WaitSent(harnessDM, "*icebase* forgot the host secrets a move brought it: "+hostsecrets.ClaudeOAuthToken)
	if _, err := m.dstStore.Get(hostsecrets.ClaudeOAuthToken); err == nil {
		t.Error("icebase still holds the secret")
	}
	for _, msg := range h.Outbox() {
		if strings.Contains(msg.Text, m.secret) {
			t.Error("a chat message holds a secret's value")
		}
	}
}

// TestMoveCoreCancelExpiryLockdown: Cancel leaves the core where it is;
// so does a poll nobody votes in, and a vote after it expired; during a
// lockdown !movecore is refused, and a "Move now" from before is a no;
// a message of the bridge's own account is no command.
func TestMoveCoreCancelExpiryLockdown(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)

	_, poll := h.moveCorePoll("mini", "icebase")
	h.vote(poll.ID, ownerNumber, outboundmsg.CoreMoveCancel)
	h.WaitSent(harnessDM, outboundmsg.CoreMoveCancelled("icebase", false))
	h.noMove(m)

	h.b.waits.coreMovePoll = time.Millisecond
	_, poll = h.moveCorePoll("mini", "icebase")
	time.Sleep(5 * time.Millisecond)
	h.approvals() // sweeps the expired ones
	h.WaitSent(harnessDM, outboundmsg.CoreMoveCancelled("icebase", true))
	h.vote(poll.ID, ownerNumber, outboundmsg.CoreMoveNow)
	time.Sleep(200 * time.Millisecond)
	h.noMove(m)
	h.b.waits.coreMovePoll = 30 * time.Minute

	// The rate limit: three asks an hour.
	_, poll = h.moveCorePoll("mini", "icebase")
	if got := h.Answer(inbound{JID: harnessDM, Text: "!lockdown"}); got != outboundmsg.LockdownReply(true, false) {
		t.Fatalf("!lockdown: %q", got)
	}
	h.vote(poll.ID, ownerNumber, outboundmsg.CoreMoveNow)
	h.WaitSent(harnessDM, outboundmsg.CoreMoveLockdownNotice)
	h.noMove(m)
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore icebase"}); got != outboundmsg.CoreMoveLockdownNotice {
		t.Errorf("!movecore in a lockdown: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!unlock"}); got != outboundmsg.LockdownReply(false, false) {
		t.Fatalf("!unlock: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!movecore icebase"}); got != outboundmsg.OwnerCommandRateLimited("!movecore", coreMoveAsksPerHour) {
		t.Errorf("a fourth !movecore in the hour: %q", got)
	}

	// The bridge's own account echoing a !movecore is no command.
	n := len(h.Outbox())
	ev := dmEvent("3EB0FROMME", textMsg("!movecore icebase"))
	ev.Info.IsFromMe = true
	h.WhatsApp(ev)
	time.Sleep(200 * time.Millisecond)
	if got := h.Outbox(); len(got) != n {
		t.Errorf("a message from the bridge's own account answered: %+v", got[n:])
	}
}

// TestMoveCoreRollbackAndRevoke: the target never gets ready, so the
// core runs on mini again and the owner hears it; icebase got the
// secrets during the preparation, so the owner is asked whether it
// keeps them, and no vote in time forgets them.
func TestMoveCoreRollback(t *testing.T) {
	t.Parallel()
	optionsOf(t).waits.coreMove = nodes.MoveOptions{HandoffDeadline: 10 * time.Second, ReadyWithin: time.Second}
	optionsOf(t).waits.coreSecretsPoll = time.Millisecond
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), false)

	_, poll := h.moveCorePoll("mini", "icebase")
	h.vote(poll.ID, ownerNumber, outboundmsg.CoreMoveNow)
	h.WaitSent(harnessDM, "Stopping the core on *mini* now")
	h.WaitSent(harnessDM, "The core move to *icebase* failed and the core runs on *mini* again")
	if running, _, _ := m.srcCore.state(); !running {
		t.Error("mini does not run the core again")
	}
	keep := h.waitPoll(harnessDM, "Keep the host secrets on icebase?")
	if keep.Poll[1] != outboundmsg.CoreSecretsForget("icebase") {
		t.Errorf("the keep/forget poll: %q", keep.Poll)
	}
	time.Sleep(5 * time.Millisecond)
	h.approvals() // sweeps the expired ones
	h.WaitSent(harnessDM, "No vote in time, so: *icebase* forgot the host secrets a move brought it: "+hostsecrets.ClaudeOAuthToken)
	if _, err := m.dstStore.Get(hostsecrets.ClaudeOAuthToken); err == nil {
		t.Error("icebase still holds the secret")
	}
	ended := h.auditOf("core_move_ended")
	if len(ended) != 1 || ended[0]["outcome"] != nodes.MoveRolledBack {
		t.Errorf("core_move_ended: %+v", ended)
	}
}

// TestRevokeCoreHostForgets: !revoke of a connected core host that holds
// secrets a move brought it makes it forget them first, and says so.
func TestRevokeCoreHostForgets(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	m := h.moveHosts(h.b.nodes.Version(), true)
	if code, tr, body := h.coreTransfer(m.src, m.dst, true); code != http.StatusOK || !tr.Ran {
		t.Fatalf("the transfer: %d %s", code, body)
	}
	if _, err := m.dstStore.Get(hostsecrets.ClaudeOAuthToken); err != nil {
		t.Fatalf("icebase did not get the secret: %v", err)
	}
	reply := h.Answer(inbound{JID: harnessDM, Text: "!revoke " + m.dst})
	if want := outboundmsg.NodeRevokedReply("icebase", m.dst) + "\n\n" + outboundmsg.NodeRevokedCoreSecretsNote([]string{hostsecrets.ClaudeOAuthToken}, ""); reply != want {
		t.Errorf("!revoke:\n%s\nwant\n%s", reply, want)
	}
	if _, err := m.dstStore.Get(hostsecrets.ClaudeOAuthToken); err == nil {
		t.Error("icebase still holds the secret")
	}
}

// TestApproveFrideSaysWhatItMeans: approving a node that offers fride
// says plainly that it is arbitrary execution there; the dashboard's
// view says it while it waits, and !fleet marks it once approved.
func TestApproveFrideSaysWhatItMeans(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()
	ff := testutil.StartFakeFride(t, "fridolin")
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	dir := t.TempDir()
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: addr, Code: jc.Code, Name: "pc", Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeagent.PairFride(context.Background(), nodeagent.FridePairOptions{Dir: dir, URL: ff.URL, Code: ff.NewCode()}); err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.HeartbeatEvery, agent.FrideCheckEvery = t.Logf, 100*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor("pc offering FrIDE", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && h.b.nodes.Online(j.ID) && n.Declared.Fride != nil
	})
	views, _ := h.b.nodes.Views()
	if len(views) != 1 || views[0].FrideApproval != nodeproto.FrideApproval {
		t.Errorf("the view of a node whose fride waits: %+v", views)
	}
	reply := h.Answer(inbound{JID: harnessDM, Text: "!approve " + j.ID})
	if !strings.HasSuffix(reply, "\n\n"+outboundmsg.NodeFrideApprovedNote(nodeproto.FrideApproval)) {
		t.Errorf("!approve of a node with fride: %q", reply)
	}
	if fleet := h.Answer(inbound{JID: harnessDM, Text: "!fleet"}); !strings.Contains(fleet, "fride: FrIDE's agents can run anything on it") {
		t.Errorf("!fleet: %q", fleet)
	}
}
