package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// The core brain runs commands on the owner's machines (docs/NODES.md,
// "The core brain runs commands"; docs/VISION.md, step 4): node_exec runs
// one command a node's exec allowlist allows, through the node's exec,
// no shell, and node_exec_rules says what each node allows. Both exist
// on the core brain only, and only while an approved node that runs
// commands is online; a sub-brain and a delegated run never have them.
//
// A command whose every allowing rule has approve waits for the owner's
// yes, each time. Without approval_id, node_exec asks for it as
// request_approval does: an approval in bridge.db and a poll in the
// owner's chat, whose action names exactly the command and the node. It
// waits the same way, runs the command on yes, and answers pending with
// the approval_id when nobody answered in time; the brain waits on with
// check_approval, or hears the answer as a new message, and calls again
// with the approval_id. An approval is good for one run (store.UseApproval)
// of exactly the command, node and folder it asked about. Approvals: auto
// does not answer for a node: the rule is the node owner's word that this
// command asks. A lockdown refuses it all (tools.Guarded) and a yes that
// comes during one.

// execNodeViews are the nodes the core brain may run commands on now:
// approved, online, with an exec rule approved.
func (b *App) execNodeViews() []nodes.View {
	views, err := b.nodes.Views()
	if err != nil {
		log.Printf("node_exec: reading the node registry: %v", err)
	}
	var out []nodes.View
	for _, v := range views {
		if runsCommands(v) && v.Online {
			out = append(out, v)
		}
	}
	return out
}

// runsCommands reports whether v is approved with an exec rule approved.
func runsCommands(v nodes.View) bool {
	return v.Device == nodeproto.DeviceApproved && v.Effective.Allows(nodeproto.MethodExec)
}

func (b *App) execTools(c mcpCaller) bool {
	return c.Core() && b.nodesEnabled && len(b.execNodeViews()) > 0
}

func (b *App) registerNodeExecTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_exec",
		Available: b.execTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Run one command on one of the owner's machines (a node), as that node's exec allowlist allows it: a program and its arguments, no shell, so no pipes, redirects, globs, variables or &&. For quick things: a status, opening an app, a backup script. node_exec_rules lists what each node allows. " +
					"A command whose rule asks the owner each time puts an approval poll in their chat itself and waits for the answer (wait_seconds): never ask them for that yes yourself, in the chat or with request_approval. Unanswered, it answers pending with an approval_id: wait with check_approval, and once that answers approved, call node_exec again with the same node, argv and dir and the approval_id. " +
					"Returns the exit code, stdout and stderr (cut at the node's limit), whether it timed out, and how long it took.",
				InputSchema: tools.Schema(map[string]any{
					"node":            tools.Str("The machine: a node's name or id; leave it out when only one node runs commands."),
					"argv":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": `The program and its arguments, one item each, as a rule of the node allows them: ["brew", "upgrade"].`},
					"dir":             tools.Str("The working directory on the node, an absolute path; leave it out for the node's own."),
					"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": nodeproto.MaxExecTimeoutSeconds, "description": "Stop the command sooner than the node's timeout."},
					"approval_id":     tools.Str("For a command that asks the owner: the approval_id of its approval, once it answered approved. Each one runs exactly that command once."),
					"wait_seconds":    map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxApprovalWait / time.Second), "description": fmt.Sprintf("For a command that asks the owner: how long to wait for the answer (default %d).", int(defaultApprovalWait/time.Second))},
				}, "argv"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callNodeExec),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_exec_rules",
		Available: b.execTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "List the commands node_exec may run on each of the owner's machines that runs commands now, and which of them ask the owner each time. A machine that runs no commands is not listed, however online it is: node_list says which machines are online.",
				InputSchema: tools.Schema(map[string]any{
					"node": tools.Str("One node's name or id; leave it out for every node."),
				}),
			}
		},
		Call: b.callNodeExecRules,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_list",
		Available: func(c mcpCaller) bool { return c.Core() && b.nodesEnabled },
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "List the owner's machines (nodes) as the core sees them now: whether each is online or when it was last seen, what it may do, and its FrIDE instances. Check it before telling the owner a machine is offline: what the chat said earlier may be old.",
				InputSchema: tools.Schema(map[string]any{}),
			}
		},
		Call: b.callNodeList,
	})
}

// callNodeList answers node_list: every node that is not shut out.
func (b *App) callNodeList(context.Context, mcpCaller, json.RawMessage) (*mcp.CallResult, error) {
	views, err := b.nodes.Views()
	if err != nil {
		return mcp.ErrorResult("reading the nodes: " + err.Error()), nil
	}
	return mcp.TextResult(nodeListText(views, time.Now())), nil
}

