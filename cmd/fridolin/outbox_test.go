package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/devchat"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// flakyMessenger is the dev messenger with a connection that drops: while
// it is down, every send fails, as whatsmeow's do, and with dropAfter set
// it goes down after that many more messages.
type flakyMessenger struct {
	*devchat.Messenger

	mu        sync.Mutex
	online    chan struct{}
	dropAfter int           // -1: never
	tries     int           // sends tried, failed ones included
	delay     time.Duration // how long each send takes
}

func newFlakyMessenger(cfg func() *Config, online bool) *flakyMessenger {
	f := &flakyMessenger{Messenger: devchat.New(func(text string) []string { return outboundmsg.Chunk(text, cfg().MaxChunkSize) }), online: make(chan struct{}), dropAfter: -1}
	if online {
		close(f.online)
	}
	return f
}

func (f *flakyMessenger) Connected() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online
}

// setOnline brings the connection up or takes it down.
func (f *flakyMessenger) setOnline(up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setOnlineLocked(up)
}

func (f *flakyMessenger) setOnlineLocked(up bool) {
	select {
	case <-f.online:
		if !up {
			f.online = make(chan struct{})
		}
	default:
		if up {
			close(f.online)
		}
	}
}

// try reports whether a send may go out now.
func (f *flakyMessenger) try() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	select {
	case <-f.online:
	default:
		return outboundmsg.ErrOffline
	}
	if f.dropAfter == 0 {
		f.dropAfter = -1
		f.setOnlineLocked(false)
		return outboundmsg.ErrOffline
	}
	if f.dropAfter > 0 {
		f.dropAfter--
	}
	return nil
}

func (f *flakyMessenger) SendMessage(ctx context.Context, jid, text string) error {
	time.Sleep(f.delay)
	// Refused like WhatsApp refuses it, which the dev messenger does not.
	if _, err := parseRecipient(jid); errors.Is(err, outboundmsg.ErrRefusedRecipient) {
		return err
	}
	if err := f.try(); err != nil {
		return err
	}
	return f.Messenger.SendMessage(ctx, jid, text)
}

func (f *flakyMessenger) SendPoll(ctx context.Context, jid, q string, opts []string, max int) (string, error) {
	if err := f.try(); err != nil {
		return "", err
	}
	return f.Messenger.SendPoll(ctx, jid, q, opts, max)
}

func (f *flakyMessenger) sent(jid string) []string {
	var out []string
	box := f.Outbox()
	for i := len(box) - 1; i >= 0; i-- {
		if box[i].JID == jid {
			out = append(out, box[i].Text)
		}
	}
	return out
}

// A reply produced while the connection is down is not lost: it goes out
// once WhatsApp connects, from the message that failed, so no part reaches
// the chat twice. The brain's history records it only then.
func TestReplyIsRecordedOnceDelivered(t *testing.T) {
	tb, br := newMainBridge(t, `, "max_chunk_size": 100`)
	wa := newFlakyMessenger(tb.cfgs.Get, true)
	wa.dropAfter = 1 // the first chunk goes out, then the connection drops
	tb.wa = wa

	tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: strings.Repeat("a long question ", 12), Source: chat.SourceWhatsApp}})
	waitFor(t, 10*time.Second, "a failed attempt", func() bool {
		wa.mu.Lock()
		defer wa.mu.Unlock()
		return wa.tries >= 2
	})
	if out := historyTexts(t, br, history.Out); len(out) != 0 {
		t.Errorf("history holds %q before the reply went out", out)
	}
	if got := wa.sent(testDMChat); len(got) != 1 || !strings.HasPrefix(got[0], "[1/") {
		t.Fatalf("sent %q, want the first chunk only", got)
	}

	wa.setOnline(true)
	waitFor(t, 10*time.Second, "the history line", func() bool { return len(historyTexts(t, br, history.Out)) == 1 })
	got := wa.sent(testDMChat)
	n := len(got)
	for i, c := range got {
		if want := fmt.Sprintf("[%d/%d] ", i+1, n); !strings.HasPrefix(c, want) {
			t.Errorf("message %d is %q, want it to start with %q: each chunk once, in order", i, c, want)
		}
	}
	if n < 3 {
		t.Errorf("sent %d chunks, want a reply long enough to split", n)
	}
	if sum, err := tb.store.OutboxSummary(context.Background(), time.Now()); err != nil || sum.Waiting != 0 || sum.Failed != 0 {
		t.Errorf("outbox = %+v, %v; want it empty", sum, err)
	}
}

