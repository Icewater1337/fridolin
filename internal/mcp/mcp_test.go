package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// session runs a Server over pipes, as a client's stdio transport would.
type session struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	done chan error
}

func newSession(t *testing.T, h Handler) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	s.out.Buffer(make([]byte, 1<<20), 8<<20)
	srv := &Server{Name: "test", Version: "1", Instructions: "Use the tools.", Handler: h}
	go func() {
		err := srv.Serve(context.Background(), inR, outW)
		outW.Close()
		s.done <- err
	}()
	t.Cleanup(func() { inW.Close() })
	return s
}

func (s *session) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

// recv reads the next message the server wrote.
func (s *session) recv() map[string]any {
	s.t.Helper()
	got := make(chan map[string]any, 1)
	go func() {
		if !s.out.Scan() {
			got <- nil
			return
		}
		var m map[string]any
		if err := json.Unmarshal(s.out.Bytes(), &m); err != nil {
			s.t.Errorf("the server wrote a line that is not JSON: %q", s.out.Text())
		}
		got <- m
	}()
	select {
	case m := <-got:
		if m == nil {
			s.t.Fatal("the server closed its output")
		}
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("no answer from the server")
		return nil
	}
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return 0
	}
	return int(e["code"].(float64))
}

type fakeHandler struct {
	block chan struct{} // "slow" waits for it, or for its context
	mu    sync.Mutex
	ended []string
}

