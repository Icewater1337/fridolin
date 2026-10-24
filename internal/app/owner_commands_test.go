package app

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

var (
	joinCodeRE  = regexp.MustCompile(`fj-[a-z2-7]+`)
	loginLinkRE = regexp.MustCompile(`http://\S+/#login=(\S+)`)
)

// TestJoinFromChat: the owner's !join in their DM hands out a join code
// with the lines to paste, which a machine joins with once; the owner
// hears that it joined, lets it in with !approve (device and commands in
// one go) and shuts it out with !revoke. The audit log has each, never
// the code.
func TestJoinFromChat(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()
	_, port, _ := net.SplitHostPort(addr)

	reply := h.Answer(inbound{JID: harnessDM, Text: "!join"})
	code := joinCodeRE.FindString(reply)
	if code == "" || !strings.HasPrefix(reply, "🔑 Join code") {
		t.Fatalf("!join: %q", reply)
	}
	sh, ps := httpapi.JoinCommands(code, "127.0.0.1:"+port)
	if !strings.Contains(reply, sh) || !strings.Contains(reply, ps) {
		t.Errorf("!join lacks the lines to paste %q and %q:\n%s", sh, ps, reply)
	}

	join := func(code string) (nodeagent.Joined, error) {
		return nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: t.TempDir(), Core: addr, Code: code, Name: "laptop",
			Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	}
	j, err := join(code)
	if err != nil {
		t.Fatal(err)
	}
	alert := h.WaitSent(harnessDM, "wants to join")
	if !strings.Contains(alert, "*laptop*") || !strings.Contains(alert, "Reply !approve") || !strings.Contains(alert, "_ref "+j.ID+"_") {
		t.Errorf("the join alert: %q", alert)
	}
	// Single-use.
	if _, err := join(code); err == nil {
		t.Error("a second machine joined with the same code")
	}

	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve one two"}); got != outboundmsg.NodeCommandUsage {
		t.Errorf("!approve with two words: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve nope"}); got != outboundmsg.NodeUnknownReply("nope") {
		t.Errorf("!approve nope: %q", got)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!APPROVE " + j.ID}); !strings.HasPrefix(got, "✅ *laptop* is approved") || !strings.Contains(got, "system details") {
		t.Errorf("!approve: %q", got)
	}
	if v := h.nodeViews(); len(v) != 1 || v[0].Device != nodeproto.DeviceApproved || v[0].Commands != nodeproto.CommandsApproved {
		t.Errorf("after !approve: %+v", v)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!revoke " + j.ID}); got != outboundmsg.NodeRevokedReply("laptop", j.ID) {
		t.Errorf("!revoke: %q", got)
	}
	if v := h.nodeViews(); len(v) != 1 || v[0].Device != nodeproto.DeviceRevoked {
		t.Errorf("after !revoke: %+v", v)
	}
	if got := h.Answer(inbound{JID: harnessDM, Text: "!approve " + j.ID}); got != outboundmsg.NodeIsRevokedReply("laptop", j.ID) {
		t.Errorf("!approve of a revoked node: %q", got)
	}

	for kind, want := range map[string][]string{
		"node_join_code": {"ok"},
		"node_approved":  {"refused: unknown node", "approved", "refused: revoked"},
		"node_revoked":   {"ok"},
	} {
		lines := h.auditOf(kind)
		if len(lines) != len(want) {
			t.Errorf("%s: %v", kind, lines)
			continue
		}
		for i, l := range lines {
			if l["outcome"] != want[i] || l["by"] != "15551234567" {
				t.Errorf("%s line %d: %v", kind, i, l)
			}
		}
	}
	for _, l := range h.auditLines() {
		for _, v := range l {
			if s, ok := v.(string); ok && strings.Contains(s, code) {
				t.Errorf("the audit log has the join code: %v", l)
			}
		}
	}
}

