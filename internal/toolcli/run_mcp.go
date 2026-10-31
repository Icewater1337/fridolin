package toolcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/version"
)

// runHeader names, on the MCP calls, the run fridolin mcp serves
// (httpapi.RunHeader).
const runHeader = "X-Fridolin-Run"

// runMCPInstructions go to the model with initialize.
const runMCPInstructions = "Fridolin's own tools for this run. Which you have depends on your brain and what it is granted, and the bridge decides that: a tool you do not see here is not yours to use."

// RunMCP is `fridolin mcp --run ID`: the MCP server a brain's run starts
// over stdio (the bridge puts it in the run's --mcp-config). It serves the
// tools the bridge gives the run, asking the bridge with the run's own
// token from FRIDOLIN_BRAIN_TOKEN_FILE: the run is who the token says,
// whatever --run claims, which only has to match it.
func RunMCP(ctx context.Context, args []string, stdin io.Reader, t IO) int {
	fs := flag.NewFlagSet("fridolin mcp", flag.ContinueOnError)
	fs.SetOutput(t.Stderr)
	run := fs.String("run", "", "the run whose tools to serve (the bridge starts the server with it)")
	live := fs.Bool("live", false, "serve a live session: also pass the bridge's messages on as Claude Code channel notifications")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *run == "" || fs.NArg() > 0 {
		fmt.Fprintln(t.Stderr, "usage: fridolin mcp --run ID [--live] (a brain's run starts it; it speaks MCP on stdin and stdout)")
		return 2
	}
	client, err := BrainClientFor(t)
	if err != nil {
		fmt.Fprintf(t.Stderr, "fridolin mcp: %v\n", err)
		return 2
	}
	// A delegated task can take minutes; each call ends with its context.
	client.http.Timeout = 0
	srv := &mcp.Server{
		Name: "fridolin", Version: version.String(), Instructions: runMCPInstructions,
		Handler: runTools{client: client, run: *run},
		Logf:    func(format string, a ...any) { fmt.Fprintf(t.Stderr, format+"\n", a...) },
	}
	if *live {
		events := make(chan mcp.Notification)
		srv.Experimental = map[string]any{"claude/channel": map[string]any{}}
		srv.Instructions = liveMCPInstructions
		srv.Events = events
		pumpCtx, stopPump := context.WithCancel(ctx)
		defer stopPump()
		// Only once Claude Code took the server's capabilities: the bridge
		// counts the session's first request for a message as its channel
		// being up.
		srv.Initialized = func() { go pumpLive(pumpCtx, client, events, srv.Logf) }
	}
	if err := srv.Serve(ctx, stdin, t.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(t.Stderr, "fridolin mcp: %v\n", err)
		return 1
	}
	return 0
}

// runTools are the bridge's tools for one run, through its brain API.
type runTools struct {
	client *brainClient
	run    string
}

func (r runTools) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	var out struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if _, err := r.do(ctx, http.MethodGet, "/brain/mcp/tools", nil, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (r runTools) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallResult, error) {
	var out mcp.CallResult
	status, err := r.do(ctx, http.MethodPost, "/brain/mcp/call", map[string]any{"name": name, "arguments": args}, &out)
	switch {
	case status == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", mcp.ErrUnknownTool, name)
	case err != nil:
		return mcp.ErrorResult(err.Error()), nil
	}
	return &out, nil
}

// do sends one request to the bridge with the run's token and decodes the
// answer into out. It returns the status, and the bridge's error message
// as the error for any other than 200.
func (r runTools) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.client.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.client.token)
	req.Header.Set(runHeader, r.run)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the bridge is not reachable at %s: %w", r.client.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, mcp.MaxLine))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("reading the bridge's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = "the bridge answered " + resp.Status
		}
		return resp.StatusCode, errors.New(e.Error)
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}
