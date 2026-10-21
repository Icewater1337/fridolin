package snapshot

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestDeadLocks(t *testing.T) {
	locks := []Lock{
		{ID: "a", Hostname: "core", PID: 10},
		{ID: "b", Hostname: "core", PID: 11},
		{ID: "c", Hostname: "laptop", PID: 10},
		{ID: "d", Hostname: "core", PID: 0},
	}
	alive := func(pid int) bool { return pid == 11 }
	got := DeadLocks(locks, "core", alive)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("DeadLocks = %+v, want only a", got)
	}
	if got := DeadLocks(locks, "", alive); len(got) != 0 {
		t.Errorf("with no host name: %+v", got)
	}
}

// holdLock starts a real restic backup that reads its file from a pipe
// it never gets to the end of, so it holds its lock until it ends, and
// waits until the repository shows the lock.
func holdLock(t *testing.T, ctx context.Context, r *Restic, before int) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.CommandContext(ctx, r.Binary, "-r", r.Repo, "--no-cache", "backup", "--stdin", "--stdin-filename", "held.txt")
	cmd.Env = r.Env
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for range 200 {
		locks, err := r.Locks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(locks) > before {
			return cmd, in
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("restic took no lock")
	return nil, nil
}

// TestResticDeadLockRemovedLiveKept leaves a lock of a killed restic in a
// real repository beside the lock of one that still runs: the dead one is
// found and Unlock removes it, the live one stays.
func TestResticDeadLockRemovedLiveKept(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("restic's locks name Unix PIDs here")
	}
	bin := realRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := localRepo(t, bin, "lock-password")
	if err := r.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	dead, _ := holdLock(t, ctx, r, 0)
	_ = dead.Process.Kill() // SIGKILL: restic cannot remove its lock
	_ = dead.Wait()
	live, liveIn := holdLock(t, ctx, r, 1)
	defer liveIn.Close()

	locks, err := r.Locks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 2 {
		t.Fatalf("locks = %+v, want 2", locks)
	}
	alive := func(pid int) bool { return pid == live.Process.Pid }
	stale := DeadLocks(locks, host, alive)
	if len(stale) != 1 || stale[0].PID != dead.Process.Pid {
		t.Fatalf("dead locks = %+v, want the one of PID %d", stale, dead.Process.Pid)
	}
	if err := r.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := r.Locks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].PID != live.Process.Pid {
		t.Fatalf("after unlock: %+v, want only the live lock of PID %d", left, live.Process.Pid)
	}
}
