package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// A scheduled agent's gate (docs/BRAINS.md, "Gates"): a cheap program
// that runs before the agent's brain run and decides whether the brain is
// woken at all (sched.ParseGate). Without a node it runs as the brain, in
// its sandbox, with the PATH of its runs and no token of a run; with one,
// on that node through its exec capability, as a command agent's program
// does. Its output goes into the run's log.

// gateOutcome is what a gate's program did.
type gateOutcome struct {
	stdout, stderr string
	exit           int
	// timedOut and over say it was stopped: it took longer than its
	// timeout, or wrote more than sched.MaxGateOutput. cut says its node
	// cut its output at the node's max_output_bytes.
	timedOut, over, cut bool
	// err says it could not run at all.
	err error
}

// gated runs ar's gate, then, if it wakes the brain, ar's brain run with
// the gate's data. A gate that says not to ends the run as gated; one
// that fails fails it, as a brain run's failure does. A lockdown refuses
// it, as it refuses a node's exec.
func (a *agents) gated(br *brainruntime.Brain, ar *agentRun) {
	g := *ar.agent.Gate
	where := "in the brain's sandbox"
	if g.Node != "" {
		where = "on node " + g.Node
	}
	ar.log.Line(fmt.Sprintf("Gate: $ %s (%s, at most %s)", strings.Join(g.Argv, " "), where, g.Timeout()))
	if a.b.locked() {
		why := "the bridge is in lockdown, which refuses a gate's program"
		a.finish(ar, sched.StatusRefused, "", why, claudecli.Usage{}, a.failureNotice(ar, why))
		return
	}
	var out gateOutcome
	if g.Node != "" {
		out = a.nodeGate(ar, g)
	} else {
		out = a.localGate(br, ar.agent.Name, g)
	}
	if out.err == nil {
		exit := out.exit
		a.mu.Lock() // list copies the run under it
		ar.run.ExitCode = &exit
		a.mu.Unlock()
	}
	for _, s := range []struct{ name, text string }{{"Gate's stdout", out.stdout}, {"Gate's stderr", out.stderr}} {
		if strings.TrimSpace(s.text) != "" {
			ar.log.Line(s.name + ":\n" + textx.Tail(s.text, 64<<10))
		}
	}
	if a.isStopping() {
		// The stop killed it, or comes before the brain could take the
		// run: the next start finds it interrupted.
		if a.take(ar.run.ID) != nil {
			a.cut(ar)
		}
		return
	}
	decision, why := gateVerdict(g, out)
	switch {
	case why != "":
		ar.log.Line("The gate failed: " + why)
		a.finish(ar, sched.StatusFailed, textx.Tail(strings.TrimSpace(out.stdout+"\n"+out.stderr), 2000), why, claudecli.Usage{}, a.failureNotice(ar, why))
	case !decision.Wake:
		ar.log.Line("The gate did not wake the brain")
		a.finish(ar, sched.StatusGated, "", "", claudecli.Usage{}, "")
	default:
		ar.log.Line("The gate woke the brain")
		ar.gateData = string(decision.Data)
		if err := a.startBrainRun(br, ar); err != nil {
			a.finish(ar, sched.StatusFailed, "", err.Error(), claudecli.Usage{}, a.failureNotice(ar, err.Error()))
		}
	}
}

// gateVerdict is the gate's decision, or why its run failed: one that
// could not run, was stopped, exited non-zero or gave no decision fails;
// it never skips the run.
func gateVerdict(g manifest.ScheduleGate, out gateOutcome) (sched.Gate, string) {
	switch {
	case out.err != nil:
		return sched.Gate{}, "its gate did not run: " + out.err.Error()
	case out.timedOut:
		return sched.Gate{}, fmt.Sprintf("its gate took longer than %s and was stopped", g.Timeout())
	case out.over:
		return sched.Gate{}, fmt.Sprintf("its gate wrote more than %d KiB and was stopped", sched.MaxGateOutput>>10)
	case out.cut:
		return sched.Gate{}, "its node cut the gate's output at its max_output_bytes"
	case out.exit != 0:
		return sched.Gate{}, fmt.Sprintf("its gate exited with %d", out.exit)
	}
	d, err := sched.ParseGate(out.stdout)
	if err != nil {
		return d, "its gate gave no decision: " + err.Error()
	}
	return d, ""
}

