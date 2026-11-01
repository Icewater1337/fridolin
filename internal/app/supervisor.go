package app

import (
	"github.com/Icewater1337/fridolin/internal/logx"

	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// RunOptions describes the layers started after a successful listener bind.
// The owning application stops services and closes storage in dependency order.
type RunOptions struct {
	Logger           *slog.Logger
	Address          string
	Listen           func(string, string) (net.Listener, error)
	Handler          func() http.Handler
	DashboardEnabled bool
	Resume           func(context.Context)
	Connect          func(context.Context)
	Watch            func(context.Context)
	Background       []func(context.Context) // start in order and return
	Loops            []func(context.Context) // run until ctx ends, each in its own goroutine
}

// Supervisor owns the HTTP server and connection/watch goroutines. Its zero
// value starts nothing; Run begins them, and the owner then tears down services.
type Supervisor struct {
	logger      *slog.Logger
	server      *http.Server
	connections sync.WaitGroup
	stopOnce    sync.Once
}

// Run binds before starting intake and propagates unexpected server failures.
// Parent cancellation remains a normal stop; its cause is retained by the caller.
func (s *Supervisor) Run(ctx context.Context, o RunOptions) error {
	s.logger = o.Logger
	run, fail := context.WithCancelCause(ctx)
	defer fail(nil)
	if o.Address != "" {
		ln, err := o.Listen("tcp", o.Address)
		if err != nil {
			return fmt.Errorf("listening on %s (is another bridge running?): %w", o.Address, err)
		}
		s.server = httpapi.NewServer(o.Handler())
		s.server.BaseContext = func(net.Listener) context.Context { return run }
		go func() {
			if err := s.server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				fail(fmt.Errorf("HTTP server: %w", err))
			}
		}()
		if o.DashboardEnabled {
			logx.Printf(s.logger, "Dashboard: http://%s/  (open it with: fridolin open)", o.Address)
		}
	}
	o.Resume(run)
	s.connections.Go(func() { o.Connect(run) })
	var watchers sync.WaitGroup
	watchers.Go(func() { o.Watch(run) })
	for _, start := range o.Background {
		start(run)
	}
	for _, loop := range o.Loops {
		watchers.Go(func() { loop(run) })
	}
	logx.Printf(s.logger, "Bridge started. Waiting for messages…")
	<-run.Done()
	watchers.Wait()
	if ctx.Err() == nil {
		return context.Cause(run)
	}
	return nil
}

// HTTPContext reports the lifetime shared by requests, nil before HTTP starts.
func (s *Supervisor) HTTPContext() context.Context {
	if s.server == nil {
		return nil
	}
	return s.server.BaseContext(nil)
}

// StopHTTP stops accepting requests, then closes ones that exceed the grace.
func (s *Supervisor) StopHTTP(grace time.Duration) {
	if s.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		logx.Printf(s.logger, "HTTP server: requests still running %v after the stop began (%v); cutting them off", grace, err)
		_ = s.server.Close()
	}
}

// WaitConnections joins connection attempts after disconnect, with the existing
// bounded grace for a transport which does not return promptly.
func (s *Supervisor) WaitConnections(grace time.Duration) bool {
	done := make(chan struct{})
	go func() { s.connections.Wait(); close(done) }()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
