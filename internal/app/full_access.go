package app

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Full access (docs/NODES.md, "Full access"): a brain that
// nodes.full_access_brains names runs any command through node_exec and
// opens terminals without the sandbox, on a node whose owner turned
// full-access on there and approved it. Both keys, or neither: every other
// brain, and every other node, keeps the allowlist and the sandbox. Each
// command and each terminal line still goes into the audit log, marked
// full access; and unless nodes.full_access_skip_approval, one that looks
// destructive or touches secrets (riskyCommand) waits for the owner's yes
// first, against prompt injection from what the brain reads.

// fullAccess reports whether c's brain calls method on v with full access.
func (b *App) fullAccess(c mcpCaller, v nodes.View, method string) bool {
	return b.cfgs.Get().Live.Nodes.FullAccess(c.run.Brain) && v.Effective.FullExec(method)
}

// workNode is node id as the core sees it now.
func (b *App) workNode(id string) (nodes.View, error) {
	views, err := b.nodes.Views()
	if err != nil {
		return nodes.View{}, err
	}
	for _, v := range views {
		if v.ID == id {
			return v, nil
		}
	}
	return nodes.View{}, fmt.Errorf("there is no node %s any more", id)
}

// fullAccessRisk is why a full-access command waits for the owner's yes,
// "" when it need not: the guard is off, or nothing in text looks risky.
func (b *App) fullAccessRisk(v nodes.View, text string) string {
	if !b.cfgs.Get().Live.Nodes.AsksBeforeRisky() {
		return ""
	}
	return riskyCommand(text, grantedRoots(v.Effective))
}

// grantedRoots are the folders node caps grants: deleting inside them is
// the brain's own business.
func grantedRoots(caps nodeproto.Capabilities) []string {
	var out []string
	if caps.FS != nil {
		for _, r := range caps.FS.Roots {
			out = append(out, r.Path)
		}
	}
	for _, w := range []*nodeproto.WorkstationCap{caps.Agent, caps.Terminal} {
		if w != nil {
			out = append(out, w.Roots...)
		}
	}
	return out
}

// protectedPaths are the places whose secrets a full-access command must
// not touch without the owner's yes: the owner's credentials and
// Fridolin's own data (its WhatsApp pairing in bridge.db, its tokens).
var protectedPaths = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])\.[A-Za-z0-9_.-]*secrets?\b`), "a hidden folder of secrets"},
	{regexp.MustCompile(`\.personal-calendar\b`), "~/.personal-calendar"},
	{regexp.MustCompile(`\.ssh(/|\b)`), "~/.ssh"},
	{regexp.MustCompile(`\.gitconfig\b`), "~/.gitconfig"},
	{regexp.MustCompile(`\.git-credentials\b`), "~/.git-credentials"},
	{regexp.MustCompile(`\.config/gh(/|\b)`), "~/.config/gh"},
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])\.[A-Za-z0-9_.-]*-token\b`), "a ~/.*-token file"},
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])\.[A-Za-z0-9_.-]*-api-key\b`), "a ~/.*-api-key file"},
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])\.fridolin(/|\b)`), "~/.fridolin"},
	{regexp.MustCompile(`\.whatsapp-claude-bridge\b`), "~/.whatsapp-claude-bridge"},
	{regexp.MustCompile(`bridge\.db\b`), "bridge.db"},
	{regexp.MustCompile(`Library/Keychains\b`), "the keychains"},
}

// commandSplit separates the commands of a command line: ; | & ( ) `
// and new lines; quotes are dropped, so a script inside sh -c '…' splits
// the same way.
var commandSplit = strings.NewReplacer(";", "\n", "|", "\n", "&", "\n", "(", "\n", ")", "\n", "`", "\n", "$", " ", "'", " ", `"`, " ", "\r", "\n", "{", " ", "}", " ")

// riskyCommand is why text, a command line or argv joined by spaces,
// looks destructive or touches secrets, "" when it does not: sudo and
// the like, deleting recursively or by force, deleting outside roots,
// find -delete, wiping disks, the keychain, and the protected paths. It
// is a tripwire against a brain talked into harm, not a sandbox: a
// command that hides what it does (a variable, base64, a script file) can
// pass it; the audit log still has it.
func riskyCommand(text string, roots []string) string {
	for _, p := range protectedPaths {
		if p.re.MatchString(text) {
			return "it touches " + p.name
		}
	}
	for seg := range strings.SplitSeq(commandSplit.Replace(text), "\n") {
		words := strings.Fields(seg)
		for i, w := range words {
			args := words[i+1:]
			switch name := strings.ToLower(filepath.Base(w)); name {
			case "sudo", "su", "doas", "pkexec":
				return "it runs as another user (" + name + ")"
			case "rm", "rmdir", "unlink", "srm", "shred", "trash":
				if why := riskyDelete(name, args, roots); why != "" {
					return why
				}
			case "find":
				for _, a := range args {
					if a == "-delete" {
						return "find -delete deletes files"
					}
				}
			case "dd", "mkfs", "newfs", "fdisk", "gpt", "wipefs":
				return name + " writes disks"
			case "diskutil":
				for _, a := range args {
					if l := strings.ToLower(a); strings.Contains(l, "erase") || strings.Contains(l, "partition") || strings.Contains(l, "zero") || strings.Contains(l, "reformat") {
						return "diskutil " + a + " wipes a disk"
					}
				}
			case "security":
				return "security reads and changes the keychains"
			default:
				if strings.HasPrefix(name, "mkfs.") {
					return name + " writes disks"
				}
			}
		}
	}
	return ""
}

