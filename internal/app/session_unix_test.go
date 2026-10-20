//go:build unix

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// fakeSession prints the process's pid and the session it is in: a process
// that leads a session of its own prints the same number twice.
func fakeSession() int {
	// syscall has no Getsid on Linux.
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		fmt.Println("getsid:", errno)
		return 1
	}
	fmt.Printf("pid=%d sid=%d\n", os.Getpid(), sid)
	return 0
}

// fakeTTY tries what a run could do to the terminal of the bridge that
// started it, and prints how each went: open /dev/tty for reading, push a
// character into its input with TIOCSTI (which only needs reading), and open
// it for writing.
func fakeTTY() int {
	f, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("read:", err)
	} else {
		fmt.Println("read: ok")
		c := byte('#')
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSTI), uintptr(unsafe.Pointer(&c))); errno != 0 {
			fmt.Println("inject:", errno)
		} else {
			fmt.Println("inject: ok")
		}
		f.Close()
	}
	if w, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err != nil {
		fmt.Println("write:", err)
	} else {
		fmt.Println("write: ok")
		w.Close()
	}
	return 0
}

// fakeTTYParent plays the bridge in a terminal: it checks that it has one,
// then runs the command spec describes (JSON: argv, dir, env, and isolate
// for platform.IsolateProcessGroup, as a brain's run gets it) and prints
// its output.
func fakeTTYParent(spec string) int {
	var s struct {
		Argv, Env []string
		Dir       string
		Isolate   bool
	}
	if err := json.Unmarshal([]byte(spec), &s); err != nil || len(s.Argv) == 0 {
		fmt.Println("bad spec:", err)
		return 1
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		fmt.Println("the parent has no terminal:", err)
		return 1
	}
	tty.Close()
	// A run in the terminal's background process group that tries TIOCSTI
	// is stopped (SIGTTOU), so it must not wait for that run forever.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	if s.Isolate {
		platform.IsolateProcessGroup(cmd)
	}
	out, err := cmd.CombinedOutput()
	os.Stdout.Write(out)
	if err != nil {
		fmt.Println("child:", err)
		return 1
	}
	return 0
}
