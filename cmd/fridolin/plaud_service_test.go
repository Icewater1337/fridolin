package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A recording that keeps failing is given up after three days, like one
// Plaud never transcribes, and its error then says so instead of promising
// another try. The log blames Plaud only for the one it never transcribed.
func TestPlaudFailingRecordingIsGivenUpInTime(t *testing.T) {
	for _, c := range []struct {
		name    string
		age     time.Duration // since the recording was first seen
		ready   bool          // whether Plaud has its transcript
		fails   bool          // whether fetching it fails
		says    string        // in the check's error; "" for none
		logs    string        // in the log; "" for no giving up
		pending bool          // whether it is tried again
	}{
		{"recent", time.Hour, true, true, "(will retry)", "", true},
		{"failing for over three days", plaudPendingMaxAge + time.Hour, true, true, "(given up after 3 days)", "still failing after 3 days, giving up", false},
		{"untranscribed for over three days", plaudPendingMaxAge + time.Hour, false, false, "", "no transcript after 3 days, giving up", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			plaudServiceSignIn(t, tb)
			first := time.Now().Add(-c.age)
			if err := tb.plaud.saveState(plaudServiceWaiting(plaudServiceSeededState("u_1", servicePlaudOld1), servicePlaudNew, first)); err != nil {
				t.Fatal(err)
			}
			fp.Files(servicePlaudNew, servicePlaudOld1)
			fp.Recording(servicePlaudNew, c.ready)
			if c.fails {
				fp.Write("fail_file", "")
			}
			logs := plaudServiceCaptureLog(t)
			err := tb.plaud.Poll(context.Background())
			if c.says == "" && err != nil || c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)) {
				t.Fatalf("poll error = %v, want it to say %q", err, c.says)
			}
			if got := strings.Join(logs(), "\n"); strings.Contains(got, "giving up") != (c.logs != "") || !strings.Contains(got, c.logs) {
				t.Errorf("log = %q, want it to say %q", got, c.logs)
			}
			st, _ := tb.plaud.loadState()
			if _, pending := st.Pending[servicePlaudNew]; pending != c.pending || slices.Contains(st.Seen, servicePlaudNew) == c.pending {
				t.Errorf("state = %+v, want pending=%v", st, c.pending)
			}
		})
	}
}

