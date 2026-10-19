package main

import (
	"context"
	"crypto/sha256"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/manifest"

	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestVoteText(t *testing.T) {
	cases := []struct {
		selected []string
		want     string
	}{
		{[]string{"Monday 2pm"}, `voted "Monday 2pm" in the poll "When should we meet?"`},
		{[]string{"Monday 2pm", "Tuesday 3pm"}, `voted "Monday 2pm", "Tuesday 3pm" in the poll "When should we meet?"`},
		{nil, `took back their vote in the poll "When should we meet?"`},
	}
	for _, c := range cases {
		if got := voteText("When should we meet?", c.selected); got != c.want {
			t.Errorf("voteText(%q) = %q, want %q", c.selected, got, c.want)
		}
	}
}

// A vote reaches the brain that sent the poll, as an event in its
// history: its next run in that chat sees who voted for what, and the
// identity map names the voter. That is the brain that sent it, even in a
// chat another brain owns.
func TestPollVoteBecomesAHistoryEvent(t *testing.T) {
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	voter := types.NewJID("15551234567", types.DefaultUserServer)
	for _, c := range []struct {
		name, sentBy, want string
	}{
		{"its own chat", "main", "main"},
		{"a chat another brain owns", "files", "files"},
		{"a poll from before polls kept their brain", "", "main"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tb, mainBrain := newMainBridge(t, "")
			tb.addFolderBrain(t, manifest.Manifest{ID: "files", Name: "Files"})
			if err := tb.store.SavePoll("3EB0POLL", testGroup, c.sentBy, "When should we meet?", []string{"Monday 2pm", "Tuesday 3pm"}); err != nil {
				t.Fatal(err)
			}
			tb.whatsApp(t).ProcessVote("3EB0POLL", voter, [][]byte{hash("Tuesday 3pm")})

			for _, br := range []*brain.Brain{mainBrain, tb.brainMgr.Get("files")} {
				h := readHistory(t, br)
				if br.ID() != c.want {
					if len(h) != 0 {
						t.Errorf("brain %s got %+v, want nothing", br.ID(), h)
					}
					continue
				}
				want := history.Entry{From: "15551234567", Chat: testGroup, Type: history.Event, Text: `voted "Tuesday 3pm" in the poll "When should we meet?"`}
				if len(h) != 1 || h[0].From != want.From || h[0].Chat != want.Chat || h[0].Type != want.Type || h[0].Text != want.Text {
					t.Fatalf("brain %s history = %+v, want [%+v]", br.ID(), h, want)
				}
				preface := capturedPrompt(t, br, brain.Inbound{Inbound: chat.Inbound{JID: testGroup, Sender: "15559876543", Text: "next"}})
				if !strings.Contains(preface, `"from":"15551234567","did":"voted \"Tuesday 3pm\" in the poll \"When should we meet?\""`) {
					t.Errorf("preface = %q, want the vote", preface)
				}
			}
			if p, err := tb.store.Poll(context.Background(), "3EB0POLL"); err != nil || p.TotalVoters != 1 {
				t.Errorf("stored poll = %+v, %v; want one voter", p, err)
			}
		})
	}
}

