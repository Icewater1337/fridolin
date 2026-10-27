// Package sandboxrun is the core's side of a brain run in a Docker
// Sandbox on a node (docs/SANDBOXES.md): the run as a claudecli.Process
// over the node's sandbox.run, sandbox.read and sandbox.stop, the cache
// of whether a node's sandboxes are ready, and the owner's notices when
// they stop being ready and are back.
package sandboxrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Caller calls method on the node with params and decodes its answer
// into out: the nodes service's Call, bound to one node.
type Caller func(ctx context.Context, method string, params, out any) error

// Options tune a run.
type Options struct {
	// Label names the run in the brain's log ("sandbox run r1 on mini").
	Label string
	// Grace is how long the node waits after TERM before it kills the
	// run's processes (default nodeproto.DefaultSandboxGraceS).
	Grace time.Duration
	// ReadWait is each read's long poll (default 15 s).
	ReadWait time.Duration
	// Retry is the wait before a read that failed is tried again, and
	// GiveUp how long reads may keep failing before the run counts as
	// lost (defaults 1 s and 30 s).
	Retry, GiveUp time.Duration
}

// Process is a run in a Docker Sandbox: a claudecli.Process whose stdout
// comes from the node's reads.
type Process struct {
	call              Caller
	run               string
	o                 Options
	out               io.Writer
	done              chan struct{} // closes when the reads are over
	stop              chan struct{} // closes when a stop was asked for
	acked             chan struct{} // closes when the node heard the stop, or Wait ended
	stopOnce, ackOnce sync.Once
	stderr            string
	err               error
}

// Start starts the run p on the node and copies its output to out. The
// run is stopped on the node when ctx ends.
func Start(ctx context.Context, call Caller, p nodeproto.SandboxRunParams, out io.Writer, o Options) (*Process, error) {
	if len(p.Stdin) > nodeproto.MaxSandboxStdin {
		return nil, fmt.Errorf("the prompt is %d bytes, more than a Docker Sandbox run takes (%d)", len(p.Stdin), nodeproto.MaxSandboxStdin)
	}
	o = withDefaults(o)
	var res nodeproto.SandboxRunResult
	if err := call(ctx, nodeproto.MethodSandboxRun, p, &res); err != nil {
		return nil, err
	}
	pr := &Process{call: call, run: p.Run, o: o, out: out, done: make(chan struct{}), stop: make(chan struct{}), acked: make(chan struct{})}
	if res.Run != "" {
		pr.run = res.Run
	}
	go pr.read()
	go func() {
		select {
		case <-ctx.Done():
			pr.Stop()
		case <-pr.done:
		}
	}()
	return pr, nil
}

func withDefaults(o Options) Options {
	if o.Grace <= 0 {
		o.Grace = nodeproto.DefaultSandboxGraceS * time.Second
	}
	if o.ReadWait <= 0 {
		o.ReadWait = 15 * time.Second
	}
	if o.Retry <= 0 {
		o.Retry = time.Second
	}
	if o.GiveUp <= 0 {
		o.GiveUp = 30 * time.Second
	}
	return o
}

// PID is 0: the run is no process on this machine.
func (p *Process) PID() int { return 0 }

// ID names the run in the brain's log.
func (p *Process) ID() string { return p.o.Label }

// Wait waits for the run to end on the node and its output to be copied.
func (p *Process) Wait() error {
	<-p.done
	p.ackOnce.Do(func() { close(p.acked) })
	return p.err
}

// StderrTail is the end of the run's stderr, as the node kept it.
func (p *Process) StderrTail() string {
	select {
	case <-p.done:
		return p.stderr
	default:
		return ""
	}
}

// Stop ends the run inside the VM: the node sends TERM to its process
// group and KILL after the grace. Killing the client of sbx exec would
// leave it running there.
func (p *Process) Stop() {
	p.stopOnce.Do(func() {
		close(p.stop)
		ctx, cancel := context.WithTimeout(context.Background(), p.o.Grace+30*time.Second)
		defer cancel()
		_ = p.call(ctx, nodeproto.MethodSandboxStop, nodeproto.SandboxStopParams{Run: p.run, GraceS: int(p.o.Grace / time.Second)}, nil)
		p.ackOnce.Do(func() { close(p.acked) })
	})
}

// ErrLost is a run whose node stopped answering before it ended.
var ErrLost = errors.New("the node stopped answering during the run")

// Stopped closes once the node heard the stop, or Wait ended.
func (p *Process) Stopped() <-chan struct{} { return p.acked }

// read copies the run's output until the node says the run is done. After
// a stop it reads on until the run ended, Grace and GiveUp at most; when
// reads keep failing for GiveUp, the run is lost.
func (p *Process) read() {
	defer close(p.done)
	var offset int64
	var failing time.Time
	var deadline <-chan time.Time
	writable := true
	for {
		if deadline == nil {
			select {
			case <-p.stop:
				deadline = time.After(p.o.Grace + p.o.GiveUp)
			default:
			}
		}
		select {
		case <-deadline:
			p.err = errors.New("the node did not end the run after it was stopped")
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.o.ReadWait+30*time.Second)
		var r nodeproto.SandboxReadResult
		err := p.call(ctx, nodeproto.MethodSandboxRead, nodeproto.SandboxReadParams{Run: p.run, Offset: offset, WaitMS: int(p.o.ReadWait / time.Millisecond)}, &r)
		cancel()
		if err != nil {
			if failing.IsZero() {
				failing = time.Now()
			}
			if time.Since(failing) >= p.o.GiveUp {
				p.err = fmt.Errorf("%w: %v", ErrLost, err)
				return
			}
			time.Sleep(p.o.Retry)
			continue
		}
		failing = time.Time{}
		if r.Data != "" && writable {
			// Once nothing reads the output any more, the rest is dropped,
			// and the reads go on for how the run ended.
			if _, err := io.WriteString(p.out, r.Data); err != nil {
				writable = false
			}
		}
		offset = max(offset, r.Next)
		if r.Done {
			p.stderr = strings.TrimSpace(r.StderrTail)
			switch {
			case r.Error != "":
				p.err = errors.New(r.Error)
			case r.ExitCode != 0:
				p.err = fmt.Errorf("exit status %d", r.ExitCode)
			}
			return
		}
	}
}
