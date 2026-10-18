package main

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// A chat's jobs run one after another in the order they came, each chat's
// apart from the others': a chat whose job is stuck holds up only itself.
func TestChatQueuesOrder(t *testing.T) {
	t.Run("one chat's jobs in order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var q chatQueues
			var mu sync.Mutex
			var ran []int
			for i := range 20 {
				if err := q.add("a", func() {
					// Yields, so that a second job running at once
					// would go first.
					time.Sleep(time.Duration(20-i) * time.Millisecond)
					mu.Lock()
					defer mu.Unlock()
					ran = append(ran, i)
				}); err != nil {
					t.Fatal(err)
				}
			}
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			mu.Lock()
			defer mu.Unlock()
			if want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}; !slices.Equal(ran, want) {
				t.Errorf("ran %v, want %v", ran, want)
			}
		})
	})

	t.Run("a stuck chat holds up only itself", func(t *testing.T) {
		var q chatQueues
		stuck := make(chan struct{})
		defer close(stuck)
		if err := q.add("a", func() { <-stuck }); err != nil {
			t.Fatal(err)
		}
		aNext := make(chan struct{})
		if err := q.add("a", func() { close(aNext) }); err != nil {
			t.Fatal(err)
		}
		bRan := make(chan struct{})
		if err := q.add("b", func() { close(bRan) }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-bRan:
		case <-time.After(2 * time.Second):
			t.Fatal("chat b waited for chat a")
		}
		select {
		case <-aNext:
			t.Error("chat a's second job ran before its first ended")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("a job that panics leaves the chat's next one", func(t *testing.T) {
		logged := captureLog(t)
		var q chatQueues
		if err := q.add("15551234567@s.whatsapp.net", func() { panic("boom") }); err != nil {
			t.Fatal(err)
		}
		next := make(chan struct{})
		if err := q.add("15551234567@s.whatsapp.net", func() { close(next) }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-next:
		case <-time.After(2 * time.Second):
			t.Fatal("the next job did not run")
		}
		got := strings.Join(logged(), "\n")
		if !strings.Contains(got, "panicked: boom") || strings.Contains(got, "15551234567") {
			t.Errorf("log %q, want the panic with the chat masked", got)
		}
	})
}

// A chat's worker starts with its first job and ends once it has none
// left, so idle chats cost nothing; the chat's next job starts another.
func TestChatQueuesWorkerEndsWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var q chatQueues
		var runs atomic.Int32
		for range 3 {
			if err := q.add("a", func() { runs.Add(1) }); err != nil {
				t.Fatal(err)
			}
			synctest.Wait() // every goroutine of the bubble ended or waits
			q.mu.Lock()
			workers := len(q.chats)
			q.mu.Unlock()
			if workers != 0 {
				t.Fatalf("%d workers left running with nothing to do", workers)
			}
		}
		if n := runs.Load(); n != 3 {
			t.Errorf("ran %d jobs, want 3", n)
		}
	})
}

// A chat that sends faster than its worker keeps up, a flood from a group
// the bridge does not read say, fills its queue, and the rest is dropped:
// with one log line for the flood, and then one a minute.
func TestChatQueuesFullQueueDrops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logged := captureLog(t)
		var q chatQueues
		stuck := make(chan struct{})
		chat := "120363000000000000@g.us"
		var ran atomic.Int32
		if err := q.add(chat, func() { <-stuck }); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // the worker is on the stuck job
		for i := range chatQueueLimit {
			if err := q.add(chat, func() { ran.Add(1) }); err != nil {
				t.Fatalf("job %d: %v, want it queued", i, err)
			}
		}
		for range 50 {
			if err := q.add(chat, func() { ran.Add(1) }); !errors.Is(err, errChatQueueFull) {
				t.Fatalf("add = %v, want errChatQueueFull", err)
			}
		}
		if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], chat) || !strings.Contains(lines[0], "dropping") {
			t.Fatalf("logged %q, want one line about the drops", lines)
		}
		// Another chat is not affected.
		other := make(chan struct{})
		if err := q.add("120363000000000001@g.us", func() { close(other) }); err != nil {
			t.Fatal(err)
		}
		<-other

		time.Sleep(chatDropLogEvery)
		if err := q.add(chat, func() {}); !errors.Is(err, errChatQueueFull) {
			t.Fatalf("add = %v, want errChatQueueFull", err)
		}
		if lines := logged(); len(lines) != 2 || !strings.Contains(lines[1], "dropped 50 more") {
			t.Fatalf("logged %q, want a second line counting the drops in between", lines)
		}
		close(stuck)
		synctest.Wait()
		if n := ran.Load(); n != chatQueueLimit {
			t.Errorf("ran %d jobs, want the %d queued", n, chatQueueLimit)
		}
	})
}

// close refuses new jobs and waits for the workers to run those they hold,
// for as long as it was given: what is left then is dropped.
func TestChatQueuesClose(t *testing.T) {
	t.Run("the jobs held run before it returns", func(t *testing.T) {
		var q chatQueues
		var ran atomic.Int32
		gate := make(chan struct{})
		if err := q.add("a", func() { <-gate }); err != nil {
			t.Fatal(err)
		}
		for _, chat := range []string{"a", "a", "b", "c"} {
			if err := q.add(chat, func() { time.Sleep(10 * time.Millisecond); ran.Add(1) }); err != nil {
				t.Fatal(err)
			}
		}
		time.AfterFunc(50*time.Millisecond, func() { close(gate) })
		if left := q.close(5 * time.Second); left != 0 {
			t.Errorf("close left %d jobs", left)
		}
		if n := ran.Load(); n != 4 {
			t.Errorf("%d jobs ran by the time close returned, want 4", n)
		}
		if err := q.add("a", func() { t.Error("a job ran after close") }); !errors.Is(err, errIntakeClosed) {
			t.Errorf("add after close = %v, want errIntakeClosed", err)
		}
	})

	t.Run("the wait is bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var q chatQueues
			stuck := make(chan struct{})
			if err := q.add("a", func() { <-stuck }); err != nil {
				t.Fatal(err)
			}
			var ran atomic.Int32
			for range 3 {
				if err := q.add("a", func() { ran.Add(1) }); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			if left := q.close(5 * time.Second); left != 3 {
				t.Errorf("close left %d jobs, want 3", left)
			}
			if waited := time.Since(start); waited != 5*time.Second {
				t.Errorf("close waited %v, want 5s", waited)
			}
			close(stuck)
			synctest.Wait()
			if n := ran.Load(); n != 0 {
				t.Errorf("%d dropped jobs ran after close", n)
			}
		})
	})
}

func TestMaskChatJID(t *testing.T) {
	cases := map[string]string{
		"120363000000000000@g.us":    "120363000000000000@g.us",
		"15551234567@s.whatsapp.net": maskPhone("15551234567") + "@s.whatsapp.net",
		"15559876543@lid":            "…43@lid",
	}
	for chat, want := range cases {
		if got := maskChatJID(chat); got != want {
			t.Errorf("maskChatJID(%q) = %q, want %q", chat, got, want)
		}
	}
}
