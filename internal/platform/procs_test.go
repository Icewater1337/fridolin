package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestProcessesListsThisOne(t *testing.T) {
	procs, err := Processes()
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p Proc) bool { return p.PID == os.Getpid() })
	if i < 0 {
		t.Fatalf("this process (%d) is not among %d", os.Getpid(), len(procs))
	}
	p := procs[i]
	if p.PPID != os.Getppid() {
		t.Errorf("PPID = %d, want %d", p.PPID, os.Getppid())
	}
	if p.RSS == 0 {
		t.Error("no RSS")
	}
	if p.Start.IsZero() || time.Since(p.Start) < 0 || time.Since(p.Start) > time.Hour {
		t.Errorf("start %v", p.Start)
	}
	if runtime.GOOS != "windows" {
		if len(p.Args) == 0 || p.Args[0] != os.Args[0] {
			t.Errorf("args %q, want to start with %q", p.Args, os.Args[0])
		}
		if p.UID != os.Getuid() {
			t.Errorf("UID %d, want %d", p.UID, os.Getuid())
		}
		if p.PGID == 0 || p.SID == 0 {
			t.Errorf("PGID %d SID %d", p.PGID, p.SID)
		}
	}
	// The same start time a second time: what tells a process apart.
	again, err := Processes()
	if err != nil {
		t.Fatal(err)
	}
	j := slices.IndexFunc(again, func(q Proc) bool { return q.PID == os.Getpid() })
	if j < 0 || !again[j].Start.Equal(p.Start) {
		t.Errorf("start time moved: %v then %v", p.Start, again[j].Start)
	}
	if !ProcessAlive(os.Getpid()) {
		t.Error("this process is not alive")
	}
}

func TestProcessEnvReadsThisOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not on Windows")
	}
	env, err := ProcessEnv(os.Getpid())
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return len(kv) > 5 && kv[:5] == "PATH=" }) {
		t.Errorf("no PATH in %d variables", len(env))
	}
}

func TestClearStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.lock")
	if _, stale, err := ClearStaleLock(path, true); stale || err != nil {
		t.Fatalf("missing file: stale %v, %v", stale, err)
	}
	// A PID that ran and has ended.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := cmd.Process.Pid
	if err := os.WriteFile(path, []byte(strconv.Itoa(dead)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, stale, err := ClearStaleLock(path, false); pid != dead || !stale || err != nil {
		t.Fatalf("dead PID: %d %v %v", pid, stale, err)
	}
	if data, _ := os.ReadFile(path); len(data) == 0 {
		t.Fatal("a look without clear emptied the file")
	}
	// Held by a running bridge (here: this process), it is not stale,
	// whatever it says.
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale, err := ClearStaleLock(path, true); stale || err != nil {
		t.Fatalf("held lock: stale %v, %v", stale, err)
	}
	l.Release()
	if err := os.WriteFile(path, []byte(strconv.Itoa(dead)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stale, err := ClearStaleLock(path, true); !stale || err != nil {
		t.Fatalf("clear: stale %v, %v", stale, err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("after clear: %q, %v (the file stays, empty)", data, err)
	}
}

func TestSignalProcessRefusesInit(t *testing.T) {
	if err := SignalProcess(1, false); err == nil {
		t.Error("signalled PID 1")
	}
}
