package plaud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
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
			tb.plaud.submit = func(br *Target, jid, sender, txt string, media []string, done func(error)) error {
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
	tb.plaud.submit = func(br *Target, jid, sender, text string, media []string, done func(error)) error {
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
	tb.plaud.submit = func(br *Target, jid, sender, text string, media []string, done func(error)) error {
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
		if err := tb.plaud.BuildApp(); !errors.Is(err, ErrStopping) {
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
		if err := tb.plaud.Connect(); !errors.Is(err, ErrStopping) {
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
			var job ConnectJob
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

// The manager uses only its owned options: it hands each recording to
// submit and makes no unsolicited notification.
func TestPlaudManagerUsesOnlyWhatItIsGiven(t *testing.T) {
	tb, fp := plaudService(t, "")
	type handed struct {
		brain, jid, sender, text string
		media                    []string
	}
	var got []handed
	p := New(tb.options)
	t.Cleanup(p.Stop)
	p.submit = func(br *Target, jid, sender, text string, media []string, done func(error)) error {
		got = append(got, handed{br.ID(), jid, sender, text, media})
		return nil
	}
	p.notify = func(context.Context, string, string) error { t.Error("notify called"); return nil }
	plaudServiceSignIn(t, tb)
	if err := p.saveState(plaudServiceSeededState("u_1", servicePlaudOld1)); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudNew, servicePlaudOld1)
	fp.Recording(servicePlaudNew, true)
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("handed over %d times, want once: %+v", len(got), got)
	}
	h := got[0]
	if h.brain != "p" || h.jid != "15551234567@s.whatsapp.net" || h.sender != "plaud" || !strings.HasPrefix(h.text, "[Plaud] New recording") ||
		len(h.media) != 1 || !strings.HasSuffix(h.media[0], " - "+servicePlaudNew+".txt") {
		t.Errorf("handed %+v", h)
	}
	time.Sleep(100 * time.Millisecond)
	if out := tb.dev.Outbox(); len(out) != 0 {
		t.Errorf("the bridge sent %+v, want nothing: only submit was to be used", out)
	}
}

// A login Plaud refuses while its tokens.json stays (revoked on Plaud's
// side) also shows as expired, until a check works again.
func TestPlaudRefusedLoginShowsExpired(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceSignIn(t, tb)
	if err := tb.plaud.saveState(plaudServiceRecordLogin(tb, plaudServiceSeededState("u_1", servicePlaudOld1))); err != nil {
		t.Fatal(err)
	}
	fp.Files(servicePlaudOld1)
	fp.Write("revoked", "")
	ctx := context.Background()
	if err := tb.plaud.Poll(ctx); !errors.Is(err, errPlaudAuth) {
		t.Fatalf("poll = %v, want the login refused", err)
	}
	if s := tb.plaud.Status(); !s.Expired || !s.Connected {
		t.Errorf("expired=%v connected=%v, want both", s.Expired, s.Connected)
	}
	_ = os.Remove(filepath.Join(fp.Dir, "revoked"))
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if s := tb.plaud.Status(); s.Expired {
		t.Error("still expired after a check that worked")
	}
}

// Connect again after Plaud refused a login whose tokens.json stays: with
// a login in place `plaud login` only says "Already logged in", so Connect
// signs the refused login out first, as Disconnect does, then signs in
// afresh, and the card shows the new login connected. A sign-in that keeps
// the refused login, because the sign-out could not remove it, fails, and
// so does one whose new login Plaud refuses as well; the card then shows
// the login expired, and the chat, told once already, is not told again.
// A first sign-in signs nothing out; one whose login Plaud refuses right
// away shows it expired too, and the check after it tells the chat once.
func TestPlaudConnectAgainAfterARefusedLogin(t *testing.T) {
	cases := []struct {
		name    string
		access  bool     // plaud.brain_access
		refused bool     // Plaud refuses the login when Connect starts
		keeps   bool     // `plaud logout` leaves the login (logout_keeps)
		again   bool     // Plaud refuses the sign-in's new login too (login_refused)
		cmds    []string // the CLI commands the sign-in runs, in order
		want    string   // how the sign-in ends
		says    string   // what its error says
	}{
		{"refused login", false, true, false, false, []string{"logout", "login", "me"}, "done", ""},
		{"refused login shared with brains", true, true, false, false, []string{"logout", "login", "me"}, "done", ""},
		// Fridolin's private copy goes even when the CLI keeps it.
		{"refused login the sign-out leaves", false, true, true, false, []string{"logout", "login", "me"}, "done", ""},
		{"refused login the sign-out leaves, shared with brains", true, true, true, false, []string{"logout", "login"}, "error", "could not be signed out"},
		{"refused again right after the sign-in", false, true, false, true, []string{"logout", "login", "me"}, "error", "Plaud refuses the login it left"},
		{"refused again right after the sign-in, shared with brains", true, true, false, true, []string{"logout", "login", "me"}, "error", "Plaud refuses the login it left"},
		{"first sign-in", false, false, false, false, []string{"login", "me"}, "done", ""},
		{"first sign-in Plaud refuses right away", false, false, false, true, []string{"login", "me"}, "error", "Plaud refuses the login it left"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb, fp := plaudService(t, fmt.Sprintf(`, "brain_access": %v`, tc.access))
			ctx := context.Background()
			notices := func() int {
				n := 0
				for _, m := range tb.dev.Outbox() {
					if strings.Contains(m.Text, "Plaud: the login expired") {
						n++
					}
				}
				return n
			}
			if tc.refused {
				plaudServiceRefuseLogin(t, tb, fp)
				if s := tb.plaud.Status(); !s.Expired || !s.Connected || notices() != 1 {
					t.Fatalf("before connecting: expired=%v connected=%v notices=%d, want expired, connected and one notice", s.Expired, s.Connected, notices())
				}
			}
			if tc.again {
				fp.Write("login_refused", "")
				fp.Write("revoked", "")
			}
			if tc.keeps {
				fp.Write("logout_keeps", "")
			}
			before, told := fp.Calls(), notices()
			st, _ := tb.plaud.loadState()
			var notified time.Time
			if st != nil {
				notified = st.AuthNotified
			}

			if err := tb.plaud.Connect(); err != nil {
				t.Fatal(err)
			}
			var job ConnectJob
			var refused bool
			// Not through Status: once the sign-in ended, that may ask who
			// is signed in, a command of its own.
			testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool {
				tb.plaud.mu.Lock()
				defer tb.plaud.mu.Unlock()
				job, refused = *tb.plaud.connect, tb.plaud.refused
				return job.State != "running"
			})
			if n := notices(); n != told {
				t.Errorf("%d expiry notices after connecting, want %d", n, told)
			}
			var cmds []string
			for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.Calls(), before)), "\n") {
				if f := strings.Fields(line); len(f) > 2 {
					cmds = append(cmds, f[2])
				}
			}
			if !slices.Equal(cmds, tc.cmds) {
				t.Errorf("the sign-in ran %q, want %q", cmds, tc.cmds)
			}
			if job.State != tc.want {
				t.Fatalf("sign-in %s (%s), want %s", job.State, job.Error, tc.want)
			}
			st, _ = tb.plaud.loadState()
			s := tb.plaud.Status()
			if tc.want == "done" {
				if refused || !s.Connected || s.Expired || s.Account != "you@example.com" || !st.AuthNotified.IsZero() {
					t.Errorf("after the sign-in: refused=%v connected=%v expired=%v account=%q notified=%v; want connected as you@example.com, the notice armed again",
						refused, s.Connected, s.Expired, s.Account, st.AuthNotified)
				}
				if err := tb.plaud.Poll(ctx); err != nil {
					t.Errorf("the check after the sign-in: %v", err)
				}
			} else {
				if !strings.Contains(job.Error, tc.says) || !refused || !s.Expired || !st.AuthNotified.Equal(notified) {
					t.Errorf("after the sign-in: error %q, refused=%v expired=%v notified=%v (was %v); want it to say so, refused and expired, notified as before",
						job.Error, refused, s.Expired, st.AuthNotified, notified)
				}
				if err := tb.plaud.Poll(ctx); !errors.Is(err, errPlaudAuth) {
					t.Errorf("the check after the sign-in = %v, want the login still refused", err)
				}
			}
			// After a sign-in Plaud refused, the chat has heard of it once:
			// before the sign-in, or from the check after a first one.
			want := told
			if tc.want == "error" {
				want = max(told, 1)
			}
			if n := notices(); n != want {
				t.Errorf("%d expiry notices after the check, want %d", n, want)
			}
		})
	}
}