// nodeListText says, for each node that is not revoked, whether it is
// online (or when it was last seen), what waits for the owner, what it
// may do, and its FrIDE instances.
func nodeListText(views []nodes.View, now time.Time) string {
	var sb strings.Builder
	shown, online := 0, 0
	for _, v := range views {
		if v.Device == nodeproto.DeviceRevoked {
			continue
		}
		shown++
		state := "offline, never seen"
		switch {
		case v.Online:
			state = "online"
			online++
		case v.LastSeen != nil:
			state = "offline, last seen " + now.Sub(*v.LastSeen).Round(time.Minute).String() + " ago"
		}
		fmt.Fprintf(&sb, "- %s (%s, id %s): %s", v.Name, v.OS, v.ID, state)
		switch {
		case v.Device == nodeproto.DevicePending:
			sb.WriteString("; waits for the owner to approve it")
		case v.Commands == nodeproto.CommandsPending:
			sb.WriteString("; asks the owner to approve more than it may do now")
		}
		if can := v.Effective.Plain(); len(can) > 0 {
			sb.WriteString("\n  may: " + strings.Join(can, "; "))
		}
		if h := v.Effective.FrideHost; h != nil {
			var ins []string
			for _, in := range h.Instances {
				ins = append(ins, fmt.Sprintf("%s %s", in.Name, in.State))
			}
			if len(ins) == 0 {
				ins = []string{"none yet"}
			}
			sb.WriteString("\n  FrIDE instances: " + strings.Join(ins, ", "))
			if h.NoLogin != "" {
				sb.WriteString(" (no Claude token of its own)")
			}
		}
		sb.WriteString("\n")
	}
	if shown == 0 {
		return "The core has no nodes."
	}
	return fmt.Sprintf("%d nodes, %d online now:\n%s", shown, online, sb.String())
}

// execNode is the node ref names, by name or id, or, for "", the one node
// that runs commands now.
func (b *App) execNode(ref string) (nodes.View, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		list := b.execNodeViews()
		switch len(list) {
		case 0:
			return nodes.View{}, errors.New("no node runs commands now: none that has exec approved is online")
		case 1:
			return list[0], nil
		}
		names := make([]string, len(list))
		for i, v := range list {
			names[i] = v.Name
		}
		return nodes.View{}, fmt.Errorf("several nodes run commands: name one (%s)", strings.Join(names, ", "))
	}
	views, err := b.nodes.Views()
	if err != nil {
		return nodes.View{}, err
	}
	v, err := nodeNamed(views, ref)
	switch {
	case errors.Is(err, nodes.ErrUnknownNode):
		return v, fmt.Errorf("there is no node %q", ref)
	case err != nil:
		return v, err
	case !runsCommands(v):
		return v, fmt.Errorf("%s runs no commands for the core: its exec is not approved, or it allows none", v.Name)
	case !v.Online:
		return v, fmt.Errorf("%s is offline", v.Name)
	}
	return v, nil
}

// execRulesText says what node v allows, one rule a line, those that ask
// the owner marked.
func execRulesText(v nodes.View) string {
	e := v.Effective.Exec
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%s, id %s), commands stop after %d s:\n", v.Name, v.OS, v.ID, e.Timeout())
	for _, r := range e.Allow {
		sb.WriteString("  " + r.Text())
		if r.Approve {
			sb.WriteString("  [asks the owner each time]")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// execRulesNote says how execRulesText reads.
const execRulesNote = "Each line is a program, then what its arguments may be: patterns (regular expressions, | between them) that every argument must match one of whole, \"(exactly)\" for just those arguments in that order, or none or any. [asks the owner each time]: node_exec asks the owner before each run. A shell, or a program that runs others (sh, bash, env, sudo, osascript, xargs, …), never runs, whatever a rule says."

func (b *App) callNodeExecRules(_ context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"node": "…"}: ` + err.Error()), nil
	}
	list := b.execNodeViews()
	if strings.TrimSpace(args.Node) != "" {
		v, err := b.execNode(args.Node)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		list = []nodes.View{v}
	}
	quiet := b.onlineWithoutCommands(args.Node)
	if len(list) == 0 {
		return mcp.TextResult("No node runs commands now." + quiet), nil
	}
	parts := []string{execRulesNote}
	for _, v := range list {
		parts = append(parts, execRulesText(v))
	}
	return mcp.TextResult(strings.Join(parts, "\n\n") + quiet), nil
}

// onlineWithoutCommands names the nodes that are online but run no
// commands, which node_exec_rules leaves out, so that none of them reads
// as offline; "" when there are none, or when one node was asked for.
func (b *App) onlineWithoutCommands(asked string) string {
	if strings.TrimSpace(asked) != "" {
		return ""
	}
	views, err := b.nodes.Views()
	if err != nil {
		return ""
	}
	var names []string
	for _, v := range views {
		if v.Device == nodeproto.DeviceApproved && v.Online && !runsCommands(v) {
			names = append(names, v.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\n\nOnline, but running no commands for the core: " + strings.Join(names, ", ") + " (node_list says what they do)."
}

type nodeExecArgs struct {
	Node           string   `json:"node"`
	Argv           []string `json:"argv"`
	Dir            string   `json:"dir"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	ApprovalID     string   `json:"approval_id"`
	WaitSeconds    int      `json:"wait_seconds"`
}

// plainArg is an argument a command line shows as it is.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// commandLine is argv as the owner reads it: arguments with spaces or
// other signs a shell would take quoted. Nothing runs it: the node runs
// argv itself.
func commandLine(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if plainArg.MatchString(a) {
			out[i] = a
		} else {
			out[i] = shellQuote(a)
		}
	}
	return strings.Join(out, " ")
}

