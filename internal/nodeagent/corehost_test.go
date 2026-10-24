package nodeagent

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// coreHostAgent is an agent with core-host on and its host secrets in a
// folder of files.
func coreHostAgent(t *testing.T, name string) (*Agent, *testutil.Secrets) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testutil.NewSecrets()
	a := &Agent{Dir: t.TempDir(), Key: key, Config: Config{Core: "x", Name: name}, CoreSecrets: st, Logf: t.Logf,
		CoreSetup: func(context.Context) ([]nodeproto.CoreCheckItem, bool, string) { return nil, true, "" }}
	if err := SaveCoreHost(a.Dir, CoreHostConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func trust(t *testing.T, src, dst *Agent) {
	t.Helper()
	c, err := LoadCoreHost(src.Dir)
	if err != nil {
		t.Fatal(err)
	}
	pub := dst.Key.Public().(ed25519.PublicKey)
	c.SendTo = append(c.SendTo, CoreHostPin{ID: dst.ID(), PublicKey: hex.EncodeToString(pub), Name: dst.Config.Name, PinnedAt: time.Now()})
	if err := SaveCoreHost(src.Dir, c); err != nil {
		t.Fatal(err)
	}
}

func callCore[T any](t *testing.T, a *Agent, method string, params any) (T, *nodeproto.Error) {
	t.Helper()
	var out T
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, perr := a.coreHostCall(context.Background(), method, raw)
	if perr != nil {
		return out, perr
	}
	b, _ := json.Marshal(res)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

const testMove = "m-20261024-0900-7f2a"

// TestCoreHostDeclaresItsFile: core-host.json, and every change of it,
// is what the agent declares, as a hash the core's approval covers.
func TestCoreHostDeclaresItsFile(t *testing.T) {
	a, _ := coreHostAgent(t, "mini")
	b, _ := coreHostAgent(t, "icebase")
	ch := a.coreHostDeclared()
	if ch == nil || ch.Check() != nil || len(ch.SendTo) != 0 {
		t.Fatalf("declared %+v", ch)
	}
	a.coreHostChanged()
	trust(t, a, b)
	if !a.coreHostChanged() {
		t.Fatal("a new pin is no change")
	}
	if now := a.coreHostDeclared(); now.Hash == ch.Hash || len(now.SendTo) != 1 || now.SendTo[0] != b.ID() {
		t.Fatalf("after trust: %+v", now)
	}
	if err := SaveCoreHost(a.Dir, CoreHostConfig{}); err != nil {
		t.Fatal(err)
	}
	if a.coreHostDeclared() != nil || !a.coreHostChanged() {
		t.Fatal("core-host off is still declared")
	}
	cfg := Config{Core: "x", Name: "n", Capabilities: nodeproto.Capabilities{CoreHost: &nodeproto.CoreHostCap{Hash: strings.Repeat("a", 64)}}}
	if err := cfg.Check(); err == nil {
		t.Fatal("node.json may not turn core-host on")
	}
}

// TestCoreSecretsEndToEnd moves the secrets from one agent to another
// as the core relays them, and forgets them on the target.
func TestCoreSecretsEndToEnd(t *testing.T) {
	src, srcStore := coreHostAgent(t, "mini")
	dst, dstStore := coreHostAgent(t, "icebase")
	trust(t, src, dst)
	values := map[string]string{hostsecrets.AgeIdentityPQ: "AGE-SECRET-KEY-PQ-1TEST", hostsecrets.ClaudeOAuthToken: "test-oauth-test", hostsecrets.B2VaultKey: "K005test"}
	for n, v := range values {
		if err := srcStore.Set(n, v); err != nil {
			t.Fatal(err)
		}
	}
	// The target already holds one of them, with the same value.
	if err := dstStore.Set(hostsecrets.B2VaultKey, "K005test"); err != nil {
		t.Fatal(err)
	}

	offer, perr := callCore[nodeproto.CoreSecretsOffer](t, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	if perr != nil {
		t.Fatal(perr)
	}
	bundle, perr := callCore[nodeproto.CoreSecretsBundle](t, src, nodeproto.MethodCoreSecretsSend, nodeproto.CoreSecretsSendParams{Offer: offer})
	if perr != nil {
		t.Fatal(perr)
	}
	if len(bundle.Names) != 3 || len(bundle.Missing) != len(secretxfer.MoveNames)-3 {
		t.Fatalf("the bundle carries %v, misses %v", bundle.Names, bundle.Missing)
	}
	for _, v := range values {
		if strings.Contains(string(bundle.Ciphertext), v) {
			t.Fatal("a value in clear in the bundle")
		}
	}
	got, perr := callCore[nodeproto.CoreSecretsReceived](t, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: testMove, From: src.ID(), Ciphertext: bundle.Ciphertext})
	if perr != nil {
		t.Fatal(perr)
	}
	if len(got.Stored) != 2 || len(got.Unchanged) != 1 || got.Unchanged[0] != hostsecrets.B2VaultKey {
		t.Fatalf("received %+v", got)
	}
	for n, v := range values {
		if have, err := dstStore.Get(n); err != nil || have != v {
			t.Errorf("%s on the target: %v", n, err)
		}
	}
	// One bundle per offer.
	if _, perr := callCore[nodeproto.CoreSecretsReceived](t, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: testMove, From: src.ID(), Ciphertext: bundle.Ciphertext}); perr == nil || perr.Code != nodeproto.CodeDenied {
		t.Fatalf("a bundle received twice: %v", perr)
	}
	// Both logs have names, never a value.
	for _, a := range []*Agent{src, dst} {
		data, err := os.ReadFile(filepath.Join(a.Dir, TransferLogFile))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			if strings.Contains(string(data), v) {
				t.Fatalf("a value in %s's transfer log", a.Config.Name)
			}
		}
	}
	check, _ := callCore[nodeproto.CoreCheckResult](t, dst, nodeproto.MethodCoreCheck, nil)
	received := 0
	for _, s := range check.Secrets {
		if s.Received {
			received++
		}
	}
	if received != 2 || check.Fingerprint != nodeproto.Fingerprint(dst.Key.Public().(ed25519.PublicKey)) {
		t.Fatalf("core.check: %d received, %+v", received, check)
	}

	// Forget removes what came over the node channel, not what was there.
	gone, perr := callCore[nodeproto.CoreSecretsForgotten](t, dst, nodeproto.MethodCoreSecretsForget, struct{}{})
	if perr != nil || len(gone.Forgotten) != 2 {
		t.Fatalf("forget: %+v %v", gone, perr)
	}
	if ok, _ := dstStore.Has(hostsecrets.B2VaultKey); !ok {
		t.Error("forget removed a secret the target held before")
	}
	if ok, _ := dstStore.Has(hostsecrets.ClaudeOAuthToken); ok {
		t.Error("forget left a received secret")
	}
}

