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
func (h *harness) coreHostNode(addr, name string, pins ...nodeagent.CoreHostPin) (string, string, *memSecrets, *nodeagent.Agent) {
	h.t.Helper()
	return h.coreHostNodeWith(addr, name, nil, pins...)
}

// coreHostNodeWith is coreHostNode with setup, when set, changing the
// agent before it runs.
func (h *harness) coreHostNodeWith(addr, name string, setup func(*nodeagent.Agent), pins ...nodeagent.CoreHostPin) (string, string, *memSecrets, *nodeagent.Agent) {
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
	st := newMemSecrets()
	agent.Logf, agent.HeartbeatEvery, agent.FrideCheckEvery = t.Logf, 100*time.Millisecond, 100*time.Millisecond
	agent.CoreSecrets, agent.CoreSecretsPlace = st, "test files"
	agent.CoreSetup = func(context.Context) ([]nodeproto.CoreCheckItem, bool, string) { return nil, true, "" }
	if setup != nil {
		setup(agent)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	// The agent's own connection, not Join's, which may still count as
	// online and then drop (#301).
	h.WaitFor(name+" offering core-host", func() bool {
		n, err := h.b.nodes.Registry().Get(j.ID)
		return err == nil && agent.Connected() && h.b.nodes.Online(j.ID) && n.Declared.CoreHost != nil
	})
	return j.ID, dir, st, agent
}

type coreTransfer struct {
	Source, Target struct {
		ID, Fingerprint string
		Stored, Missing []string
	}
	Pinned, Confirmed       bool
	NeedsConfirm            bool `json:"needs_confirm"`
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
// core with two agents in this process: both are core hosts without any
// approval of it, and the owner is not asked; the secrets go towards a
// target the source pins, end to end, and the core sees only names;
// towards one it does not pin, only once the owner confirmed it (their
// vote on a move: never through this API); and not to a machine whose
// core-host is off.
func TestCoreSecretsTransfer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, `{"nodes": {"enabled": true, "listen": "127.0.0.1:8766"}}`)
	addr := h.StartNodes()

	dst, dstDir, dstStore, _ := h.coreHostNode(addr, "icebase")
	dstKey, err := nodeagent.LoadKey(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	dstPub := dstKey.Public().(ed25519.PublicKey)
	pin := nodeagent.CoreHostPin{ID: dst, PublicKey: hex.EncodeToString(dstPub), Name: "icebase", PinnedAt: time.Now().UTC().Truncate(time.Second)}
	src, _, srcStore, _ := h.coreHostNode(addr, "mini", pin)
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

	// Devices not approved: the dry run says so, and a run refuses.
	_, rep, _ := h.coreTransfer(src, dst, false)
	if len(rep.Problems) != 2 || !strings.Contains(rep.Problems[0], "the device is pending") || !strings.Contains(rep.Problems[1], "the device is pending") {
		t.Fatalf("before the approval: %+v", rep.Problems)
	}
	if code, _, _ := h.coreTransfer(src, dst, true); code != http.StatusConflict {
		t.Fatalf("a run before the approval: %d", code)
	}
	// The transfer's steps do not go through the generic call.
	if code, _, _ := h.nodeCall(src, nodeproto.MethodCoreSecretsSend, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("core.secrets.send through /call: %d", code)
	}
	for _, id := range []string{src, dst} {
		if w := h.Do("POST", "/api/nodes/"+id+"/approve", nil); w.Code != http.StatusOK {
			t.Fatalf("approve: %d %s", w.Code, w.Body.String())
		}
	}
	h.WaitFor("both agents serving core-host", func() bool { return h.coreHostApproved(src) && h.coreHostApproved(dst) })

	// The other way round icebase pins nothing, and the owner confirmed
	// no one: nothing goes, and this API cannot confirm it.
	_, rep, _ = h.coreTransfer(dst, src, false)
	if rep.Pinned || rep.Confirmed || !rep.NeedsConfirm || !strings.Contains(strings.Join(rep.Problems, "\n"), "only your vote in WhatsApp") {
		t.Fatalf("towards an unconfirmed target: %+v", rep)
	}
	if code, _, _ := h.coreTransfer(dst, src, true); code != http.StatusConflict {
		t.Fatalf("a run towards an unconfirmed target: %d", code)
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

	// Once the owner confirmed mini (as their vote on a move does), the
	// core names icebase mini's key, and the secrets go back unpinned.
	if _, err := h.b.nodes.Registry().ConfirmCoreHost(src); err != nil {
		t.Fatal(err)
	}
	code, rep, body = h.coreTransfer(dst, src, true)
	if code != http.StatusOK || !rep.Ran || rep.Pinned || !rep.Confirmed || len(rep.Unchanged) != 3 {
		t.Fatalf("towards a confirmed target: %d %s", code, body)
	}
	for _, m := range h.SentTo(harnessDM) {
		if strings.Contains(m, "core-host") {
			t.Errorf("the owner was asked about core-host:\n%s", m)
		}
	}

	// core-host off on icebase: it is no core host any more.
	if err := nodeagent.SaveCoreHost(dstDir, nodeagent.CoreHostConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("icebase no core host", func() bool { return !h.coreHostApproved(dst) })
	if _, rep, _ := h.coreTransfer(src, dst, false); len(rep.Problems) == 0 || !strings.Contains(rep.Problems[0], "is no core host") {
		t.Fatalf("after core-host off: %+v", rep.Problems)
	}
}

// coreHostApproved reports whether node id may serve core-host now: an
// approved device that offers it.
func (h *harness) coreHostApproved(id string) bool {
	n, err := h.b.nodes.Registry().Get(id)
	return err == nil && n.Effective().CoreHost != nil
}

// newMemSecrets is an empty memSecrets (github_test.go): the file store
// refuses Windows' file modes.
func newMemSecrets() *memSecrets { return &memSecrets{m: map[string]string{}} }
