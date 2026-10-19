package brain

import (
	"context"

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
}