// A recording that is not ready is asked about at the next check, then
// ever less often, up to every 15 minutes, rather than once a minute for
// three days.
func TestPlaudBacksOffOnARecordingThatIsNotReady(t *testing.T) {
	for tries, want := range []time.Duration{0, 0, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		if got := plaudRetryDelay(tries); got != want {
			t.Errorf("plaudRetryDelay(%d) = %v, want %v", tries, got, want)
		}
	}
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceWaiting(plaudServiceSeededState("u_1", servicePlaudOld1), servicePlaudNew, time.Now())); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, false)
	asked := func() int { return strings.Count(fp.Calls(), " file "+servicePlaudNew+"\n") }
	for i, want := range []int{1, 2, 2, 2} {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if asked() != want {
			t.Fatalf("after check %d Plaud was asked %d times, want %d", i+1, asked(), want)
		}
	}
	st, _ := tb.plaud.loadState()
	if it := st.Pending[servicePlaudNew]; it.Tries != 2 || time.Until(it.NextTry) < time.Minute || it.Name != "Team sync" {
		t.Errorf("waiting = %+v, want 2 tries, the next in about 2 minutes, and its name", it)
	}
	if err := tb.plaud.update(func(st *plaudState) error { st.Pending[servicePlaudNew].NextTry = time.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	fp.Recording(servicePlaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tb.plaudForwarded() {
		t.Error("not handed over once due and ready")
	}
}

// A recording Fridolin gives up on is listed on the card with the reason,
// and the chat hears about it once, unless it was deleted in Plaud, which
// gives up at once and quietly.
func TestPlaudGivesUpVisibly(t *testing.T) {
	for _, c := range []struct {
		name    string
		age     time.Duration
		setup   func(fp *testutil.PlaudFixture)
		reason  string
		notices int
	}{
		{"never transcribed", plaudPendingMaxAge + time.Hour,
			func(fp *testutil.PlaudFixture) { fp.Recording(servicePlaudNew, false) }, "Plaud had no transcript for it after 3 days", 1},
		{"fetching keeps failing", plaudPendingMaxAge + time.Hour,
			func(fp *testutil.PlaudFixture) { fp.Recording(servicePlaudNew, true); fp.Write("fail_transcript", "") }, "fetching it from Plaud failed for 3 days", 1},
		{"deleted in Plaud", time.Minute,
			func(*testutil.PlaudFixture) {}, "it was deleted in Plaud", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			plaudServiceSignIn(t, tb)
			if err := tb.plaud.saveState(plaudServiceWaiting(plaudServiceSeededState("u_1", servicePlaudOld1), servicePlaudNew, time.Now().Add(-c.age))); err != nil {
				t.Fatal(err)
			}
			fp.Files(servicePlaudNew, servicePlaudOld1)
			c.setup(fp)
			for range 2 {
				_ = tb.plaud.Poll(context.Background())
			}
			s := tb.plaud.Status()
			if len(s.GivenUp) != 1 || s.GivenUp[0].ID != servicePlaudNew || !strings.Contains(s.GivenUp[0].Reason, c.reason) {
				t.Fatalf("given up = %+v, want %s: %q", s.GivenUp, servicePlaudNew, c.reason)
			}
			if *s.Pending != 0 {
				t.Errorf("%d still waiting", *s.Pending)
			}
			var got []string
			for _, m := range tb.dev.Outbox() {
				got = append(got, m.Text)
			}
			if len(got) != c.notices || c.notices > 0 && (!strings.Contains(got[0], "“Team sync” was not handed to brain p: "+c.reason) || tb.dev.Outbox()[0].JID != "15551234567@s.whatsapp.net") {
				t.Errorf("notices = %q, want %d about it in the DM", got, c.notices)
			}
		})
	}
}

// The expiry notice goes out once a day. Its time is saved in the state
// and survives a restart, also before the first sync, which the saved
// time does not count as.
func TestPlaudExpiredLoginNotifiesOncePerDay(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("seeded=%v", seeded), func(t *testing.T) {
			tb, fp := plaudService(t, "")
			plaudServiceSignIn(t, tb)
			if seeded {
				if err := tb.plaud.saveState(plaudServiceSeededState("", servicePlaudOld1)); err != nil {
					t.Fatal(err)
				}
			}
			fp.Write("expired", "")
			for i := 0; i < 2; i++ {
				if err := tb.plaud.Poll(context.Background()); err != errPlaudAuth {
					t.Fatalf("poll %d: err = %v", i, err)
				}
			}
			out := tb.dev.Outbox()
			if len(out) != 1 || !strings.Contains(out[0].Text, "Plaud: the login expired") {
				t.Errorf("notices = %+v, want exactly one", out)
			}
			if tb.plaudSeeded() != seeded {
				t.Errorf("seeded = %v after the notice, want %v", !seeded, seeded)
			}
			if st, _ := tb.plaud.loadState(); st.AuthNotified.IsZero() {
				t.Error("the notice time was not saved in the state")
			}
			if err := plaudServiceAfterRestart(t, tb).Poll(context.Background()); err != errPlaudAuth {
				t.Fatalf("poll after restart: err = %v", err)
			}
			if out := tb.dev.Outbox(); len(out) != 1 {
				t.Errorf("notices after a restart = %+v, want still one", out)
			}
		})
	}
}

