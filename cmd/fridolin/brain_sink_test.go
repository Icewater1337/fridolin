package main

import (
	"errors"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

func TestBrainSinkPreservesHistoryLockOrdering(t *testing.T) {
	br := bareBrain(t)
	written := 0
	queried := false
	br.sink = sinkForHooks(&BrainHooks{
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
		Undelivered: func(id string) []history.Entry {
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

func TestBrainSinkKeepsWaitingFallbackAndQuestionAcknowledgement(t *testing.T) {
	for _, sink := range []*brainSinkRef{nil, {}, sinkForHooks(&BrainHooks{})} {
		droppedCalls := 0
		sink.Waiting(nil, []Inbound{{Inbound: chat.Inbound{Done: func(err error) {
			if !errors.Is(err, errBrainDropped) {
				t.Errorf("dropped error = %v", err)
			}
			droppedCalls++
		}}}})
		if droppedCalls != 1 || sink.Question(nil, "chat", "question") {
			t.Fatal("missing sink lost dropped callback or acknowledged a question")
		}
	}
	accepted := false
	sink := sinkForHooks(&BrainHooks{Question: func(_ *Brain, jid, text string) bool {
		if jid != "chat" || text != outboundmsg.QuestionPrefix+outboundmsg.WorkingNotice {
			t.Fatalf("question lost its distinct prefix: %q %q", jid, text)
		}
		return accepted
	}})
	if sink.Question(nil, "chat", outboundmsg.WorkingNotice) {
		t.Fatal("refused question was acknowledged")
	}
	accepted = true
	if !sink.Question(nil, "chat", outboundmsg.WorkingNotice) {
		t.Fatal("accepted question was lost")
	}
}
