package app_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
)

func TestSupervisorBindsBeforeStartingServices(t *testing.T) {
	t.Parallel()
	var s app.Supervisor
	sentinel := errors.New("port occupied")
	unexpected := func(context.Context) { t.Error("started a service after bind failure") }
	err := s.Run(t.Context(), app.RunOptions{Address: "127.0.0.1:1", Listen: func(string, string) (net.Listener, error) { return nil, sentinel }, Resume: unexpected, Connect: unexpected, Watch: unexpected, Background: []func(context.Context){unexpected}})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "is another bridge running?") {
		t.Fatalf("bind error: %v", err)
	}
	if s.HTTPContext() != nil {
		t.Fatal("created HTTP state after failed bind")
	}
	s.StopHTTP(time.Second)
	if !s.WaitConnections(time.Second) {
		t.Fatal("connection started after bind failure")
	}
}

func TestSupervisorJoinsWatcherBeforeReturningOnCancellation(t *testing.T) {
	t.Parallel()
	var s app.Supervisor
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWatcher := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWatcher()
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, app.RunOptions{
			Resume:  func(context.Context) {},
			Connect: func(ctx context.Context) { <-ctx.Done() },
			Watch:   func(ctx context.Context) { close(started); <-ctx.Done(); close(cancelled); <-release },
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not receive cancellation")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before watcher completed: %v", err)
	default:
	}
	releaseWatcher()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if !s.WaitConnections(time.Second) {
		t.Fatal("connection did not stop")
	}
}