// A message that waited while its chat's run before it answered runs
// knowing that answer, however its delivery goes: at once, slowly, or not
// at all yet while WhatsApp is down. The chat gets the answer before
// anything the second run says, so that run must read it as said.
func TestQueuedRunReadsTheAnswerBeforeIt(t *testing.T) {
	cases := []struct {
		name   string
		delay  time.Duration
		online bool
	}{
		{"delivered at once", 0, true},
		{"delivered slowly", 300 * time.Millisecond, true},
		{"not delivered yet", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb, br := newMainBridge(t, "")
			prompts := filepath.Join(t.TempDir(), "prompts")
			t.Setenv("FRIDOLIN_FAKE_PROMPT_LOG", prompts)
			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "800")
			wa := newFlakyMessenger(tb.cfgs.Get, c.online)
			wa.delay = c.delay
			tb.wa = wa
			ask := func(text string) {
				tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: text, Source: chat.SourceWhatsApp}})
			}
			ask("first question")
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
			ask("second question")
			waitFor(t, 20*time.Second, "the second run", func() bool { return len(fakePromptsIn(t, prompts)) == 2 })
			second := fakePromptsIn(t, prompts)[1]
			answered := slices.ContainsFunc(strings.Split(second, "\n"), func(line string) bool {
				return strings.Contains(line, `"from":"You (main)","text":"echo:`) && strings.Contains(line, "first question")
			})
			if !answered {
				t.Errorf("the second run's prompt lacks the first answer:\n%s", textx.Tail(second, 600))
			}
			if strings.Count(second, `"from":"You (main)"`) != 1 {
				t.Errorf("the second run's prompt holds the first answer more than once:\n%s", textx.Tail(second, 600))
			}
		})
	}
}

// The answers on their way go among the history's lines by time, and one
// the history holds by then, delivered after they were read, only once.
func TestWithUndelivered(t *testing.T) {
	at := func(hhmmss string) string { return "2026-10-18T" + hhmmss + "+02:00" }
	in := func(ts, text string) history.Entry {
		return history.Entry{TS: at(ts), Chat: testDMChat, From: "15551234567", Text: text, Type: history.In}
	}
	out := func(ts, text string) history.Entry {
		return history.Entry{TS: at(ts), Chat: testDMChat, From: "main", Text: text, Type: history.Out}
	}
	cases := []struct {
		name             string
		history, pending []history.Entry
		want             []string
	}{
		{"nothing waits", []history.Entry{in("10:00:00", "a")}, nil, []string{"a"}},
		{"after what came before it", []history.Entry{in("10:00:00", "a"), in("10:00:01", "b")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "b", "A"}},
		{"before what came after it", []history.Entry{in("10:00:00", "a"), in("10:00:05", "c")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "A", "c"}},
		{"in order among themselves", []history.Entry{in("10:00:00", "a")}, []history.Entry{out("10:00:02", "A"), out("10:00:03", "B")}, []string{"a", "A", "B"}},
		{"delivered meanwhile", []history.Entry{in("10:00:00", "a"), out("10:00:04", "A")}, []history.Entry{out("10:00:02", "A")}, []string{"a", "A"}},
		{"the same words said before", []history.Entry{out("09:00:00", "OK"), in("10:00:00", "a")}, []history.Entry{out("10:00:02", "OK")}, []string{"OK", "a", "OK"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, e := range withUndelivered(slices.Clone(c.history), c.pending) {
				got = append(got, e.Text)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("withUndelivered = %q, want %q", got, c.want)
			}
		})
	}
}

// A reply that went out in full while its row could not be removed is
// recorded once, when the removal works: the history must not hold a
// line that the retry writes again, nor the chat get the reply twice.
func TestReplyIsRecordedOnceWhenItsRowStays(t *testing.T) {
	tb, br := newMainBridge(t, "")
	db := testStoreSQL(t, tb.cfgs.Startup().DBPath)
	// bridge.db refuses to delete from the outbox until the trigger goes.
	if _, err := db.Exec(`CREATE TRIGGER keep_outbox BEFORE DELETE ON outbox BEGIN SELECT RAISE(FAIL, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	tb.handleInbound(context.Background(), Inbound{Inbound: chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "hello", Source: chat.SourceWhatsApp}})
	waitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.sentTo(testDMChat)) == 1 })
	waitFor(t, 5*time.Second, "the row, all of it sent", func() bool {
		var left int
		err := db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE state = 'pending' AND next_part = 1`).Scan(&left)
		return err == nil && left == 1
	})
	if out := historyTexts(t, br, history.Out); len(out) != 0 {
		t.Errorf("history holds %q while the reply is still in the outbox", out)
	}
	if _, err := db.Exec(`DROP TRIGGER keep_outbox`); err != nil {
		t.Fatal(err)
	}
	tb.outbox.Wake()
	waitFor(t, 15*time.Second, "the history line", func() bool { return len(historyTexts(t, br, history.Out)) == 1 })
	tb.outbox.Wake()
	time.Sleep(200 * time.Millisecond)
	if out := historyTexts(t, br, history.Out); len(out) != 1 {
		t.Errorf("history holds the reply %d times, want once", len(out))
	}
	if sent := tb.sentTo(testDMChat); len(sent) != 1 {
		t.Errorf("sent %q, want the reply once", sent)
	}
}