// Changes to the state never get lost to a poll that works through
// recordings meanwhile: a sign-in that finishes during a poll keeps its
// record. A login from before sign-ins were recorded gets one with the
// first check that works.
func TestPlaudStateChangesDuringAPollStay(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	signedIn := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	run := tb.plaud.run
	tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "file" {
			st, _ := tb.plaud.loadState()
			if st.Login == nil || st.Login.Dir != tb.plaud.cliHome() {
				t.Errorf("the old login was not recorded: %+v", st.Login)
			}
			if err := tb.plaud.update(func(st *plaudState) error {
				st.Login = &plaudLogin{Dir: tb.plaud.cliHome(), Since: signedIn}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}
		return run(ctx, args...)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	tb.waitPlaudSeen(t, servicePlaudNew)
	st, _ := tb.plaud.loadState()
	if st.Login == nil || !st.Login.Since.Equal(signedIn) || !slices.Contains(st.Seen, servicePlaudNew) {
		t.Errorf("state = %+v, login %+v: want the sign-in made during the poll, and the recording seen", st, st.Login)
	}
}

// The account is checked again when the login changes, and only then.
func TestPlaudChecksTheAccountWhenTheLoginChanges(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudOld1)
	for i := 0; i < 2; i++ {
		if err := tb.plaud.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(fp.Calls(), " me\n"); n != 1 {
		t.Errorf("`plaud me` ran %d times in two polls, want once", n)
	}

	// Signed out, and in again with another account.
	fp.Write("account", "u_2")
	plaudServiceSignIn(t, tb)
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(tb.plaud.tokenPath(), later, later)
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(fp.Calls(), " me\n"); n != 2 {
		t.Errorf("`plaud me` ran %d times, want again after the new login", n)
	}
	if tb.plaudForwarded() {
		t.Error("the other account's existing recording was sent")
	}
	if st, _ := tb.plaud.loadState(); st.Account != "u_2" || !slices.Contains(st.Seen, servicePlaudNew) {
		t.Errorf("state = %+v, want it seeded for u_2", st)
	}
}

// A login that changes in the middle of a poll ("Connect again" with another
// account finishing, brain_access switching) is not trusted for that poll:
// what Plaud lists may already be the other account's, and none of it may
// be sent. The next poll checks the account and starts over for it.
func TestPlaudLoginChangingDuringAPollSendsNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		state  *plaudState // nil: no first sync yet
		after  string      // the login changes once this command ran…
		before string      // …or just before this one runs
	}{
		{"while the first sync lists the recordings", nil, "", "files"},
		{"just after the account was looked up", plaudServiceSeededState("u_1", servicePlaudOld1), "me", ""},
		{"before the recordings are listed", plaudServiceSeededState("u_1", servicePlaudOld1), "", "files"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			plaudServiceSignIn(t, tb)
			if c.state != nil {
				if err := tb.plaud.saveState(c.state); err != nil {
					t.Fatal(err)
				}
			}
			fp.Files(servicePlaudOld1)
			for _, id := range []string{servicePlaudOld2, servicePlaudNew} {
				fp.Recording(id, true)
			}
			// u_2 signs in: `me` names it, Plaud lists its recordings, and
			// the login file is rewritten.
			switched := false
			switchAccount := func() {
				switched = true
				fp.Write("account", "u_2")
				fp.Files(servicePlaudNew, servicePlaudOld2)
				if err := os.WriteFile(tb.plaud.tokenPath(), []byte(`{"user":"u_2"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				later := time.Now().Add(time.Minute)
				_ = os.Chtimes(tb.plaud.tokenPath(), later, later)
			}
			run := tb.plaud.run
			tb.plaud.run = func(ctx context.Context, args ...string) (string, error) {
				if !switched && args[0] == c.before {
					switchAccount()
				}
				out, err := run(ctx, args...)
				if !switched && args[0] == c.after {
					switchAccount()
				}
				return out, err
			}

			for i := 0; i < 3; i++ {
				if err := tb.plaud.Poll(context.Background()); err != nil {
					t.Fatalf("poll %d: %v", i, err)
				}
			}
			if !switched {
				t.Fatal("the login never changed")
			}
			if tb.plaudForwarded() {
				t.Error("the other account's existing recordings were sent")
			}
			st, _ := tb.plaud.loadState()
			if st.Account != "u_2" || !slices.Contains(st.Seen, servicePlaudNew) || !slices.Contains(st.Seen, servicePlaudOld2) || len(st.Pending) != 0 {
				t.Errorf("state = %+v, want it seeded for u_2", st)
			}
		})
	}
}

// The message a recording comes with says what arrived, in a fixed first
// line, then the instruction: plaud.message, or a built-in one that points
// to the plaud skill and names no folder or date convention of its own.
// Dates go year first. The status gives the dashboard the built-in
// instruction and the interval it uses.
func TestPlaudMessageSaysWhatArrived(t *testing.T) {
	recorded := time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC).Local()
	head := `[Plaud] New recording "Team sync" (` + recorded.Format("2006-01-02 15:04") + `, 5m00s). The attached .txt is Plaud's finished transcript`
	for _, c := range []struct {
		name, settings string
		instruction    string
		interval       int
	}{
		{"built in", "", defaultPlaudMessage, 60},
		{"plaud.message", `, "message": "File {name} under inbox/.", "interval_seconds": 120`, "File Team sync under inbox/.", 120},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, c.settings)
			var text, file string
			tb.plaud.submit = func(br *PlaudTarget, jid, sender, txt string, media []string, done func(error)) error {
				text, file = txt, filepath.Base(media[0])
				return nil
			}
			if err := plaudServiceDeliver(t, tb, fp); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, "\n\n"+c.instruction) {
				t.Errorf("message = %q, want %q… then %q", text, head, c.instruction)
			}
			for _, personal := range []string{"transcripts/", "DD.MM.YYYY", "memories"} {
				if strings.Contains(text, personal) {
					t.Errorf("the message names %q: %q", personal, text)
				}
			}
			if want := recorded.Format("2006-01-02") + " - Team sync - " + servicePlaudNew + ".txt"; file != want {
				t.Errorf("file = %s, want %s", file, want)
			}
			if s := tb.plaud.Status(); s.DefaultMessage != defaultPlaudMessage || s.IntervalSeconds != c.interval {
				t.Errorf("status default_message = %q, interval_seconds = %d", s.DefaultMessage, s.IntervalSeconds)
			}
		})
	}
}

// A check pages back through Plaud's list until it reaches a recording it
// knows, so a gap (the bridge down, the login expired, sending off) misses
// nothing. A backlog arrives a few recordings per check, oldest first.
func TestPlaudPagesBackToTheLastKnownRecording(t *testing.T) {
	tb, fp := plaudService(t, "")
	var handed []string
	tb.plaud.submit = func(br *PlaudTarget, jid, sender, text string, media []string, done func(error)) error {
		handed = append(handed, strings.TrimSuffix(media[0][strings.LastIndex(media[0], " - ")+3:], ".txt"))
		done(nil)
		return nil
	}
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld2, servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudServiceIDs("a", 120)
	fp.Files(append(slices.Clone(backlog), servicePlaudOld1, servicePlaudOld2)...)
	for _, id := range backlog {
		fp.Recording(id, true)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := fp.Calls(); !strings.Contains(calls, "files --page 2 --page-size 100\n") || strings.Contains(calls, "--page 3") {
		t.Errorf("listed:\n%s\nwant pages 1 and 2 of 100", calls)
	}
	oldest := slices.Clone(backlog)
	slices.Reverse(oldest)
	if !slices.Equal(handed, oldest[:plaudHandedMax]) {
		t.Errorf("handed over %v, want the oldest %d: %v", handed, plaudHandedMax, oldest[:plaudHandedMax])
	}
	if st, _ := tb.plaud.loadState(); len(st.Pending) != len(backlog)-plaudHandedMax {
		t.Errorf("%d waiting, want %d", len(st.Pending), len(backlog)-plaudHandedMax)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(handed, oldest[:2*plaudHandedMax]) {
		t.Errorf("after two checks handed over %v, want the oldest %d", handed, 2*plaudHandedMax)
	}
}

// Only so many recordings are with brains at once, those of earlier checks
// included: a backlog goes on, oldest first, as their runs end. The limit
// leaves half of a small queue for your own messages.
func TestPlaudBacklogTricklesInAsRunsEnd(t *testing.T) {
	for _, c := range []struct{ maxPending, want int }{{10, plaudHandedMax}, {100, plaudHandedMax}, {6, 3}, {3, 1}, {1, 1}} {
		if got := plaudHandedLimit(c.maxPending); got != c.want {
			t.Errorf("limit with queue.max_pending %d = %d, want %d", c.maxPending, got, c.want)
		}
	}
	tb, fp := plaudService(t, "")
	rec := &plaudServiceRecorder{}
	tb.plaud.submit = rec.submit
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	backlog := plaudServiceIDs("d", 12)
	fp.Files(append(slices.Clone(backlog), servicePlaudOld1)...)
	for _, id := range backlog {
		fp.Recording(id, true)
	}
	oldest := slices.Clone(backlog)
	slices.Reverse(oldest)
	ctx := context.Background()
	ended := 0
	for _, step := range []struct {
		end  int // runs that end before the check, oldest first
		want int // hand-offs in all after it
	}{{0, plaudHandedMax}, {0, plaudHandedMax}, {2, plaudHandedMax + 2}, {0, plaudHandedMax + 2}, {5, 2*plaudHandedMax + 2}} {
		for _, id := range oldest[ended : ended+step.end] {
			rec.end(id, nil)
		}
		ended += step.end
		if err := tb.plaud.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		rec.mu.Lock()
		handed := slices.Clone(rec.handed)
		rec.mu.Unlock()
		if !slices.Equal(handed, oldest[:step.want]) {
			t.Fatalf("after %d more runs ended: handed over %d, want the oldest %d", step.end, len(handed), step.want)
		}
	}
}

// The first sync remembers every page of recordings, oldest first, so
// that trimming the list drops the oldest.
func TestPlaudFirstSyncReadsEveryPage(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	existing := plaudServiceIDs("b", 150)
	fp.Files(existing...)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := tb.plaud.loadState()
	want := slices.Clone(existing)
	slices.Reverse(want)
	if !slices.Equal(st.Seen, want) || len(st.Pending) != 0 {
		t.Errorf("seen %d, pending %d; want all %d seen, oldest first", len(st.Seen), len(st.Pending), len(existing))
	}
}

// A state.json from before the version field still reads: its Unix-second
// times become times, and it counts as seeded when it named the account or
// remembered a recording, as it did then. A saved state is version 2.
func TestPlaudReadsTheOldState(t *testing.T) {
	for _, c := range []struct {
		name, file string
		want       plaudState
	}{
		{"seeded", `{"account":"u_1","seen":["of_a"],"pending":{"of_b":1760000000},"auth_notified":1760000100}`,
			plaudState{Version: 1, Seeded: true, Account: "u_1", Seen: []string{"of_a"},
				Pending: map[string]*plaudPending{"of_b": {FirstSeen: time.Unix(1760000000, 0)}}, AuthNotified: time.Unix(1760000100, 0)}},
		{"from before accounts were recorded", `{"seen":["of_a"],"pending":{},"auth_notified":0}`,
			plaudState{Version: 1, Seeded: true, Seen: []string{"of_a"}, Pending: map[string]*plaudPending{}}},
		{"only an expiry notice", `{"seen":null,"pending":{},"auth_notified":5}`,
			plaudState{Version: 1, Pending: map[string]*plaudPending{}, AuthNotified: time.Unix(5, 0)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, _ := plaudService(t, "")
			if err := os.MkdirAll(tb.plaud.dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tb.plaud.dir(), "state.json"), []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(st)
			want, _ := json.Marshal(c.want)
			if st.Version != c.want.Version || string(got) != string(want) {
				t.Errorf("read %s\nwant %s", got, want)
			}
			if err := tb.plaud.saveState(st); err != nil {
				t.Fatal(err)
			}
			saved, _ := os.ReadFile(filepath.Join(tb.plaud.dir(), "state.json"))
			if !strings.Contains(string(saved), `"version": 2`) || strings.Contains(string(saved), "1760000000") {
				t.Errorf("saved:\n%s", saved)
			}
			again, err := tb.plaud.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if again.seededFor("") != st.Seeded || !again.AuthNotified.Equal(st.AuthNotified) {
				t.Errorf("read back %+v, want %+v", again, st)
			}
		})
	}
}

// Recordings waiting to be handed over go in the order they showed up,
// whatever their ids, and the last one handed over is still on the card
// after a restart.
func TestPlaudPendingGoesOldestFirst(t *testing.T) {
	const older, newer = "of_00000000000000000000bb", "of_00000000000000000000ba"
	tb, fp := plaudService(t, "")
	var order []string
	tb.plaud.submit = func(br *PlaudTarget, jid, sender, text string, media []string, done func(error)) error {
		order = append(order, media[0][len(media[0])-len(older)-4:len(media[0])-4])
		done(nil)
		return nil
	}
	plaudServiceSignIn(t, tb)
	st := plaudServiceWaiting(plaudServiceSeededState("u_1", servicePlaudOld1), older, time.Now().Add(-2*time.Hour))
	if err := tb.plaud.saveState(plaudServiceWaiting(st, newer, time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	fp.Files(newer, older, servicePlaudOld1)
	fp.Recording(older, true)
	fp.Recording(newer, true)
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != older+","+newer {
		t.Errorf("handed over %v, want %s first", order, older)
	}
	if fw := plaudServiceAfterRestart(t, tb).Status().LastForwarded; fw == nil || fw.Name != "Team sync" || fw.Brain != "p" {
		t.Errorf("last forwarded after a restart = %+v", fw)
	}
}

// A sign-in that waits for the browser ends when Fridolin stops, and Stop
// waits for it: it would write to the data folder after Fridolin stopped.
func TestPlaudStopEndsASignIn(t *testing.T) {
	tb, fp := plaudService(t, "")
	fp.Write("login_wait", "")
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, "the sign-in to start", func() bool { return strings.Contains(fp.Calls(), " login") })
	tb.plaud.Stop()
	if c := tb.plaud.Status().Connect; c == nil || c.State != "error" {
		t.Errorf("sign-in after Stop = %+v, want it ended", c)
	}
}

// Status polling and a new sign-in must not add background work once
// Stop has finished waiting. In particular, a status read after shutdown
// must not start an account lookup that writes into the old data folder.
func TestPlaudStopRefusesNewBackgroundWork(t *testing.T) {
	t.Run("phone app build", func(t *testing.T) {
		tb, _ := plaudService(t, "")
		tb.plaud.Stop()
		if err := tb.plaud.BuildApp(); !errors.Is(err, errPlaudStopping) {
			t.Errorf("BuildApp after Stop = %v, want errPlaudStopping", err)
		}
	})
	t.Run("account lookup", func(t *testing.T) {
		tb, _ := plaudService(t, "")
		tb.plaud.Stop()
		release := make(chan struct{})
		tb.plaud.run = func(context.Context, ...string) (string, error) {
			<-release
			return "", context.Canceled
		}
		tb.plaud.refreshAccount(time.Time{})
		tb.plaud.mu.Lock()
		busy := tb.plaud.accountBusy
		tb.plaud.mu.Unlock()
		close(release)
		tb.plaud.bg.Wait()
		if busy {
			t.Error("account lookup started after Stop returned")
		}
	})
	t.Run("sign-in", func(t *testing.T) {
		tb, _ := plaudService(t, "")
		tb.plaud.Stop()
		if err := tb.plaud.Connect(); !errors.Is(err, errPlaudStopping) {
			t.Errorf("Connect after Stop = %v, want errPlaudStopping", err)
		}
		tb.plaud.Stop()
	})
}

// A run that ends while Fridolin stops has its end recorded before Stop
// returns: the brain's worker records it after the reply went out, and
// would otherwise write to the data folder after Fridolin stopped.
func TestPlaudStopWaitsForARunsEnd(t *testing.T) {
	tb, fp := plaudService(t, "")
	rec := &plaudServiceRecorder{}
	tb.plaud.submit = rec.submit
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	if err := tb.plaud.Poll(context.Background()); err != nil || rec.count() != 1 {
		t.Fatalf("poll = %v, handed over %d times", err, rec.count())
	}
	// The run's end waits to write the state while Stop is called.
	tb.plaud.stMu.Lock()
	go rec.end(servicePlaudNew, nil)
	testutil.WaitFor(t, 5*time.Second, "the run's end to begin", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		return !tb.plaud.inFlight[servicePlaudNew]
	})
	stopped := make(chan struct{})
	go func() { tb.plaud.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("Stop returned before the run's end was recorded")
	case <-time.After(200 * time.Millisecond):
	}
	tb.plaud.stMu.Unlock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if !tb.plaudSeenAll(servicePlaudNew) {
		t.Error("the run's end was not recorded")
	}
}

// A fetch the bridge was killed in leaves its folder, with the recording's
// text, in the data folder. The next start removes it, and nothing else.
func TestPlaudStartRemovesUnfinishedFetches(t *testing.T) {
	tb, _ := plaudService(t, "")
	left := filepath.Join(tb.plaud.dir(), plaudFetchPrefix+"123")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "transcript.txt"), []byte("[00:00 - 00:04] Speaker 1: Let's ship on Friday."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	tb.plaud.Start(context.Background())
	tb.plaud.Stop()
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unfinished fetch is still there: %v", err)
	}
	if st, err := tb.plaud.loadState(); err != nil || !st.Seeded {
		t.Errorf("the state = %+v, %v; want it kept", st, err)
	}
}

// A sign-in that fails says why, from the CLI's exit status: a denial is
// not an expired login, and a timeout says how long the CLI waits.
func TestPlaudSignInSaysWhyItFailed(t *testing.T) {
	for _, c := range []struct {
		fixture string
		want    string
	}{
		{"login_deny", "access was not granted"},
		{"login_timeout", "the sign-in timed out: Plaud's tool waits two minutes"},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			fp.Write(c.fixture, "")
			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			var job PlaudConnect
			testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				job = *tb.plaud.Status().Connect
				return job.State != "running"
			})
			if job.State != "error" || !strings.Contains(job.Error, c.want) || strings.Contains(job.Error, "expired") || strings.Contains(job.Error, "Waiting for") {
				t.Errorf("sign-in = %+v, want an error saying %q", job, c.want)
			}
		})
	}
}
