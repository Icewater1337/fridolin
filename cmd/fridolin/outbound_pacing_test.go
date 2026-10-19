package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// Pin the send loop before extracting it: only successful preceding parts
// are reported, pacing is between sends, and cancellation interrupts a wait.
func TestSendChunksPacingAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pace        time.Duration
		failAt      int
		cancelAt    time.Duration
		cancelFirst bool
		wantSent    []string
		wantElapsed time.Duration
		wantPartial bool
	}{
		{name: "WhatsApp pace", pace: 300 * time.Millisecond, failAt: -1, wantSent: []string{"a", "b", "c"}, wantElapsed: 600 * time.Millisecond},
		{name: "dev has no pace", failAt: -1, wantSent: []string{"a", "b", "c"}},
		{name: "first failure", pace: 300 * time.Millisecond, failAt: 0},
		{name: "partial failure", pace: 300 * time.Millisecond, failAt: 1, wantSent: []string{"a"}, wantElapsed: 300 * time.Millisecond, wantPartial: true},
		{name: "cancel before first", pace: 300 * time.Millisecond, failAt: -1, cancelFirst: true},
		{name: "cancel during pause", pace: 300 * time.Millisecond, failAt: -1, cancelAt: 100 * time.Millisecond, wantSent: []string{"a"}, wantElapsed: 100 * time.Millisecond, wantPartial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.cancelFirst {
					cancel()
				}
				if tc.cancelAt > 0 {
					go func() { time.Sleep(tc.cancelAt); cancel() }()
				}
				var sent []string
				fail := errors.New("send failed")
				started := time.Now()
				err := sendChunks(ctx, []string{"a", "b", "c"}, tc.pace, func(s string) error {
					if len(sent) == tc.failAt {
						return fail
					}
					sent = append(sent, s)
					return nil
				})
				if !reflect.DeepEqual(sent, tc.wantSent) {
					t.Fatalf("sent=%v, want %v", sent, tc.wantSent)
				}
				if elapsed := time.Since(started); elapsed != tc.wantElapsed {
					t.Fatalf("elapsed=%s, want %s", elapsed, tc.wantElapsed)
				}
				var partial *partialSendError
				if got := errors.As(err, &partial); got != tc.wantPartial {
					t.Fatalf("partial=%v, want %v: %v", got, tc.wantPartial, err)
				}
				if partial != nil && (!reflect.DeepEqual(partial.Sent, tc.wantSent) || partial.Total != 3) {
					t.Fatalf("partial=%+v", partial)
				}
				switch {
				case tc.cancelFirst || tc.cancelAt > 0:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("err=%v, want cancellation", err)
					}
				case tc.failAt >= 0:
					if !errors.Is(err, fail) {
						t.Fatalf("err=%v, want failure", err)
					}
				case err != nil:
					t.Fatal(err)
				}
			})
		})
	}
}
