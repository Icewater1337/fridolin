package noderepo

import (
	"bytes"
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// resticBinary is restic where the core finds it; tests that need it skip
// without it.
func resticBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/restic", "/usr/local/bin/restic", "/usr/bin/restic"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("restic is not installed")
	return ""
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fleet is a core's node port with one node agent, in this process, whose
// backup folder is folder, approved.
func fleet(t *testing.T, folder string) (*nodes.Service, string) {
	t.Helper()
	svc, err := nodes.New(nodes.Options{Dir: t.TempDir(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caps := nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{filepath.Dir(folder)}}}
	if _, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: svc.Addr(), Code: code, Name: "nas", Capabilities: caps, Version: "test", Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	a, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Logf, a.MinBackoff, a.MaxBackoff = t.Logf, 10*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "the node", a.Connected)
	for _, step := range []func(string) (*nodes.Node, error){svc.ApproveDevice, svc.ApproveCommands} {
		if _, err := step(a.ID()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ApproveBackupFolder(a.ID(), folder); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the folder approved", func() bool { return a.State().Approved.AllowsBackupFolder(folder) })
	return svc, a.ID()
}

// TestResticThroughTheNode: the real restic on the "core" makes a
// repository in a folder "on the node", backs up, checks, restores and
// prunes through the node channel; the folder is then an ordinary local
// repository restic opens directly, as on another machine.
func TestResticThroughTheNode(t *testing.T) {
	t.Parallel()
	bin := resticBinary(t)
	disk := t.TempDir()
	folder := filepath.Join(disk, "fridolin-backup")
	svc, id := fleet(t, folder)

	srv, err := Start(svc, id, folder)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	pw := "correct horse battery staple"
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "RESTIC_PASSWORD=" + pw}
	r := &snapshot.Restic{Binary: bin, Repo: srv.Repo(), Env: append(append([]string{}, base...), srv.Env()...), Secrets: []string{pw, srv.Secret()}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if ok, err := r.Exists(ctx); err != nil || ok {
		t.Fatalf("no repository yet: %v %v", ok, err)
	}
	if err := r.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	big := make([]byte, 12<<20) // more than one chunk, and incompressible
	_, _ = rand.Read(big)
	files := map[string][]byte{"brain.json": []byte(`{"id":"work"}`), "notes/a.md": []byte("# a"), "big.bin": big}
	for name, data := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := r.Backup(ctx, snapshot.BackupOptions{Paths: []string{src}, Tags: []string{"fridolin", "nightly"}, Host: "fridolin-core"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalFilesProcessed != 3 {
		t.Fatalf("backup: %+v", sum)
	}
	if err := r.Check(ctx, "100%"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes/b.md"), []byte("# b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx, snapshot.BackupOptions{Paths: []string{src}, Tags: []string{"fridolin", "hourly"}, Host: "fridolin-core"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(ctx, snapshot.Retention{Hourly: 1}, []string{"fridolin"}, true); err != nil {
		t.Fatal(err)
	}
	restore := func(t *testing.T, rr *snapshot.Restic) {
		t.Helper()
		target := t.TempDir()
		if err := rr.Restore(ctx, "latest", target); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(src), "/")), filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s restored wrong: %v", name, err)
			}
		}
	}
	// What only reads the repository runs side by side, as restic allows:
	// each restic opens it with a key derivation of half a second or so.
	t.Run("read", func(t *testing.T) {
		t.Run("snapshots", func(t *testing.T) {
			t.Parallel()
			snaps, err := r.Snapshots(ctx, nil)
			if err != nil || len(snaps) == 0 {
				t.Fatalf("snapshots: %v %v", snaps, err)
			}
		})
		t.Run("restore through the node", func(t *testing.T) {
			t.Parallel()
			restore(t, r)
		})
		// On another machine: the folder alone, as a local repository.
		t.Run("restore from the folder", func(t *testing.T) {
			t.Parallel()
			restore(t, &snapshot.Restic{Binary: bin, Repo: folder, Env: base, Secrets: []string{pw}})
		})
	})
	if t.Failed() {
		return
	}

	if info, err := svc.BackupInfo(ctx, id, folder); err != nil || !info.Repo || info.Size < int64(len(big)) {
		t.Fatalf("info: %+v %v", info, err)
	}
	// The data on the node is restic's ciphertext: no plain file in it.
	_ = filepath.Walk(folder, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte(`{"id":"work"}`)) {
				t.Errorf("%s holds plain text of the backup", p)
			}
		}
		return nil
	})
}

func TestServerRefusesWithoutItsPassword(t *testing.T) {
	srv, err := Start(nil, "n", "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	resp, err := http.Get("http://" + srv.ln.Addr().String() + "/config")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestParseRange(t *testing.T) {
	for in, want := range map[string][2]int64{"": {0, -1}, "bytes=5-": {5, -1}, "bytes=0-9": {0, 9}} {
		a, b, err := parseRange(in)
		if err != nil || a != want[0] || b != want[1] {
			t.Errorf("%q: %d %d %v", in, a, b, err)
		}
	}
	for _, bad := range []string{"bytes=9-1", "items=1-2", "bytes=-5", "bytes=1-2,4-5"} {
		if _, _, err := parseRange(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
