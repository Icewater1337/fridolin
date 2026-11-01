package nodes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func TestACallTheCallerCancelsDoesNotSayTheNodeTimedOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	err := awaitAnswer(ctx, make(chan nodeproto.Response), "abc", nodeproto.MethodExec, 15*time.Minute, nil)
	var perr *nodeproto.Error
	if !errors.As(err, &perr) || perr.Code != nodeproto.CodeUnavailable {
		t.Fatalf("a cancelled call answers %v, want an %s error", err, nodeproto.CodeUnavailable)
	}
	if strings.Contains(err.Error(), "15m") || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("a cancelled call says %q", err)
	}
}

func TestACallThatRunsOutOfTimeSaysHowLong(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		window time.Duration
		want   string
	}{
		{"its own limit", func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, 20 * time.Millisecond, "within 20ms"},
		{"the caller's limit", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, time.Hour, "within the caller's"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			err := awaitAnswer(ctx, make(chan nodeproto.Response), "abc", nodeproto.MethodExec, tc.window, nil)
			var perr *nodeproto.Error
			if !errors.As(err, &perr) || perr.Code != nodeproto.CodeTimeout {
				t.Fatalf("got %v, want a %s error", err, nodeproto.CodeTimeout)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %q, want it to say %q", err, tc.want)
			}
		})
	}
}
