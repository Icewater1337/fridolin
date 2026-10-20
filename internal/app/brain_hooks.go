package app

import (
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// BrainHooks connect brains to the rest of the bridge.
type BrainHooks struct {
	// Notice tells the chat jid, ready to send, that a run is working on
	// its message: of use only now.
	Notice func(jid, text string)
	// Question sends the chat jid a question br asks mid-run, marked as
	// one, and reports whether it is on its way, ahead of the run's final
	// reply. br's history records it once it arrived, like an answer: the
	// reply to it comes as a new message, whose run must see what it
	// answers. A silent run asks nothing (see runSink.Question).
	Question func(br *brain.Brain, jid, text string) bool
	// Deliver receives every finished run. It records the answer in the
	// brain's history once it is sent.
	Deliver func(br *brain.Brain, m brain.Inbound, res claudecli.Result)
	// Waiting gets, as the bridge stops, what br had waiting, which never
	// ran, before its run in flight gets its grace (see shutdown). Nil
	// drops it.
	Waiting func(br *brain.Brain, ms []brain.Inbound)
	// Interrupted gets, as the bridge stops, the message whose run br
	// stopped once its grace was over.
	Interrupted func(br *brain.Brain, m brain.Inbound)
	// Undelivered returns the brain's answers still on their way to
	// WhatsApp, as its history will record them once they arrived. A
	// chat gets them before anything a later run says there, so that run
	// reads them as said (see undelivered).
	Undelivered func(brainID string) []history.Entry
	// OnHistory sees every line written to a brain's history.
	OnHistory func(brainID string, e history.Entry)
	// ExtraArgs adds CLI arguments to a brain's runs (the site builder's
	// plugin and MCP server, for brains allowed to build the site).
	ExtraArgs func(brainID string) []string
	// CodexSetup supplies the site builder's instructions and MCP settings
	// using Codex's format; credentials travel in the environment, not argv.
	CodexSetup func(brainID string) (args, env []string, prompt string, err error)
	// RunEnv adds variables to one run's environment (the brain API's
	// address and a token for the run) and the MCP server with the
	// bridge's tools, and says what to call once the run is over (Done).
	RunEnv func(run runInfo) runAccess
}

// brainHooksSink keeps command callbacks compatible while runtime users move
// to the owned Sink interface. Chat formatting stays in command composition.
type brainHooksSink struct{ hooks *BrainHooks }

func (s brainHooksSink) Working(jid string) {
	if s.hooks != nil && s.hooks.Notice != nil {
		s.hooks.Notice(jid, outboundmsg.WorkingNotice)
	}
}

func (s brainHooksSink) Question(br *brain.Brain, jid, text string) bool {
	return s.hooks != nil && s.hooks.Question != nil && s.hooks.Question(br, jid, outboundmsg.QuestionPrefix+text)
}

func (s brainHooksSink) Finished(br *brain.Brain, m brain.Inbound, res claudecli.Result) {
	if s.hooks != nil && s.hooks.Deliver != nil {
		s.hooks.Deliver(br, m, res)
	}
}

func (s brainHooksSink) Waiting(br *brain.Brain, ms []brain.Inbound) {
	if s.hooks != nil && s.hooks.Waiting != nil {
		s.hooks.Waiting(br, ms)
	} else {
		brain.Dropped(ms)
	}
}

func (s brainHooksSink) Interrupted(br *brain.Brain, m brain.Inbound) {
	if s.hooks != nil && s.hooks.Interrupted != nil {
		s.hooks.Interrupted(br, m)
	}
}

func (s brainHooksSink) Undelivered(id string) []history.Entry {
	if s.hooks != nil && s.hooks.Undelivered != nil {
		return s.hooks.Undelivered(id)
	}
	return nil
}

func (s brainHooksSink) History(id string, e history.Entry) {
	if s.hooks != nil && s.hooks.OnHistory != nil {
		s.hooks.OnHistory(id, e)
	}
}
