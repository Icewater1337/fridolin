package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FakeMCPStep is one thing the fake CLI does with the MCP servers it was
// given (FRIDOLIN_FAKE_MCP): list the tools of server "fridolin" (Tool
// "") or call one, in the runs of Brain only when set.
type FakeMCPStep struct {
	Brain string          `json:"brain,omitempty"`
	Tool  string          `json:"tool,omitempty"`
	Args  json.RawMessage `json:"args,omitempty"`
	// When, if set, plays the step only if what the step before got back
	// starts with it, as a model goes on only once a tool said so.
	When string `json:"when,omitempty"`
	// Write, instead of a tool call, writes the file of that name in the
	// run's folder: the step a skill takes, which the test then finds.
	Write string `json:"write,omitempty"`
}

// FakeApprovalID in a step's Args stands for the last approval_id the
// run saw: in what a step before got back, else in its new message, as
// a model passes on the id a tool or the task gave it.
const FakeApprovalID = "$APPROVAL_ID"

// approvalIDRe finds an approval_id in a tool's answer or a message.
var approvalIDRe = regexp.MustCompile(`approval_id (a[0-9A-Za-z_-]+)`)

// lastApprovalID is the last approval_id in text, or "".
func lastApprovalID(text string) string {
	all := approvalIDRe.FindAllStringSubmatch(text, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1]
}

// FakeMCPTrigger in the new message makes the fake CLI play its
// FRIDOLIN_FAKE_MCP steps; other messages are only echoed.
const FakeMCPTrigger = "#mcp"

// fakeMCP plays steps, a FRIDOLIN_FAKE_MCP JSON list of FakeMCPStep,
// against the MCP server named "fridolin" in the CLI's --mcp-config
// arguments, as Claude Code does: it starts the server's command with its
// env, initializes it and lists or calls tools over its stdio. What each
// step got back is returned as " [brain:tool] result" for the run's
// answer. The server runs this test binary as the bridge's CLI
// (FRIDOLIN_TEST_RUN_MAIN), so it is the real `fridolin mcp`.
func fakeMCP(args []string, msg, steps string) string {
	var plan []FakeMCPStep
	if err := json.Unmarshal([]byte(steps), &plan); err != nil {
		return " [mcp] bad FRIDOLIN_FAKE_MCP: " + err.Error()
	}
	brain := os.Getenv("FRIDOLIN_BRAIN_ID")
	plan = slices.DeleteFunc(plan, func(s FakeMCPStep) bool { return s.Brain != "" && s.Brain != brain })
	if len(plan) == 0 {
		return ""
	}
	server, err := fakeMCPServer(args)
	if err != nil {
		return " [mcp] " + err.Error()
	}
	// Not as the live session's channel server: that one is the fake live
	// CLI's own, which would lose its messages to this one.
	cmd := exec.Command(server.Command, slices.DeleteFunc(slices.Clone(server.Args), func(a string) bool { return a == "--live" })...)
	cmd.Env = append(os.Environ(), "FRIDOLIN_TEST_RUN_MAIN=1")
	for k, v := range server.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return " [mcp] starting the server: " + err.Error()
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	c := &fakeMCPClient{in: stdin, out: bufio.NewScanner(stdout)}
	c.out.Buffer(make([]byte, 1<<20), 8<<20)
	if _, err := c.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fake-claude", "version": "1"}}); err != nil {
		return " [mcp] initialize: " + err.Error()
	}
	c.notify("notifications/initialized")
	var out strings.Builder
	last := ""
	approvalID := lastApprovalID(msg)
	for _, s := range plan {
		if s.When != "" && !strings.HasPrefix(last, s.When) {
			fmt.Fprintf(&out, " [%s:skipped %s%s]", brain, s.Tool, s.Write)
			continue
		}
		if s.Write != "" {
			if err := os.WriteFile(s.Write, []byte("done\n"), 0o600); err != nil {
				fmt.Fprintf(&out, " [%s:write] error %v", brain, err)
				continue
			}
			fmt.Fprintf(&out, " [%s:write] %s", brain, s.Write)
			continue
		}
		label := brain + ":" + s.Tool
		if s.Tool == "" {
			label = brain + ":list"
			res, err := c.request("tools/list", map[string]any{})
			if err != nil {
				fmt.Fprintf(&out, " [%s] error %v", label, err)
				continue
			}
			var list struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			_ = json.Unmarshal(res, &list)
			var names []string
			for _, t := range list.Tools {
				names = append(names, t.Name)
			}
			fmt.Fprintf(&out, " [%s] %s", label, strings.Join(names, ","))
			continue
		}
		params := map[string]any{"name": s.Tool}
		if s.Args != nil {
			params["arguments"] = json.RawMessage(strings.ReplaceAll(string(s.Args), FakeApprovalID, approvalID))
		}
		res, err := c.request("tools/call", params)
		if err != nil {
			fmt.Fprintf(&out, " [%s] error %v", label, err)
			continue
		}
		var call struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		_ = json.Unmarshal(res, &call)
		var text []string
		for _, c := range call.Content {
			text = append(text, c.Text)
		}
		if call.IsError {
			label += " failed"
		}
		last = strings.Join(text, " ")
		if id := lastApprovalID(last); id != "" {
			approvalID = id
		}
		fmt.Fprintf(&out, " [%s] %s", label, last)
	}
	return out.String()
}

type fakeMCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// fakeMCPServer finds server "fridolin" in the --mcp-config arguments,
// each a file or the JSON itself, as the CLI takes them.
func fakeMCPServer(args []string) (fakeMCPServerConfig, error) {
	for i, a := range args {
		if a != "--mcp-config" || i+1 >= len(args) {
			continue
		}
		data := []byte(args[i+1])
		if !strings.HasPrefix(args[i+1], "{") {
			var err error
			if data, err = os.ReadFile(args[i+1]); err != nil {
				continue
			}
		}
		var cfg struct {
			MCPServers map[string]fakeMCPServerConfig `json:"mcpServers"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			if s, ok := cfg.MCPServers["fridolin"]; ok {
				return s, nil
			}
		}
	}
	return fakeMCPServerConfig{}, fmt.Errorf("no MCP server named fridolin in the arguments")
}

type fakeMCPClient struct {
	in  io.Writer
	out *bufio.Scanner
	id  int
}

func (c *fakeMCPClient) notify(method string) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	_, _ = c.in.Write(append(data, '\n'))
}

// request sends a request and reads lines until its answer.
func (c *fakeMCPClient) request(method string, params any) (json.RawMessage, error) {
	c.id++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	for c.out.Scan() {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(c.out.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("the server wrote %q", c.out.Text())
		}
		if r.ID != c.id {
			continue
		}
		if r.Error != nil {
			return nil, fmt.Errorf("%d %s", r.Error.Code, r.Error.Message)
		}
		return r.Result, nil
	}
	return nil, fmt.Errorf("the server closed its output: %v", c.out.Err())
}
