package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// A node that runs on the machine itself reaches the core over the
// machine's Tailscale. That Tailscale can come back switched off after the
// machine restarts, as the standalone macOS app once did. The node then
// tried for hours to reach a core it could not even look up,
// and nothing on the core could reach the machine to help. So when the
// core has been out of reach for a while and the machine's Tailscale is
// off but still signed in, the node switches it on, as Tailscale's own
// Connect does, and says so in its next hello; the core tells the owner.
// A node in a box dials over a tailnet device of its own and leaves this
// alone (Agent.Tailscale is nil there).

// Tailscale is the machine's Tailscale as the node drives it.
type Tailscale interface {
	// State is its backend state: Running, Stopped, NeedsLogin, …
	State(ctx context.Context) (string, error)
	// Up switches it on with the settings it has.
	Up(ctx context.Context) error
}

const (
	// DefaultTailscaleGrace is how long the core is out of reach before
	// the node looks at Tailscale.
	DefaultTailscaleGrace = 2 * time.Minute
	// tailscaleRetryMax bounds the wait between two tries to switch it on.
	tailscaleRetryMax = time.Hour
	// tailscaleStopped is the state of a Tailscale that is signed in and
	// switched off; any other is not the node's to change.
	tailscaleStopped = "Stopped"
)

// TailscaleCLI is the machine's Tailscale driven through its CLI at path
// (`node run` finds it), run with env (nil: the agent's own). The macOS
// app's binary is its CLI too, but started without a terminal, as a
// launchd agent is, it tries to open the app instead and answers "The
// Tailscale GUI failed to start"; TAILSCALE_BE_CLI=1 makes it the CLI
// (tailscaleCLIEnv), and other systems' tailscale ignores it.
func TailscaleCLI(path string, env []string) Tailscale {
	return tailscaleCLI{path: path, env: env}
}

// TailscaleCLIEnv is env with what the CLI needs to answer as one.
func TailscaleCLIEnv(env []string) []string {
	return append(slices.Clip(env), "TAILSCALE_BE_CLI=1")
}

type tailscaleCLI struct {
	path string
	env  []string
}

func (t tailscaleCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, t.path, args...)
	cmd.Env = t.env
	return cmd
}

func (t tailscaleCLI) State(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := t.command(ctx, "status", "--json").Output()
	// status exits 1 when Tailscale is stopped, and still says so.
	var st struct{ BackendState string }
	if jerr := json.Unmarshal(out, &st); jerr != nil || st.BackendState == "" {
		if err == nil {
			err = errors.New("no state in its answer")
		}
		return "", fmt.Errorf("%s status: %w", t.path, err)
	}
	return st.BackendState, nil
}

func (t tailscaleCLI) Up(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Without flags, up only switches Tailscale on, with every setting it
	// has.
	if out, err := t.command(ctx, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("%s up: %w: %s", t.path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tailscaleHeal is the agent's account of an outage, kept by Run's
// goroutine only.
type tailscaleHeal struct {
	// down is when the core went out of reach, zero while it is reached.
	down time.Time
	// next is the earliest time of the next try, tries how many there
	// were in this outage.
	next  time.Time
	tries int
	// report is the switch-on the next hello tells the core about.
	report *nodeproto.TailscaleOn
}

// tailscaleGrace is how long the core is out of reach before the agent
// looks at Tailscale.
func (a *Agent) tailscaleGrace() time.Duration {
	if a.TailscaleGrace > 0 {
		return a.TailscaleGrace
	}
	return DefaultTailscaleGrace
}

// tailscaleReached notes that the core answered: the outage, if any, is
// over. connected says the core also welcomed the node, and with it heard
// the report of its hello.
func (a *Agent) tailscaleReached(connected bool) {
	h := &a.ts
	h.down, h.next, h.tries = time.Time{}, time.Time{}, 0
	if connected {
		h.report = nil
	}
}

// tailscaleHeal follows an attempt to reach the core that failed with
// err, and switches the machine's Tailscale on when the core has been out
// of reach for tailscaleGrace and Tailscale is off.
func (a *Agent) tailscaleHeal(ctx context.Context, err error) {
	if a.Tailscale == nil {
		return
	}
	if _, ok := errors.AsType[*CloseError](err); ok {
		// The core answered: the way to it works.
		a.tailscaleReached(false)
		return
	}
	h := &a.ts
	now := time.Now()
	if h.down.IsZero() {
		h.down = now
	}
	if now.Sub(h.down) < a.tailscaleGrace() || now.Before(h.next) {
		return
	}
	h.tries++
	h.next = now.Add(min(a.tailscaleGrace()<<h.tries, tailscaleRetryMax))
	st, serr := a.Tailscale.State(ctx)
	switch {
	case serr != nil:
		a.logf("node: the core is out of reach since %s, and Tailscale here does not say how it is: %v", h.down.Format(time.DateTime), serr)
		return
	case st != tailscaleStopped:
		// Running: the trouble is elsewhere. NeedsLogin and the like
		// need the owner.
		if st != "Running" {
			a.logf("node: the core is out of reach since %s, and Tailscale here is %s, which needs you: open Tailscale on this machine", h.down.Format(time.DateTime), st)
		}
		return
	}
	if uerr := a.Tailscale.Up(ctx); uerr != nil {
		a.logf("node: the core is out of reach since %s and Tailscale here is off; switching it on failed: %v; trying again after %s", h.down.Format(time.DateTime), uerr, h.next.Format(time.DateTime))
		return
	}
	a.logf("node: the core was out of reach since %s and Tailscale here was off; switched it on (tailscale up)", h.down.Format(time.DateTime))
	h.report = &nodeproto.TailscaleOn{Down: h.down, On: now}
}
