//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The variables that make this test binary, run again by a test below,
// play the other process.
const (
	lockChildVar    = "FRIDOLIN_TEST_LOCK_DIR"
	execChildVar    = "FRIDOLIN_TEST_EXEC"
	sessionChildVar = "FRIDOLIN_TEST_SESSION"
)

// runAgain runs the test binary again for test alone, with env added, and
// returns what it printed.
func runAgain(t *testing.T, test string, env ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s, run again: %v\n%s", test, err, out)
	}
	return string(out)
}

func TestInstanceLockRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireInstanceLock(dir); err == nil {
		second.Release()
		t.Fatal("a second lock on the same data dir must fail")
	} else if !strings.Contains(err.Error(), "another bridge is already running") {
		t.Errorf("err = %v", err)
	}
	other, err := acquireInstanceLock(t.TempDir())
	if err != nil {
		t.Fatalf("another data dir must still lock: %v", err)
	}
	other.Release()

	first.Release()
	first.Release() // a second release is harmless
	again, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("after release the lock must be free: %v", err)
	}
	again.Release()
}

// The flock belongs to the open file and the kernel holds it for the
// process: a second bridge, a process of its own, is refused with the same
// message while the first holds the lock, and gets it once the first lets
// go.
func TestInstanceLockRefusesAnotherProcess(t *testing.T) {
	if dir := os.Getenv(lockChildVar); dir != "" {
		l, err := acquireInstanceLock(dir)
		if err != nil {
			fmt.Println("refused:", err)
			return
		}
		l.Release()
		fmt.Println("acquired")
		return
	}
	dir := t.TempDir()
	l, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "refused: another bridge is already running with data dir " + dir
	if out := runAgain(t, "TestInstanceLockRefusesAnotherProcess", lockChildVar+"="+dir); !strings.Contains(out, want) {
		t.Errorf("a second process got the lock:\n%s", out)
	}
	l.Release()
	if out := runAgain(t, "TestInstanceLockRefusesAnotherProcess", lockChildVar+"="+dir); !strings.Contains(out, "acquired") {
		t.Errorf("after release another process must get the lock:\n%s", out)
	}
	again, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("after the other process the lock must be free: %v", err)
	}
	again.Release()
}

// A restart runs the bridge again in the same process, with the arguments
// and the environment it is given.
func TestExecInPlaceReplacesTheProcess(t *testing.T) {
	if os.Getenv(execChildVar) == "1" {
		err := execInPlace("/bin/sh", []string{"sh", "-c", `echo "replaced by $0, with $WORD"`}, []string{"WORD=its own environment"})
		fmt.Println("exec failed:", err)
		os.Exit(1)
	}
	if out := runAgain(t, "TestExecInPlaceReplacesTheProcess", execChildVar+"=1"); !strings.Contains(out, "replaced by sh, with its own environment") {
		t.Errorf("the process was not replaced:\n%s", out)
	}
}

// Killing a run's process group also kills what it started, such as a
// shell command still holding its output.
func TestKillProcessGroupKillsWhatTheProcessStarted(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 60 & echo $!; wait")
	isolateProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	if _, err := fmt.Fscan(stdout, &child); err != nil {
		t.Fatal(err)
	}
	if err := killProcessGroup(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the shell's child %d outlived the group", child)
		}
	}
	// A group that is gone, and no process at all, are no error.
	if err := killProcessGroup(cmd.Process.Pid); err != nil {
		t.Errorf("killing a group that is gone: %v", err)
	}
	if err := killProcessGroup(0); err != nil {
		t.Errorf("killProcessGroup(0) = %v", err)
	}
}

// A run that shares the session of a bridge started in your terminal has
// that terminal as its controlling terminal, and could push keystrokes into
// your shell through /dev/tty (TIOCSTI), which ran them outside any
// sandbox. An isolated process leads a session of its own, without a
// terminal, and with it its own process group.
func TestIsolateProcessGroupStartsASessionOfItsOwn(t *testing.T) {
	if os.Getenv(sessionChildVar) == "1" {
		// syscall has no Getsid on Linux.
		sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
		if errno != 0 {
			fmt.Println("getsid:", errno)
			os.Exit(1)
		}
		fmt.Printf("pid=%d sid=%d\n", os.Getpid(), sid)
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestIsolateProcessGroupStartsASessionOfItsOwn$", "-test.count=1")
	cmd.Env = append(os.Environ(), sessionChildVar+"=1")
	isolateProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var pid, sid int
	if _, err := fmt.Sscanf(string(out), "pid=%d sid=%d", &pid, &sid); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if sid != pid {
		t.Errorf("the process ran in session %d, want one of its own (%d)", sid, pid)
	}
}

func TestIsTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file is no terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	// A character device counts, so this is a best guess.
	if !isTerminal(null) {
		t.Errorf("%s is a character device", os.DevNull)
	}
}

func TestFolderPicker(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		if argv := folderPicker("/Users/you"); len(argv) == 0 || argv[0] != "osascript" {
			t.Errorf("folderPicker() = %q, want osascript's dialog", argv)
		}
	case "linux":
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		if argv := folderPicker("/Users/you"); argv != nil {
			t.Errorf("folderPicker() = %q without a display, want none", argv)
		}
	}
}
