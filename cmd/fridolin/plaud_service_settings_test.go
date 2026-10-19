package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// Connect signs the refused login out in the CLI home it was refused in,
// taken when Connect starts: brain_access turned on before the sign-out
// runs must not have it sign out ~/.plaud instead, the login your
// terminal's plaud uses.
func TestPlaudConnectSignsOutTheRefusedLoginWhereItIs(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceRefuseLogin(t, tb, fp)
	terminal := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(terminal), 0o700); err != nil {
		t.Fatal(err)
	}
	const yours = `{"access_token":"terminal"}`
	if err := os.WriteFile(terminal, []byte(yours), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fp.Calls()
	// The sign-out waits for the CLI, which this holds, until brain_access
	// is on.
	tb.plaud.cliMu.Lock()
	err := tb.plaud.Connect()
	if err == nil {
		err = tb.updateSettings(func(c *PlaudSnapshot) error { c.Plaud.BrainAccess = true; return nil })
	}
	tb.plaud.cliMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return tb.plaud.connect.State != "running"
	})
	var logouts []string
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.Calls(), before)), "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[2] == "logout" {
			logouts = append(logouts, strings.TrimPrefix(f[0], "HOME="))
		}
	}
	if want := tb.plaud.privateHome(); !slices.Equal(logouts, []string{want}) {
		t.Errorf("the sign-out ran with HOME %q, want %s once", logouts, want)
	}
	// The sign-in after it may adopt that login ("Already logged in"), as
	// any Connect after brain_access changed does, but not replace it.
	if got, _ := os.ReadFile(terminal); string(got) != yours {
		t.Errorf("the login in ~/.plaud is now %q, want yours, which the sign-out must not have removed", got)
	}
}

// A check that loses its login because you disconnected, or moved it with
// brain_access, while it ran tells the chat nothing: only a login that
// lapsed gets the expiry notice. Nor does it record the login again, which
// would show it as expired and bring the daily notice back.
func TestPlaudSigningOutDuringACheckIsNoExpiry(t *testing.T) {
	disconnect := func(tb *plaudServiceFixture) error { return tb.plaud.Disconnect() }
	moveLogin := func(tb *plaudServiceFixture) error {
		err := tb.updateSettings(func(c *PlaudSnapshot) error { c.Plaud.BrainAccess = true; return nil })
		return err
	}
	for _, c := range []struct {
		name   string
		seeded bool
		cmd    string // the command before (or after) which it happens
		after  bool
		act    func(tb *plaudServiceFixture) error
		state  string
	}{
		{"disconnected before a recording is fetched", true, "file", false, disconnect, plaudSignedOut},
		{"brain_access turned on before a recording is fetched", true, "file", false, moveLogin, plaudMoved},
		{"disconnected while the account is looked up", true, "me", true, disconnect, plaudSignedOut},
		{"disconnected during the first sync", false, "files", true, disconnect, plaudSignedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			plaudServiceSignIn(t, tb)
			st := newPlaudState()
			if c.seeded {
				st = plaudServiceSeededState("u_1", servicePlaudOld1)
			}
			if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, st)); err != nil {
				t.Fatal(err)
			}
			fp.Files(servicePlaudNew, servicePlaudOld1)
			fp.Recording(servicePlaudNew, true)
			run := tb.plaud.run
			var once sync.Once
			act := func() {
				once.Do(func() {
					if err := c.act(tb); err != nil {
						t.Error(err)
					}
				})
			}
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == c.cmd && !c.after {
					act()
				}
				out, err := run(ctx, args...)
				if args[0] == c.cmd && c.after {
					act()
				}
				return out, err
			}
			ctx := context.Background()
			if err := tb.plaud.Poll(ctx); err != nil && !errors.Is(err, errPlaudAuth) {
				t.Fatal(err)
			}
			tb.plaud.tick(ctx, time.Now().Add(-time.Second))
			if out := tb.dev.Outbox(); len(out) != 0 {
				t.Errorf("sent %+v, want nothing", out)
			}
			if got := tb.plaud.loginStatus(); got != c.state {
				t.Errorf("login %s, want %s", got, c.state)
			}
		})
	}
}

