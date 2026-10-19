package main

import (
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
)

// brainSink connects runtime events to delivery and durable shutdown. Working
// is an ephemeral notice; Question reports whether the chat accepted it.
// History runs after the history lock is released. Undelivered is queried
// under that lock, ordering the outbox-to-history transition.
type brainSink interface {
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
type brainSinkRef struct{ target brainSink }

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
		dropped(ms)
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
