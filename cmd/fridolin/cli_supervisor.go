package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// cliInvocation carries the already resolved command and its runtime limits.
// Its caller owns configuration, environment projection and sandbox wrapping.
type cliInvocation struct {
	Run                    claudeRun
	Provider               string
	ExitGrace, NoticeAfter time.Duration
	DeadlineError          error
}

// cliCallbacks keep process supervision independent of brains and delivery.
// All callbacks are supplied by the runtime. Start serialises publication
// with force-stop; Started may return cleanup that runs after process reaping.
type cliCallbacks struct {
	Start        func(context.Context, claudeRun) (*claudeProcess, error)
	Started      func(pid int) func()
	Reaped       func()
	OpenStream   func(pid int, result func(RunResult)) (StreamSink, func())
	ReadPanic    func(any, []byte)
	Notice       func()
	GraceExpired func()
	SlowResponse func(time.Duration)
	Stderr       func(pid int, text string)
}

// superviseCLI owns one process, output parser and cancellation path. Its
// stop acknowledgement remains independent of blocked output or delivery.
func superviseCLI(ctx context.Context, spec cliInvocation, callbacks cliCallbacks) RunResult {
	// The run's processes end with this context: when ctx does, at a stop
	// or run_timeout, and when the grace after the result runs out.
	// cmd.Cancel then kills the run's process group, and WaitDelay closes
	// the pipes something outside the group may still hold.
	cmdCtx, stopCmd := context.WithCancel(ctx)
	defer stopCmd()

	// The output goes through a pipe of the bridge's own, which the parser
	// reads: os/exec copies it there, and Wait waits for the copy, up to
	// WaitDelay after the CLI exited. So Wait, which runs alongside the
	// parser, ends the run even when a process the bridge cannot stop,
	// one that left the run's process group, holds the CLI's output open,
	// and the parser gets all the CLI wrote before that.
	stdout, stdoutW := io.Pipe()
	spawn := spec.Run
	spawn.stdout = stdoutW
	proc, err := callbacks.Start(cmdCtx, spawn)
	if err != nil {
		stdout.Close()
		stdoutW.Close()
		if ctx.Err() != nil {
			// Stopped before the CLI started, which Start then refuses: a
			// stop like any other, no failure.
			return RunResult{Err: errBrainInterrupted}
		}
		return RunResult{Err: fmt.Errorf("starting %s: %w", agentName(spec.Provider), err)}
	}
	pid := proc.pid()
	if cleanup := callbacks.Started(pid); cleanup != nil {
		defer cleanup()
	}

	var waitErr error
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		waitErr = proc.wait()
		// The PID is free for another process now.
		callbacks.Reaped()
		// Everything the CLI wrote has reached the parser: this ends its
		// output.
		stdoutW.Close()
	}()
	// However invoke ends, a panic in a hook included, the run's processes
	// are stopped and reaped here, before its record goes: left running,
	// the CLI would stay a zombie for as long as the bridge runs.
	defer func() {
		stopCmd()
		<-waited
	}()

	// Parse the stream in a goroutine so we can race it against the 10s timer.
	parseDone := make(chan RunResult, 1)
	resultSeen := make(chan struct{})
	var resultOnce sync.Once
	var recordedResult RunResult
	go func() {
		// Once the parser is done, the copy into its pipe ends too: a parser
		// that failed would leave it blocked otherwise.
		defer stdout.Close()
		// A panic while the output is read, in the hook that sends a
		// question to the chat say, ends the run as one in invoke does
		// (invokeGuarded). In a goroutine of its own, unrecovered, it would
		// end the whole bridge.
		defer func() {
			if p := recover(); p != nil {
				callbacks.ReadPanic(p, debug.Stack())
				parseDone <- RunResult{Err: errRunPanicked}
			}
		}()
		sink, closeStream := callbacks.OpenStream(pid, func(res RunResult) {
			resultOnce.Do(func() {
				recordedResult = res
				close(resultSeen)
			})
		})
		defer closeStream()
		parseDone <- parseAgentStream(stdout, sink, spec.Provider)
	}()

	notice := time.NewTimer(spec.NoticeAfter)
	defer notice.Stop()

	invocationStart := time.Now()
	var res RunResult
	parsed := false
	var grace <-chan time.Time
	resultIn, stopped := resultSeen, ctx.Done() // each nil once it came
	for running := true; running; {
		select {
		case <-waited:
			running = false
		case res = <-parseDone:
			// The parser reads to the end of the output, which comes after
			// Wait, unless it failed: then nothing reads what the CLI
			// writes any more, and the run is over.
			parsed, parseDone = true, nil
			stopCmd()
		case <-notice.C:
			// Untagged (silent) messages never produce a visible reply, so they
			// must not produce a visible "working" notice either.
			callbacks.Notice()
		case <-resultIn:
			// The answer is in, and the CLI exits right after it, unless
			// it, or something it started, keeps going with its output
			// open, a command left running in the background, say. That
			// gets postResultGrace, or the brain would stay busy as long
			// as it ran.
			resultIn, grace = nil, time.After(spec.ExitGrace)
			notice.Stop()
		case <-grace:
			grace = nil
			callbacks.GraceExpired()
			stopCmd()
		case <-stopped:
			// Stopped, or at run_timeout: cmdCtx ends with ctx, which
			// stops the process group. Nothing is working on it any more.
			stopped = nil
			notice.Stop()
		}
	}
	if !parsed {
		res = <-parseDone
	}

	if elapsed := time.Since(invocationStart); elapsed > 5*time.Minute && res.Text != "" {
		callbacks.SlowResponse(elapsed)
	}

	stderr := proc.stderrTail()
	callbacks.Stderr(pid, stderr)
	// Whether the result event came: readStream has returned, so it would
	// have closed resultSeen by now.
	gotResult := false
	select {
	case <-resultSeen:
		gotResult = true
	default:
	}
	// A result is the CLI's final answer, including an error result. A
	// stop while its inherited output drains must not replace it with an
	// interruption or timeout, even when Wait had to close those pipes.
	if gotResult && ctx.Err() != nil {
		return recordedResult
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return RunResult{Err: spec.DeadlineError}
	case ctx.Err() != nil:
		return RunResult{Err: errBrainInterrupted}
	case waitErr != nil:
		// Without a result, what the CLI wrote to stderr is the only
		// account of what went wrong.
		if errors.Is(res.Err, errNoResult) {
			return RunResult{Err: fmt.Errorf("%s exited: %w — stderr: %s", agentName(spec.Provider), waitErr, textx.Tail(stderr, 500))}
		}
	}
	return res
}
