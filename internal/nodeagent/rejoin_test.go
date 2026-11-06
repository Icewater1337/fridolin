package nodeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// boxStore is the sealed store of the box in dir, under its node.key.
func boxStore(t *testing.T, dir string) (*Agent, hostsecrets.Store) {
	t.Helper()
	a, err := Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	k, err := CoreSecretsKey(a.Key)
	if err != nil {
		t.Fatal(err)
	}
	a.Logf = t.Logf
	return a, BoxSecretStore(dir, k)
}

// A rejoin makes a new key, so what was sealed with the old one no longer
// opens: it moves aside with the key, and the box is a fresh standby box
// that can take secrets and start a core.
func TestRejoinMovesTheOldStoresAside(t *testing.T) {
	c := startCore(t, t.TempDir())
	dir := t.TempDir()
	join := func() Joined {
		t.Helper()
		j, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: c.code(t), Name: "box",
			Capabilities: nodeproto.Capabilities{Host: true}, Box: &BoxConfig{Hostname: BoxHostname("box")}, Version: "test", Logf: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	first := join()
	if m, _ := filepath.Glob(filepath.Join(dir, "rejoined-*")); len(m) != 0 {
		t.Fatalf("a first join moved something aside: %v", m)
	}
	old, oldStore := boxStore(t, dir)
	if err := oldStore.Set(hostsecrets.ClaudeOAuthToken, "old-token"); err != nil {
		t.Fatal(err)
	}
	ns, err := nodeSecretStore(dir, old.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.Set(hostsecrets.ClaudeOAuthToken, "old-node-token"); err != nil {
		t.Fatal(err)
	}
	if err := saveCoreRole(dir, RoleCore); err != nil {
		t.Fatal(err)
	}
	// The pin of another core, which the rejoin must not keep.
	otherCore, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := writeCoreKey(dir, otherCore); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{coreReceivedFile, filepath.Join(coreMovesDir, testMove+".json")} {
		if err := writeFile(filepath.Join(dir, f), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// What the box made and what its owner set stay.
	if err := SaveCoreHost(dir, CoreHostConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}

	again := join()
	if again.ID == first.ID {
		t.Fatal("the rejoin kept the device")
	}

	m, _ := filepath.Glob(filepath.Join(dir, "rejoined-*"))
	if len(m) != 1 {
		t.Fatalf("aside folders: %v", m)
	}
	aside := m[0]
	if fi, err := os.Stat(aside); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Fatalf("the aside folder holds a key: %v, %v", fi, err)
	}
	for _, name := range []string{KeyFile, coreSecretsDir, nodeSecretsDir, CoreRoleFile, coreReceivedFile, CoreKeyFile, coreMovesDir} {
		if _, err := os.Stat(filepath.Join(aside, name)); err != nil {
			t.Errorf("%s did not move aside: %v", name, err)
		}
	}
	// What moved still opens, with the key that moved with it.
	oldKey, err := LoadKey(aside)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := CoreSecretsKey(oldKey)
	if v, err := BoxSecretStore(aside, k).Get(hostsecrets.ClaudeOAuthToken); err != nil || v != "old-token" {
		t.Errorf("the old secret: %q, %v", v, err)
	}

	if role, err := LoadCoreRole(dir); err != nil || role != RoleStandby {
		t.Errorf("role %q, %v", role, err)
	}
	if _, err := os.Stat(filepath.Join(dir, coreReceivedFile)); !os.IsNotExist(err) {
		t.Errorf("the received records stayed: %v", err)
	}
	if ch, err := LoadCoreHost(dir); err != nil || ch.Enabled {
		t.Errorf("core-host.json did not stay: %+v, %v", ch, err)
	}
	// The rejoin pins the core it joined, as a first join does.
	pin, err := readCoreKey(dir)
	if err != nil || pin == nil || slices.Equal(pin, []byte(otherCore)) {
		t.Errorf("the pin after the rejoin: %x, %v", pin, err)
	}

	// The box receives secrets again, even without replace, and starts a
	// core with them.
	src, srcStore := coreHostAgent(t, "source")
	if err := srcStore.Set(hostsecrets.ClaudeOAuthToken, "new-token"); err != nil {
		t.Fatal(err)
	}
	dst, st := boxStore(t, dir)
	dst.CoreSecrets = st
	if err := SaveCoreHost(dir, CoreHostConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	trust(t, src, dst)
	offer, perr := callCore[nodeproto.CoreSecretsOffer](t, dst, nodeproto.MethodCoreSecretsOffer, nodeproto.CoreSecretsOfferParams{Move: testMove})
	if perr != nil {
		t.Fatal(perr)
	}
	bundle, perr := callCore[nodeproto.CoreSecretsBundle](t, src, nodeproto.MethodCoreSecretsSend, nodeproto.CoreSecretsSendParams{Offer: offer})
	if perr != nil {
		t.Fatal(perr)
	}
	if _, perr := callCore[nodeproto.CoreSecretsReceived](t, dst, nodeproto.MethodCoreSecretsReceive, nodeproto.CoreSecretsReceiveParams{Move: testMove, From: src.ID(), Ciphertext: bundle.Ciphertext}); perr != nil {
		t.Fatalf("receiving after the rejoin: %v", perr)
	}
	env, err := (&BoxCore{Secrets: st}).env(t.TempDir())
	if err != nil {
		t.Fatalf("the core's environment after the rejoin: %v", err)
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasSuffix(kv, "=new-token") }) {
		t.Errorf("the received secret is not in the core's environment")
	}
}

// A join that stops at its checks has not begun: nothing moves.
func TestRejoinWithoutACodeMovesNothing(t *testing.T) {
	c := startCore(t, t.TempDir())
	dir := t.TempDir()
	if _, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: c.code(t), Name: "box", Version: "test", Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	if _, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Name: "box", Version: "test", Logf: t.Logf}); err == nil {
		t.Fatal("a join without a code went on")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "rejoined-*")); len(m) != 0 {
		t.Fatalf("moved aside: %v", m)
	}
}
