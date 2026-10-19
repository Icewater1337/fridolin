package plaud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// Plaud's CLI deletes a login that expired. Fridolin remembers it signed
// in, so the card says the login expired rather than that it never was
// connected, and the chat keeps getting its daily notice, without any CLI
// call, until you connect again or disconnect.
func TestPlaudExpiredLoginKeepsNotifying(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudOld1)
	fp.Write("expired", "")
	ctx := context.Background()
	past := time.Now().Add(-time.Second)
	notices := func() int {
		n := 0
		for _, m := range tb.dev.Outbox() {
			if strings.Contains(m.Text, "Plaud: the login expired") {
				n++
			}
		}
		return n
	}
	tb.plaud.tick(ctx, past)
	if s := tb.plaud.Status(); !s.Expired || s.Connected || notices() != 1 {
		t.Fatalf("after the login lapsed: expired=%v connected=%v notices=%d, want expired and one notice", s.Expired, s.Connected, notices())
	}
	calls := fp.Calls()
	tb.plaud.tick(ctx, past)
	if notices() != 1 {
		t.Errorf("notices = %d within a day, want 1", notices())
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.AuthNotified = time.Now().Add(-25 * time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	tb.plaud.tick(ctx, past)
	if notices() != 2 {
		t.Errorf("notices = %d a day later, want 2", notices())
	}
	if fp.Calls() != calls {
		t.Errorf("the CLI ran while the login was gone:\n%s", strings.TrimPrefix(fp.Calls(), calls))
	}

	if err := tb.plaud.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.AuthNotified = time.Now().Add(-25 * time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	tb.plaud.tick(ctx, past)
	if s := tb.plaud.Status(); s.Expired || s.Connected || notices() != 2 {
		t.Errorf("after disconnecting: expired=%v connected=%v notices=%d, want neither and no more notices", s.Expired, s.Connected, notices())
	}
}

// Connecting again after a disconnect makes the login Fridolin's again:
// the card shows who signed in, and once the login lapses the chat hears
// so, as before the disconnect.
func TestPlaudConnectingAfterADisconnectCountsAgain(t *testing.T) {
	for _, access := range []bool{false, true} {
		t.Run(fmt.Sprintf("brain_access=%v", access), func(t *testing.T) {
			tb, fp := plaudService(t, fmt.Sprintf(`, "brain_access": %v`, access))
			plaudServiceSignIn(t, tb)
			if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
				t.Fatal(err)
			}
			fp.Files(servicePlaudOld1)
			if err := tb.plaud.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 10*time.Second, "the sign-in", func() bool { c := tb.plaud.Status().Connect; return c != nil && c.State != "running" })
			if c := tb.plaud.Status().Connect; c.State != "done" {
				t.Fatalf("sign-in %s: %s", c.State, c.Error)
			}
			if st, _ := tb.plaud.loadState(); st == nil || st.Disconnected {
				t.Errorf("state = %+v, want the disconnect forgotten", st)
			}
			if s := tb.plaud.Status(); !s.Connected || s.Account != "you@example.com" {
				t.Errorf("connected=%v account=%q, want connected as you@example.com", s.Connected, s.Account)
			}

			fp.Write("expired", "")
			tb.plaud.tick(context.Background(), time.Now().Add(-time.Second))
			n := 0
			for _, m := range tb.dev.Outbox() {
				if strings.Contains(m.Text, "Plaud: the login expired") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d expiry notices, want 1", n)
			}
		})
	}
}

// The CLI runs in its own home, not in the bridge's working folder (the
// data folder under the service), whose .env it would load. It runs in a
// process group of its own, so a check that is cancelled ends with all of
// it, and what it prints is capped.
func TestPlaudCLIRunsContained(t *testing.T) {
	t.Run("in its home", func(t *testing.T) {
		tb, fp := plaudService(t, "")
		if _, err := tb.plaud.run(context.Background(), "logout"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fp.Calls(), "CWD="+tb.plaud.cliHome()+" logout") {
			t.Errorf("the CLI did not run in %s:\n%s", tb.plaud.cliHome(), fp.Calls())
		}
	})
	t.Run("a cancel ends what it started", func(t *testing.T) {
		tb, fp := plaudService(t, "")
		plaudServiceSignIn(t, tb)
		fp.Write("hang", "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := tb.plaud.run(ctx, "files"); done <- err }()
		// Cancel only once the child exists: slow runner startup must not
		// turn this process-group test into cancellation before execution.
		pid := 0
		testutil.WaitFor(t, 10*time.Second, "the fake child to start", func() bool {
			data, _ := os.ReadFile(filepath.Join(fp.Dir, "child.pid"))
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			return pid > 0
		})
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a cancelled run reported success")
			}
		case <-time.After(8 * time.Second):
			t.Fatal("the cancelled run did not end")
		}
		testutil.WaitFor(t, 5*time.Second, "the child to end", func() bool { return syscall.Kill(pid, 0) != nil })
	})
	t.Run("its output is capped", func(t *testing.T) {
		tb, fp := plaudService(t, "")
		plaudServiceSignIn(t, tb)
		fp.Write("flood", "")
		out, err := tb.plaud.run(context.Background(), "files")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != plaudOutputLimit {
			t.Errorf("kept %d bytes of the output, want the first %d", len(out), plaudOutputLimit)
		}
	})
}

