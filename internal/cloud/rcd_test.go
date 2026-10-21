package cloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localCryptDaemon is an RcloneRC store on a crypt remote over a local
// folder, on its own daemon, configured only by environment variables.
func localCryptDaemon(t *testing.T) (*RcloneRC, string) {
	t.Helper()
	ex, under := localCryptStore(t)
	d := &RcloneDaemon{Binary: ex.Binary, Env: ex.Env, Secrets: ex.Secrets}
	t.Cleanup(func() { _ = d.Close() })
	return d.Store(ex.Remote), under
}

// TestRcloneDaemonContract runs the store contract through rclone rcd
// against a crypt remote over a local folder, and checks that what lands
// on disk is encrypted.
func TestRcloneDaemonContract(t *testing.T) {
	r, under := localCryptDaemon(t)
	storeContract(t, r)
	ctx := context.Background()
	secret := "plaintext through the daemon"
	if err := r.Put(ctx, "private/notes.md", strings.NewReader(secret)); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(under, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Name() == "notes.md" || fi.Name() == "private" {
			t.Errorf("clear name on the crypt backend: %s", p)
		}
		if fi.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("clear contents on the crypt backend: %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// One daemon serves every operation: its process stays the same.
	p1, err := r.d.running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stat(ctx, "private/notes.md"); err != nil {
		t.Fatal(err)
	}
	p2, _ := r.d.running(ctx)
	if p1 != p2 {
		t.Fatal("the daemon was started again between two operations")
	}
}

// TestRcloneDaemonRestartsAfterDeath: a daemon that died is started again
// by the next operation.
func TestRcloneDaemonRestartsAfterDeath(t *testing.T) {
	r, _ := localCryptDaemon(t)
	ctx := context.Background()
	if err := r.Put(ctx, "a.txt", strings.NewReader("one")); err != nil {
		t.Fatal(err)
	}
	p, err := r.d.running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.cmd.Process.Kill()
	<-p.done
	rc, err := r.Get(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Get after the daemon died: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "one" {
		t.Fatalf("Get = %q", data)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stat(ctx, "a.txt"); err == nil {
		t.Fatal("a closed daemon answered")
	}
}

// failingReader returns some bytes, then an error.
type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("disk went away")
	}
	k := min(len(p), f.n)
	for i := range k {
		p[i] = 'x'
	}
	f.n -= k
	return k, nil
}

// TestRcloneDaemonBrokenBody: a body that fails part way is an error and
// leaves no object, small or streamed.
func TestRcloneDaemonBrokenBody(t *testing.T) {
	r, _ := localCryptDaemon(t)
	ctx := context.Background()
	for _, n := range []int{1000, 6 << 20} {
		err := r.Put(ctx, "broken.bin", &failingReader{n: n})
		if err == nil || !strings.Contains(err.Error(), "disk went away") {
			t.Fatalf("Put of a failing body (%d bytes) = %v", n, err)
		}
		// rclone notices the broken stream a moment after the request
		// failed here, and removes what it wrote (on B2 a large upload is
		// not visible before it is finished at all).
		gone := waitGone(t, r, "broken.bin")
		if !gone {
			t.Fatalf("a broken upload of %d bytes left an object", n)
		}
	}
	// Cancelling stops an upload that waits for input.
	cctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan error, 1)
	go func() { done <- r.Put(cctx, "slow.bin", pr) }()
	go func() { _, _ = pw.Write([]byte("partial")) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Put = %v, want context.Canceled", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled Put did not return")
	}
	if !waitGone(t, r, "slow.bin") {
		t.Fatal("a cancelled upload left an object")
	}
}

