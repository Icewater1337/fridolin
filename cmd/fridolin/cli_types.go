package main

import (
	"errors"
	"fmt"
	"time"
)

// RunResult is the outcome of one brain run.
type RunResult struct {
	Text string
	// Asked is set when the run forwarded a question mid-run. Its answer
	// comes as a new message; Text, the run's own final reply, is sent too.
	Asked    bool
	Err      error
	Duration time.Duration
}

// errRunPanicked is what a run that panicked inside the bridge ends with;
// the panic and its stack go to the log only.
var errRunPanicked = errors.New("the bridge failed while running this message (details in the bridge log)")

// runTimedOut is the error of a run stopped at run_timeout_minutes.
func runTimedOut(timeout time.Duration) error {
	after := timeout.String()
	if timeout%time.Minute == 0 {
		after = fmt.Sprintf("%d minutes", int(timeout/time.Minute))
	}
	return fmt.Errorf("%w after %s (run_timeout_minutes)", errRunTimedOut, after)
}

type claudeStatus struct {
	Path    string `json:"path"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
}

// cliError is the error the CLI itself reported in its result event, as
// opposed to a failure of the process or of the bridge. Its text is the
// CLI's own short account, such as "Credit balance is too low".
type cliError struct{ msg, provider string }

func (e *cliError) Error() string { return agentName(e.provider) + " error: " + e.msg }

var (
	errBrainInterrupted = errors.New("brain run interrupted")
	errNoResult         = errors.New("no result from the agent (empty output)")
	errRunTimedOut      = errors.New("the run took too long and was stopped")
)