// The sign-out before the sign-in gets as long as Disconnect gives one,
// not the sign-in's ten minutes: a Plaud that hangs on it must not keep
// the card running for that long before `plaud login` prints its link.
// Fridolin's private copy of the login goes all the same, so the sign-in
// after it signs in afresh.
func TestPlaudConnectGivesTheSignOutItsOwnTime(t *testing.T) {
	tb, fp := plaudService(t, "")
	plaudServiceRefuseLogin(t, tb, fp)
	old := plaudSignOutTimeout
	plaudSignOutTimeout = 300 * time.Millisecond
	t.Cleanup(func() { plaudSignOutTimeout = old })
	fp.Write("hang_logout", "")
	before := fp.Calls()
	if err := tb.plaud.Connect(); err != nil {
		t.Fatal(err)
	}
	// Well below the 30 s the fake's logout hangs for.
	var job ConnectJob
	testutil.WaitFor(t, 10*time.Second, "the sign-in to end", func() bool {
		tb.plaud.mu.Lock()
		defer tb.plaud.mu.Unlock()
		job = *tb.plaud.connect
		return job.State != "running"
	})
	var cmds []string
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(fp.Calls(), before)), "\n") {
		if f := strings.Fields(line); len(f) > 2 {
			cmds = append(cmds, f[2])
		}
	}
	if want := []string{"logout", "login", "me"}; !slices.Equal(cmds, want) || job.State != "done" {
		t.Errorf("the sign-in ran %q and ended %s (%s), want %q and done", cmds, job.State, job.Error, want)
	}
}

