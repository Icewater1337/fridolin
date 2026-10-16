package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the Claude CLI: with
// WCB_FAKE_CLAUDE=1 it prints a stream-json result echoing its working
// directory and the new message, after WCB_FAKE_SLEEP_MS. Tests point
// claude_path at os.Args[0], so no script or real CLI is needed.
func TestMain(m *testing.M) {
	if os.Getenv("WCB_FAKE_PLAUD") == "1" && len(os.Args) > 1 && isPlaudCommand(os.Args[1]) {
		os.Exit(fakePlaud(os.Args[1:]))
	}
	if os.Getenv("WCB_FAKE_CLAUDE") == "1" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	args := os.Args[1:]
	prompt := ""
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			prompt = args[i+1]
		}
	}
	if os.Getenv("WCB_FAKE_CHILD") == "1" {
		// A grandchild that inherits stdout, like a shell command Claude ran.
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		_ = child.Start()
	}
	if ms, _ := strconv.Atoi(os.Getenv("WCB_FAKE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if log := os.Getenv("WCB_FAKE_ARGS_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(args[:len(args)-1], " "))
			f.Close()
		}
	}
	msg := prompt
	if i := strings.LastIndex(prompt, "<new_message>\n"); i >= 0 {
		msg = strings.TrimSuffix(strings.TrimSpace(prompt[i+len("<new_message>\n"):]), "</new_message>")
		msg = strings.TrimSpace(msg)
	}
	wd, _ := os.Getwd()
	out := fmt.Sprintf("echo:%s:%s", wd, msg)
	q := strconv.Quote(out)
	fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"text","text":%s}]}}`+"\n", q)
	fmt.Printf(`{"type":"result","result":%s}`+"\n", q)
}

// fakeClaudeEnv makes brains in this test spawn the fake CLI.
func fakeClaudeEnv(t *testing.T, sleepMS int) string {
	t.Helper()
	t.Setenv("WCB_FAKE_CLAUDE", "1")
	t.Setenv("WCB_FAKE_SLEEP_MS", strconv.Itoa(sleepMS))
	return os.Args[0]
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func isPlaudCommand(c string) bool {
	switch c {
	case "files", "file", "transcript", "summary", "me", "login", "logout":
		return true
	}
	return false
}

// fakePlaud plays Plaud's CLI from fixture files in WCB_FAKE_PLAUD_DIR:
// files.txt, file_<id>.txt, transcript_<id>.txt, summary_<id>.txt, and an
// "expired" file that makes every call exit 2. Each call is logged with HOME.
func fakePlaud(args []string) int {
	dir := os.Getenv("WCB_FAKE_PLAUD_DIR")
	if f, err := os.OpenFile(dir+"/calls.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "HOME=%s %s\n", os.Getenv("HOME"), strings.Join(args, " "))
		f.Close()
	}
	token := os.Getenv("HOME") + "/.plaud/tokens.json"
	if _, err := os.Stat(dir + "/expired"); err == nil && args[0] != "login" {
		fmt.Fprintln(os.Stderr, "login expired")
		return 2
	}
	read := func(name string) string { b, _ := os.ReadFile(dir + "/" + name); return string(b) }
	switch args[0] {
	case "login":
		if _, err := os.Stat(dir + "/login_wait"); err == nil {
			// Like the real CLI without a browser: print the URL, wait for the approval.
			fmt.Println("Could not open browser. Open this URL manually: https://example.com/oauth?client=x")
			for i := 0; i < 200; i++ {
				if _, err := os.Stat(dir + "/login_go"); err == nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		_ = os.MkdirAll(os.Getenv("HOME")+"/.plaud", 0o700)
		_ = os.WriteFile(token, []byte(`{"fake":true}`), 0o600)
		fmt.Println("Logged in")
	case "logout":
		_ = os.Remove(token)
	case "me":
		fmt.Print("- Fetching user info...\n\nUser Info:\n\n  id: 1\n  email: you@example.com\n  nickname: You\n")
	case "files":
		fmt.Print(read("files.txt"))
	case "file", "transcript", "summary":
		fmt.Print(read(args[0] + "_" + args[1] + ".txt"))
	}
	return 0
}
