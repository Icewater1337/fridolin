package nodeagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

const (
	boxNodeImage  = "ghcr.io/owner/fridolin-core@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	boxFrideImage = "ghcr.io/owner/fridolin-fride@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// boxNode is a joined node in a box whose agent can be started again with
// another version, as the box's updater does when it recreates the node
// service on new images.
type boxNode struct {
	t   *testing.T
	dir string
	cur *running
}

func newBoxNode(t *testing.T, c *core) *boxNode {
	t.Helper()
	dir := t.TempDir()
	no := false
	if _, err := Join(context.Background(), JoinOptions{Dir: dir, Core: c.addr, Code: c.code(t), Name: "box",
		Capabilities: nodeproto.Capabilities{Host: true}, AndroidBuild: &no, Box: &BoxConfig{Hostname: "fridolin-box-box"}, Version: "v1.0.0", Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	b := &boxNode{t: t, dir: dir}
	t.Cleanup(b.stop)
	return b
}

func (b *boxNode) start(version string) *Agent {
	b.t.Helper()
	b.stop()
	a, err := Load(b.dir, version)
	if err != nil {
		b.t.Fatal(err)
	}
	a.Logf, a.MinBackoff, a.MaxBackoff, a.HeartbeatEvery = b.t.Logf, 10*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{agent: a, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- a.Run(ctx) }()
	b.cur = r
	return a
}

func (b *boxNode) stop() {
	if b.cur != nil {
		b.cur.cancel()
		<-b.cur.done
		b.cur = nil
	}
}

func boxImagesOf(node, fride string) func(context.Context, string) (string, string, error) {
	return func(context.Context, string) (string, string, error) { return node, fride, nil }
}

// The core asks a box for the images of its version; the agent keeps the
// signed request; the updater (here by hand) takes it and recreates the
// node on the new version, which connects; the core records the update.
func TestBoxUpdateEndToEnd(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	c := startCoreWith(t, nodes.Options{Version: vB, BoxImages: boxImagesOf(boxNodeImage, boxFrideImage)})
	b := newBoxNode(t, c)
	a := b.start(vA)
	id := a.ID()
	waitFor(t, "the box to connect", func() bool { return c.svc.Online(id) })
	approveUpd(t, c, id)
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })
	if to, why := c.svc.UpdateTarget(nodeOf(t, c, id)); to != vB {
		t.Fatalf("the box is offered %q (%s)", to, why)
	}

	done := make(chan error, 1)
	go func() { done <- c.svc.Update(context.Background(), id, nodes.UpdateOptions{}) }()
	waitFor(t, "the request in the box's volume", func() bool {
		r, err := ReadBoxUpdate(b.dir)
		return err == nil && r != nil
	})
	r, _ := ReadBoxUpdate(b.dir)
	if r.Version != vB || r.NodeImage != boxNodeImage || r.FrideImage != boxFrideImage || r.From != vA {
		t.Fatalf("the request %+v", r)
	}
	if err := VerifyBoxUpdate(b.dir, r); err != nil {
		t.Fatalf("the updater's check: %v", err)
	}
	if err := TakeBoxUpdate(b.dir); err != nil {
		t.Fatal(err)
	}
	// The updater recreates the node on the new image.
	a = b.start(vB)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the core did not see the box come back")
	}
	nd := nodeOf(t, c, id)
	if nd.Version != vB || nd.Update == nil || nd.Update.State != nodes.UpdateOK {
		t.Fatalf("version %s, update %+v", nd.Version, nd.Update)
	}
	// The updater's result reaches the core and goes.
	if err := FinishBoxUpdate(b.dir, nodeproto.UpdateReport{From: vA, To: vB, OK: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the result to be reported", func() bool {
		_, err := os.Stat(filepath.Join(b.dir, BoxUpdateResultFile))
		return errors.Is(err, os.ErrNotExist)
	})
	if err := BoxHealthy(b.dir, time.Minute, time.Now()); err != nil {
		t.Fatalf("a connected box is not healthy: %v", err)
	}
}

