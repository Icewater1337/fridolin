package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

const scenarioEnv = "FRIDOLIN_FAKE_SCENARIO"

// Scenario describes one fake provider invocation. Lines override the default
// provider result; WaitFile is a deterministic release barrier before output.
// Grandchild inherits stdout and remains in the provider's process group.
type Scenario struct {
	Provider                               string
	Lines                                  []string
	Delay, AfterOutput                     time.Duration
	Stderr                                 string
	ExitCode                               int
	Result, ErrorResult                    string
	Ask                                    json.RawMessage
	Grandchild                             bool
	PIDLog, ChildPIDLog, ArgsLog, WaitFile string
}

// FakeClaude returns this test executable and per-child environment entries.
// It never mutates the process environment and requires TestMain to call Main.
func FakeClaude(t testing.TB, s Scenario) (binary string, env []string) {
	t.Helper()
	if !sharedMainInstalled {
		t.Fatal("testutil.FakeClaude requires TestMain to call testutil.Main")
		return "", nil
	}
	if s.Provider == "" {
		s.Provider = "claude"
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return os.Args[0], []string{"FRIDOLIN_FAKE_CLAUDE=1", scenarioEnv + "=" + string(data), "GORACE=atexit_sleep_ms=0"}
}

// FakeCodex uses the same subprocess harness with Codex JSONL terminal events.
func FakeCodex(t testing.TB, s Scenario) (string, []string) {
	t.Helper()
	s.Provider = "codex"
	return FakeClaude(t, s)
}

func scenarioMain(raw string) int {
	var s Scenario
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		fmt.Fprintln(os.Stderr, "fake scenario:", err)
		return 2
	}
	if s.PIDLog != "" {
		if err := os.WriteFile(s.PIDLog, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return 2
		}
	}
	if s.ArgsLog != "" {
		data, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(s.ArgsLog, data, 0600); err != nil {
			return 2
		}
	}
	if s.Grandchild {
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fake grandchild:", err)
			return 2
		}
		if s.ChildPIDLog != "" {
			if err := os.WriteFile(s.ChildPIDLog, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
				return 2
			}
		}
	}
	if s.WaitFile != "" {
		for {
			if _, err := os.Stat(s.WaitFile); err == nil {
				break
			} else if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "fake release:", err)
				return 2
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(s.Delay)
	if s.Stderr != "" {
		fmt.Fprint(os.Stderr, s.Stderr)
	}
	if len(s.Ask) > 0 {
		fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":%s}]}}`+"\n", s.Ask)
	}
	if s.Lines != nil {
		for _, line := range s.Lines {
			fmt.Println(line)
		}
	} else if s.Provider == "codex" {
		if s.ErrorResult != "" {
			fmt.Printf(`{"type":"turn.failed","error":{"message":%s}}`+"\n", strconv.Quote(s.ErrorResult))
		} else {
			fmt.Printf(`{"type":"item.completed","item":{"type":"agent_message","text":%s}}`+"\n"+`{"type":"turn.completed"}`+"\n", strconv.Quote(s.Result))
		}
	} else {
		fmt.Printf(`{"type":"result","is_error":%t,"result":%s}`+"\n", s.ErrorResult != "", strconv.Quote(func() string {
			if s.ErrorResult != "" {
				return s.ErrorResult
			}
			return s.Result
		}()))
	}
	time.Sleep(s.AfterOutput)
	return s.ExitCode
}
