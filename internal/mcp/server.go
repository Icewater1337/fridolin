package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"sync"
)

// ProtocolVersions are the MCP versions this server speaks, newest first.
// initialize answers with the client's version when it is one of them,
// else with the newest, which the client then accepts or disconnects.
var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// MaxLine is the longest line Serve reads. A longer one is answered with
// a parse error and skipped, never held in memory whole.
const MaxLine = 4 << 20

// Tool is a tool as tools/list describes it.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Content is one part of a tool's result; only text is used here.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallResult is what tools/call answers. IsError marks a tool that ran
// and failed, which the model reads and can recover from, as opposed to
// a protocol error such as an unknown tool.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// TextResult is a successful result of one text.
func TextResult(text string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}}
}

// ErrorResult is a failed result that says why.
func ErrorResult(text string) *CallResult {
	return &CallResult{Content: []Content{{Type: "text", Text: text}}, IsError: true}
}

// ErrUnknownTool is a call of a tool the caller does not have. The client
// hears it as invalid params, as MCP asks.
var ErrUnknownTool = errors.New("unknown tool")

// Handler supplies the tools.
type Handler interface {
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool runs a tool. An error wrapping ErrUnknownTool is a call of
	// a tool there is none of; any other is reported as an internal error.
	// A tool that ran and failed returns an ErrorResult instead.
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error)
}

// Server answers MCP over one stdio connection.
type Server struct {
	Name, Version string
	// Instructions, when set, go to the client with initialize, which may
	// put them in the model's context.
	Instructions string
	Handler      Handler
	// Logf, when set, gets what the server logs; stdout is the protocol's.
	Logf func(format string, args ...any)
}

// Serve reads requests from in and writes their answers to out, one JSON
// message per line, until in ends or ctx is done. Calls run concurrently,
// so a ping is answered while a long tool call runs; a
// notifications/cancelled for a call cancels its context. Once in ends,
// Serve cancels the calls still running and waits for them.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	c := &conn{s: s, out: out, inflight: map[string]context.CancelFunc{}}
	defer c.wg.Wait()
	defer cancel()
	r := bufio.NewReaderSize(in, 64<<10)
	for {
		line, tooLong, err := readLine(r, MaxLine)
		if tooLong {
			c.write(response{Error: &Error{CodeParseError, fmt.Sprintf("parse error: a message is at most %d bytes", MaxLine)}})
		} else if len(bytes.TrimSpace(line)) > 0 {
			c.handle(ctx, line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// readLine reads up to the next newline. A line longer than max is read to
// its end and dropped, with tooLong set. err is io.EOF at the end of the
// input, after the last line.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max+1 { // +1: the newline
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		return line, tooLong, err
	}
}

type conn struct {
	s        *Server
	out      io.Writer
	writeMu  sync.Mutex
	mu       sync.Mutex
	inflight map[string]context.CancelFunc // by the request's id
	wg       sync.WaitGroup
}

func (c *conn) logf(format string, args ...any) {
	if c.s.Logf != nil {
		c.s.Logf(format, args...)
	}
}

func (c *conn) write(r response) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.out.Write(r.encode()); err != nil {
		c.logf("mcp: writing an answer: %v", err)
	}
}

// handle answers one line. Calls run in a goroutine of their own; the
// rest is quick and answered in order.
func (c *conn) handle(ctx context.Context, line []byte) {
	m, perr := Parse(line)
	if perr != nil {
		c.write(response{ID: m.ID, Error: perr})
		return
	}
	switch {
	case m.IsResponse():
		return // the server sends no requests, so there is nothing to match
	case m.IsNotification():
		c.notification(m)
		return
	}
	switch m.Method {
	case "initialize":
		c.write(c.initialize(m))
	case "ping":
		c.write(response{ID: m.ID, Result: struct{}{}})
	case "tools/list":
		tools, err := c.s.Handler.ListTools(ctx)
		if err != nil {
			c.write(response{ID: m.ID, Error: &Error{CodeInternalError, "listing the tools: " + err.Error()}})
			return
		}
		if tools == nil {
			tools = []Tool{}
		}
		c.write(response{ID: m.ID, Result: map[string]any{"tools": tools}})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
			c.write(response{ID: m.ID, Error: &Error{CodeInvalidParams, `invalid params: tools/call takes {"name": "…", "arguments": {…}}`}})
			return
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		} else if p.Arguments[0] != '{' {
			c.write(response{ID: m.ID, Error: &Error{CodeInvalidParams, "invalid params: arguments must be an object"}})
			return
		}
		c.call(ctx, m.ID, p.Name, p.Arguments)
	default:
		c.write(response{ID: m.ID, Error: &Error{CodeMethodNotFound, "method not found: " + m.Method}})
	}
}

func (c *conn) initialize(m Message) response {
	var p struct {
		ProtocolVersion *string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.ProtocolVersion == nil {
		return response{ID: m.ID, Error: &Error{CodeInvalidParams, "invalid params: initialize needs a protocolVersion"}}
	}
	version := ProtocolVersions[0]
	if slices.Contains(ProtocolVersions, *p.ProtocolVersion) {
		version = *p.ProtocolVersion
	}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": c.s.Name, "version": c.s.Version},
	}
	if c.s.Instructions != "" {
		result["instructions"] = c.s.Instructions
	}
	return response{ID: m.ID, Result: result}
}

func (c *conn) notification(m Message) {
	if m.Method != "notifications/cancelled" {
		return // notifications/initialized and the rest need nothing
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil || !validID(p.RequestID) {
		return
	}
	c.mu.Lock()
	cancel := c.inflight[idKey(p.RequestID)]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// idKey makes an id a map key: the string "1" and the number 1 differ.
func idKey(id json.RawMessage) string {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// call runs a tool in a goroutine of its own and answers when it is done.
// A cancelled call is not answered, as MCP asks.
func (c *conn) call(ctx context.Context, id json.RawMessage, name string, args json.RawMessage) {
	ctx, cancel := context.WithCancel(ctx)
	key := idKey(id)
	c.mu.Lock()
	if prev := c.inflight[key]; prev != nil {
		c.mu.Unlock()
		cancel()
		c.write(response{ID: id, Error: &Error{CodeInvalidRequest, "invalid request: a request with this id is still running"}})
		return
	}
	c.inflight[key] = cancel
	c.mu.Unlock()
	c.wg.Go(func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
			cancel()
		}()
		res, err := c.run(ctx, name, args)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrUnknownTool):
			c.write(response{ID: id, Error: &Error{CodeInvalidParams, "unknown tool: " + name}})
		case err != nil:
			c.write(response{ID: id, Error: &Error{CodeInternalError, err.Error()}})
		case res == nil:
			c.write(response{ID: id, Result: TextResult("")})
		default:
			if res.Content == nil {
				res.Content = []Content{}
			}
			c.write(response{ID: id, Result: res})
		}
	})
}

// run calls the handler, turning a panic into an error: one tool's bug
// must not end the connection, and with it every tool of the run.
func (c *conn) run(ctx context.Context, name string, args json.RawMessage) (res *CallResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			c.logf("mcp: tool %s panicked: %v\n%s", name, p, debug.Stack())
			res, err = nil, fmt.Errorf("the tool %s failed", name)
		}
	}()
	return c.s.Handler.CallTool(ctx, name, args)
}