// maxExecAction is how much of an approval's action the poll shows, in
// bytes (outboundmsg.ApprovalQuestion): a longer command is named in
// short, never cut where the owner cannot see it, and the owner reads it
// whole with "Show me the draft".
const maxExecAction = 200

// execApproval is the action and the details of the approval that asks
// the owner to run argv in dir on node v: what the owner says yes to, and
// what an approval_id must have been asked for.
func execApproval(v nodes.View, argv []string, dir string) (action, details string) {
	line := commandLine(argv)
	in := ""
	if dir != "" {
		in = " in " + dir
	}
	action = fmt.Sprintf("Run `%s`%s on %s", line, in, v.Name)
	if len(action) > maxExecAction {
		prog := argv[0]
		if i := strings.LastIndexAny(prog, `/\`); i >= 0 {
			prog = prog[i+1:]
		}
		action = fmt.Sprintf("Run a long %s command (%d arguments) on %s: Show me the draft has all of it", textx.Truncate(prog, 40), len(argv)-1, textx.Truncate(v.Name, 40))
	}
	argvJSON, _ := json.Marshal(argv)
	details = fmt.Sprintf("On %s (node %s): %s\nargv: %s", v.Name, v.ID, line, argvJSON)
	if dir != "" {
		details += "\nin: " + dir
	}
	details += "\nThe rule on " + v.Name + " that allows it asks you each time."
	return action, details
}

func (b *App) callNodeExec(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args nodeExecArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"node": "…", "argv": ["program", "argument"], "dir": "…", "timeout_seconds": N, "approval_id": "…"}: ` + err.Error()), nil
	}
	if len(args.Argv) == 0 || args.Argv[0] == "" {
		return mcp.ErrorResult(`argv is empty: give the program and its arguments, ["uptime"]`), nil
	}
	if args.TimeoutSeconds < 0 || args.TimeoutSeconds > nodeproto.MaxExecTimeoutSeconds {
		return mcp.ErrorResult(fmt.Sprintf("timeout_seconds is from 1 to %d", nodeproto.MaxExecTimeoutSeconds)), nil
	}
	args.Dir = strings.TrimSpace(args.Dir)
	if args.Dir != "" {
		if err := nodeproto.CheckAbsPath(args.Dir); err != nil {
			return mcp.ErrorResult("dir must be an absolute path on the node: " + err.Error()), nil
		}
	}
	v, err := b.execNode(args.Node)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	if err := v.Effective.PermitsExec(args.Argv); err != nil {
		return mcp.ErrorResult(fmt.Sprintf("%s does not run this: %v. %s\n\n%s", v.Name, err, execRulesNote, execRulesText(v))), nil
	}
	if !v.Effective.ExecNeedsApproval(args.Argv) {
		return b.runNodeExec(ctx, c, v, args, store.Approval{}), nil
	}
	action, details := execApproval(v, args.Argv, args.Dir)
	if id := strings.TrimSpace(args.ApprovalID); id != "" {
		a, err := b.takeExecApproval(ctx, c, id, action, details)
		if err != nil {
			b.auditExec(c, v, args.Argv, a, "refused: "+err.Error(), 0)
			return mcp.ErrorResult(err.Error() + ". Do not run it another way."), nil
		}
		return b.runNodeExec(ctx, c, v, args, a), nil
	}
	if !c.canAsk() {
		return mcp.ErrorResult("this command asks the owner each time, and this run cannot ask anyone: do not run it; say so in your reply"), nil
	}
	n, err := b.store.CountPending(ctx, approvalKind, string(skill.RiskIrreversible))
	if err != nil {
		return nil, err
	}
	if max := b.cfgs.Get().Live.Policy.MaxPendingIrreversible; n >= max {
		return mcp.ErrorResult(fmt.Sprintf("%d irreversible actions wait for approval already, the most policy.max_pending_irreversible allows: wait for those to be answered. Do not run the command.", n)), nil
	}
	a, err := b.ask(ctx, c, store.Approval{Kind: approvalKind, Action: action, Details: details, Risk: string(skill.RiskIrreversible)})
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	wait := approvalArgs{WaitSeconds: args.WaitSeconds}.wait()
	got, res := b.awaitDecision(ctx, a, wait)
	if res != nil {
		b.auditExec(c, v, args.Argv, a, "pending", 0)
		res.Content[0].Text += fmt.Sprintf(" To run it once approved, call node_exec again with the same node, argv and dir and approval_id %s.", a.ID)
		return res, nil
	}
	if got.Status != store.ApprovalApproved {
		b.auditExec(c, v, args.Argv, got, got.Status, 0)
		return answerResult(got), nil
	}
	ok, err := b.store.UseApproval(ctx, got.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		b.auditExec(c, v, args.Argv, got, "refused: the approval was used already", 0)
		return mcp.ErrorResult(fmt.Sprintf("approval %s was used already: call node_exec without approval_id to ask again", got.ID)), nil
	}
	return b.runNodeExec(ctx, c, v, args, got), nil
}

