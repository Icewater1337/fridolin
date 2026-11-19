package brain

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

func TestBrainSinkPreservesHistoryLockOrdering(t *testing.T) {
	br := bareBrain(t)
	written := 0
	queried := false
	br.sink = sinkForTest(&testSink{
		OnHistory: func(id string, e history.Entry) {
			if !br.historyMu.TryLock() {
				t.Fatal("history callback runs under historyMu")
			}
			br.historyMu.Unlock()
			written++
			if id != br.ID() || e.Text != "recorded" {
				t.Fatalf("history callback = %q, %+v", id, e)
			}
		},
		Pending: func(id string) []history.Entry {
			if br.historyMu.TryLock() {
				br.historyMu.Unlock()
				t.Fatal("outbox read is not ordered under historyMu")
			}
			queried = true
			return []history.Entry{{Chat: "chat", Text: "waiting"}, {Chat: "other", Text: "elsewhere"}}
		},
	})
	br.AppendEntry(history.Entry{Chat: "chat", Text: "recorded", Type: history.In})
	got := br.undelivered([]string{"chat"})
	if written != 1 || !queried || len(got) != 1 || got[0].Text != "waiting" {
		t.Fatalf("callbacks/filtering: written=%d queried=%v entries=%+v", written, queried, got)
	}
}

func TestBrainSinkKeepsWaitingFallback(t *testing.T) {
	for _, sink := range []*brainSinkRef{nil, {}, sinkForTest(&testSink{})} {
		droppedCalls := 0
		sink.Waiting(nil, []Inbound{{Inbound: chat.Inbound{Done: func(err error) {
			if !errors.Is(err, ErrDropped) {
				t.Errorf("dropped error = %v", err)
			}
			droppedCalls++
		}}}})
		if droppedCalls != 1 || sink.Question(nil, "chat", "question") {
			t.Fatal("missing sink lost dropped callback or acknowledged a question")
		}
	}
}

// The sink hears that a run starts before the run does, and the run's end
// before Finished, for a chat's request and for chatter alike: it decides
// itself what shows typing.
func TestSinkHearsARunStartAndEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var events []string
		note := func(e string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		}
		mgr := NewManager(Options{
			Runner: brainRunnerFunc(func(_ context.Context, spec RunSpec, _ claudecli.Callbacks) claudecli.Result {
				spec.BeforeStart()
				note("run " + spec.Message.Text)
				return claudecli.Result{Text: "answer"}
			}),
			Sink: &testSink{
				Run: func(_ *Brain, m Inbound) func() {
					note("running " + m.Text)
					return func() { note("ended " + m.Text) }
				},
				Deliver: func(_ *Brain, m Inbound, _ claudecli.Result) { note("finished " + m.Text) },
			},
			Input:         inputRules(),
			ClaimMedia:    func(string, string, []string) map[string]string { return nil },
			ShutdownGrace: func() time.Duration { return 0 },
		})
		defer mgr.Stop()
		spec := manifest.Spec{Dir: t.TempDir(), Manifest: manifest.Manifest{ID: "a", Observe: manifest.ObserveRun}}
		if failures, err := mgr.Apply([]manifest.Spec{spec}, Globals{Defaults: Settings{QueueMode: "queue", MaxPending: 4}}, ApplyOptions{}); err != nil || len(failures) != 0 {
			t.Fatalf("apply: %v %v", failures, err)
		}
		br := mgr.Get("a")
		for _, m := range []chat.Inbound{{JID: "chat", Sender: "s", Text: "hello"}, {JID: "chat", Sender: "s", Text: "chatter", Silent: true}} {
			if _, _, err := br.Submit(Inbound{Inbound: m}); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		mu.Lock()
		defer mu.Unlock()
		want := []string{"running hello", "run hello", "ended hello", "finished hello", "running chatter", "run chatter", "ended chatter", "finished chatter"}
		if !slices.Equal(events, want) {
			t.Errorf("events = %q, want %q", events, want)
		}
	})
}

// A sink that returns no end gets one that does nothing, and a missing
// sink one too.
func TestSinkRunningWithoutAnEnd(t *testing.T) {
	for _, sink := range []*brainSinkRef{nil, {}, sinkForTest(&testSink{})} {
		sink.Running(nil, Inbound{})()
	}
}

// MessageIDs are the WhatsApp ids of every part of a batch, oldest first,
// and none for a message from elsewhere.
func TestMessageIDs(t *testing.T) {
	one := Inbound{Inbound: chat.Inbound{ID: "A1", Text: "photo"}}
	batch := one.Merge(Inbound{Inbound: chat.Inbound{ID: "A2", Text: "what is this?"}}).Merge(Inbound{Inbound: chat.Inbound{Text: "handed in"}})
	for _, c := range []struct {
		m    Inbound
		want []string
	}{
		{one, []string{"A1"}},
		{batch, []string{"A1", "A2"}},
		{Inbound{Inbound: chat.Inbound{Text: "from the dev page"}}, nil},
	} {
		if got := c.m.MessageIDs(); !slices.Equal(got, c.want) {
			t.Errorf("MessageIDs of %q = %v, want %v", c.m.Text, got, c.want)
		}
	}
}
