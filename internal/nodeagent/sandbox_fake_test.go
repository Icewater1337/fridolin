package nodeagent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// fakeSbx stands in for sbx when FAKE_SBX_DIR is set: it records each
// call in calls.log (its arguments, and whether the Claude token came in
// its environment), keeps its sandboxes in sandboxes.json, files "in the
// VM" under vm/, and reads canned answers and failures from files in the
// dir (fail-<subcommand>, settings, policy, codex).
func fakeSbx(dir string) int {
	args := os.Args[1:]
	tok := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	rec := map[string]any{"args": args, "token_env": tok != "", "telemetry_off": os.Getenv("SBX_NO_TELEMETRY") == "1"}
	line, _ := json.Marshal(rec)
	appendFile(filepath.Join(dir, "calls.log"), string(line)+"\n")
	if len(args) == 0 {
		return 2
	}
	if b, err := os.ReadFile(filepath.Join(dir, "fail-"+args[0])); err == nil {
		fmt.Fprint(os.Stderr, string(b))
		return 1
	}
	names := fakeSandboxes(dir)
	switch args[0] {
	case "version":
		fmt.Println("sbx version v0.50.0 (darwin/arm64)")
	case "ls":
		rows := []map[string]string{}
		for _, n := range names {
			rows = append(rows, map[string]string{"name": n, "status": "running", "agent": "claude"})
		}
		b, _ := json.Marshal(rows)
		fmt.Println(string(b))
	case "settings":
		fmt.Print(cannedOr(dir, "settings", "ssh.agentForwardingEnabled  false\nbrowser.enabled  false\n"))
	case "policy":
		if len(args) > 1 && args[1] == "ls" {
			fmt.Print(cannedOr(dir, "policy", "Global policy: deny-all\n"))
		}
	case "secret":
		return fakeSecret(dir, args[1:])
	case "create":
		i := slices.Index(args, "--name")
		saveFakeSandboxes(dir, append(names, args[i+1]))
	case "rm":
		name := args[len(args)-1]
		saveFakeSandboxes(dir, slices.DeleteFunc(names, func(n string) bool { return n == name }))
	case "exec":
		return fakeExec(dir, args[1:], tok)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return 2
	}
	return 0
}

func sleepMS(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

func appendFile(path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString(s)
		_ = f.Close()
	}
}

func cannedOr(dir, name, def string) string {
	if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
		return string(b)
	}
	return def
}

func fakeSandboxes(dir string) []string {
	var names []string
	b, _ := os.ReadFile(filepath.Join(dir, "sandboxes.json"))
	_ = json.Unmarshal(b, &names)
	return names
}

func saveFakeSandboxes(dir string, names []string) {
	b, _ := json.Marshal(names)
	_ = os.WriteFile(filepath.Join(dir, "sandboxes.json"), b, 0o600)
}

// fakeSecret is `sbx secret`: ls lists the openai login when the file
// codex is there (with part of a value, as sbx does); set-custom reads
// its --command's file as sandboxd would, into secret-value.
func fakeSecret(dir string, args []string) int {
	switch args[0] {
	case "ls":
		if _, err := os.Stat(filepath.Join(dir, "codex")); err == nil {
			fmt.Println(`[{"service":"openai","scope":"global","value":"oai-…f00d"}]`)
		} else {
			fmt.Println(`[]`)
		}
	case "set-custom":
		cmd := args[slices.Index(args, "--command")+1]
		file, ok := strings.CutPrefix(cmd, "/bin/cat ")
		if !ok {
			return 2
		}
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = os.WriteFile(filepath.Join(dir, "secret-value"), b, 0o600)
		fmt.Println("Applied secret to sandbox")
	case "rm":
		fmt.Println("Removed secret sbx-r…cdef") // part of a value: never logged
	}
	return 0
}

// vmPath is where a file of the VM lives in the fake.
func vmPath(dir, p string) string { return filepath.Join(dir, "vm", filepath.Base(p)) }

// fakeExec is `sbx exec [-i] [-w DIR] [-e NAME[=V]]… NAME CMD…`, with the
// commands the node runs inside: writing the token file, removing files,
// signalling a run's process group, and a run under setsid, whose
// program is fake-agent.
func fakeExec(dir string, args []string, tok string) int {
	var env []string
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-i":
			args = args[1:]
		case "-w", "-u":
			args = args[2:]
		case "-e":
			env = append(env, args[1])
			args = args[2:]
		default:
			return 125
		}
	}
	if len(args) < 2 || !slices.Contains(fakeSandboxes(dir), args[0]) {
		fmt.Fprintln(os.Stderr, "no such sandbox")
		return 125
	}
	cmd := args[1:]
	_ = os.MkdirAll(filepath.Join(dir, "vm"), 0o700)
	switch {
	case cmd[0] == "sh" && strings.Contains(cmd[2], `cat > "$0"`):
		b, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(vmPath(dir, cmd[3]), b, 0o600)
	case cmd[0] == "sh" && strings.HasPrefix(cmd[2], "kill -"):
		sig := strings.Fields(strings.TrimPrefix(cmd[2], "kill -"))[0]
		appendFile(vmPath(dir, cmd[3])+".signal", sig+"\n")
	case cmd[0] == "rm":
		for _, f := range cmd[2:] {
			_ = os.Remove(vmPath(dir, f))
		}
	case cmd[0] == "setsid":
		// setsid -w sh -c SCRIPT PIDFILE PROGRAM ARGS…
		pidFile := cmd[5]
		_ = os.WriteFile(vmPath(dir, pidFile), []byte("42"), 0o600)
		return fakeAgentRun(dir, pidFile, cmd[6:], env, tok)
	default:
		fmt.Fprintf(os.Stderr, "the fake runs no %q\n", cmd[0])
		return 127
	}
	return 0
}

// fakeAgentRun is a run's program: "fake-agent echo CODE" prints its
// stdin, its argv and environment and exits CODE; "fake-agent wait
// [ignore-term]" prints "started" and ends on the signal sbx exec
// delivers into the VM; "fake-agent flood" writes past the node's
// buffer first, then waits.
func fakeAgentRun(dir, pidFile string, argv, env []string, tok string) int {
	// Its mode words, without the -c options a Codex run gets.
	full := argv
	var words []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-c" {
			i++
			continue
		}
		words = append(words, argv[i])
	}
	if len(words) < 2 || words[0] != "fake-agent" {
		return 127
	}
	argv = words
	switch argv[1] {
	case "echo":
		in, _ := io.ReadAll(os.Stdin)
		fmt.Printf("prompt: %s\n", in)
		fmt.Printf("argv: %s\n", strings.Join(full, " "))
		fmt.Printf("env: %s\n", strings.Join(env, " "))
		fmt.Printf("token: %v\n", tok != "" && slices.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN"))
		fmt.Print("grüße\n")
		fmt.Fprintln(os.Stderr, "a warning")
		code := 0
		fmt.Sscan(argv[2], &code)
		return code
	case "flood":
		chunk := strings.Repeat("x", 1<<20)
		for range 17 {
			fmt.Print(chunk)
		}
	}
	fmt.Println("started")
	sig := vmPath(dir, pidFile) + ".signal"
	for range 3000 {
		b, _ := os.ReadFile(sig)
		s := string(b)
		switch {
		case strings.Contains(s, "KILL"):
			return 137
		case strings.Contains(s, "TERM") && !slices.Contains(argv, "ignore-term"):
			return 143
		}
		sleepMS(10)
	}
	return 99
}
