package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
func fakeMCP(args []string, steps string) string {
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
	cmd := exec.Command(server.Command, server.Args...)
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
	for _, s := range plan {
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
			params["arguments"] = s.Args
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
		fmt.Fprintf(&out, " [%s] %s", label, strings.Join(text, " "))
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
