package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func joinedState(t *testing.T, name string) (string, ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"core": "c:1", "name": "` + name + `", "capabilities": {"host": true}}`
	if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := nodeagent.NewKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, key
}

// TestNodeCoreHost turns core-host on, pins a target's key as its show
// prints it, and refuses a key that is not the target's.
func TestNodeCoreHost(t *testing.T) {
	src, _ := joinedState(t, "mini")
	dst, dstKey := joinedState(t, "icebase")
	run := func(dir string, args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := nodeCmd(append(append([]string{"core-host"}, args...), "--state-dir", dir), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, out, _ := run(src, "show"); code != 0 || !strings.Contains(out, "core-host: off") {
		t.Fatalf("show before on: %d %q", code, out)
	}
	if code, out, e := run(src, "on"); code != 0 || !strings.Contains(out, "core-host is on") {
		t.Fatalf("on: %d %q %q", code, out, e)
	}
	ch, err := nodeagent.LoadCoreHost(src)
	if err != nil || !ch.Enabled {
		t.Fatalf("core-host.json after on: %+v %v", ch, err)
	}

	pub := dstKey.Public().(ed25519.PublicKey)
	id := nodeproto.DeviceIDFor(pub)
	_, show, _ := run(dst, "show")
	if !strings.Contains(show, hex.EncodeToString(pub)) || !strings.Contains(show, nodeproto.Fingerprint(pub)) {
		t.Fatalf("show prints no key and fingerprint: %q", show)
	}

	// Someone else's key under the target's id.
	_, other, _ := ed25519.GenerateKey(nil)
	if code, _, e := run(src, "trust", id, "--key", hex.EncodeToString(other.Public().(ed25519.PublicKey))); code == 0 || !strings.Contains(e, "nothing was pinned") {
		t.Fatalf("a key that is not the target's: %d %q", code, e)
	}
	if code, _, _ := run(src, "trust", id); code != 2 {
		t.Fatalf("trust without --key: %d", code)
	}
	code, out, e := run(src, "trust", id, "--key", hex.EncodeToString(pub), "--name", "icebase")
	if code != 0 || !strings.Contains(out, nodeproto.Fingerprint(pub)) {
		t.Fatalf("trust: %d %q %q", code, out, e)
	}
	ch, _ = nodeagent.LoadCoreHost(src)
	if p, ok := ch.Pin(id); !ok || p.Name != "icebase" {
		t.Fatalf("the pin: %+v", ch)
	}
	if _, out, _ := run(src, "show"); !strings.Contains(out, "icebase ("+id+")") {
		t.Fatalf("show lists no pin: %q", out)
	}
	if code, _, _ := run(src, "untrust", id); code != 0 {
		t.Fatal("untrust")
	}
	if code, _, _ := run(src, "off"); code != 0 {
		t.Fatal("off")
	}
	ch, _ = nodeagent.LoadCoreHost(src)
	if ch.Enabled || len(ch.SendTo) != 0 {
		t.Fatalf("after untrust and off: %+v", ch)
	}
}