// takeExecApproval takes approval id for one run of the command whose
// approval has action and details: an approved approval of c's brain,
// asked for exactly that, not used before.
func (b *App) takeExecApproval(ctx context.Context, c mcpCaller, id, action, details string) (store.Approval, error) {
	a, err := b.store.ApprovalByID(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows) || err == nil && (a.Brain != c.run.Brain || a.Kind != approvalKind):
		return store.Approval{ID: id}, fmt.Errorf("there is no approval %q of yours", id)
	case err != nil:
		return store.Approval{ID: id}, err
	case a.Action != action || a.Details != details:
		return a, fmt.Errorf("approval %s is for %q, not for this command on this node: call node_exec without approval_id, and it asks the owner for this one", id, a.Action)
	case a.Status == store.ApprovalPending:
		return a, fmt.Errorf("approval %s is still pending: wait for it with check_approval", id)
	case a.Status != store.ApprovalApproved:
		return a, fmt.Errorf("approval %s is %s, not approved", id, a.Status)
	}
	ok, err := b.store.UseApproval(ctx, id)
	if err != nil {
		return a, err
	}
	if !ok {
		return a, fmt.Errorf("approval %s was used already: call node_exec without approval_id to ask again", id)
	}
	return a, nil
}

// runNodeExec runs the command on v; approval is the owner's yes for a
// command that asks, zero for one that does not.
func (b *App) runNodeExec(ctx context.Context, c mcpCaller, v nodes.View, args nodeExecArgs, approval store.Approval) *mcp.CallResult {
	if b.locked() {
		// A yes that came during a lockdown is no yes.
		b.auditExec(c, v, args.Argv, approval, "refused: lockdown", 0)
		return mcp.ErrorResult("the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply.")
	}
	start := time.Now()
	res, err := b.nodes.Exec(ctx, v.ID, nodeproto.ExecParams{Argv: args.Argv, Dir: args.Dir, TimeoutSeconds: args.TimeoutSeconds})
	if err != nil {
		b.auditExec(c, v, args.Argv, approval, "failed: "+err.Error(), time.Since(start).Milliseconds())
		return mcp.ErrorResult(fmt.Sprintf("%s: %v", v.Name, err))
	}
	outcome := fmt.Sprintf("exit %d", res.ExitCode)
	if res.TimedOut {
		outcome = "timed out"
	}
	b.auditExec(c, v, args.Argv, approval, outcome, res.DurationMS)
	out := map[string]any{"node": v.Name, "exit_code": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr, "timed_out": res.TimedOut, "duration_ms": res.DurationMS}
	if res.Truncated {
		out["truncated"] = "the node cut the output at its max_output_bytes"
	}
	if approval.ID != "" {
		out["approval_id"] = approval.ID
	}
	return jsonResult(out)
}

// auditExec writes a node_exec line: the node and the command line (Scrub
// masks what looks like a secret), and for a command that asks, the
// approval and who gave it.
func (b *App) auditExec(c mcpCaller, v nodes.View, argv []string, approval store.Approval, outcome string, ms int64) {
	b.audit.Append(audit.Entry{Kind: audit.NodeExec, Brain: c.run.Brain, Run: c.run.ID, Chat: c.run.Chat, Thread: c.run.Chat,
		ID: approval.ID, By: approval.DecidedBy, Detail: v.Name + ": " + commandLine(argv), Outcome: outcome, DurationMS: ms})
}