// The transcript file holds what Plaud has, read the way the real CLI
// prints it: the summary whole and without the "Summary: <name>" header,
// even when a line of it starts like the spinner; the transcript without
// its header; the recording's own date even when Plaud has no start time;
// and no summary for an empty note.
func TestPlaudForwardReadsWhatTheCLIPrints(t *testing.T) {
	created := time.Date(2026, 10, 15, 7, 30, 0, 0, time.UTC).Local()
	for _, c := range []struct {
		name    string
		change  func(r *testutil.PlaudRecording)
		want    []string // in the file, which starts with its name
		notWant []string
	}{
		{name: "a summary line that starts like the spinner",
			change:  func(r *testutil.PlaudRecording) { r.Notes[0].Content = servicePlaudGoldenSummary },
			want:    []string{"=== Plaud AI summary ===\n" + servicePlaudGoldenSummary + "\n\n=== Transcript ===\n"},
			notWant: []string{"Team sync\n## Team sync"}},
		{name: "the transcript without its header",
			change:  func(*testutil.PlaudRecording) {},
			want:    []string{"=== Transcript ===\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n"},
			notWant: []string{"Transcript: Team sync"}},
		{name: "no start time: the day it reached Plaud",
			change:  func(r *testutil.PlaudRecording) { r.StartAt, r.CreatedAt = nil, "2026-10-15T07:30:00" },
			want:    []string{created.Format("2006-01-02") + " - Team sync - ", "Recorded: " + created.Format("2006-01-02 15:04") + "\n"},
			notWant: []string{"Recorded: -"}},
		{name: "an empty summary note",
			change:  func(r *testutil.PlaudRecording) { r.Notes[0].Content = "" },
			want:    nil,
			notWant: []string{"=== Plaud AI summary ===", "no content yet"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, "")
			r := fp.TeamSync(servicePlaudNew, true)
			c.change(&r)
			if err := plaudServiceDeliverRecording(t, tb, fp, r); err != nil {
				t.Fatal(err)
			}
			got := tb.plaudDelivered(t, servicePlaudNew)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("the file lacks %q:\n%s", w, got)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got, w) {
					t.Errorf("the file has %q:\n%s", w, got)
				}
			}
		})
	}
}