// localGate runs g as brain br, in its sandbox when the brain's runs have
// one: in its folder (or g.Dir in it), with the PATH of its runs, its
// TMPDIR and caches, FRIDOLIN_BRAIN_ID, FRIDOLIN_SCHEDULE (the agent),
// FRIDOLIN_TOOLS_DIR and the owner's TZ, and no token of a run. It stops
// the whole process group once g's timeout is over, the output passes
// sched.MaxGateOutput, or the bridge stops.
func (a *agents) localGate(br *brainruntime.Brain, agent string, g manifest.ScheduleGate) gateOutcome {
	cfg := RuntimeConfig(a.b.cfgs)
	id := br.ID()
	dir := br.Dir()
	if g.Dir != "" {
		dir = filepath.Join(dir, filepath.FromSlash(g.Dir))
	}
	path := strings.Join(append(br.OwnPATH(), filepath.SplitList(getenv(a.b.env, "PATH"))...), string(filepath.ListSeparator))
	prog := g.Argv[0]
	switch {
	case filepath.IsAbs(prog):
	case strings.ContainsRune(prog, '/'):
		prog = filepath.Join(dir, filepath.FromSlash(prog))
	default:
		if prog = lookPathIn(g.Argv[0], path); prog == "" {
			return gateOutcome{err: fmt.Errorf("%s is not on the brain's PATH", g.Argv[0])}
		}
	}
	extra := []string{"PATH=" + path, "FRIDOLIN_BRAIN_ID=" + id, "FRIDOLIN_SCHEDULE=" + agent}
	if tools := ownLayout().Tools; tools != "" {
		extra = append(extra, "FRIDOLIN_TOOLS_DIR="+tools)
	}
	extra = append(extra, zoneEnv(a.b.zone().String())...)
	bin, args := prog, g.Argv[1:]
	if sp := a.b.brainMgr.profiler; sp != nil && sp.Enabled() {
		state, err := prepareBrainState(AbsPath(cfg.DataDir), AbsPath(cfg.MediaDir), id, cfg.InheritClaudeUserConfig)
		if err == nil {
			bin, args, err = sp.Command(id, prog, args)
		}
		if err != nil {
			// Not err itself, here or in the run's record: it names the
			// sandbox's files and the user's. The brain's own runs log
			// why their sandbox cannot be set up.
			return gateOutcome{err: errors.New("the brain's sandbox could not be set up")}
		}
		extra = append(extra, state...)
	}
	ctx, cancel := context.WithTimeout(a.ctx, g.Timeout())
	defer cancel()
	out := &sched.GateOutput{Over: cancel}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, childEnv(a.b.env, anyChild, nil, extra...)
	cmd.Stdout, cmd.Stderr = out.Stdout(), out.Stderr()
	platform.IsolateProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := gateOutcome{over: out.Exceeded(), timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	res.stdout, res.stderr = out.Text()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		res.exit = ee.ExitCode()
	} else if err != nil && !res.over && !res.timedOut {
		res.err = err
	}
	if (res.over || res.timedOut) && res.exit == 0 {
		res.exit = -1
	}
	return res
}

// nodeGate runs g on its node, through the node's exec: its allowlist and
// the core's approval of its commands apply, as for a command agent.
func (a *agents) nodeGate(ar *agentRun, g manifest.ScheduleGate) gateOutcome {
	node, err := a.nodeID(g.Node)
	if err != nil {
		return gateOutcome{err: err}
	}
	a.mu.Lock() // list copies the run under it
	ar.run.Node = node
	a.mu.Unlock()
	params := nodeproto.ExecParams{Argv: g.Argv, Dir: g.Dir, TimeoutSeconds: int(g.Timeout() / time.Second)}
	// The node stops the program; this bounds the call, should the node
	// not answer.
	ctx, cancel := context.WithTimeout(a.ctx, g.Timeout()+time.Minute)
	defer cancel()
	res, err := a.b.nodes.Exec(ctx, node, params)
	if err != nil {
		return gateOutcome{err: err}
	}
	return gateOutcome{stdout: res.Stdout, stderr: res.Stderr, exit: res.ExitCode, timedOut: res.TimedOut,
		over: len(res.Stdout)+len(res.Stderr) > sched.MaxGateOutput, cut: res.Truncated}
}