// riskyDelete is why deleting args with program name is risky: by force or
// recursively, or a path outside roots.
func riskyDelete(name string, args []string, roots []string) string {
	for _, a := range args {
		switch {
		case a == "--":
			continue
		case a == "--recursive" || a == "--force" || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rRf"):
			return fmt.Sprintf("%s %s deletes recursively or by force", name, a)
		case strings.HasPrefix(a, "-"):
		case strings.HasPrefix(a, "~") || strings.Contains(a, ".."):
			return fmt.Sprintf("%s deletes %s, outside the granted folders", name, a)
		case filepath.IsAbs(a) && !onNodeWithin(a, roots):
			return fmt.Sprintf("%s deletes %s, outside the granted folders", name, a)
		}
	}
	return ""
}

// onNodeWithin reports whether path, on a node, lies in one of roots.
func onNodeWithin(path string, roots []string) bool {
	for _, r := range roots {
		if nodeproto.PathWithin(r, path) {
			return true
		}
	}
	return false
}

// askAgain says how a tool's call goes on once its approval answered.
type askAgain struct {
	// tool is the tool and same what the call again repeats: "node_exec",
	// "the same node, argv and dir".
	tool, same string
	// audit records the approval's outcome for the call.
	audit func(a store.Approval, outcome string)
}

// askFirst gets the owner's yes for the step action names, as node_exec's
// approval rules have it: approvalID's approval, asked for exactly action
// and details, used once; or, without one, a new approval poll waited for
// wait seconds. It returns the approval to go ahead with, or the result to
// answer with instead.
func (b *App) askFirst(ctx context.Context, c mcpCaller, action, details, approvalID string, waitSeconds int, again askAgain) (store.Approval, *mcp.CallResult, error) {
	if id := strings.TrimSpace(approvalID); id != "" {
		a, err := b.takeExecApproval(ctx, c, id, action, details, again.tool)
		if err != nil {
			again.audit(a, "refused: "+err.Error())
			return a, mcp.ErrorResult(err.Error() + ". Do not run it another way."), nil
		}
		return a, nil, nil
	}
	if !c.canAsk() {
		return store.Approval{}, mcp.ErrorResult("this command asks the owner each time, and this run cannot ask anyone: do not run it; say so in your reply"), nil
	}
	n, err := b.store.CountPending(ctx, approvalKind, string(skill.RiskIrreversible))
	if err != nil {
		return store.Approval{}, nil, err
	}
	if max := b.cfgs.Get().Live.Policy.MaxPendingIrreversible; n >= max {
		return store.Approval{}, mcp.ErrorResult(fmt.Sprintf("%d irreversible actions wait for approval already, the most policy.max_pending_irreversible allows: wait for those to be answered. Do not run the command.", n)), nil
	}
	a, err := b.ask(ctx, c, store.Approval{Kind: approvalKind, Action: action, Details: details, Risk: string(skill.RiskIrreversible)})
	if err != nil {
		return a, mcp.ErrorResult(err.Error()), nil
	}
	got, res := b.awaitDecision(ctx, a, approvalArgs{WaitSeconds: waitSeconds}.wait())
	if res != nil {
		again.audit(a, "pending")
		res.Content[0].Text += fmt.Sprintf(" To run it once approved, call %s again with %s and approval_id %s.", again.tool, again.same, a.ID)
		return a, res, nil
	}
	if got.Status != store.ApprovalApproved {
		again.audit(got, got.Status)
		return got, answerResult(got), nil
	}
	ok, err := b.store.UseApproval(ctx, got.ID)
	if err != nil {
		return got, nil, err
	}
	if !ok {
		again.audit(got, "refused: the approval was used already")
		return got, mcp.ErrorResult(fmt.Sprintf("approval %s was used already: call %s without approval_id to ask again", got.ID, again.tool)), nil
	}
	return got, nil, nil
}
