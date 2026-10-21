package housekeeping

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// Stopper ends processes.
type Stopper struct {
	// List lists the processes (platform.Processes); Signal signals one
	// (platform.SignalProcess).
	List   func() ([]platform.Proc, error)
	Signal func(pid int, force bool) error
	// Grace is how long the processes get after SIGTERM before SIGKILL
	// (default 5 s); Poll how often they are looked for meanwhile.
	Grace time.Duration
	Poll  time.Duration
}

// Stop ends f's processes: each that still is the process f saw (same
// PID, same start time) gets SIGTERM, the topmost first, and SIGKILL if
// it has not ended after the grace. It reports how many it stopped; a
// process that ended by itself meanwhile counts as stopped.
func (st Stopper) Stop(ctx context.Context, f Finding) (int, error) {
	if len(f.Members) == 0 {
		return 0, errors.New("nothing to stop")
	}
	grace, poll := st.Grace, st.Poll
	if grace <= 0 {
		grace = 5 * time.Second
	}
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	same, err := st.still(f.Members)
	if err != nil {
		return 0, err
	}
	if len(same) == 0 {
		return 0, nil // all gone already
	}
	var errs []error
	for _, m := range f.Members {
		if same[m.PID] {
			if err := st.Signal(m.PID, false); err != nil {
				errs = append(errs, fmt.Errorf("PID %d: %w", m.PID, err))
			}
		}
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		left, err := st.still(f.Members)
		if err == nil && len(left) == 0 {
			return len(same), errors.Join(errs...)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(poll):
		}
	}
	left, err := st.still(f.Members)
	if err != nil {
		return 0, err
	}
	for pid := range left {
		if err := st.Signal(pid, true); err != nil {
			errs = append(errs, fmt.Errorf("PID %d: %w", pid, err))
		}
	}
	// SIGKILL takes effect at once, but a process is listed until its
	// parent collects it: a zombie no longer holds anything.
	time.Sleep(poll)
	left, _ = st.still(f.Members)
	if len(left) > 0 {
		errs = append(errs, fmt.Errorf("%d of %d processes still run after SIGKILL", len(left), len(same)))
	}
	return len(same) - len(left), errors.Join(errs...)
}

// still says which of members still run as the process they were: same
// PID and start time, and not a zombie.
func (st Stopper) still(members []Member) (map[int]bool, error) {
	procs, err := st.List()
	if err != nil {
		return nil, err
	}
	want := make(map[int]time.Time, len(members))
	for _, m := range members {
		want[m.PID] = m.Start
	}
	out := map[int]bool{}
	for _, p := range procs {
		if start, ok := want[p.PID]; ok && !p.Zombie && p.Start.Equal(start) {
			out[p.PID] = true
		}
	}
	return out, nil
}