// waitGone waits up to five seconds for key to be missing.
func waitGone(t *testing.T, r ObjectStore, key string) bool {
	t.Helper()
	for range 50 {
		_, err := r.Stat(context.Background(), key)
		if errors.Is(err, ErrNotFound) {
			return true
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestRcloneDaemonCountsUsage: the daemon logs in once per remote, then
// counts only what each operation costs.
func TestRcloneDaemonCountsUsage(t *testing.T) {
	r, _ := localCryptDaemon(t)
	u := NewUsage()
	r.SetUsage(u, "vault")
	ctx := context.Background()
	if err := r.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if _, err := r.Stat(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := r.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if _, err := r.List(ctx, ""); err != nil {
		t.Fatal(err)
	}
	day, _ := u.Today()
	// One log-in (1 C, 1 B), ten stats (10 B), one get (2 B), one list (1 B, 1 C).
	if day.Count(ClassC) != 2 || day.Count(ClassB) != 14 || day.Count(ClassA) != 1 {
		t.Fatalf("usage = %v", day.Classes)
	}
	if day.Ops["C vault:authorize"] != 1 || day.Ops["B vault:stat"] != 10 {
		t.Fatalf("ops = %v", day.Ops)
	}
	if day.LastOK.IsZero() {
		t.Fatal("the daemon's calls that succeeded left no LastOK")
	}

	// The same through one rclone process per operation pays the log-in
	// each time.
	ex, _ := localCryptStore(t)
	ue := NewUsage()
	ex.SetUsage(ue, "vault")
	if err := ex.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if _, err := ex.Stat(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	day, _ = ue.Today()
	if day.Count(ClassC) != 11 {
		t.Fatalf("exec usage = %v", day.Classes)
	}
	if day.LastOK.IsZero() {
		t.Fatal("the rclone runs that succeeded left no LastOK")
	}
}

// TestCapExceededIsRecognised: B2's refusal for a used-up cap is a
// CapError, and Usage keeps it for doctor.
func TestCapExceededIsRecognised(t *testing.T) {
	err := &RcloneError{Op: "rcd", Stderr: "failed to authorize account: Transaction cap exceeded, see the Caps & Alerts page (403 transaction_cap_exceeded)"}
	if !IsCapExceeded(err) {
		t.Fatal("cap exceeded not recognised")
	}
	if IsCapExceeded(errors.New("connection refused")) {
		t.Fatal("another error taken for cap exceeded")
	}
	u := NewUsage()
	u.NoteError(&CapError{Err: err})
	day, _ := u.Today()
	if day.CapExceeded.IsZero() || !strings.Contains(day.CapError, "transaction_cap_exceeded") {
		t.Fatalf("cap not recorded: %+v", day)
	}
}

func TestUsageRollsOverAndRestores(t *testing.T) {
	now := time.Date(2026, 10, 21, 23, 59, 0, 0, time.UTC)
	u := NewUsageAt(func() time.Time { return now })
	u.Add(ClassC, "lease:get", 5)
	now = now.Add(2 * time.Minute)
	u.Add(ClassC, "lease:get", 1)
	today, prev := u.Today()
	if today.Day != "2026-10-22" || today.Count(ClassC) != 1 || prev == nil || prev.Count(ClassC) != 5 {
		t.Fatalf("today %+v prev %+v", today, prev)
	}
	// A restart takes up what the last process kept.
	v := NewUsageAt(func() time.Time { return now })
	v.Restore(today, prev)
	v.Add(ClassC, "lease:get", 1)
	today, prev = v.Today()
	if today.Count(ClassC) != 2 || prev == nil || prev.Count(ClassC) != 5 {
		t.Fatalf("restored today %+v prev %+v", today, prev)
	}
	// The last success and refusal are kept too, the later of each.
	v.NoteOK()
	ok := now
	w := NewUsageAt(func() time.Time { return now.Add(-time.Minute) })
	w.NoteOK()
	vt, _ := v.Today()
	w.Restore(vt, nil)
	if d, _ := w.Today(); !d.LastOK.Equal(ok) {
		t.Fatalf("restored LastOK %v, want %v", d.LastOK, ok)
	}
	var nilUsage *Usage
	nilUsage.Add(ClassB, "x", 1) // counts nothing, does not panic
	nilUsage.NoteOK()
}
