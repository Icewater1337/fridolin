package whatsapp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunConnectLoopRetrySchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		var attempts []time.Duration
		RunConnectLoop(ctx, func() error {
			attempts = append(attempts, time.Since(start))
			if len(attempts) == 8 {
				return nil
			}
			return errors.New("network unavailable")
		})
		want := []time.Duration{0, 5 * time.Second, 15 * time.Second, 35 * time.Second, 75 * time.Second, 155 * time.Second, 275 * time.Second, 395 * time.Second}
		if !reflect.DeepEqual(attempts, want) {
			t.Fatalf("connect attempts = %v, want %v", attempts, want)
		}
	})
}

func TestRunConnectLoopCancellation(t *testing.T) {
	t.Run("before first attempt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		RunConnectLoop(ctx, func() error { t.Fatal("connected after cancellation"); return nil })
	})
	t.Run("during retry wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunConnectLoop(ctx, func() error { attempts++; return errors.New("network unavailable") })
			}()
			synctest.Wait()
			cancel()
			<-done
			if attempts != 1 {
				t.Fatalf("connect attempts = %d, want 1", attempts)
			}
		})
	})
}
