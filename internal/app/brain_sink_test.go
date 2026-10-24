package app

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func TestBrainSinkKeepsWaitingFallbackAndQuestionAcknowledgement(t *testing.T) {
	t.Parallel()
	accepted := false
	sink := brainHooksSink{&BrainHooks{Question: func(_ *brain.Brain, jid, text string) bool {
		if jid != "chat" || text != outboundmsg.QuestionPrefix+outboundmsg.WorkingNotice {
			t.Fatalf("question lost its distinct prefix: %q %q", jid, text)
		}
		return accepted
	}}}
	if sink.Question(nil, "chat", outboundmsg.WorkingNotice) {
		t.Fatal("refused question was acknowledged")
	}
	accepted = true
	if !sink.Question(nil, "chat", outboundmsg.WorkingNotice) {
		t.Fatal("accepted question was lost")
	}
}
