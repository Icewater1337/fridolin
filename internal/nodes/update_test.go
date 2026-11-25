package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func TestBinaries(t *testing.T) {
	bins := t.TempDir()
	svc, err := New(Options{Dir: t.TempDir(), Version: "v2.0.0", BinariesDir: bins, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Binary("linux", "arm64"); !errors.Is(err, ErrNoBinary) || !strings.Contains(err.Error(), "make node-binaries") {
		t.Fatalf("no manifest: %v", err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(bins, BinariesManifest), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":"v1.0.0","binaries":[{"os":"linux","arch":"arm64","file":"f.gz","sha256":"x","size":1}]}`)
	if _, _, err := svc.Binary("linux", "arm64"); !errors.Is(err, ErrNoBinary) || !strings.Contains(err.Error(), "v1.0.0") {
		t.Fatalf("binaries of another version: %v", err)
	}
	write(`{"version":"v2.0.0","binaries":[{"os":"linux","arch":"arm64","file":"f.gz","sha256":"x","size":1},{"os":"windows","arch":"amd64","file":"../evil","sha256":"x","size":1}]}`)
	b, path, err := svc.Binary("linux", "arm64")
	if err != nil || b.File != "f.gz" || path != filepath.Join(bins, "f.gz") {
		t.Fatalf("Binary = %+v %s %v", b, path, err)
	}
	if _, _, err := svc.Binary("windows", "amd64"); err == nil {
		t.Fatal("a file outside the folder")
	}
	if _, _, err := svc.Binary("plan9", "386"); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("a platform without a binary: %v", err)
	}
	none, _ := New(Options{Dir: t.TempDir(), Version: "v2.0.0", Logf: t.Logf})
	if _, _, err := none.Binary("linux", "arm64"); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("no folder: %v", err)
	}
}

func TestUpdateKeyIsKept(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(Options{Dir: dir, Logf: t.Logf})
	if _, err := os.Stat(filepath.Join(dir, UpdateKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the key is made before it is needed")
	}
	k1 := a.CoreKey()
	if len(k1) != ed25519.PublicKeySize {
		t.Fatal("no key")
	}
	if st, err := os.Stat(filepath.Join(dir, UpdateKeyFile)); err != nil || st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("the key file: %v %v", st, err)
	}
	b, _ := New(Options{Dir: dir, Logf: t.Logf})
	if !bytes.Equal(b.CoreKey(), k1) {
		t.Fatal("another core on the same folder has another key")
	}
}

func TestUpdateTarget(t *testing.T) {
	svc, _ := New(Options{Dir: t.TempDir(), Version: "v2.0.0", Logf: t.Logf})
	n := &Node{ID: "x", Device: nodeproto.DeviceApproved, Version: "v1.0.0", OS: "linux", Arch: "arm64", CommandsApprovedAt: nil}
	if to, why := svc.UpdateTarget(n); to != "" || !strings.Contains(why, "by hand") {
		t.Fatalf("a node that predates updates: %q %q", to, why)
	}
	n.Declared = nodeproto.Capabilities{Update: &nodeproto.UpdateCap{}}
	if to, why := svc.UpdateTarget(n); to != "" || !strings.Contains(why, "approve") {
		t.Fatalf("update not approved: %q %q", to, why)
	}
	n.Approved = n.Declared
	if to, why := svc.UpdateTarget(n); to != "" || !strings.Contains(why, "make node-binaries") {
		t.Fatalf("no binaries: %q %q", to, why)
	}
	n.Version = "v3.0.0"
	if to, why := svc.UpdateTarget(n); to != "" || why != "" {
		t.Fatalf("a newer node: %q %q", to, why)
	}
}

// The core remembers the versions it ran: auto updates take a node only
// from one of them, whatever its name.
func TestCoreVersionsAreRemembered(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"abc123", "def456", "abc123"} {
		svc, _ := New(Options{Dir: dir, Version: v, Logf: t.Logf})
		svc.noteVersion()
	}
	svc, _ := New(Options{Dir: dir, Version: "0789ff", Logf: t.Logf})
	svc.noteVersion()
	for v, want := range map[string]bool{"abc123": true, "def456": true, "0789ff": false, "fff000": false} {
		if got := svc.ranBefore(v); got != want {
			t.Errorf("ranBefore(%q) = %v", v, got)
		}
	}
}

// The core waits as long as the box says its updater takes, so a box made
// before the hourly updater keeps its shorter wait.
func TestBoxUpdateWait(t *testing.T) {
	for _, c := range []struct {
		minutes int
		want    time.Duration
	}{
		{2, 32 * time.Minute},
		{60, 90 * time.Minute},
		{0, time.Duration(nodeproto.BoxUpdaterMinutes)*time.Minute + 30*time.Minute},
	} {
		if got := boxUpdateWait(c.minutes); got != c.want {
			t.Errorf("boxUpdateWait(%d) = %s, want %s", c.minutes, got, c.want)
		}
	}
}

// A box whose connection ends after it got the update request and before
// its answer reached the core (the updater took the request and recreated
// the node at once, or the network dropped) may keep the request: the
// core waits for it as after an answer, instead of recording a refusal
// the box never made (#769).
func TestABoxThatDisconnectsBeforeItAnswersIsWaitedFor(t *testing.T) {
	conns := make(<-chan string)
	svc := serveNodes(t, func(s *Service) {
		conns = connected(s)
		s.version = "v2"
		s.boxImages = func(context.Context, string) (string, string, error) {
			return "ghcr.io/owner/fridolin-core@sha256:" + strings.Repeat("1", 64), "ghcr.io/owner/fridolin-fride@sha256:" + strings.Repeat("2", 64), nil
		}
	})
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeproto.DeviceIDFor(key.Public().(ed25519.PublicKey))
	ws := dialAsNodeWith(t, svc.Addr(), key, code, nodeproto.Capabilities{Host: true, Update: &nodeproto.UpdateCap{Box: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	asked := make(chan struct{})
	go func() {
		for {
			env, err := readEnvelope(ctx, ws)
			if err != nil {
				return
			}
			if env.Type != nodeproto.TypeRequest {
				continue
			}
			if req, err := nodeproto.DecodeBody[nodeproto.Request](env); err == nil && req.Method == nodeproto.MethodUpdateBox {
				// The box kept the request; it is gone before it answers.
				_ = ws.CloseNow()
				close(asked)
				return
			}
		}
	}()
	waitConnected(t, conns, id)
	if _, err := svc.ApproveDevice(id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveCommands(id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Update(ctx, id, UpdateOptions{}) }()
	select {
	case <-asked:
	case err := <-done:
		t.Fatalf("the update ended before it asked the box: %v", err)
	}
	for {
		select {
		case err := <-done:
			t.Fatalf("the core gave up on a box that may keep the request: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
		if n, err := svc.reg.Get(id); err == nil && n.Update != nil && n.Update.State == UpdateRestarting {
			break
		}
	}
	cancel()
	<-done
}
