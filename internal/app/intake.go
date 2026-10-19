package app

import "sync"

// IntakeGate counts the calls of handleInbound and handleChange at work.
// Stop closes it before anything else stops: from then on what comes in
// is kept for the next start instead (keepLate), and close waits for the
// calls at work, which may still hand a message to the debouncer or a
// brain. What comes in once the store is about to close (end) is lost.
type IntakeGate struct {
	mu     sync.Mutex
	closed bool
	ended  bool
	busy   sync.WaitGroup
}

// Enter reports whether a call may act on what came in, and then counts
// it until leave.
func (in *IntakeGate) Enter() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return false
	}
	in.busy.Add(1)
	return true
}

func (in *IntakeGate) Leave() { in.busy.Done() }

// Close makes what comes in from now on wait for the next start, and
// waits for the calls acting on what came in before.
func (in *IntakeGate) Close() {
	in.mu.Lock()
	in.closed = true
	in.mu.Unlock()
	in.busy.Wait()
}

// Keep runs fn, which keeps what came in once intake closed, unless the
// store is closing (end), and reports whether it did.
func (in *IntakeGate) Keep(fn func()) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.ended {
		return false
	}
	fn()
	return true
}

// End is when the store closes: what comes in after is lost.
func (in *IntakeGate) End() {
	in.mu.Lock()
	in.ended = true
	in.mu.Unlock()
}

// Closed reports whether new work must wait for the next application start.
func (in *IntakeGate) Closed() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closed
}