// Fridolin runs one Plaud command at a time, since the CLI rewrites its
// login without a lock; only the sign-in, which waits for the browser,
// runs beside them. Disconnect signs out even when the request that asked
// for it is gone.
func TestPlaudRunsOneCommandAtATime(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	fp.Files(servicePlaudOld1)
	fp.Write("slow", "")
	done := make(chan struct{})
	for _, cmd := range []string{"me", "files", "me", "file"} {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = tb.plaud.run(context.Background(), cmd, servicePlaudOld1)
		}()
	}
	for range 4 {
		<-done
	}
	if overlap, _ := os.ReadFile(filepath.Join(fp.Dir, "overlap.log")); len(overlap) > 0 {
		t.Errorf("commands ran at the same time:\n%s", overlap)
	}

	// A sign-in waiting for the browser holds nothing up.
	_ = os.Remove(tb.plaud.tokenPath())
	fp.Write("login_wait", "")
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "the sign-in to start", func() bool { return strings.Contains(fp.Calls(), " login") })
	start := time.Now()
	_, _ = tb.plaud.run(context.Background(), "me")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("a command waited %v for the sign-in", d)
	}
	fp.Write("login_go", "")
	testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool { return tb.plaud.connected() })

	if err := tb.plaud.Disconnect(); err != nil || !strings.Contains(fp.Calls(), " logout\n") {
		t.Fatalf("disconnect = %v, calls:\n%s", err, fp.Calls())
	}
}

// The settings switch is checked again after a check has been accepted, at
// the processor boundary; the command integration separately pins 409/202.
func TestPlaudDisabledAfterCheckBegins(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	if !tb.plaud.beginPoll() {
		t.Fatal("a check is still running")
	}
	if err := tb.updateSettings(func(s *Snapshot) error { s.Plaud.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.runPoll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := fp.Calls(); calls != "" || tb.plaudForwarded() {
		t.Fatalf("Plaud was checked with sending off:\n%s", calls)
	}
}

// A check that Check now started ends when Fridolin stops, like the
// poller's own checks, instead of running on for up to ten minutes, and
// Stop waits for its end: what a check writes to the data folder must not
// come after it. That holds in a command of the CLI and while it sends the
// expiry notice of a login Plaud refused. Once Fridolin is stopping, Check
// now starts none.
func TestPlaudStopFinishesCheckProcessor(t *testing.T) {
	cases := []struct {
		name string
		// hold has the check wait in held until it is cancelled.
		hold    func(t *testing.T, tb *plaudServiceFixture, fp *testutil.PlaudFixture, held func(context.Context) error)
		lastErr error // what the check then ends with
	}{
		{"in a command", func(t *testing.T, tb *plaudServiceFixture, fp *testutil.PlaudFixture, held func(context.Context) error) {
			plaudServiceSignIn(t, tb)
			fp.Files(servicePlaudOld1)
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) { return "", held(ctx) }
		}, context.Canceled},
		{"sending the expiry notice", func(t *testing.T, tb *plaudServiceFixture, fp *testutil.PlaudFixture, held func(context.Context) error) {
			plaudServiceRevokeLogin(t, tb, fp)
			tb.plaud.notify = func(ctx context.Context, _, _ string) error { return held(ctx) }
		}, errPlaudAuth},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			asked, done := make(chan struct{}), make(chan struct{})
			// Released at the end, so that a Stop that does not end the
			// check fails the test instead of hanging it.
			t.Cleanup(func() { close(done) })
			var once sync.Once
			c.hold(t, tb, fp, func(ctx context.Context) error {
				once.Do(func() { close(asked) })
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-done:
					return errors.New("held past the test")
				}
			})
			tb.plaud.Start(context.Background())
			if started, err := tb.plaud.CheckNow(); !started || err != nil {
				t.Fatalf("check now = %v,%v", started, err)
			}
			select {
			case <-asked:
			case <-time.After(10 * time.Second):
				t.Fatal("the check did not get there")
			}
			stopped := make(chan struct{})
			go func() { tb.plaud.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Fatal("Stop did not end the check")
			}
			tb.plaud.mu.Lock()
			busy, lastErr := tb.plaud.busy, tb.plaud.lastErr
			tb.plaud.mu.Unlock()
			if busy || !strings.Contains(lastErr, c.lastErr.Error()) {
				t.Errorf("after Stop: checking %v, last error %q; want the check over, with %q", busy, lastErr, c.lastErr)
			}

			if _, err := tb.plaud.CheckNow(); !errors.Is(err, ErrStopping) {
				t.Errorf("check now while stopping = %v", err)
			}
		})
	}
}
