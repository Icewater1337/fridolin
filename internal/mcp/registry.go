package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Entry is one tool of a Registry, for callers of type C (a run, say).
type Entry[C any] struct {
	Name string
	// Available reports whether caller c has the tool at all. A tool c
	// does not have is neither listed for it nor callable by it, as if
	// there were none. Nil means every caller has it.
	Available func(c C) bool
	// Describe is the tool as c sees it: its description and input
	// schema may name what c may reach, such as the brains it may
	// delegate to. Its name is set from Name.
	Describe func(c C) Tool
	// Call runs the tool for c with the arguments object the client sent.
	Call func(ctx context.Context, c C, args json.RawMessage) (*CallResult, error)
}

// Registry is a set of tools, each with its own availability predicate,
// keyed by name. Register every tool before the first List or Call; the
// registry is not safe for registering concurrently with them.
type Registry[C any] struct {
	order   []string
	entries map[string]Entry[C]
}

// Register adds a tool. A name registered twice is a programming error.
func (r *Registry[C]) Register(e Entry[C]) {
	if e.Name == "" || e.Describe == nil || e.Call == nil {
		panic("mcp: a tool needs a name, Describe and Call")
	}
	if _, dup := r.entries[e.Name]; dup {
		panic(fmt.Sprintf("mcp: tool %q registered twice", e.Name))
	}
	if r.entries == nil {
		r.entries = map[string]Entry[C]{}
	}
	r.entries[e.Name] = e
	r.order = append(r.order, e.Name)
}

// Names lists every registered tool, in the order registered.
func (r *Registry[C]) Names() []string { return append([]string(nil), r.order...) }

func (r *Registry[C]) available(e Entry[C], c C) bool {
	return e.Available == nil || e.Available(c)
}

// List returns the tools c has, in the order registered.
func (r *Registry[C]) List(c C) []Tool {
	tools := []Tool{}
	for _, name := range r.order {
		e := r.entries[name]
		if !r.available(e, c) {
			continue
		}
		t := e.Describe(c)
		t.Name = name
		if len(t.InputSchema) == 0 {
			t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, t)
	}
	return tools
}

// Call runs tool name for c. A tool c does not have is ErrUnknownTool,
// exactly like one that does not exist.
func (r *Registry[C]) Call(ctx context.Context, c C, name string, args json.RawMessage) (*CallResult, error) {
	e, ok := r.entries[name]
	if !ok || !r.available(e, c) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	return e.Call(ctx, c, args)
}

// Bind is the Handler that serves r's tools to one caller.
func (r *Registry[C]) Bind(c C) Handler { return bound[C]{r, c} }

type bound[C any] struct {
	r *Registry[C]
	c C
}

func (b bound[C]) ListTools(context.Context) ([]Tool, error) { return b.r.List(b.c), nil }
func (b bound[C]) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	return b.r.Call(ctx, b.c, name, args)
}