func (f *fakeHandler) ListTools(context.Context) ([]Tool, error) {
	return []Tool{{Name: "echo", Description: "Echoes.", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

func (f *fakeHandler) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	switch name {
	case "echo":
		return TextResult(string(args)), nil
	case "fail":
		return ErrorResult("it failed"), nil
	case "broken":
		return nil, errors.New("the bridge is gone")
	case "panic":
		panic("boom")
	case "slow":
		select {
		case <-f.block:
		case <-ctx.Done():
			f.mu.Lock()
			f.ended = append(f.ended, "cancelled")
			f.mu.Unlock()
			return nil, ctx.Err()
		}
		return TextResult("slow done"), nil
	}
	return nil, ErrUnknownTool
}

func TestServerSpeaksTheProtocol(t *testing.T) {
	s := newSession(t, &fakeHandler{})

	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`)
	m := s.recv()
	res := m["result"].(map[string]any)
	if m["id"] != float64(1) || res["protocolVersion"] != "2025-06-18" || res["instructions"] != "Use the tools." {
		t.Fatalf("initialize: %v", m)
	}
	if res["capabilities"].(map[string]any)["tools"] == nil || res["serverInfo"].(map[string]any)["name"] != "test" {
		t.Fatalf("initialize capabilities: %v", res)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// A version the server does not know gets its newest.
	s.send(`{"jsonrpc":"2.0","id":"again","method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if m := s.recv(); m["id"] != "again" || m["result"].(map[string]any)["protocolVersion"] != ProtocolVersions[0] {
		t.Fatalf("negotiation: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`)
	if m := s.recv(); errCode(m) != CodeInvalidParams {
		t.Fatalf("initialize without a version: %v", m)
	}

	s.send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if m := s.recv(); m["id"] != float64(4) || m["result"] == nil {
		t.Fatalf("ping: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`)
	m = s.recv()
	tools := m["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" || tools[0].(map[string]any)["inputSchema"] == nil {
		t.Fatalf("tools/list: %v", m)
	}

	s.send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`)
	m = s.recv()
	content := m["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != `{"x":1}` || m["result"].(map[string]any)["isError"] != nil {
		t.Fatalf("tools/call echo: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo"}}`)
	if m := s.recv(); m["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "{}" {
		t.Fatalf("tools/call without arguments: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"fail","arguments":{}}}`)
	if m := s.recv(); m["result"].(map[string]any)["isError"] != true {
		t.Fatalf("a failed tool: %v", m)
	}

	for _, tc := range []struct {
		line string
		code int
		id   any
	}{
		{`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"nope"}}`, CodeInvalidParams, float64(10)},
		{`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"arguments":{}}}`, CodeInvalidParams, float64(11)},
		{`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"echo","arguments":[1]}}`, CodeInvalidParams, float64(12)},
		{`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"broken"}}`, CodeInternalError, float64(13)},
		{`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"panic"}}`, CodeInternalError, float64(14)},
		{`{"jsonrpc":"2.0","id":15,"method":"resources/list"}`, CodeMethodNotFound, float64(15)},
		{`{"jsonrpc":"2.0","id":16`, CodeParseError, nil},
		{`[{"jsonrpc":"2.0","id":17,"method":"ping"}]`, CodeInvalidRequest, nil},
		{`{"jsonrpc":"1.0","id":18,"method":"ping"}`, CodeInvalidRequest, float64(18)},
		{`{"jsonrpc":"2.0","id":null,"method":"ping"}`, CodeInvalidRequest, nil},
		{`{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`, CodeInvalidRequest, nil},
		{`{"jsonrpc":"2.0","id":19,"method":7}`, CodeInvalidRequest, float64(19)},
		{`{"jsonrpc":"2.0","id":20,"method":"ping","params":"x"}`, CodeInvalidRequest, float64(20)},
		{`{"jsonrpc":"2.0","id":21}`, CodeInvalidRequest, float64(21)},
	} {
		s.send(tc.line)
		m := s.recv()
		if errCode(m) != tc.code || m["id"] != tc.id {
			t.Errorf("%s: answered %v, want error %d with id %v", tc.line, m, tc.code, tc.id)
		}
	}

	// Responses and unknown notifications get no answer: the ping after
	// them is answered next.
	s.send(`{"jsonrpc":"2.0","id":"x","result":{}}`)
	s.send(`{"jsonrpc":"2.0","method":"notifications/whatever"}`)
	s.send("   ")
	s.send(`{"jsonrpc":"2.0","id":22,"method":"ping"}`)
	if m := s.recv(); m["id"] != float64(22) {
		t.Fatalf("after notifications: %v", m)
	}

	s.in.Close()
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatalf("Serve = %v at the end of the input", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end with its input")
	}
}

// TestServerRunsCallsConcurrently: a slow call does not hold up a ping,
// and notifications/cancelled cancels it without an answer.
func TestServerRunsCallsConcurrently(t *testing.T) {
	h := &fakeHandler{block: make(chan struct{})}
	s := newSession(t, h)
	s.send(`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"slow"}}`)
	s.send(`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"slow"}}`)
	if m := s.recv(); m["id"] != "slow" || errCode(m) != CodeInvalidRequest {
		t.Fatalf("a second call with a running id: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if m := s.recv(); m["id"] != float64(1) {
		t.Fatalf("ping during a slow call: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow","reason":"user"}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow"}}`)
	close(h.block)
	if m := s.recv(); m["id"] != float64(2) || m["result"] == nil {
		t.Fatalf("after the cancel: %v (the cancelled call must not answer)", m)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ended) != 1 {
		t.Fatalf("cancelled calls: %v", h.ended)
	}
}

func TestServerSkipsOverlongLines(t *testing.T) {
	var out bytes.Buffer
	in := strings.Repeat("x", MaxLine+10) + "\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	srv := &Server{Handler: &fakeHandler{}}
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"code":-32700`) || !strings.Contains(lines[1], `"id":1`) {
		t.Fatalf("answers: %q", lines)
	}
}

type caller struct {
	core bool
}

func TestRegistryKeepsUnavailableToolsOut(t *testing.T) {
	var r Registry[caller]
	r.Register(Entry[caller]{
		Name:      "delegate",
		Available: func(c caller) bool { return c.core },
		Describe:  func(caller) Tool { return Tool{Description: "Hands work on."} },
		Call: func(context.Context, caller, json.RawMessage) (*CallResult, error) {
			return TextResult("done"), nil
		},
	})
	r.Register(Entry[caller]{
		Name:     "read_memory",
		Describe: func(c caller) Tool { return Tool{Description: map[bool]string{true: "any", false: "own"}[c.core]} },
		Call: func(context.Context, caller, json.RawMessage) (*CallResult, error) {
			return TextResult("notes"), nil
		},
	})
	names := func(c caller) []string {
		var out []string
		for _, tl := range r.List(c) {
			out = append(out, tl.Name+":"+tl.Description+":"+string(tl.InputSchema))
		}
		return out
	}
	if got := names(caller{core: true}); len(got) != 2 || got[0] != `delegate:Hands work on.:{"type":"object","properties":{}}` || got[1] != "read_memory:any:"+`{"type":"object","properties":{}}` {
		t.Fatalf("core sees %v", got)
	}
	if got := names(caller{}); len(got) != 1 || !strings.HasPrefix(got[0], "read_memory:own") {
		t.Fatalf("sub-brain sees %v", got)
	}
	if _, err := r.Call(context.Background(), caller{}, "delegate", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("calling a tool the caller lacks: %v", err)
	}
	if _, err := r.Call(context.Background(), caller{}, "nope", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("calling a tool that does not exist: %v", err)
	}
	if res, err := r.Bind(caller{core: true}).CallTool(context.Background(), "delegate", nil); err != nil || res.Content[0].Text != "done" {
		t.Fatalf("bound call: %v %v", res, err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a tool registered twice must panic")
		}
	}()
	r.Register(Entry[caller]{Name: "delegate", Describe: func(caller) Tool { return Tool{} }, Call: func(context.Context, caller, json.RawMessage) (*CallResult, error) { return nil, nil }})
}

// A channel server declares its experimental capability and passes the
// events it is given on to the client, unasked, one JSON line each.
func TestServerSendsChannelEvents(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	events := make(chan Notification)
	srv := &Server{Name: "test", Version: "1", Handler: &fakeHandler{}, Experimental: map[string]any{"claude/channel": map[string]any{}}, Events: events}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), inR, outW); outW.Close() }()
	t.Cleanup(func() { inW.Close() })
	out := bufio.NewScanner(outR)

	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n")
	if !out.Scan() {
		t.Fatal("no answer to initialize")
	}
	var init struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &init); err != nil {
		t.Fatal(err)
	}
	if _, ok := init.Result.Capabilities.Experimental["claude/channel"]; !ok {
		t.Errorf("capabilities: %s", out.Text())
	}

	events <- Notification{Method: "notifications/claude/channel", Params: map[string]any{"content": "hi", "meta": map[string]string{"kind": "message"}}}
	if !out.Scan() {
		t.Fatal("no event")
	}
	var ev struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != nil || ev.Method != "notifications/claude/channel" || ev.Params.Content != "hi" || ev.Params.Meta["kind"] != "message" {
		t.Errorf("event: %s", out.Text())
	}

	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end with its input")
	}
}

// Without channel settings the server declares nothing experimental.
func TestServerDeclaresNoChannelByDefault(t *testing.T) {
	s := newSession(t, &fakeHandler{})
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if got := s.recv(); strings.Contains(string(mustJSON(t, got)), "experimental") {
		t.Errorf("initialize: %v", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
