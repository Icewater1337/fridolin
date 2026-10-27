package app

import (
	"context"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// waits are how long a bridge and its brain manager wait for what may not
// come, which tests shorten. Each has its own (Options.waits, which the
// binary leaves nil: defaultWaits), so that tests that shorten one can run
// beside the others.
type waits struct {
	// shutdownGrace is how long the run in flight gets to finish and be
	// delivered when the bridge stops (see shutdownGrace).
	shutdownGrace time.Duration
	// workingNotice is how long a run goes before the chat is told that it
	// is working on it (workingNotice).
	workingNotice time.Duration
	// postResultGrace is how long a run may go on after its result.
	postResultGrace time.Duration
	// runWaitDelay is how long Wait waits, once the CLI has exited, for the
	// copies of its input and output to end.
	runWaitDelay time.Duration
	// runTimeoutUnit is what one of run_timeout_minutes is.
	runTimeoutUnit time.Duration
	// frideRetry is how often waiting work is offered to the nodes again,
	// besides whenever a node's FrIDE comes up.
	frideRetry time.Duration
	// frideGather is how long the relay gathers FrIDE's news before it
	// sends any, so that one text a node sends for several threads at
	// once goes out once; frideRepeat is how long after that the same
	// text from that node is still taken as sent.
	frideGather, frideRepeat time.Duration
	// frideStallRepeat is how long the relay drops a thread's stall notice
	// that only repeats the last one.
	frideStallRepeat time.Duration
	// frideAgentGap is the least time between two runs of a scheduled
	// agent that FrIDE's news starts; what comes meanwhile waits for the
	// next.
	frideAgentGap time.Duration
	// nodeSessionMinAge is the least age of a node session housekeeping
	// stops (see nodeSessionFindings).
	nodeSessionMinAge time.Duration
	// reapGrace is how long reapOrphanedRuns gives a group to end after
	// SIGTERM before it kills it.
	reapGrace time.Duration
	// vaultTick is how often the vault's loop looks at what is due.
	vaultTick time.Duration
	// secretFoldersChange is how long after a version of the secret
	// folders a change to them is backed up at the earliest.
	secretFoldersChange time.Duration
	// approvalTimeout is how long an approval waits for its answer; 0 is
	// policy.approval_timeout_minutes.
	approvalTimeout time.Duration
	// coreMovePoll and coreSecretsPoll are how long the !movecore poll
	// and the keep/forget poll after a move wait for a vote.
	coreMovePoll, coreSecretsPoll time.Duration
	// coreMove are a move's timings (the defaults of nodes when zero).
	coreMove nodes.MoveOptions
}

// defaultWaits are the waits of the binary's bridge.
func defaultWaits() waits {
	return waits{
		shutdownGrace:       shutdownGrace,
		workingNotice:       10 * time.Second,
		postResultGrace:     5 * time.Second,
		runWaitDelay:        5 * time.Second,
		runTimeoutUnit:      time.Minute,
		frideRetry:          30 * time.Second,
		frideGather:         3 * time.Second,
		frideRepeat:         time.Minute,
		frideStallRepeat:    6 * time.Hour,
		frideAgentGap:       time.Hour,
		nodeSessionMinAge:   time.Minute,
		reapGrace:           3 * time.Second,
		vaultTick:           time.Minute,
		secretFoldersChange: 10 * time.Minute,
		coreMovePoll:        30 * time.Minute,
		coreSecretsPoll:     24 * time.Hour,
	}
}

// waitsOr is *w, or defaultWaits when w is nil.
func waitsOr(w *waits) waits {
	if w == nil {
		return defaultWaits()
	}
	return *w
}

// testHooks are what tests watch or replace in a bridge (Options.hooks);
// the binary leaves them nil.
type testHooks struct {
	// approvalWaitEnded runs as a call's wait for approval id ends
	// unanswered, once the call stopped waiting: tests decide the approval
	// at that moment.
	approvalWaitEnded func(id string)
	// approvalDecided runs in decide once bridge.db has the answer to
	// approval id and before decide hands it on: tests start a call at
	// that moment.
	approvalDecided func(id string)
	// vaultBackends builds the vault's backends in place of
	// defaultVaultBackends: tests hand in local ones.
	vaultBackends func(ctx context.Context, s *vaultService, cfg *config.Settings) (vaultBackends, error)
	// connections points the calls of the connections at fake OpenAI and
	// ElevenLabs servers in place of the real ones.
	connections *connections.Endpoints
	// sandboxFleet is the nodes with Docker Sandboxes in place of the
	// nodes service: tests hand in a fake node.
	sandboxFleet sandboxFleet
}