// The owner's commands answer only the owner, and only in their direct
// chat: in a group, even from the owner, and from anyone else they hand
// out nothing.
func TestOwnerCommandsOwnerOnly(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true}, "group_members": {"120363000000000000@g.us": ["*"]}}`)
	for _, cmd := range []string{"!join", "!dashboard", "!approve x", "!revoke x"} {
		got := h.Answer(inbound{JID: harnessGroup, Sender: "15551234567", Text: "@claude " + cmd})
		if got != outboundmsg.OwnerCommandGroupNotice {
			t.Errorf("%s in a group: %q", cmd, got)
		}
	}
	if l := h.auditOf("node_join_code"); len(l) != 1 || l[0]["outcome"] != "refused: a group" {
		t.Errorf("audit of !join in a group: %v", l)
	}

	// A bridge open to every DM has no owner in a chat.
	open := newHarness(t, `{"allowed_numbers": [], "allow_any_dm": true, "nodes": {"enabled": true}}`)
	stranger := strangerNumber + "@s.whatsapp.net"
	for _, cmd := range []string{"!join", "!dashboard", "!approve x", "!revoke x"} {
		got := open.Answer(inbound{JID: stranger, Sender: strangerNumber, Text: cmd})
		if got != outboundmsg.OwnerCommandOwnerOnly {
			t.Errorf("%s from a stranger: %q", cmd, got)
		}
	}
	if l := open.auditOf("dashboard_link"); len(l) != 1 || l[0]["outcome"] != "refused: not the owner" {
		t.Errorf("audit of a stranger's !dashboard: %v", l)
	}
}

// !join and !dashboard answer ownerAsksPerHour times an hour each; with
// nodes off !join says so.
func TestOwnerCommandsRateLimit(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true}}`)
	for _, cmd := range []string{"!join", "!dashboard"} {
		for i := range ownerAsksPerHour {
			if got := h.Answer(inbound{JID: harnessDM, Text: cmd}); strings.HasPrefix(got, "⏳") {
				t.Fatalf("%s %d: %q", cmd, i+1, got)
			}
		}
		if got := h.Answer(inbound{JID: harnessDM, Text: cmd}); got != outboundmsg.OwnerCommandRateLimited(cmd, ownerAsksPerHour) {
			t.Errorf("%s once too often: %q", cmd, got)
		}
	}
	var r ownerCommandRate
	now := time.Now()
	for range ownerAsksPerHour {
		r.allow("!join", now)
	}
	if r.allow("!join", now.Add(59*time.Minute)) || !r.allow("!join", now.Add(time.Hour+time.Second)) {
		t.Error("the limit is not an hour's")
	}

	off := newHarness(t, "")
	if got := off.Answer(inbound{JID: harnessDM, Text: "!join"}); got != outboundmsg.NodesOffNotice {
		t.Errorf("!join with nodes off: %q", got)
	}
}

// !dashboard's link signs one browser in, once, here on the core machine
// (the tailnet's is in TestTailnetSignInWithALink).
func TestDashboardLinkFromChat(t *testing.T) {
	h := newHarness(t, "")
	reply := h.Answer(inbound{JID: harnessDM, Text: "!dashboard"})
	m := loginLinkRE.FindStringSubmatch(reply)
	if m == nil || !strings.Contains(reply, "only on the core machine") || !strings.HasPrefix(m[0], "http://"+h.listen+"/") {
		t.Fatalf("!dashboard: %q", reply)
	}
	login := func() int {
		return h.Do("POST", "/api/login", map[string]string{"code": m[1]}, func(r *http.Request) { r.Header.Del("X-Bridge-Token") }).Code
	}
	if c := login(); c != http.StatusOK {
		t.Fatalf("signing in with the link: %d", c)
	}
	if c := login(); c != http.StatusUnauthorized {
		t.Errorf("the link a second time: %d", c)
	}
	if l := h.auditOf("dashboard_link"); len(l) != 1 || l[0]["outcome"] != "ok" || strings.Contains(l[0]["detail"].(string), m[1]) {
		t.Errorf("audit: %v", l)
	}
}
