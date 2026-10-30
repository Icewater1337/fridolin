package testutil

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// fakeNewMessage is the new message in prompt, as the fake CLI echoes it:
// "<from> says: <text>".
func NewMessage(prompt string) string {
	msg := prompt
	if i := strings.LastIndex(prompt, "<new_message>\n"); i >= 0 {
		msg = strings.TrimSuffix(strings.TrimSpace(prompt[i+len("<new_message>\n"):]), "</new_message>")
		msg = strings.TrimSpace(msg)
	}
	var m struct {
		From string `json:"from"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(msg), &m); err == nil {
		return m.From + " says: " + m.Text
	}
	return msg
}

func runFakeClaude(version string, polls func(io.Writer) int) {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		// What the dashboard and `fridolin check` ask for.
		if v := os.Getenv("FRIDOLIN_FAKE_VERSION"); v != "" {
			fmt.Println(v)
			return
		}
		fmt.Println(version + " (Claude Code)")
		return
	}
	if slices.Contains(args, "--dangerously-load-development-channels") {
		fakeLive(args)
		return
	}
	// Like the CLI, it reads the prompt from stdin when -p comes last.
	prompt := ""
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			prompt = args[i+1]
		}
	}
	if len(args) > 0 && args[len(args)-1] == "-p" {
		data, _ := io.ReadAll(os.Stdin)
		prompt = string(data)
	}
	if log := os.Getenv("FRIDOLIN_FAKE_PID_LOG"); log != "" {
		// One line a run, so that a test can look for the process later.
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, os.Getpid())
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_PROMPT_LOG"); log != "" {
		// Each run's prompt after a line of its own (fakePromptsIn).
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s\n%s\n", PromptMark, prompt)
			f.Close()
		}
	}
	if os.Getenv("FRIDOLIN_FAKE_CHILD") == "1" {
		// A grandchild that inherits stdout, like a shell command Claude ran.
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		if child.Start() == nil {
			if path := os.Getenv("FRIDOLIN_FAKE_CHILD_PID_LOG"); path != "" {
				_ = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0600)
			}
		}
	}
	if holds := os.Getenv("FRIDOLIN_FAKE_SETSID_CHILD"); holds != "" {
		// A grandchild that leaves the run's process group, as a daemon or
		// setsid does, holding stdout, or stdout and stderr with
		// "stdout,stderr": no kill of the group reaches it. Its PID goes
		// to FRIDOLIN_FAKE_PID_LOG after the CLI's own.
		child := exec.Command("sleep", "30")
		child.SysProcAttr = newSessionAttr()
		child.Stdout = os.Stdout
		if strings.Contains(holds, "stderr") {
			child.Stderr = os.Stderr
		}
		if child.Start() == nil {
			if log := os.Getenv("FRIDOLIN_FAKE_PID_LOG"); log != "" {
				if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					fmt.Fprintln(f, child.Process.Pid)
					f.Close()
				}
			}
		}
	}
	if path := os.Getenv("FRIDOLIN_FAKE_WAIT_FILE"); path != "" && (os.Getenv("FRIDOLIN_FAKE_WAIT_BRAIN") == "" || os.Getenv("FRIDOLIN_FAKE_WAIT_BRAIN") == os.Getenv("FRIDOLIN_BRAIN_ID")) {
		for {
			if _, err := os.Stat(path); err == nil {
				break
			} else if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "waiting for test release:", err)
				os.Exit(1)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if ms, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if n, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_STDERR_BYTES")); n > 0 {
		// A CLI that floods stderr and fails without a result: n bytes of
		// filler, then the line that says what went wrong.
		w := bufio.NewWriter(os.Stderr)
		for i := 0; i < n; i++ {
			_ = w.WriteByte('x')
		}
		fmt.Fprintln(w, "\nfatal: the last thing the CLI said")
		_ = w.Flush()
		os.Exit(1)
	}
	if log := os.Getenv("FRIDOLIN_FAKE_ARGS_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_RUN_LOG"); log != "" {
		// Each run as one JSON line: the arguments, with a prompt that
		// follows -p set apart, the prompt, from there or from stdin, and
		// the working directory.
		wd, _ := os.Getwd()
		argv := slices.Clone(args)
		if i := slices.Index(argv, "-p"); i >= 0 && i+1 < len(argv) {
			argv[i+1] = "<prompt>"
		}
		line, _ := json.Marshal(map[string]any{"args": argv, "prompt": prompt, "dir": wd})
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s\n", line)
			f.Close()
		}
	}
	if log := os.Getenv("FRIDOLIN_FAKE_ENV_LOG"); log != "" {
		// What a brain inherits, one VAR=value per line.
		_ = os.WriteFile(log, []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	}
	msg := NewMessage(prompt)
	wd, _ := os.Getwd()
	out := fmt.Sprintf("echo:%s:%s", wd, msg)
	if steps := os.Getenv("FRIDOLIN_FAKE_MCP"); steps != "" && strings.Contains(msg, FakeMCPTrigger) {
		out += fakeMCP(args, msg, steps)
	}
	if p := os.Getenv("FRIDOLIN_FAKE_READ"); p != "" {
		switch _, err := os.ReadFile(p); {
		case err == nil:
			out += " read=ok"
		case errors.Is(err, fs.ErrPermission):
			out += " read=denied"
		default:
			out += " read=error: " + err.Error()
		}
	}
	if os.Getenv("FRIDOLIN_FAKE_POLLS") == "1" {
		// A tool the run starts, asking the bridge with the run's token.
		var res bytes.Buffer
		code := polls(&res)
		out += fmt.Sprintf(" polls=%d %s", code, strings.Join(strings.Fields(res.String()), " "))
	}
	if name := os.Getenv("FRIDOLIN_FAKE_ENV"); name != "" {
		if v, ok := os.LookupEnv(name); ok {
			out += " " + name + "=" + v
		} else {
			out += " " + name + " unset"
		}
	}
	if ask := os.Getenv("FRIDOLIN_FAKE_ASK_ON"); ask != "" && strings.HasSuffix(msg, ask) {
		// A message ending in it asks the chat a question first.
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Which day suits you?"}]}}]}}`)
		if os.Getenv("FRIDOLIN_FAKE_HANG_AFTER_ASK") == "1" {
			// A run that hangs once it has asked, until it is stopped.
			time.Sleep(30 * time.Second)
			return
		}
		if ms, _ := strconv.Atoi(os.Getenv("FRIDOLIN_FAKE_SLEEP_AFTER_ASK_MS")); ms > 0 {
			// A run that is still at work when the answer comes.
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if ask := os.Getenv("FRIDOLIN_FAKE_ASK"); ask != "" {
		// An AskUserQuestion call, its input as given, which nobody answers.
		fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion","input":%s}]}}`+"\n", ask)
	}
	if result := os.Getenv("FRIDOLIN_FAKE_RESULT"); result != "" {
		fmt.Println(result)
		return
	}
	q := strconv.Quote(out)
	fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"text","text":%s}]}}`+"\n", q)
	fmt.Printf(`{"type":"result","result":%s}`+"\n", q)
}

// fakeClaudeEnv makes brains in this test spawn the fake CLI.
// fakePromptMark starts each prompt in FRIDOLIN_FAKE_PROMPT_LOG.
const PromptMark = "=====PROMPT====="

func isPlaudCommand(c string) bool {
	switch c {
	case "files", "file", "transcript", "summary", "me", "login", "logout":
		return true
	}
	return false
}
