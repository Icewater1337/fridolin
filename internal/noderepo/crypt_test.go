package noderepo

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/rcrypt"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// TestResticThroughTheNodeEncrypted: with the outer layer, what lands on
// the node is rclone crypt's format around restic's own; restic on the
// core sees its repository as usual, and on another machine rclone's
// crypt remote over the folder opens it for restic.
func TestResticThroughTheNodeEncrypted(t *testing.T) {
	bin := resticBinary(t)
	disk := t.TempDir()
	folder := filepath.Join(disk, "fridolin-backup")
	svc, id := fleet(t, folder)
	srv, err := Start(svc, id, folder)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if l, err := srv.Detect(ctx); err != nil || l != LayersNone {
		t.Fatalf("an empty folder: %v %v", l, err)
	}
	cryptPW := "outer layer password"
	c, err := rcrypt.New(cryptPW, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.SetCipher(c)
	pw := "inner layer password"
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "RESTIC_PASSWORD=" + pw}
	r := &snapshot.Restic{Binary: bin, Repo: srv.Repo(), Env: append(append([]string{}, base...), srv.Env()...), Secrets: []string{pw, srv.Secret()}}
	if err := r.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	big := make([]byte, 3<<20+12345)
	_, _ = rand.Read(big)
	if err := os.WriteFile(filepath.Join(src, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "brain.json"), []byte(`{"id":"work"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx, snapshot.BackupOptions{Paths: []string{src}, Host: "fridolin-core"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(ctx, "100%"); err != nil {
		t.Fatal(err)
	}
	if l, err := srv.Detect(ctx); err != nil || l != LayersCrypt {
		t.Fatalf("after init: %v %v", l, err)
	}
	cfg, err := os.ReadFile(filepath.Join(folder, "config"))
	if err != nil || !bytes.HasPrefix(cfg, []byte("RCLONE\x00\x00")) {
		t.Fatalf("the config on the node is not in rclone crypt's format: %v", err)
	}
	// restic alone cannot open the folder any more.
	if ok, err := (&snapshot.Restic{Binary: bin, Repo: folder, Env: base}).Exists(ctx); err == nil && ok {
		t.Error("restic opened the folder without the outer layer")
	}
	restoreAndCompare := func(rr *snapshot.Restic) {
		t.Helper()
		target := t.TempDir()
		if err := rr.Restore(ctx, "latest", target); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(target, strings.TrimPrefix(filepath.ToSlash(src), "/"), "big.bin"))
		if err != nil || !bytes.Equal(got, big) {
			t.Fatalf("restored wrong: %v", err)
		}
	}
	restoreAndCompare(r)

	// Another machine: rclone crypt over the folder, restic over rclone.
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed: the restore through rclone is not tried")
	}
	out, err := exec.Command(rclone, "obscure", cryptPW).Output()
	if err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{}, base...),
		"RCLONE_CONFIG_NODECRYPT_TYPE=crypt", "RCLONE_CONFIG_NODECRYPT_REMOTE="+folder,
		"RCLONE_CONFIG_NODECRYPT_FILENAME_ENCRYPTION=off", "RCLONE_CONFIG_NODECRYPT_DIRECTORY_NAME_ENCRYPTION=false",
		"RCLONE_CONFIG_NODECRYPT_SUFFIX=none", "RCLONE_CONFIG_NODECRYPT_PASSWORD="+strings.TrimSpace(string(out)))
	restoreAndCompare(&snapshot.Restic{Binary: bin, Repo: "rclone:nodecrypt:", Env: env, Options: []string{"rclone.program=" + rclone}})
}
