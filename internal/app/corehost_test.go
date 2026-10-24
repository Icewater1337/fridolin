package app

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// coreHostNode joins a node named name with core-host on in its
// core-host.json (pins: the targets it trusts), keeping its host secrets
// in a folder of files, and runs its agent in this process.
func (h *harness) coreHostNode(addr, name string, pins ...nodeagent.CoreHostPin) (string, string, hostsecrets.Files, *nodeagent.Agent) {
	h.t.Helper()
	t := h.t
	w := h.Do("POST", "/api/nodes/join-code", map[string]any{})
	var jc struct{ Code string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jc) != nil {
		t.Fatalf("join-code: %d %s", w.Code, w.Body.String())
	}
	dir := t.TempDir()
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: addr, Code: jc.Code, Name: name, Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: noAndroidBuild, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveCoreHost(dir, nodeagent.CoreHostConfig{Enabled: true, SendTo: pins}); err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st := hostsecrets.Files{Dir: t.TempDir()}
	agent.Logf, agent.HeartbeatEvery, agent.FrideCheckEvery = t.Logf, 100*time.Millisecond, 100*time.Millisecond
	agent.CoreSecrets, agent.CoreSecretsPlace = st, "test files"
	agent.CoreSetup = func(context.Context) ([]nodeproto.CoreCheckItem, bool, string) { return nil, true, "" }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h.WaitFor(name+" offering core-host", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && h.b.nodes.Online(j.ID) && n.Declared.CoreHost != nil
	})
	return j.ID, dir, st, agent
}

type coreTransfer struct {
	Source, Target struct {
		ID, Fingerprint string
		Stored, Missing []string
	}
	Pinned                  bool
	Problems                []string
	Ran                     bool
	Move                    string
	Sent, Stored, Unchanged []string
	Error                   string
}

func (h *harness) coreTransfer(src, dst string, run bool) (int, coreTransfer, string) {
	h.t.Helper()
	w := h.Do("POST", "/api/nodes/core-secrets-transfer", map[string]any{"source": src, "target": dst, "run": run})
	var rep coreTransfer
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		h.t.Fatalf("core-secrets-transfer: %d %s", w.Code, w.Body.String())
	}
	return w.Code, rep, w.Body.String()
}

