package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// Set is the tools a run's MCP server offers, for callers of type C. A
// tool's Available decides, per caller, whether it has the tool at all;
// one it does not have is neither listed nor callable (mcp.Registry).
// Tools that act on the world register through Guarded, and every call
// writes an audit line.
type Set[C any] struct {
	reg   mcp.Registry[C]
	guard Guard[C]
	audit func(c C, tool, outcome string)
}

// NewSet is a Set without tools. Its guarded tools go through guard, and
// each call goes to audit: the caller, the tool's name and how the call
// ended. The arguments stay out of it: they may hold what is not the
// log's.
func NewSet[C any](guard Guard[C], audit func(c C, tool, outcome string)) *Set[C] {
	return &Set[C]{guard: guard, audit: audit}
}

// Register adds a tool. Register every tool before the first List or
// Call; a name registered twice is a programming error.
func (s *Set[C]) Register(e mcp.Entry[C]) { s.reg.Register(e) }

// Guarded is call behind the Set's guard for a tool of risk (Guarded).
func (s *Set[C]) Guarded(risk skill.Risk, call Call[C]) Call[C] {
	return Guarded(s.guard, risk, call)
}

// List returns the tools c has, in the order registered.
func (s *Set[C]) List(c C) []mcp.Tool { return s.reg.List(c) }

// Call runs tool name for c with args, an empty object when the client
// sent none, and writes the call's audit line. A tool c does not have is
// mcp.ErrUnknownTool, exactly like one that does not exist.
func (s *Set[C]) Call(ctx context.Context, c C, name string, args json.RawMessage) (*mcp.CallResult, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := s.reg.Call(ctx, c, name, args)
	s.audit(c, name, outcome(res, err))
	return res, err
}

// outcome is how a call ended, for its audit line: ok, refused, failed,
// error with the tool's message, or the word an answer about an approval
// or a question starts with.
func outcome(res *mcp.CallResult, err error) string {
	out := "ok"
	switch {
	case errors.Is(err, mcp.ErrUnknownTool):
		out = "refused: not this run's tool"
	case err != nil:
		out = "failed"
	case res != nil && res.IsError:
		out = "error"
		if len(res.Content) > 0 {
			out += ": " + res.Content[0].Text
		}
	case res != nil && len(res.Content) > 0:
		if word, _, _ := strings.Cut(res.Content[0].Text, ":"); slices.Contains([]string{"approved", "denied", "pending", "answered", "expired"}, word) {
			out = word
		}
	}
	return out
}
