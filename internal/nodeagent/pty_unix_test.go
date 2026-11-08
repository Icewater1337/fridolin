//go:build unix

package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// startTestTerminal runs script as the shell of a terminal, with its loops.
func startTestTerminal(t *testing.T, script string) *terminal {
	t.Helper()
	proc, err := startPTY(exec.Command("/bin/sh", "-c", script), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	tm := &terminal{id: "x", proc: proc, wake: make(chan struct{}), drained: make(chan struct{}), tmp: t.TempDir()}
	go tm.readLoop()
	go tm.waitLoop()
	return tm
}

// readToEnd reads as a client does, until a read says the terminal closed.
func readToEnd(t *testing.T, tm *terminal) string {
	t.Helper()
	a := &Agent{}
	a.work.terms = map[string]*terminal{tm.id: tm}
	var got strings.Builder
	var off int64
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		v, perr := a.terminalRead(context.Background(), nodeproto.TerminalReadParams{ID: tm.id, Offset: off, WaitMS: 500})
		if perr != nil {
			t.Fatal(perr)
		}
		res := v.(nodeproto.TerminalOutput)
		got.Write(res.Data)
		off = res.Next
		if res.Closed {
			return got.String()
		}
	}
	t.Fatalf("no close; so far %d bytes", got.Len())
	return ""
}

// Every byte the shell wrote just before it exited reaches the reader, in
// the read that says the terminal closed or before it.
func TestTerminalOutputDrainsBeforeClose(t *testing.T) {
	file := t.TempDir() + "/lines"
	var want strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&want, "line %d\n", i)
	}
	if err := os.WriteFile(file, []byte(want.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		got := readToEnd(t, startTestTerminal(t, "stty -onlcr; cat '"+file+"'; exit 7"))
		if got != want.String() {
			t.Fatalf("%d of %d bytes arrived; the last: %q", len(got), want.Len(), got[max(len(got)-40, 0):])
		}
	}
}

// A background job that keeps the PTY open must not keep the terminal open
// (Linux keeps it open; macOS ends it with the session's leader).
func TestTerminalClosesPastABackgroundJob(t *testing.T) {
	tm := startTestTerminal(t, "sleep 30 & echo before; exit 3")
	if got := readToEnd(t, tm); !strings.Contains(got, "before") {
		t.Fatalf("output %q", got)
	}
	if info := tm.info(); info.ExitCode == nil || *info.ExitCode != 3 {
		t.Fatalf("exit code %v", info.ExitCode)
	}
	tm.proc.Kill() // the job, which the shell's end left behind
}