// With plaud.polished the cleaned-up transcript is sent when Plaud has
// one. Plaud makes the raw transcript first and may never make the other,
// so after plaudPolishedWait the raw one goes instead, with a note that
// says so; before that the recording waits.
func TestPlaudPolishedFallsBackToTheRawTranscript(t *testing.T) {
	raw := "[00:00 - 00:04] Speaker 1: Let's ship on Friday."
	for _, c := range []struct {
		name     string
		polished *string // the polished block's segment, nil for none
		age      time.Duration
		want     string // the transcript sent, "" for none yet
		note     bool
	}{
		{"cleaned up", plaudServicePtr("Let us ship on Friday."), time.Minute, "[00:00 - 00:04] Speaker 1: Let us ship on Friday.", false},
		{"none yet", nil, time.Minute, "", false},
		{"an empty one yet", plaudServicePtr(""), time.Minute, "", false},
		{"none after the wait", nil, plaudPolishedWait + time.Minute, raw, true},
		{"an empty one after the wait", plaudServicePtr(""), plaudPolishedWait + time.Minute, raw, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, fp := plaudService(t, `, "polished": true`)
			plaudServiceSignIn(t, tb)
			first := time.Now().Add(-c.age)
			if err := tb.plaud.saveState(plaudServiceWaiting(plaudServiceSeededState("u_1", servicePlaudOld1), servicePlaudNew, first)); err != nil {
				t.Fatal(err)
			}
			r := fp.TeamSync(servicePlaudNew, true)
			if c.polished != nil {
				b := testutil.PlaudBlock{DataType: "transaction_polish"}
				if *c.polished != "" {
					b.Content = testutil.PlaudTranscript(testutil.PlaudSegment{Start: 0, End: 4000, Speaker: "Speaker 1", Content: *c.polished})
				}
				r.Sources = append(r.Sources, b)
			}
			fp.Files(servicePlaudNew, servicePlaudOld1)
			fp.Put(r)
			logs := plaudServiceCaptureLog(t)
			if err := tb.plaud.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if tb.plaudForwarded() {
					t.Fatal("handed over before the cleaned-up transcript was there or the wait was up")
				}
				if st, _ := tb.plaud.loadState(); st.Pending[servicePlaudNew] == nil {
					t.Errorf("state = %+v, want the recording still waiting", st)
				}
				return
			}
			got := tb.plaudDelivered(t, servicePlaudNew)
			if !strings.Contains(got, "=== Transcript ===\n"+c.want+"\n") {
				t.Errorf("the file lacks the transcript %q:\n%s", c.want, got)
			}
			if strings.Contains(got, "this is the raw one") != c.note || plaudServiceLogContains(logs, "sending the raw one") != c.note {
				t.Errorf("note about the raw transcript in the file or the log, want %v:\n%s\n%q", c.note, got, logs())
			}
		})
	}
}

// The earlier transcript is found whatever characters the media folder's
// path holds, and only the file of that very recording.
func TestPlaudTranscriptPathFindsTheEarlierFile(t *testing.T) {
	const id = "of_00000000000000000000aa"
	for _, folder := range []string{"media", "media [1]", "media[", "media *?"} {
		t.Run(folder, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), folder)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			fresh := filepath.Join(dir, "17.10.2026 - Weekly sync - "+id+".txt")
			if got := plaudTranscriptPath(dir, "17.10.2026", "Weekly sync", id); got != fresh {
				t.Errorf("with no earlier file: %s, want %s", got, fresh)
			}
			for _, other := range []string{"16.10.2026 - Team sync - " + id + "b.txt", "16.10.2026 - Team sync - x" + id + ".txt"} {
				if err := os.WriteFile(filepath.Join(dir, other), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			earlier := filepath.Join(dir, "16.10.2026 - Team sync - "+id+".txt")
			if err := os.WriteFile(earlier, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := plaudTranscriptPath(dir, "17.10.2026", "Weekly sync", id); got != earlier {
				t.Errorf("with an earlier file: %s, want %s", got, earlier)
			}
		})
	}
}

