package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func (b *App) callTerminalOpen(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	t, info, err := b.work.openTerminal(ctx, c.run.Brain, c.run.ID, a.Node, a.Folder, 120, 40)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(fmt.Sprintf("Opened terminal #%d on %s in %s (%s, sandbox %s). Type with node_terminal_send, read with node_terminal_read, and close it when done.",
		t.N, b.work.nodeName(t.Node), t.Folder, info.Shell, info.Sandbox)), nil
}

func (b *App) callTerminalSend(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	t, err := b.work.term(a.Terminal)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	text := a.Text
	if a.Enter == nil || *a.Enter {
		text = strings.TrimRight(text, "\r\n") + "\r"
	}
	if len(text) > nodeproto.MaxTerminalInput {
		return mcp.ErrorResult(fmt.Sprintf("text is longer than %d bytes", nodeproto.MaxTerminalInput)), nil
	}
	if _, err := b.work.send(ctx, t, c.run.Brain, text); err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(fmt.Sprintf("Typed into terminal #%d; read its output with node_terminal_read.", t.N)), nil
}

func (b *App) callTerminalRead(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	t, err := b.work.term(a.Terminal)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	wait := 2
	if a.WaitSeconds != nil {
		wait = min(max(*a.WaitSeconds, 0), nodeproto.MaxTerminalWaitMS/1000)
	}
	b.work.mu.Lock()
	at := t.ReadAt
	b.work.mu.Unlock()
	o, err := b.readSettled(ctx, t, at, wait*1000)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	b.work.mu.Lock()
	t.ReadAt = o.Next
	b.work.mu.Unlock()
	if o.Closed {
		b.work.ended(t, c.run.Brain, "exited")
	}
	var sb strings.Builder
	if o.Dropped {
		sb.WriteString("(earlier output is gone)\n")
	}
	sb.WriteString(plainTerminal(o.Data))
	if o.Closed {
		code := "unknown"
		if o.ExitCode != nil {
			code = fmt.Sprint(*o.ExitCode)
		}
		fmt.Fprintf(&sb, "\n(the shell exited, code %s)", code)
	}
	if sb.Len() == 0 {
		return mcp.TextResult("(no new output)"), nil
	}
	return mcp.TextResult(sb.String()), nil
}

func (b *App) callTerminalClose(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	t, err := b.work.term(a.Terminal)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	if _, err := b.work.closeTerm(ctx, t, c.run.Brain, "closed"); err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(fmt.Sprintf("Closed terminal #%d.", t.N)), nil
}

// ansiSeq matches the escape sequences a terminal prints for colours,
// cursor moves and titles.
var ansiSeq = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[()][0-9A-Za-z]|[=>78MDEc])`)

// plainTerminal is terminal output as text for the brain: escape
// sequences and carriage returns out, invalid UTF-8 replaced.
func plainTerminal(data []byte) string {
	s := ansiSeq.ReplaceAllString(strings.ToValidUTF8(string(data), "�"), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	var sb strings.Builder
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// readSettled reads a terminal's output from at, waiting up to waitMS
// for the first of it, then on while more comes within settleMS, so that
// a command's echo and its output come back together; at most
// nodeproto.MaxTerminalRead bytes.
func (b *App) readSettled(ctx context.Context, t *workTerm, at int64, waitMS int) (nodeproto.TerminalOutput, error) {
	const settleMS = 400
	o, err := b.nodes.TerminalRead(ctx, t.Node, nodeproto.TerminalReadParams{ID: t.ID, Offset: at, WaitMS: waitMS})
	if err != nil || len(o.Data) == 0 {
		return o, err
	}
	for !o.Closed && len(o.Data) < nodeproto.MaxTerminalRead {
		more, err := b.nodes.TerminalRead(ctx, t.Node, nodeproto.TerminalReadParams{ID: t.ID, Offset: o.Next, WaitMS: settleMS})
		if err != nil || len(more.Data) == 0 {
			o.Closed, o.ExitCode = more.Closed, more.ExitCode
			break
		}
		o.Data = append(o.Data, more.Data...)
		o.Next, o.Closed, o.ExitCode = more.Next, more.Closed, more.ExitCode
	}
	return o, nil
}
