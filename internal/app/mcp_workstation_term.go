package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

func (b *App) callTerminalOpen(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readWorkArgs(raw)
	if bad != nil {
		return bad, nil
	}
	trusted := func(v nodes.View) bool { return b.fullAccess(c, v, nodeproto.MethodTerminalOpen) }
	t, info, err := b.work.openTerminal(ctx, c.run.Brain, c.run.ID, a.Node, a.Folder, 120, 40, trusted)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	full := ""
	if t.Full {
		full = ", full access: any command, the owner asked first for one that looks destructive or touches secrets"
	}
	return mcp.TextResult(fmt.Sprintf("Opened terminal #%d on %s in %s (%s, sandbox %s%s). Type with node_terminal_send, read with node_terminal_read, and close it when done.",
		t.N, b.work.nodeName(t.Node), t.Folder, info.Shell, info.Sandbox, full)), nil
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
	var approval store.Approval
	if t.Full {
		var res *mcp.CallResult
		if approval, res, err = b.guardTerminal(ctx, c, t, text, a); res != nil || err != nil {
			return res, err
		}
	}
	if _, err := b.work.send(ctx, t, c.run.Brain, text); err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	if approval.ID != "" {
		return mcp.TextResult(fmt.Sprintf("The owner approved it (%s). Typed into terminal #%d; read its output with node_terminal_read.", approval.ID, t.N)), nil
	}
	return mcp.TextResult(fmt.Sprintf("Typed into terminal #%d; read its output with node_terminal_read.", t.N)), nil
}

// guardTerminal asks the owner before text goes into full-access terminal
// t when the command line it ends, with what was typed before it, looks
// destructive or touches secrets (fullAccessRisk). A zero approval and no
// result: it need not ask.
func (b *App) guardTerminal(ctx context.Context, c mcpCaller, t *workTerm, text string, a workArgs) (store.Approval, *mcp.CallResult, error) {
	v, err := b.workNode(t.Node)
	if err != nil {
		return store.Approval{}, mcp.ErrorResult(err.Error()), nil
	}
	// The lines text ends, as the shell gets them; nothing runs before
	// its Enter.
	b.work.mu.Lock()
	ahead := workTerm{line: slices.Clone(t.line), escape: t.escape}
	b.work.mu.Unlock()
	lines := ahead.feed(text)
	why := b.fullAccessRisk(v, strings.Join(lines, "\n"))
	if why == "" {
		return store.Approval{}, nil, nil
	}
	shown := audit.Scrub(strings.Join(lines, "; "))
	action := fmt.Sprintf("Type `%s` into terminal #%d on %s", shown, t.N, v.Name)
	if len(action) > maxExecAction {
		action = fmt.Sprintf("Type a long command line into terminal #%d on %s: Show me the draft has all of it", t.N, textx.Truncate(v.Name, 40))
	}
	details := fmt.Sprintf("Terminal #%d on %s (node %s), full access, in %s:\n%s\nFull access asks you first: %s.", t.N, v.Name, v.ID, t.Folder, shown, why)
	wait := 0
	if a.WaitSeconds != nil {
		wait = *a.WaitSeconds
	}
	return b.askFirst(ctx, c, action, details, a.ApprovalID, wait, askAgain{
		tool: "node_terminal_send", same: "the same terminal and text",
		audit: func(ap store.Approval, outcome string) {
			b.audit.Append(audit.Entry{Kind: audit.NodeCommand, Brain: c.run.Brain, Run: c.run.ID, By: ap.DecidedBy, ID: fmt.Sprintf("terminal#%d", t.N),
				Detail: v.Name + " [full access]: " + shown, Outcome: "approval " + ap.ID + ": " + outcome})
		},
	})
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