// A shared login that is a symlink, which an older version's sandbox let a
// brain put in its place, is not used: the CLI, which runs outside the
// sandbox, would write your next sign-in through it (into ~/.zshrc, say),
// and chmod what it points to. A sign-out still runs, and removes only the
// link.
func TestPlaudRefusesALoginThatIsASymlink(t *testing.T) {
	tb, fp := plaudService(t, `, "brain_access": true`)
	fp.Files()
	const rcText = "export PATH=/usr/bin\n"
	rc := plaudServiceWriteFile(t, filepath.Join(tb.home, ".zshrc"), rcText)
	if err := os.MkdirAll(filepath.Dir(tb.plaud.tokenPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rc, tb.plaud.tokenPath()); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"files", "login"} {
		if _, err := tb.plaud.runCLIOut(context.Background(), nil, cmd); err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Errorf("plaud %s: %v, want it refused", cmd, err)
		}
	}
	if calls := fp.Calls(); calls != "" {
		t.Errorf("the CLI ran:\n%s", calls)
	}
	if _, err := tb.plaud.runCLIOut(context.Background(), nil, "logout"); err != nil {
		t.Errorf("plaud logout: %v", err)
	}
	if _, err := os.Lstat(tb.plaud.tokenPath()); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	if data, _ := os.ReadFile(rc); string(data) != rcText {
		t.Errorf("~/.zshrc = %q, want it untouched", data)
	}
}

// TestPlaudRefusesALoginElsewhere: only a symlinked tokens.json was
// refused, but a brain under an older sandbox could also have swapped
// ~/.plaud itself for a symlink, or left a hard link to the login, which
// the bridge's CLI, outside the sandbox, then wrote through. Both are
// refused now as the symlink is; a login of its own is not.
func TestPlaudRefusesALoginElsewhere(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home, elsewhere string)
		says  string // "" for a login that is fine
	}{
		{"a login of its own", func(t *testing.T, home, _ string) {
			plaudServiceWriteFile(t, filepath.Join(home, ".plaud", "tokens.json"), "{}")
		}, ""},
		{"no login yet", func(t *testing.T, home, _ string) {}, ""},
		{"a symlinked folder", func(t *testing.T, home, elsewhere string) {
			plaudServiceWriteFile(t, filepath.Join(elsewhere, "tokens.json"), "{}")
			if err := os.Symlink(elsewhere, filepath.Join(home, ".plaud")); err != nil {
				t.Fatal(err)
			}
		}, ".plaud is a symlink"},
		{"a file where the folder goes", func(t *testing.T, home, _ string) {
			plaudServiceWriteFile(t, filepath.Join(home, ".plaud"), "x")
		}, ".plaud is not a folder"},
		{"a hard link", func(t *testing.T, home, elsewhere string) {
			other := plaudServiceWriteFile(t, filepath.Join(elsewhere, "zshrc"), "x")
			if err := os.MkdirAll(filepath.Join(home, ".plaud"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(other, filepath.Join(home, ".plaud", "tokens.json")); err != nil {
				t.Fatal(err)
			}
		}, "tokens.json is a file with another name too"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, elsewhere := t.TempDir(), t.TempDir()
			c.setup(t, home, elsewhere)
			err := checkPlaudLogin(home)
			if c.says == "" {
				if err != nil {
					t.Errorf("checkPlaudLogin = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Errorf("checkPlaudLogin = %v, want it saying %q", err, c.says)
			}
		})
	}
}

// The card offers the sign-in link the CLI prints when it cannot open a
// browser, and no other: not a link from a line before it, not one that is
// not https (or http to this machine), and not one still arriving.
func TestPlaudSignInURL(t *testing.T) {
	const link = "https://web.plaud.ai/platform/oauth?client_id=x&state=y"
	for _, c := range []struct {
		name, out, want string
	}{
		{"as the CLI prints it", "\nOpening browser for authentication...\n\nCould not open browser. Open this URL manually:\n  " + link + "\n", link},
		{"on the same line", "Could not open browser. Open this URL manually: " + link + "\n", link},
		{"after another link", "See https://docs.example.com/help first.\nCould not open browser. Open this URL manually:\n  " + link + "\n", link},
		{"another link only", "See https://docs.example.com/help first.\n", ""},
		{"plain http elsewhere", "Open this URL manually:\n  http://web.plaud.ai/platform/oauth\n", ""},
		{"http to this machine", "Open this URL manually:\n  http://127.0.0.1:8199/auth/callback?code=1\n", "http://127.0.0.1:8199/auth/callback?code=1"},
		{"not a web link", "Open this URL manually:\n  javascript:alert(1)\n", ""},
		{"still arriving", "Open this URL manually:\n  " + link, ""},
		{"in colour", "\x1b[33mCould not open browser. Open this URL manually:\n  " + link + "\x1b[39m\n", link},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := plaudSignInURL(c.out); got != c.want {
				t.Errorf("plaudSignInURL(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}
