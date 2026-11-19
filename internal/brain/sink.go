package brain

import (
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
)

// brainSink connects runtime events to delivery and durable shutdown. Working
// is an ephemeral notice; Question reports whether the chat accepted it.
// History runs after the history lock is released. Undelivered is queried
// under that lock, ordering the outbox-to-history transition.
type Sink interface {
	// Running hears that a run for m starts, and returns what the worker
	// calls once the run ended, before Finished: the chat shows typing
	// meanwhile.
	Running(br *Brain, m Inbound) (ended func())
	Working(jid string)
	Question(br *Brain, jid, text string) bool
	Finished(br *Brain, m Inbound, res claudecli.Result)
	Waiting(br *Brain, ms []Inbound)
	Interrupted(br *Brain, m Inbound)
	Undelivered(brainID string) []history.Entry
	History(brainID string, e history.Entry)
}

// brainSinkRef is shared by the manager's live and retired brains. Wiring is
// done before messages arrive; no lock is held across delivery callbacks.
type brainSinkRef struct{ target Sink }

func (s *brainSinkRef) Running(br *Brain, m Inbound) func() {
	if s != nil && s.target != nil {
		if ended := s.target.Running(br, m); ended != nil {
			return ended
		}
	}
	return func() {}
}
func (s *brainSinkRef) Working(jid string) {
	if s != nil && s.target != nil {
		s.target.Working(jid)
	}
}
func (s *brainSinkRef) Question(br *Brain, jid, text string) bool {
	return s != nil && s.target != nil && s.target.Question(br, jid, text)
}
func (s *brainSinkRef) Finished(br *Brain, m Inbound, res claudecli.Result) {
	if s != nil && s.target != nil {
		s.target.Finished(br, m, res)
	}
}
func (s *brainSinkRef) Waiting(br *Brain, ms []Inbound) {
	if s != nil && s.target != nil {
		s.target.Waiting(br, ms)
	} else {
		Dropped(ms)
	}
}
func (s *brainSinkRef) Interrupted(br *Brain, m Inbound) {
	if s != nil && s.target != nil {
		s.target.Interrupted(br, m)
	}
}
func (s *brainSinkRef) Undelivered(id string) []history.Entry {
	if s != nil && s.target != nil {
		return s.target.Undelivered(id)
	}
	return nil
}
func (s *brainSinkRef) History(id string, e history.Entry) {
	if s != nil && s.target != nil {
		s.target.History(id, e)
	}
}
