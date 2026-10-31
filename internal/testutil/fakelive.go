package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// fakeLive plays an interactive Claude Code given a channel server
// (--dangerously-load-development-channels), as the bridge's live
// sessions start it: it shows the development-channels dialog and waits
// for Enter, starts the "fridolin" MCP server of its --mcp-config, and
// answers each channel notification as the one-off fake answers a prompt,
// "echo:<dir>:<from> says: <text>" (a check, "READY"), by running the Stop
// hook of its --settings with the answer as last_assistant_message.
//
// FRIDOLIN_FAKE_LIVE_LOG gets one JSON line per start and per message
// (args, content, pid, and the gh login it sees: ghLogin);
// FRIDOLIN_FAKE_LIVE_DEAF=1 makes it ignore every
// notification, as a CLI with channels off would; FRIDOLIN_FAKE_LIVE_EXIT_ON
// makes it exit at a message holding that text, before it answers;
// FRIDOLIN_FAKE_LIVE_HANG_ON makes it sit a minute on a message holding
// that text; FRIDOLIN_FAKE_LIVE_NO_DIALOG=1 never shows the dialog nor waits for the
// key; FRIDOLIN_FAKE_MCP steps play on a message holding #mcp, as in the
// one-off fake.
func fakeLive(args []string) {
	logLine := func(v map[string]any) {
		path := os.Getenv("FRIDOLIN_FAKE_LIVE_LOG")
		if path == "" {
			return
		}
		v["pid"] = os.Getpid()
		line, _ := json.Marshal(v)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s\n", line)
			f.Close()
		}
	}
	logLine(map[string]any{"event": "start", "args": args, "brain": os.Getenv("FRIDOLIN_BRAIN_ID"), "gh_config_dir": os.Getenv("GH_CONFIG_DIR")})
	if os.Getenv("FRIDOLIN_FAKE_LIVE_NO_DIALOG") != "1" {
		fmt.Print("WARNING: Loading development channels\r\n\r\n 1. I am using this for local development\r\n 2. Exit\r\n\r\nEnter to confirm\r\n")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			os.Exit(1)
		}
	}
	server, err := fakeMCPServer(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = append(os.Environ(), "FRIDOLIN_TEST_RUN_MAIN=1")
	for k, v := range server.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		data, _ := json.Marshal(v)
		_, _ = stdin.Write(append(data, '\n'))
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fake-claude", "version": "1"}}})
	hook := stopHook(args)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var m struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Content string            `json:"content"`
				Meta    map[string]string `json:"meta"`
			} `json:"params"`
			Result struct {
				Capabilities struct {
					Experimental map[string]any `json:"experimental"`
				} `json:"capabilities"`
			} `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.ID == 1 {
			if _, ok := m.Result.Capabilities.Experimental["claude/channel"]; !ok {
				fmt.Fprintln(os.Stderr, "the server did not declare claude/channel")
				os.Exit(1)
			}
			send(map[string]any{"method": "notifications/initialized"})
			send(map[string]any{"id": 2, "method": "tools/list", "params": map[string]any{}})
			continue
		}
		if m.Method != "notifications/claude/channel" || os.Getenv("FRIDOLIN_FAKE_LIVE_DEAF") == "1" {
			continue
		}
		content := m.Params.Content
		logLine(map[string]any{"event": "message", "kind": m.Params.Meta["kind"], "turn": m.Params.Meta["turn"], "content": content, "gh": ghLogin()})
		if log := os.Getenv("FRIDOLIN_FAKE_PROMPT_LOG"); log != "" {
			if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintf(f, "%s\n%s\n", PromptMark, content)
				f.Close()
			}
		}
		answer := "READY"
		if m.Params.Meta["kind"] == "message" {
			msg := NewMessage(content)
			if on := os.Getenv("FRIDOLIN_FAKE_LIVE_EXIT_ON"); on != "" && strings.Contains(msg, on) {
				os.Exit(1)
			}
			if on := os.Getenv("FRIDOLIN_FAKE_LIVE_HANG_ON"); on != "" && strings.Contains(msg, on) {
				time.Sleep(time.Minute)
			}
			if ms := os.Getenv("FRIDOLIN_FAKE_SLEEP_MS"); ms != "" {
				d, _ := time.ParseDuration(ms + "ms")
				time.Sleep(d)
			}
			wd, _ := os.Getwd()
			answer = fmt.Sprintf("live%s:%s:%s", m.Params.Meta["turn"], wd, msg)
			if steps := os.Getenv("FRIDOLIN_FAKE_MCP"); steps != "" && strings.Contains(msg, FakeMCPTrigger) {
				answer += fakeMCP(args, msg, steps)
			}
		}
		if hook == "" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "last_assistant_message": answer})
		h := exec.Command("sh", "-c", hook)
		h.Env = append(os.Environ(), "FRIDOLIN_TEST_RUN_MAIN=1")
		h.Stdin = strings.NewReader(string(body))
		h.Stderr = os.Stderr
		if err := h.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "the Stop hook failed:", err)
		}
	}
}

// ghLogin is the token gh would use: the oauth_token in
// $GH_CONFIG_DIR/hosts.yml, "" without one.
func ghLogin() string {
	dir := os.Getenv("GH_CONFIG_DIR")
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if tok, ok := strings.CutPrefix(strings.TrimSpace(line), "oauth_token: "); ok {
			return tok
		}
	}
	return ""
}

// stopHook is the command of the Stop hook in the --settings argument.
func stopHook(args []string) string {
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	var s struct {
		Hooks struct {
			Stop []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	if json.Unmarshal([]byte(args[i+1]), &s) != nil || len(s.Hooks.Stop) == 0 || len(s.Hooks.Stop[0].Hooks) == 0 {
		return ""
	}
	return s.Hooks.Stop[0].Hooks[0].Command
}
