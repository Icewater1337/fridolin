package brain

import (
	"context"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// brainRunner owns host-specific process preparation and supervision. The
// brain owns queue state, start publication, stream callbacks and delivery.
type Runner interface {
	Run(context.Context, RunSpec, claudecli.Callbacks) claudecli.Result
}

// brainRunSpec is one run's immutable input. BeforeStart preserves the point
// where the viewer may start: after environment and sandbox setup succeed,
// before the process is spawned. It must not be called for a refused run.
type RunSpec struct {
	ID, Dir, Prompt string
	Settings        *Settings
	BeforeStart     func()
	// LivePrompt is the prompt of a turn of a live session, which the
	// session's first turn and its later ones are told differently.
	LivePrompt func(prompttext.LiveTurn) string
	// Message is what the run answers: the brain API's token for the run
	// knows who asked, and where (see the bridge's standing intents), and
	// what the run may do through the bridge's tools depends on its chat
	// and source (a delegated task's run gets no delegate tool).
	Message chat.Inbound
}

// RunRefused is a run the bridge's policy did not start (a lockdown, a
// rate limit): Notice is what the chat is told, Reason what the log says.
type RunRefused struct {
	Reason, Notice string
}

func (e *RunRefused) Error() string { return "refused by the policy: " + e.Reason }
