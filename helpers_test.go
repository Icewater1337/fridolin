package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the Claude CLI: with
// WCB_FAKE_CLAUDE=1 it prints a stream-json result echoing its working
// directory and the new message, after WCB_FAKE_SLEEP_MS. With
// WCB_FAKE_READ it also reports whether it could read that file, and with
// WCB_FAKE_ENV the value of that environment variable. Tests point
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
	if p := os.Getenv("WCB_FAKE_READ"); p != "" {
		switch _, err := os.ReadFile(p); {
		case err == nil:
			out += " read=ok"
		case errors.Is(err, fs.ErrPermission):
			out += " read=denied"
		default:
			out += " read=error: " + err.Error()
		}
	}
	if name := os.Getenv("WCB_FAKE_ENV"); name != "" {
		if v, ok := os.LookupEnv(name); ok {
			out += " " + name + "=" + v
		} else {
			out += " " + name + " unset"
		}
	}
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
// files.txt, file_<id>.txt, transcript_<id>.txt, summary_<id>.txt, and
// "account" (the id `me` reports). Like @plaud-ai/cli 0.3.15 it prints its
// spinner line to stderr and exits 2 only when the login is rejected: an
// "expired" file makes every call do that, "offline" makes every call exit 3
// (Plaud unreachable), and fail_<command> makes that command exit 1. Each
// call is logged with HOME.
func fakePlaud(args []string) int {
	dir := os.Getenv("WCB_FAKE_PLAUD_DIR")
	if f, err := os.OpenFile(dir+"/calls.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "HOME=%s %s\n", os.Getenv("HOME"), strings.Join(args, " "))
		f.Close()
	}
	token := os.Getenv("HOME") + "/.plaud/tokens.json"
	exists := func(name string) bool { _, err := os.Stat(dir + "/" + name); return err == nil }
	read := func(name string) string { b, _ := os.ReadFile(dir + "/" + name); return string(b) }
	if args[0] != "login" && args[0] != "logout" {
		fmt.Fprintf(os.Stderr, "- Fetching %s...\n", args[0])
		switch {
		case exists("expired"):
			fmt.Fprintln(os.Stderr, "✗ [AUTH_FAILED] Token invalid or expired. Run `plaud login`.")
			return 2
		case exists("offline"):
			fmt.Fprintln(os.Stderr, "✗ [UNREACHABLE] Cannot reach Plaud servers. Check your network.")
			return 3
		case exists("fail_" + args[0]):
			fmt.Fprintf(os.Stderr, "✗ [FETCH_FAILED] Failed to fetch %s.\n", args[0])
			return 1
		}
	}
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
		id := strings.TrimSpace(read("account"))
		if id == "" {
			id = "u_1"
		}
		fmt.Printf("\nUser Info:\n\n  id: %s\n  nickname: You\n  email: you@example.com\n\n", id)
	case "files":
		fmt.Print(read("files.txt"))
	case "file", "transcript", "summary":
		fmt.Print(read(args[0] + "_" + args[1] + ".txt"))
	}
	return 0
}
