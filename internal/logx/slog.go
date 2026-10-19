package logx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// textState serializes a complete line across a handler and all its children.
type textState struct {
	mu    sync.Mutex
	out   io.Writer
	attrs bytes.Buffer
}

// TextHandler keeps the bridge's timestamp/message line format. Attributes use
// slog's standard text encoding and are appended without changing legacy lines.
type TextHandler struct {
	state *textState
	attrs slog.Handler
}

func NewTextHandler(out io.Writer, level slog.Leveler) *TextHandler {
	if level == nil {
		level = slog.LevelDebug
	}
	state := &textState{out: out}
	attrs := slog.NewTextHandler(&state.attrs, &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey) {
			return slog.Attr{}
		}
		return a
	}})
	return &TextHandler{state: state, attrs: attrs}
}
func (h *TextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.attrs.Enabled(ctx, level)
}
func (h *TextHandler) Handle(ctx context.Context, r slog.Record) error {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	h.state.attrs.Reset()
	if err := h.attrs.Handle(ctx, r); err != nil {
		return err
	}
	var line strings.Builder
	if !r.Time.IsZero() {
		line.WriteString(r.Time.Format("2006/01/02 15:04:05"))
		line.WriteByte(' ')
	}
	line.WriteString(strings.TrimSuffix(r.Message, "\n"))
	if attrs := strings.TrimSuffix(h.state.attrs.String(), "\n"); attrs != "" {
		line.WriteByte(' ')
		line.WriteString(attrs)
	}
	line.WriteByte('\n')
	_, err := io.WriteString(h.state.out, line.String())
	return err
}
func (h *TextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TextHandler{state: h.state, attrs: h.attrs.WithAttrs(attrs)}
}
func (h *TextHandler) WithGroup(name string) slog.Handler {
	return &TextHandler{state: h.state, attrs: h.attrs.WithGroup(name)}
}

// NewLogger preserves existing output levels. Transport-specific thresholds
// still apply before a message reaches the process-wide handler.
func NewLogger(out io.Writer) *slog.Logger { return slog.New(NewTextHandler(out, nil)) }

// Printf permits embedding owners to supply a logger. A nil logger retains the
// standard log bridge, which slog.SetDefault connects to the CLI's handler.
func Printf(logger *slog.Logger, format string, args ...any) {
	if logger == nil {
		log.Printf(format, args...)
		return
	}
	logger.Info(fmt.Sprintf(format, args...))
}

// Fanout sends a record to each enabled sink. Child handlers retain the same
// grouping/attributes and independent level checks.
func Fanout(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler(append([]slog.Handler(nil), handlers...))
}

type fanoutHandler []slog.Handler

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, sink := range h {
		if sink.Enabled(ctx, level) {
			return true
		}
	}
	return false
}
func (h fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, sink := range h {
		if sink.Enabled(ctx, r.Level) {
			if err := sink.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanoutHandler, len(h))
	for i, sink := range h {
		out[i] = sink.WithAttrs(attrs)
	}
	return out
}
func (h fanoutHandler) WithGroup(name string) slog.Handler {
	out := make(fanoutHandler, len(h))
	for i, sink := range h {
		out[i] = sink.WithGroup(name)
	}
	return out
}

// StandardHandler forwards a scoped embedding logger to the existing standard
// logger. It is a leaf adapter, not a replacement for the process default.
func StandardHandler() slog.Handler { return standardHandler{NewTextHandler(standardWriter{}, nil)} }

type standardHandler struct{ slog.Handler }

func (h standardHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Time = time.Time{}
	return h.Handler.Handle(ctx, r)
}
func (h standardHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return standardHandler{h.Handler.WithAttrs(attrs)}
}
func (h standardHandler) WithGroup(name string) slog.Handler {
	return standardHandler{h.Handler.WithGroup(name)}
}

type standardWriter struct{}

func (standardWriter) Write(p []byte) (int, error) {
	log.Print(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}