// Who voted for what goes to the debug log only: the main log, which is
// kept and sent along with bug reports, names no voter and no choice.
func TestPollVoteIsLoggedAtDebug(t *testing.T) {
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	logged := captureLog(t)
	tb, _ := newMainBridge(t, "")
	if err := tb.store.SavePoll("3EB0POLL", testGroup, "main", "When should we meet?", []string{"Monday 2pm", "Tuesday 3pm"}); err != nil {
		t.Fatal(err)
	}
	rec := &testWhatsApp{}
	wa := tb.whatsAppOffline(t, &wastore.Device{}, func(opts *whatsapp.Options) { opts.Log = testWaLogger{rec} })
	wa.ProcessVote("3EB0POLL", types.NewJID(otherNumber, types.DefaultUserServer), [][]byte{hash("Tuesday 3pm")})
	if got := strings.Join(logged(), "\n"); strings.Contains(got, otherNumber) || strings.Contains(got, "Tuesday 3pm") {
		t.Errorf("main log %q names the voter or the choice", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.ContainsFunc(rec.logs, func(l string) bool {
		return strings.HasPrefix(l, "DEBUG Poll vote recorded") && strings.Contains(l, otherNumber) && strings.Contains(l, "Tuesday 3pm")
	}) {
		t.Errorf("debug log %q, want the vote", rec.logs)
	}
}

// A vote that cannot be decrypted or saved is logged as an error, the
// voter masked there too, in what whatsmeow says as well.
func TestPollVoteFailuresMaskTheVoter(t *testing.T) {
	voter := types.NewJID(otherNumber, types.DefaultUserServer)
	cases := []struct {
		name string
		vote func(t *testing.T, tb *testBridge, wa *whatsapp.Client)
		want string
	}{
		{"not decrypted", func(t *testing.T, _ *testBridge, wa *whatsapp.Client) {
			// The bridge keeps another secret than the phone used.
			wa.ProcessMessage(pollVoteEvent(t, []byte("0123456789abcdef0123456789abcdef"), voter, "3EB0POLL", "Tuesday 3pm"))
		}, "ERROR Failed to decrypt poll vote from " + MaskPhone(otherNumber) + "@s.whatsapp.net: "},
		{"not saved", func(t *testing.T, tb *testBridge, wa *whatsapp.Client) {
			if _, err := testStoreSQL(t, tb.cfgs.Get().DBPath).Exec(`CREATE TRIGGER refuse_votes BEFORE INSERT ON poll_votes BEGIN SELECT RAISE(ABORT, 'the disk is full'); END`); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte("Tuesday 3pm"))
			wa.ProcessVote("3EB0POLL", voter, [][]byte{hash[:]})
		}, "ERROR Failed to save poll vote from " + MaskPhone(otherNumber) + "@s.whatsapp.net: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logged := captureLog(t)
			tb, _ := newMainBridge(t, "")
			if err := tb.store.SavePoll("3EB0POLL", voter.String(), "main", "When should we meet?", []string{"Monday 2pm", "Tuesday 3pm"}); err != nil {
				t.Fatal(err)
			}
			rec := &testWhatsApp{}
			wa := tb.whatsAppOffline(t, &wastore.Device{MsgSecrets: pollSecrets{key: make([]byte, 32)}}, func(opts *whatsapp.Options) { opts.Log = testWaLogger{rec} })
			c.vote(t, tb, wa)
			// The debug log may name the voter; nothing else may.
			got := logged()
			rec.mu.Lock()
			defer rec.mu.Unlock()
			for _, l := range rec.logs {
				if !strings.HasPrefix(l, "DEBUG ") {
					got = append(got, l)
				}
			}
			if s := strings.Join(got, "\n"); !strings.Contains(s, c.want) || strings.Contains(s, otherNumber) {
				t.Errorf("log %q, want %q and the voter masked", s, c.want)
			}
		})
	}
}

// A poll a brain sends is kept with that brain's id.
func TestSentPollRemembersItsBrain(t *testing.T) {
	tb, _ := newMainBridge(t, "")
	if err := tb.outbox.Reply(context.Background(), "main", testDMChat, "[POLL]\nQuestion: Lunch?\n- yes\n- no\n[/POLL]", 0); err != nil {
		t.Fatal(err)
	}
	var polls []store.PollResult
	testutil.WaitFor(t, 5*time.Second, "the poll", func() bool {
		polls, _ = tb.store.RecentPolls(context.Background(), "", 10, func(string) bool { return true })
		return len(polls) == 1
	})
	p, err := tb.store.SentPoll(context.Background(), polls[0].MsgID)
	if err != nil || p.Brain != "main" || p.Chat != testDMChat || p.Question != "Lunch?" {
		t.Errorf("SentPoll = %+v, %v; want brain main's poll in %s", p, err, testDMChat)
	}
}
