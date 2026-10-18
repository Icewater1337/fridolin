//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestRunsLeadASessionOfTheirOwn: a run that shares the session of a bridge
// started in your terminal has that terminal as its controlling terminal,
// and could push keystrokes into your shell through /dev/tty (TIOCSTI),
// which ran them outside any sandbox. Brain runs and the commands the site
// builder runs as a brain now each lead a session of their own, without a
// terminal. The site builder's used to share the bridge's process group too,
// so its timeout left what the command started running.
func TestRunsLeadASessionOfTheirOwn(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) (string, error)
	}{
		{"isolateProcessGroup", func(t *testing.T) (string, error) {
			cmd := exec.CommandContext(context.Background(), os.Args[0])
			cmd.Env = append(os.Environ(), "FRIDOLIN_FAKE_SESSION=1")
			isolateProcessGroup(cmd)
			out, err := cmd.Output()
			return string(out), err
		}},
		{"the site builder's sandboxed commands", func(t *testing.T) (string, error) {
			m, dir := newTestBuilderSite(t)
			m.hooks.sandbox = func(_, bin string, args []string) (string, []string, []string, error) {
				return bin, args, []string{"FRIDOLIN_FAKE_SESSION=1"}, nil
			}
			return m.sandboxed(context.Background(), "web", dir, 30*time.Second, os.Args[0])
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.run(t)
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var pid, sid int
			if _, err := fmt.Sscanf(out, "pid=%d sid=%d", &pid, &sid); err != nil {
				t.Fatalf("output %q: %v", out, err)
			}
			if sid != pid {
				t.Errorf("the command ran in session %d, want one of its own (%d)", sid, pid)
			}
		})
	}
}
