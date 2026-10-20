package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/snapshot"
	"github.com/Icewater1337/fridolin/internal/vault"
)

// TestLiveB2 is M3's try against the real Backblaze B2 buckets, run by
// hand, never in CI: only under test prefixes (fridolin-vault/test-m3/<random>
// for the cold tier, fridolin-snapshots/test-m3-<random> for restic), which
// it deletes again, every version of every object, at the end. It runs
// with FRIDOLIN_LIVE_B2=1 and the keys in B2_VAULT_KEY_ID, B2_VAULT_KEY,
// B2_SNAPSHOTS_KEY_ID and B2_SNAPSHOTS_KEY (from the keychain, through the
// environment only):
//
//	FRIDOLIN_LIVE_B2=1 \
//	B2_VAULT_KEY_ID="$(security find-generic-password -a fridolin -s fridolin.b2.vault.keyid -w)" \
//	… go test -run TestLiveB2 -v ./internal/app
//
// It never touches lifecycle rules, and never deletes outside its prefixes.
func TestLiveB2(t *testing.T) {
	if os.Getenv("FRIDOLIN_LIVE_B2") != "1" {
		t.Skip("set FRIDOLIN_LIVE_B2=1 and the B2 keys to run against Backblaze")
	}
	keys := map[string]string{}
	for _, k := range []string{"B2_VAULT_KEY_ID", "B2_VAULT_KEY", "B2_SNAPSHOTS_KEY_ID", "B2_SNAPSHOTS_KEY"} {
		if keys[k] = os.Getenv(k); keys[k] == "" {
			t.Fatalf("%s is not set", k)
		}
	}
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		t.Fatal(err)
	}
	restic := findVaultTool("restic")
	if restic == "" {
		t.Fatal("no restic")
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	id := hex.EncodeToString(rnd[:])
	coldPrefix := "test-m3/" + id   // in fridolin-vault
	resticPrefix := "test-m3-" + id // in fridolin-snapshots
	secrets := []string{keys["B2_VAULT_KEY_ID"], keys["B2_VAULT_KEY"], keys["B2_SNAPSHOTS_KEY_ID"], keys["B2_SNAPSHOTS_KEY"]}
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	t.Logf("test prefixes: fridolin-vault/%s and fridolin-snapshots/%s", coldPrefix, resticPrefix)

	// raw runs rclone on the buckets themselves, for counting (with every
	// version) and the final purge; the keys reach it by environment only.
	rawEnv := append(append([]string{}, base...),
		"RCLONE_CONFIG_LIVEVAULT_TYPE=b2", "RCLONE_CONFIG_LIVEVAULT_ACCOUNT="+keys["B2_VAULT_KEY_ID"], "RCLONE_CONFIG_LIVEVAULT_KEY="+keys["B2_VAULT_KEY"],
		"RCLONE_CONFIG_LIVESNAP_TYPE=b2", "RCLONE_CONFIG_LIVESNAP_ACCOUNT="+keys["B2_SNAPSHOTS_KEY_ID"], "RCLONE_CONFIG_LIVESNAP_KEY="+keys["B2_SNAPSHOTS_KEY"])
	raw := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, rclone, append([]string{"--config", ""}, args...)...)
		cmd.Env = rawEnv
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := stderr.String()
			for _, s := range secrets {
				msg = strings.ReplaceAll(msg, s, "***")
			}
			t.Logf("rclone %s: %v: %s", args[0], err, msg)
		}
		return out, err
	}
	count := func(remote string) (objects, versions int) {
		for _, v := range []bool{false, true} {
			args := []string{"lsjson", "-R", "--files-only", remote}
			if v {
				args = append(args, "--b2-versions")
			}
			out, err := raw(args...)
			if err != nil {
				return -1, -1
			}
			var list []json.RawMessage
			_ = json.Unmarshal(out, &list)
			if v {
				versions = len(list)
			} else {
				objects = len(list)
			}
		}
		return
	}
	coldRemote := "livevault:fridolin-vault/" + coldPrefix
	resticRemote := "livesnap:fridolin-snapshots/" + resticPrefix
	t.Cleanup(func() {
		o, v := count(coldRemote)
		ro, rv := count(resticRemote)
		t.Logf("before cleanup: cold prefix %d objects (%d with versions), restic prefix %d objects (%d with versions)", o, v, ro, rv)
		// Only the test prefixes, with hard delete so no hidden version is
		// left for the lifecycle rules: never the bucket.
		_, _ = raw("purge", "--b2-hard-delete", coldRemote)
		_, _ = raw("purge", "--b2-hard-delete", resticRemote)
		o, v = count(coldRemote)
		ro, rv = count(resticRemote)
		t.Logf("after cleanup: cold prefix %d objects (%d with versions), restic prefix %d objects (%d with versions)", o, v, ro, rv)
		if o > 0 || v > 0 || ro > 0 || rv > 0 {
			t.Errorf("objects are left under the test prefixes")
		}
	})

	// The vault: crypt over the test prefix, with passwords made here.
	pw, _ := newVaultPassword()
	salt, _ := newVaultPassword()
	env, remote, err := cloud.VaultRemote{Name: "livecold", KeyID: keys["B2_VAULT_KEY_ID"], Key: keys["B2_VAULT_KEY"],
		Bucket: "fridolin-vault", Path: coldPrefix, Password: pw, Password2: salt}.Env()
	if err != nil {
		t.Fatal(err)
	}
	store := &cloud.Rclone{Binary: rclone, Remote: remote, Env: append(append([]string{}, base...), env...), Secrets: append(secrets, pw, salt)}
	hot := t.TempDir()
	rec := filepath.Join(hot, "media", "main", "recording.m4a")
	sum := writeSized(t, rec, 8<<20)
	writeFile(t, filepath.Join(hot, "media", "main", "recording.transcript.md"), "# What was said\n")
	writeFile(t, filepath.Join(hot, "audit", time.Now().Format("2006-01")+".jsonl"), "{}\n")
	v, err := vault.New(vault.Options{Root: hot, DBPath: filepath.Join(t.TempDir(), "vault.db"), Store: store, BudgetBytes: 1 << 30, HeadroomPercent: 10, VerifyByDownload: true})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	ctx := context.Background()
	start := time.Now()
	// B2 answers 503 ("no tomes available") now and then; the vault's own
	// answer is the next sync, which uploads what failed.
	srep, err := v.Sync(ctx)
	for try := 1; try < 4 && err == nil && len(srep.Failed) > 0; try++ {
		t.Logf("sync try %d: %v; again", try, srep.Failed)
		time.Sleep(5 * time.Second)
		var again vault.SyncReport
		again, err = v.Sync(ctx)
		again.Uploaded = append(srep.Uploaded, again.Uploaded...)
		again.UploadedBytes += srep.UploadedBytes
		srep = again
	}
	if err != nil || len(srep.Failed) > 0 {
		t.Fatalf("sync: %v %v", err, srep.Failed)
	}
	t.Logf("sync: %d files, %d bytes in %s", len(srep.Uploaded), srep.UploadedBytes, time.Since(start).Round(time.Millisecond))
	start = time.Now()
	erep, err := v.Evict(ctx)
	if err != nil || len(erep.Evicted) != 1 || erep.Evicted[0].Path != "media/main/recording.m4a" {
		t.Fatalf("evict: %v %+v", err, erep)
	}
	t.Logf("evict (verified by download): %s in %s", erep.Evicted[0].Path, time.Since(start).Round(time.Millisecond))
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatalf("the recording is still hot: %v", err)
	}
	start = time.Now()
	if _, err := v.Get(ctx, "media/main/recording.m4a", vault.GetOptions{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if got := fileSHA(t, rec); got != sum {
		t.Fatalf("hydrated sha256 %s, want %s", got, sum)
	}
	t.Logf("hydrate: back with the right hash in %s", time.Since(start).Round(time.Millisecond))
	o, ov := count(coldRemote)
	t.Logf("cold prefix holds %d objects (%d with versions); names are encrypted", o, ov)

	// restic: one backup and restore of a scratch folder.
	rpw, _ := newVaultPassword()
	repo := &snapshot.Restic{Binary: restic, Repo: "b2:fridolin-snapshots:" + resticPrefix,
		Env:     append(append([]string{}, base...), "B2_ACCOUNT_ID="+keys["B2_SNAPSHOTS_KEY_ID"], "B2_ACCOUNT_KEY="+keys["B2_SNAPSHOTS_KEY"], "RESTIC_PASSWORD="+rpw),
		Secrets: append(secrets, rpw)}
	if ok, err := repo.Exists(ctx); err != nil || ok {
		t.Fatalf("a repository at the fresh prefix: %v %v", ok, err)
	}
	if err := repo.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	writeFile(t, filepath.Join(scratch, "brain", "brain.json"), `{"id":"main"}`)
	writeFile(t, filepath.Join(scratch, "brain", "memories", "a.md"), "remember")
	bs, err := repo.Backup(ctx, snapshot.BackupOptions{Paths: []string{scratch}, Tags: []string{snapshotTag, "test"}, Host: snapshotHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restic backup: snapshot %s, %d files, %d bytes added", shortID(bs.SnapshotID), bs.TotalFilesProcessed, bs.DataAdded)
	target := t.TempDir()
	if err := repo.Restore(ctx, bs.SnapshotID, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, scratch, "brain", "memories", "a.md"))
	if err != nil || string(got) != "remember" {
		t.Fatalf("restored: %q %v", got, err)
	}
	if err := repo.Check(ctx, ""); err != nil {
		t.Fatal(err)
	}
	ro, rv := count(resticRemote)
	t.Logf("restic restore ok; check ok; restic prefix holds %d objects (%d with versions)", ro, rv)
}