// TestCoreSecretsSendRefuses: a source sends only to an offer signed by
// the device key it pinned for the target. A core that swaps the offer's
// key, signs with a key of its own, or asks for a target the source does
// not trust gets nothing.
func TestCoreSecretsSendRefuses(t *testing.T) {
	src, srcStore := coreHostAgent(t, "mini")
	dst, _ := coreHostAgent(t, "icebase")
	other, _ := coreHostAgent(t, "stranger")
	trust(t, src, dst)
	if err := srcStore.Set(hostsecrets.ClaudeOAuthToken, "test-oauth-test"); err != nil {
		t.Fatal(err)
	}
	offer, perr := callCore[nodeproto.CoreSecretsOffer](t, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	if perr != nil {
		t.Fatal(perr)
	}
	mine, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, coreKey, _ := ed25519.GenerateKey(nil)

	swapped := offer
	swapped.Recipient = mine.Recipient().String()
	resigned := swapped
	resigned.Signature = nodeproto.SignCoreSecretsOffer(coreKey, resigned)
	unpinned, perr := callCore[nodeproto.CoreSecretsOffer](t, other, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	if perr != nil {
		t.Fatal(perr)
	}
	expired := offer
	expired.Expires = time.Now().Add(-time.Minute).Truncate(time.Second)
	expired.Signature = nodeproto.SignCoreSecretsOffer(dst.Key, expired)
	own, _ := callCore[nodeproto.CoreSecretsOffer](t, src, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: "m-20261024-0900-beef"})

	for name, o := range map[string]nodeproto.CoreSecretsOffer{
		"the key swapped":        swapped,
		"signed by the core":     resigned,
		"an unpinned target":     unpinned,
		"expired":                expired,
		"the source's own offer": own,
	} {
		_, perr := callCore[nodeproto.CoreSecretsBundle](t, src, nodeproto.MethodCoreSecretsSend, nodeproto.CoreSecretsSendParams{Offer: o})
		if perr == nil || perr.Code != nodeproto.CodeDenied {
			t.Errorf("%s: sent (%v)", name, perr)
		}
	}
	if _, perr := callCore[nodeproto.CoreSecretsBundle](t, src, nodeproto.MethodCoreSecretsSend, nodeproto.CoreSecretsSendParams{Offer: offer}); perr != nil {
		t.Fatalf("the real offer: %v", perr)
	}
	data, _ := os.ReadFile(filepath.Join(src.Dir, TransferLogFile))
	if strings.Count(string(data), `"refused: `) != 5 {
		t.Errorf("the refusals are not logged:\n%s", data)
	}
}

// TestCoreSecretsReceiveRefusesUnknownNames: a bundle with a name a move
// does not carry is refused whole, and nothing of it is stored.
func TestCoreSecretsReceiveRefusesUnknownNames(t *testing.T) {
	dst, dstStore := coreHostAgent(t, "icebase")
	offer, perr := callCore[nodeproto.CoreSecretsOffer](t, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	if perr != nil {
		t.Fatal(perr)
	}
	ct, err := secretxfer.SealBundle("mini", map[string]string{hostsecrets.ClaudeOAuthToken: "test-oauth-test", hostsecrets.TailscaleAuthKey: "tskey-auth-test"}, offer.Recipient)
	if err != nil {
		t.Fatal(err)
	}
	_, perr = callCore[nodeproto.CoreSecretsReceived](t, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: testMove, From: "4f1e2d3c4b5a", Ciphertext: ct})
	if perr == nil || !strings.Contains(perr.Message, hostsecrets.TailscaleAuthKey) {
		t.Fatalf("a bundle with the Tailscale key: %v", perr)
	}
	if ok, _ := dstStore.Has(hostsecrets.ClaudeOAuthToken); ok {
		t.Fatal("part of a refused bundle was stored")
	}
	// A value that differs from the stored one waits for replace.
	if err := dstStore.Set(hostsecrets.ClaudeOAuthToken, "test-oauth-old"); err != nil {
		t.Fatal(err)
	}
	offer, _ = callCore[nodeproto.CoreSecretsOffer](t, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	ct, _ = secretxfer.SealBundle("mini", map[string]string{hostsecrets.ClaudeOAuthToken: "test-oauth-new"}, offer.Recipient)
	if _, perr = callCore[nodeproto.CoreSecretsReceived](t, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: testMove, From: "4f1e2d3c4b5a", Ciphertext: ct}); perr == nil {
		t.Fatal("a different value replaced the stored one without replace")
	}
	if v, _ := dstStore.Get(hostsecrets.ClaudeOAuthToken); v != "test-oauth-old" {
		t.Fatal("the stored value changed")
	}
}