// Asking for the summary, which the dashboard does every few seconds,
// changes nothing; the outbox itself forgets replies given up on long ago.
func TestOutboxSummaryOnlyReads(t *testing.T) {
	dir := t.TempDir()
	s := openOutboxStore(t, dir)
	db := testStoreSQL(t, filepath.Join(dir, "bridge.db"))
	ctx := context.Background()
	old := time.Now().Add(-store.OutboxFailedKeep - time.Hour)
	id, err := s.AddOutbox(ctx, store.OutboxRow{Chat: testDMChat, Parts: []store.OutboxPart{{Text: "x"}}, CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.OutboxGiveUp(ctx, id, "offline", old); err != nil {
		t.Fatal(err)
	}
	rows := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	sum, err := s.OutboxSummary(ctx, time.Now())
	if err != nil || sum.Failed != 0 {
		t.Errorf("summary = %+v, %v; want a reply given up on long ago left out", sum, err)
	}
	if n := rows(); n != 1 {
		t.Fatalf("the summary removed rows: %d left, want 1", n)
	}
	o := newOutbox(s, func() outboundmsg.Link { return newFlakyMessenger(func() *Config { return &Config{} }, true) }, 0, nil)
	defer o.Stop()
	waitFor(t, 5*time.Second, "the outbox to forget it", func() bool { return rows() == 0 })
}

// A question a brain asks mid-run waits in the outbox for the connection;
// the working notice goes out at once or not at all, whatever its text.
func TestQuestionsWaitAndNoticesDoNot(t *testing.T) {
	tb, br := newMainBridge(t, "")
	tb.wa = newFlakyMessenger(tb.cfgs.Get, false)
	br.hooks.Question(br, testDMChat, outboundmsg.QuestionPrefix+"Which day suits you?")
	br.hooks.Notice(testDMChat, "⏳ Still at it")
	sum, err := tb.store.OutboxSummary(context.Background(), time.Now())
	if err != nil || sum.Waiting != 1 {
		t.Errorf("outbox = %+v, %v; want the question alone waiting", sum, err)
	}
}

// openOutboxStore opens a bridge.db in dir.
func openOutboxStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "bridge.db"), "error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A reply still waiting when the bridge stops goes out after it starts
// again, recorded in the history of the brain that wrote it.
func TestOutboxSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	cfg := func() *Config { return &Config{MaxChunkSize: 4000} }
	s := openOutboxStore(t, dir)
	down := newFlakyMessenger(cfg, false)
	first := newOutbox(s, func() outboundmsg.Link { return down }, 0, func(context.Context, store.OutboxRow) error {
		t.Error("delivered while offline")
		return nil
	})
	if err := first.Reply(context.Background(), "main", testDMChat, "hello\n[POLL]\nQuestion: Lunch?\n- yes\n- no\n[/POLL]", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "an attempt", func() bool {
		down.mu.Lock()
		defer down.mu.Unlock()
		return down.tries > 0
	})
	first.Stop()
	s.Close()

	s = openOutboxStore(t, dir)
	up := newFlakyMessenger(cfg, true)
	got := make(chan store.OutboxRow, 1)
	second := newOutbox(s, func() outboundmsg.Link { return up }, 0, func(ctx context.Context, r store.OutboxRow) error {
		got <- r
		return s.OutboxDelivered(ctx, r.ID)
	})
	defer second.Stop()
	select {
	case r := <-got:
		if r.Brain != "main" || r.Chat != testDMChat || r.History != "📊 *Lunch?*\n1. yes\n2. no\n\nhello" {
			t.Errorf("delivered %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply did not go out after the restart")
	}
	if sent := up.sent(testDMChat); !slices.Equal(sent, []string{"Lunch?", "hello"}) {
		t.Errorf("sent %q, want the poll, then the text", sent)
	}
	polls, err := s.RecentPolls(context.Background(), "", 10, func(string) bool { return true })
	if err != nil || len(polls) != 1 {
		t.Errorf("polls = %+v, %v; want the poll kept for its votes", polls, err)
	}
}

// A reply that cannot go out within outboxTTL is given up on, without its
// text, and a recipient the bridge refuses is given up on at once. Either
// way the dashboard hears of it.
func TestOutboxGivesUp(t *testing.T) {
	dir := t.TempDir()
	s := openOutboxStore(t, dir)
	db := testStoreSQL(t, filepath.Join(dir, "bridge.db"))
	m := newFlakyMessenger(func() *Config { return &Config{} }, true)
	o := newOutbox(s, func() outboundmsg.Link { return m }, 0, nil)
	defer o.Stop()
	if _, err := s.AddOutbox(context.Background(), store.OutboxRow{Chat: testDMChat, Brain: "main", History: "stale", Parts: []store.OutboxPart{{Text: "stale"}}, CreatedAt: time.Now().Add(-2 * outboxTTL)}); err != nil {
		t.Fatal(err)
	}
	if err := o.Text(context.Background(), "status@broadcast", "to everyone", 0); err != nil {
		t.Fatal(err)
	}
	var sum store.OutboxSummary
	waitFor(t, 5*time.Second, "both given up", func() bool {
		sum, _ = s.OutboxSummary(context.Background(), time.Now())
		return sum.Failed == 2
	})
	if sum.Waiting != 0 {
		t.Errorf("summary = %+v, want nothing waiting", sum)
	}
	if sent := m.Outbox(); len(sent) != 0 {
		t.Errorf("sent %+v", sent)
	}
	var texts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE parts != '[]' OR history != ''`).Scan(&texts); err != nil || texts != 0 {
		t.Errorf("%d rows still hold their text (%v)", texts, err)
	}
}

// One chat's replies go out in order: a later one waits for an earlier
// that failed. Another chat's go on.
func TestOutboxKeepsAChatsOrder(t *testing.T) {
	s := openOutboxStore(t, t.TempDir())
	m := newFlakyMessenger(func() *Config { return &Config{} }, false)
	o := newOutbox(s, func() outboundmsg.Link { return m }, 0, nil)
	defer o.Stop()
	for _, text := range []string{"first", "second"} {
		if err := o.Text(context.Background(), testDMChat, text, 0); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 5*time.Second, "a failed attempt", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.tries > 0
	})
	m.mu.Lock()
	tries := m.tries
	m.mu.Unlock()
	if tries != 1 {
		t.Errorf("%d sends tried, want only the first reply's while it fails", tries)
	}
	m.setOnline(true)
	waitFor(t, 5*time.Second, "both", func() bool { return len(m.sent(testDMChat)) == 2 })
	if got := m.sent(testDMChat); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("sent %q, want them in order", got)
	}
}

// The Overview says when replies wait for long, and when some were given
// up on.
func TestStatusWarnsAboutTheOutbox(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.wa = newFlakyMessenger(tb.cfgs.Get, false)
	ctx := context.Background()
	if _, err := tb.store.AddOutbox(ctx, store.OutboxRow{Chat: testDMChat, Parts: []store.OutboxPart{{Text: "late"}}, CreatedAt: time.Now().Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.store.AddOutbox(ctx, store.OutboxRow{Chat: testGroup, Parts: []store.OutboxPart{{Text: "too late"}}, CreatedAt: time.Now().Add(-2 * outboxTTL)}); err != nil {
		t.Fatal(err)
	}
	tb.outbox.Wake()
	waitFor(t, 5*time.Second, "the warnings", func() bool {
		w := tb.outboxWarnings(ctx)
		return len(w) == 2 && strings.Contains(w[0], "1 reply is waiting") && strings.Contains(w[1], "1 reply could not be delivered")
	})
}

// The summary sees the outbox at one moment: a reply given up on while it
// reads counts as waiting or as given up on, never as both or neither.
// TestOutboxGivesUp saw one counted twice on CI.
func TestOutboxSummaryIsOneMoment(t *testing.T) {
	dir := t.TempDir()
	s := openOutboxStore(t, dir)
	db := testStoreSQL(t, filepath.Join(dir, "bridge.db"))
	ctx := context.Background()
	if _, err := s.AddOutbox(ctx, store.OutboxRow{Chat: testDMChat, Parts: []store.OutboxPart{{Text: "hi"}}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// The reply goes back and forth between waiting and given up on.
	done := make(chan struct{})
	flipped := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				flipped <- nil
				return
			default:
			}
			if _, err := db.ExecContext(ctx, `UPDATE outbox SET state = CASE state WHEN 'pending' THEN 'failed' ELSE 'pending' END, next_try_at = ?`, time.Now().UTC()); err != nil {
				flipped <- err
				return
			}
			// Not at once again: the summary must get its turn.
			time.Sleep(100 * time.Microsecond)
		}
	}()
	defer func() {
		close(done)
		if err := <-flipped; err != nil {
			t.Errorf("flipping the reply: %v", err)
		}
	}()
	for i := range 100 {
		sum, err := s.OutboxSummary(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if sum.Waiting+sum.Failed != 1 {
			t.Fatalf("summary %d = %+v, want the one reply once", i, sum)
		}
	}
}
