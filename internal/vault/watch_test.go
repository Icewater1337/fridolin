package vault

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

func TestWatchDebouncesIntoSyncs(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(bg)
	reports := make(chan SyncReport, 16)
	done := make(chan error, 1)
	go func() {
		done <- f.v.Watch(ctx, WatchOptions{
			Debounce: 100 * time.Millisecond,
			OnSync:   func(r SyncReport, _ error) { reports <- r },
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // let the watches settle

	// A burst of writes, some to ignored names, and a new folder.
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(f.root, "a.md"), []byte(strings.Repeat("a", i+1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"bridge.db", "bridge.db-wal", "x.tmp", "vault.db-journal"} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte("ignored"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.root, "new/deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "new/deeper/b.md"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	uploaded := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for !uploaded["a.md"] || !uploaded["new/deeper/b.md"] {
		select {
		case r := <-reports:
			for _, p := range r.Uploaded {
				uploaded[p] = true
			}
		case <-deadline:
			t.Fatalf("uploaded so far: %v", uploaded)
		}
	}
	for p := range uploaded {
		if IgnoredName(filepath.Base(p)) {
			t.Fatalf("ignored file uploaded: %s", p)
		}
	}
	if got, _ := f.coldBytes("a.md"); got != "aaaaa" {
		t.Fatalf("cold a.md = %q", got)
	}

	// An eviction's own changes (the move aside, .cold.json, the delete)
	// do not start uploads.
	f.write("old/c.md", "c", nDays(100))
	f.sync()
	f.evict()
	time.Sleep(400 * time.Millisecond)
	for {
		select {
		case r := <-reports:
			if len(r.Uploaded) > 0 && !slices.Equal(r.Uploaded, []string{"old/c.md"}) {
				t.Fatalf("eviction caused uploads: %v", r.Uploaded)
			}
			continue
		default:
		}
		break
	}
	if e := f.entry("old/c.md"); e.Tier != TierCold {
		t.Fatalf("old/c.md = %+v", e)
	}
}

// TestVaultOverRcloneCrypt runs a whole cycle — upload, evict, rebuild,
// hydrate — against the real rclone on a crypt remote over a local folder,
// configured only by environment variables.
func TestVaultOverRcloneCrypt(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	under := t.TempDir()
	base, err := cloud.RemoteEnv("lcl", map[string]string{"type": "local"})
	if err != nil {
		t.Fatal(err)
	}
	crypt, err := cloud.CryptEnv("lclcrypt", "lcl:"+filepath.ToSlash(under), "vault test password", "vault test salt")
	if err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{"HOME=" + t.TempDir()}, base...), crypt...)
	store := &cloud.Rclone{Binary: bin, Remote: "lclcrypt:", Env: env}

	f := newFixture(t, func(o *Options) { o.Store = store })
	recording := strings.Repeat("\x10\x20\x30", 100_000)
	f.write("media/call.m4a", recording, time.Hour)
	f.write("media/call.txt", "transcript of the call", 0)
	f.sync()
	ev := f.evict()
	if len(ev.Evicted) != 1 || ev.Evicted[0].Path != "media/call.m4a" {
		t.Fatalf("evict = %+v", ev)
	}
	// Nothing readable on the backend.
	_ = filepath.Walk(under, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(fi.Name(), "call") || strings.Contains(fi.Name(), "media") {
			t.Errorf("clear name on the backend: %s", p)
		}
		if fi.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			if bytes.Contains(data, []byte("transcript of the call")) {
				t.Errorf("clear contents on the backend: %s", p)
			}
		}
		return nil
	})
	// Lose vault.db; rebuild from the encrypted cold tier.
	if err := f.v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.db); err != nil {
		t.Fatal(err)
	}
	f.open()
	rep, err := f.v.Rebuild(bg)
	if err != nil || rep.Cold != 1 || rep.Both != 1 || rep.ManifestMissing {
		t.Fatalf("rebuild = %+v, %v", rep, err)
	}
	p, err := f.v.Get(bg, "media/call.m4a", GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, p) != recording {
		t.Fatal("the recording came back different")
	}
}
