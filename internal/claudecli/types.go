package claudecli

import (
	"errors"
	"fmt"
	"time"
)

// Result is the outcome of one brain run.
type Result struct {
	Text string
	// Asked is set when the run forwarded a question mid-run. Its answer
	// comes as a new message; Text, the run's own final reply, is sent too.
	Asked    bool
	Err      error
	Duration time.Duration
	// Usage is what the run cost, as the CLI's result reported it.
	Usage Usage
	// LastText is, for a run that ended in an error result, the last text
	// the model wrote before it: a summary the error would lose otherwise.
	LastText string
}

// Usage is what a run cost: Claude reports dollars and tokens, Codex
// tokens only. Input counts the cached tokens too.
type Usage struct {
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
}

// ErrPanicked is what a run that panicked inside the bridge ends with;
// the panic and its stack go to the log only.
var ErrPanicked = errors.New("the bridge failed while running this message (details in the bridge log)")

// TimedOut is the error of a run stopped at run_timeout_minutes.
func TimedOut(timeout time.Duration) error {
	after := timeout.String()
	if timeout%time.Minute == 0 {
		after = fmt.Sprintf("%d minutes", int(timeout/time.Minute))
	}
	return fmt.Errorf("%w after %s (run_timeout_minutes)", ErrTimedOut, after)
}

type Status struct {
	Path    string `json:"path"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
}

// Error is the error the CLI itself reported in its result event, as
// opposed to a failure of the process or of the bridge. Its text is the
// CLI's own short account, such as "Credit balance is too low".
type Error struct{ Message, Provider string }

func (e *Error) Error() string { return AgentName(e.Provider) + " error: " + e.Message }

var (
	ErrInterrupted = errors.New("brain run interrupted")
	ErrNoResult    = errors.New("no result from the agent (empty output)")
	ErrTimedOut    = errors.New("the run took too long and was stopped")
)