// TestCoreSecretsTransfer is #187's first slice end to end on a dev-mode
// core with two agents in this process: both turn core-host on and the
// owner hears of it with the device key; nothing moves before the owner
// approved it, nor towards a target the source does not pin; once
// approved, the secrets go end to end and the core sees only names; and
// a change of core-host.json waits for approval again.
func TestCoreSecretsTransfer(t *testing.T) {
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()

	dst, dstDir, dstStore, dstAgent := h.coreHostNode(addr, "icebase")
	dstKey, err := nodeagent.LoadKey(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	dstPub := dstKey.Public().(ed25519.PublicKey)
	pin := nodeagent.CoreHostPin{ID: dst, PublicKey: hex.EncodeToString(dstPub), Name: "icebase", PinnedAt: time.Now().UTC().Truncate(time.Second)}
	src, srcDir, srcStore, srcAgent := h.coreHostNode(addr, "mini", pin)
	values := map[string]string{
		hostsecrets.AgeIdentityPQ:    "AGE-SECRET-KEY-PQ-1E2ETEST",
		hostsecrets.ExportKey:        "export-key-e2e-test",
		hostsecrets.ClaudeOAuthToken: "test-oauth-e2e-value",
	}
	for n, v := range values {
		if err := srcStore.Set(n, v); err != nil {
			t.Fatal(err)
		}
	}

	// The owner hears of both, with what approving means and the key.
	alert := h.WaitSent(harnessDM, "*icebase* ("+dst+") offers core-host")
	for _, want := range []string{nodeproto.CoreHostApproval, nodeproto.Fingerprint(dstPub), "!approve " + dst} {
		if !strings.Contains(alert, want) {
			t.Errorf("the alert lacks %q:\n%s", want, alert)
		}
	}
	if alert := h.WaitSent(harnessDM, "*mini* ("+src+") offers core-host"); !strings.Contains(alert, "Sends secrets only to: icebase ("+dst+")") {
		t.Errorf("the alert does not name the trusted target:\n%s", alert)
	}

	// Not approved: the dry run says so, and a run refuses. mini's
	// device is approved, its core-host is not.
	if w := h.Do("POST", "/api/nodes/"+src+"/approve", nil); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	_, rep, _ := h.coreTransfer(src, dst, false)
	if len(rep.Problems) != 2 || !strings.Contains(rep.Problems[0], "core-host waits for your approval") || !strings.Contains(rep.Problems[1], "the device is pending") {
		t.Fatalf("before the approval: %+v", rep.Problems)
	}
	if code, _, _ := h.coreTransfer(src, dst, true); code != http.StatusConflict {
		t.Fatalf("a run before the approval: %d", code)
	}
	// The transfer's steps do not go through the generic call.
	if code, _, _ := h.nodeCall(src, nodeproto.MethodCoreSecretsSend, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("core.secrets.send through /call: %d", code)
	}

	// !approve says what core-host means; the Nodes page approves the other.
	h.Inject(inbound{JID: harnessDM, Text: "!approve " + dst})
	h.WaitSent(harnessDM, "That includes core-host")
	if w := h.Do("POST", "/api/nodes/"+src+"/approve-commands", nil); w.Code != http.StatusOK {
		t.Fatalf("approve-commands: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("both agents approved for core-host", func() bool {
		return srcAgent.State().Approved.CoreHost != nil && dstAgent.State().Approved.CoreHost != nil
	})
	h.Inject(inbound{JID: harnessDM, Text: "!fleet"})
	if fleet := h.WaitSent(harnessDM, "*Fleet*"); !strings.Contains(fleet, "core-host (trusts 1 target)") {
		t.Errorf("!fleet does not show core-host:\n%s", fleet)
	}

	// The other way round, icebase does not trust mini: nothing goes.
	_, rep, _ = h.coreTransfer(dst, src, false)
	if rep.Pinned || len(rep.Problems) == 0 || !strings.Contains(strings.Join(rep.Problems, "\n"), "does not trust") {
		t.Fatalf("towards an unpinned target: %+v", rep)
	}
	if code, _, _ := h.coreTransfer(dst, src, true); code != http.StatusConflict {
		t.Fatalf("a run towards an unpinned target: %d", code)
	}

	// A dry run moves nothing.
	code, rep, _ := h.coreTransfer(src, dst, false)
	if code != http.StatusOK || !rep.Pinned || len(rep.Problems) != 0 || rep.Ran || len(rep.Source.Stored) != 3 || len(rep.Target.Stored) != 0 {
		t.Fatalf("the dry run: %d %+v", code, rep)
	}
	if rep.Target.Fingerprint != nodeproto.Fingerprint(dstPub) {
		t.Errorf("the target's fingerprint %q", rep.Target.Fingerprint)
	}
	if ok, _ := dstStore.Has(hostsecrets.ClaudeOAuthToken); ok {
		t.Fatal("the dry run sent something")
	}

	// The run: end to end, names only through the core.
	code, rep, body := h.coreTransfer(src, dst, true)
	if code != http.StatusOK || !rep.Ran || len(rep.Sent) != 3 || len(rep.Stored) != 3 || nodeproto.CheckMoveID(rep.Move) != nil {
		t.Fatalf("the run: %d %s", code, body)
	}
	for n, v := range values {
		if have, err := dstStore.Get(n); err != nil || have != v {
			t.Errorf("%s on icebase: %v", n, err)
		}
		if strings.Contains(body, v) {
			t.Errorf("the core's answer holds the value of %s", n)
		}
		for _, m := range h.Outbox() {
			if strings.Contains(m.Text, v) {
				t.Errorf("a chat message holds the value of %s", n)
			}
		}
	}

	// core-host.json changed on mini (another target trusted, or the
	// pin gone): it waits for the owner again, and nothing goes.
	if err := nodeagent.SaveCoreHost(srcDir, nodeagent.CoreHostConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(harnessDM, "*mini* ("+src+") changed its core-host settings")
	h.WaitFor("mini's core-host pending again", func() bool { return srcAgent.State().Approved.CoreHost == nil || !h.coreHostApproved(src) })
	if _, rep, _ := h.coreTransfer(src, dst, false); len(rep.Problems) == 0 || !strings.Contains(rep.Problems[0], "waits for your approval") {
		t.Fatalf("after core-host.json changed: %+v", rep.Problems)
	}
}

// coreHostApproved reports whether the approval of node id covers the
// core-host.json it declares now.
func (h *harness) coreHostApproved(id string) bool {
	n, err := h.b.nodes.Registry().Get(id)
	return err == nil && n.Effective().CoreHost != nil
}
