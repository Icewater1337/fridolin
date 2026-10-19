package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSignalWatcherPreservesForcedSecondSignal(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	signals := make(chan os.Signal, 2)
	forced := make(chan struct{})
	signalled, stop := watchSignals(signals, cancel, func() { close(forced) })
	t.Cleanup(stop)
	signals <- syscall.SIGTERM
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first signal did not cancel")
	}
	if !signalled() || context.Cause(ctx).Error() != "received signal terminated" {
		t.Fatalf("signal=%v cause=%v", signalled(), context.Cause(ctx))
	}
	select {
	case <-forced:
		t.Fatal("first signal forced shutdown")
	default:
	}
	signals <- syscall.SIGINT
	select {
	case <-forced:
	case <-time.After(time.Second):
		t.Fatal("second signal did not force shutdown")
	}
	stop()
	stop()
}

func TestSignalWatcherCleanupJoinsWhileWaiting(t *testing.T) {
	for _, first := range []bool{false, true} {
		ctx, cancel := context.WithCancelCause(t.Context())
		signals := make(chan os.Signal, 2)
		signalled, stop := watchSignals(signals, cancel, func() { t.Error("cleanup forced shutdown") })
		t.Cleanup(stop)
		if first {
			signals <- syscall.SIGTERM
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("first signal did not cancel")
			}
		}
		stopped := make(chan struct{})
		go func() { stop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("cleanup left watcher blocked")
		}
		signals <- syscall.SIGINT
		if signalled() != first {
			t.Fatalf("signalled=%v, want %v", signalled(), first)
		}
		if !first && ctx.Err() != nil {
			t.Fatal("cleanup cancelled application")
		}
	}
}