// A check ends with the command it is at once its login is no longer the
// one in use. brain_access may be saved while your terminal is signed in
// to ~/.plaud with another account: the commands after it would run with
// that login, which has none of Fridolin's recordings, also once you
// connected it: the check looked up the account of the login before. Or
// you disconnect with brain_access on, and the sign-out leaves the login
// in ~/.plaud behind. No recording is given up as
// deleted in Plaud or handed over after that, the state does not start
// over for the other account, and what was not handed over waits for the
// login you connect.
func TestPlaudCheckEndsWhenItsLoginGoes(t *testing.T) {
	const newer = "of_0000000000000000000004"
	// Your terminal's login, of another account, which has none of these
	// recordings, and brain_access turned on.
	moveLogin := func(tb *plaudServiceFixture, fp *testutil.PlaudFixture) error {
		token := filepath.Join(tb.home, ".plaud", "tokens.json")
		if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(token, []byte("{}"), 0o600); err != nil {
			return err
		}
		fp.Write("account", "u_2")
		for _, id := range []string{servicePlaudNew, newer} {
			if err := os.Remove(filepath.Join(fp.Dir, "rec_"+id+".json")); err != nil {
				return err
			}
		}
		err := tb.updateSettings(func(c *PlaudSnapshot) error { c.Plaud.BrainAccess = true; return nil })
		return err
	}
	// The same, and you connect that login, as a sign-in that finishes does.
	moveAndConnect := func(tb *plaudServiceFixture, fp *testutil.PlaudFixture) error {
		if err := moveLogin(tb, fp); err != nil {
			return err
		}
		return tb.plaud.update(func(st *plaudState) error { plaudServiceRecordLogin(tb, st); return nil })
	}
	disconnectKeeping := func(tb *plaudServiceFixture, fp *testutil.PlaudFixture) error {
		fp.Write("logout_keeps", "")
		if err := tb.plaud.Disconnect(); err == nil {
			return errors.New("disconnect did not say the login is still there")
		}
		return nil
	}
	for _, c := range []struct {
		name   string
		access bool   // brain_access before
		cmd    string // the command before (or after) which it happens
		after  bool
		act    func(tb *plaudServiceFixture, fp *testutil.PlaudFixture) error
		// later is how many of the check's commands ran after it happened:
		// the one that was on its way then.
		later   int
		waiting []string
		state   string
	}{
		{"brain_access turned on while the account is looked up", false, "me", false, moveLogin, 1, nil, plaudMoved},
		{"brain_access turned on before a recording is fetched", false, "file", false, moveLogin, 1, []string{servicePlaudNew, newer}, plaudMoved},
		{"brain_access turned on once a recording was handed over", false, "summary", true, moveLogin, 0, []string{newer}, plaudMoved},
		{"brain_access turned on and connected before a recording is fetched", false, "file", false, moveAndConnect, 1, []string{servicePlaudNew, newer}, plaudConnected},
		{"disconnected once a recording was handed over", true, "summary", true, disconnectKeeping, 0, []string{newer}, plaudSignedOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, fmt.Sprintf(`, "brain_access": %v`, c.access))
			plaudServiceSignIn(t, tb)
			if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
				t.Fatal(err)
			}
			fp.Files(newer, servicePlaudNew, servicePlaudOld1)
			fp.Recording(servicePlaudNew, true)
			fp.Recording(newer, true)
			var once sync.Once
			var acted bool
			var later []string
			act := func() {
				once.Do(func() {
					if err := c.act(tb, fp); err != nil {
						t.Error(err)
					}
					acted = true
				})
			}
			run := tb.plaud.run
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == c.cmd && !c.after {
					act()
				}
				if acted {
					later = append(later, strings.Join(args, " "))
				}
				out, err := run(ctx, args...)
				if args[0] == c.cmd && c.after {
					act()
				}
				return out, err
			}
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			st, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.GivenUp) != 0 {
				t.Errorf("given up: %+v, want none", st.GivenUp)
			}
			if st.Account != "u_1" || !slices.Contains(st.Seen, servicePlaudOld1) {
				t.Errorf("account %q, seen %v: want the state of u_1 kept", st.Account, st.Seen)
			}
			for _, id := range c.waiting {
				if item := st.Pending[id]; item == nil || item.Tries != 0 || !item.Handed.IsZero() {
					t.Errorf("recording %s: %+v, want it waiting, untried", id, item)
				}
			}
			if len(later) != c.later {
				t.Errorf("ran %q afterwards, want %d commands", later, c.later)
			}
			if got := tb.plaud.loginStatus(); got != c.state {
				t.Errorf("login %s, want %s", got, c.state)
			}
		})
	}
}

// A look-up of the account that is still running when brain_access is
// saved does not take over the login in the new home, which may be your
// terminal's: the login stays moved until you connect again.
func TestPlaudAccountLookUpLeavesAMovedLoginAlone(t *testing.T) {
	tb, _ := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
		t.Fatal(err)
	}
	private := tb.plaud.cliHome()
	shared := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := tb.plaud.run
	tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
		out, err := run(ctx, args...)
		if args[0] == "me" {
			if err := tb.updateSettings(func(c *PlaudSnapshot) error { c.Plaud.BrainAccess = true; return nil }); err != nil {
				t.Error(err)
			}
		}
		return out, err
	}
	if _, err := tb.plaud.me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tb.plaud.loginStatus(); got != plaudMoved {
		t.Errorf("login %s, want moved", got)
	}
	if st, _ := tb.plaud.loadState(); st.Login == nil || st.Login.Dir != private || st.Login.Account != "" {
		t.Errorf("login = %+v, want Fridolin's sign-in in %s, untouched", st.Login, private)
	}
}

// The scheduled path does not run even one CLI command against a terminal's
// login after the setting moves HOME. The command integration separately pins
// CheckNow's refusal and the dashboard setting/reconnect round trip.
func TestPlaudMovedLoginTickWaitsForReconnect(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(tb.home, ".plaud", "tokens.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	fp.Write("account", "u_2")
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	if err := tb.updateSettings(func(s *PlaudSnapshot) error { s.Plaud.BrainAccess = true; return nil }); err != nil {
		t.Fatal(err)
	}
	tb.plaud.tick(context.Background(), time.Now().Add(-time.Second))
	if calls := fp.Calls(); calls != "" {
		t.Fatalf("the CLI ran with a login Fridolin did not make:\n%s", calls)
	}
	if got := tb.plaud.loginStatus(); got != plaudMoved {
		t.Errorf("login = %s, want moved", got)
	}
}