// When the updater cannot pull, the box stays on its images and says so;
// the core stops waiting at once.
func TestBoxUpdateFailureIsReported(t *testing.T) {
	const vA, vB = "v1.0.0", "v1.1.0"
	c := startCoreWith(t, nodes.Options{Version: vB, BoxImages: boxImagesOf(boxNodeImage, boxFrideImage)})
	b := newBoxNode(t, c)
	a := b.start(vA)
	id := a.ID()
	waitFor(t, "the box to connect", func() bool { return c.svc.Online(id) })
	approveUpd(t, c, id)
	waitFor(t, "the approval", func() bool { return a.State().Approved.Update != nil })
	done := make(chan error, 1)
	go func() { done <- c.svc.Update(context.Background(), id, nodes.UpdateOptions{}) }()
	waitFor(t, "the request", func() bool { r, _ := ReadBoxUpdate(b.dir); return r != nil })
	if err := TakeBoxUpdate(b.dir); err != nil {
		t.Fatal(err)
	}
	if err := FinishBoxUpdate(b.dir, nodeproto.UpdateReport{From: vA, To: vB, Error: "pulling the node's image failed"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pulling the node's image failed") {
			t.Fatalf("the core says %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the core kept waiting after the box reported the failure")
	}
	if nd := nodeOf(t, c, id); nd.Update == nil || nd.Update.State != nodes.UpdateFailed {
		t.Fatalf("update %+v", nd.Update)
	}
}

// update.box checks the core's signature; a box takes no binary; a
// native node takes no images.
func TestBoxUpdateCallChecks(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	pub := key.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	if err := writeCoreKey(dir, pub); err != nil {
		t.Fatal(err)
	}
	box := &Agent{Dir: dir, Version: "v1.0.0", Config: Config{Box: &BoxConfig{Hostname: "fridolin-box-x"}}, Logf: t.Logf}
	call := func(a *Agent, method string, p any) *nodeproto.Error {
		raw, _ := json.Marshal(p)
		_, e := a.updateCall(context.Background(), method, raw)
		return e
	}
	good := nodeproto.BoxUpdateParams{Version: "v1.1.0", NodeImage: boxNodeImage, FrideImage: boxFrideImage}
	good.Signature = nodeproto.SignBoxUpdate(key, good.Version, good.NodeImage, good.FrideImage)

	_, other, _ := ed25519.GenerateKey(nil)
	forged := good
	forged.Signature = nodeproto.SignBoxUpdate(other, good.Version, good.NodeImage, good.FrideImage)
	if e := call(box, nodeproto.MethodUpdateBox, forged); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("a request signed by another key: %v", e)
	}
	swapped := good
	swapped.NodeImage = "ghcr.io/evil/fridolin-core@sha256:" + strings.Repeat("3", 64)
	if e := call(box, nodeproto.MethodUpdateBox, swapped); e == nil || e.Code != nodeproto.CodeDenied {
		t.Fatalf("a request whose image changed after signing: %v", e)
	}
	tagged := good
	tagged.NodeImage = "ghcr.io/owner/fridolin-core:latest"
	if e := call(box, nodeproto.MethodUpdateBox, tagged); e == nil || e.Code != nodeproto.CodeBadRequest {
		t.Fatalf("an image by tag: %v", e)
	}
	if r, _ := ReadBoxUpdate(dir); r != nil {
		t.Fatalf("a refused request was kept: %+v", r)
	}
	if e := call(box, nodeproto.MethodUpdateBox, good); e != nil {
		t.Fatal(e)
	}
	if r, err := ReadBoxUpdate(dir); err != nil || r == nil || VerifyBoxUpdate(dir, r) != nil {
		t.Fatalf("the kept request %+v %v", r, err)
	}
	if e := call(box, nodeproto.MethodUpdateWrite, nodeproto.FrideChunkParams{}); e == nil || e.Code != nodeproto.CodeUnsupported {
		t.Fatalf("a box took a binary: %v", e)
	}
	native := &Agent{Dir: t.TempDir(), Version: "v1.0.0", Logf: t.Logf}
	if e := call(native, nodeproto.MethodUpdateBox, good); e == nil || e.Code != nodeproto.CodeUnsupported {
		t.Fatalf("a native node took images: %v", e)
	}
}

func TestBoxHealthy(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := BoxHealthy(dir, time.Minute, now); err == nil {
		t.Fatal("healthy before the agent ever connected")
	}
	a := &Agent{Dir: dir, Version: "v1", Config: Config{Box: &BoxConfig{Hostname: "fridolin-box-x"}}, Logf: t.Logf}
	a.boxStatus(true)
	if err := BoxHealthy(dir, time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := BoxHealthy(dir, time.Minute, time.Now().Add(2*time.Minute)); err == nil {
		t.Fatal("healthy with a stale status")
	}
	a.boxStatus(false)
	if err := BoxHealthy(dir, time.Minute, time.Now()); err == nil {
		t.Fatal("healthy while disconnected")
	}
}
