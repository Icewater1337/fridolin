package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain's FrIDE tools (PLAN.md, Workstream E): coding work goes
// to FrIDE on a node, never into a repository the core brain edits itself.
// A core run has them while nodes are on; a sub-brain and a delegated run
// never do.

// frideTools reports whether the run has the FrIDE tools.
func (b *App) frideTools(c mcpCaller) bool { return c.Core() && b.nodesEnabled }

func threadProp() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": "The FrIDE thread's number, as fride_task returned it and FrIDE's messages in the chat name it (FrIDE #3 is 3)."}
}

// registerFrideTools registers them behind M5's policy (tools.Guarded):
// fride_task, fride_reply, fride_move, fride_close and fride_repo_add
// change work or files on a node and so are write, refused in a lockdown
// and to a read budget; fride_status only reads.
func (b *App) registerFrideTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_task",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			desc := "Hand coding work to FrIDE, the coding IDE on one of the owner's machines: anything that changes code (a feature, a fix, a refactor) goes here, never into a repository you edit yourself. FrIDE's orchestrator decides how to do it, may ask back, and reports in this chat; its questions and news arrive here as messages naming the thread (FrIDE #3). Returns the thread number. If no machine can take it now, it waits and starts when one can."
			props := map[string]any{
				"message": tools.Str("What to build or change, in full: the person's request with everything FrIDE needs. FrIDE cannot see this chat."),
				"repos":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The repositories to work in, by folder name, when the person named them."},
				"node":    tools.Str("The machine to run it on: a node's name, id or tag (such as macos or gpu), when the person asked for one; leave it out to let fridolin pick. Not with instance."),
			}
			if list := b.frideInstanceList(); list != "" {
				desc += " Work that belongs to one of the FrIDE instances goes to that instance (instance): each is a FrIDE of its own for one trust domain, which is started when work needs it (medium, unless it runs) and keeps the work until it is closed. Pick it by what the work is for: " + list + ". Work that belongs to none of them goes to a machine's own FrIDE (leave instance out)."
				props["instance"] = tools.Str("The FrIDE instance to run it in, by name, when the work belongs to one: " + list + ".")
			}
			return mcp.Tool{Description: desc, InputSchema: tools.Schema(props, "message")}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideTask),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_status",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "What FrIDE says about a thread now: its status, its task and branch, what it asks, its recent messages. Without a thread, lists every FrIDE thread.",
				InputSchema: tools.Schema(map[string]any{"thread": threadProp()}),
			}
		},
		Call: b.callFrideStatus,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_reply",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Pass the person's answer to FrIDE's question, or more instructions for its work, to a FrIDE thread. Use it when the person answers a FrIDE message in this chat.",
				InputSchema: tools.Schema(map[string]any{"thread": threadProp(), "text": tools.Str("The answer or instruction, in the person's words.")}, "thread", "text"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideReply),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_move",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Move a FrIDE thread's task to another machine: its agents stop, the task travels with its branches, uncommitted work and conversation, and continues there. Only when the person asks for it. Work of a FrIDE instance does not move.",
				InputSchema: tools.Schema(map[string]any{"thread": threadProp(), "node": tools.Str("The machine to move it to: a node's name, id or tag.")}, "thread", "node"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideMove),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_close",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Close a FrIDE thread's task when the person says the work is finished: FrIDE writes its rationale, stops its agents and stack, and its workspace is archived into the vault and removed from the machine. The branch stays in the repositories. Work still waiting for a machine is dropped.",
				InputSchema: tools.Schema(map[string]any{"thread": threadProp()}, "thread"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideClose),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "fride_repo_add",
		Available: b.frideTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Add a repository to FrIDE on a machine, so fride_task can work in it: clone it from url, or with new make an empty one (an initial commit, and by default a private GitHub repository the owner's gh account creates and pushes to). FrIDE knows it at once. Use it when the person wants work in a repository FrIDE does not have, or asks for a new one.",
				InputSchema: tools.Schema(map[string]any{
					"name":     tools.Str("The repository's folder name on the machine, and for new its name on GitHub: letters, digits, '.', '_' and '-'."),
					"url":      tools.Str("The repository to clone: an https:// or ssh:// URL, or git@host:owner/repo.git. Leave it out with new."),
					"new":      map[string]any{"type": "boolean", "description": "Make a new, empty repository instead of cloning one."},
					"github":   map[string]any{"type": "boolean", "description": "With new: also create it on GitHub and push (default true); false keeps it on the machine only."},
					"private":  map[string]any{"type": "boolean", "description": "With new on GitHub: private (default true); false only when the person asked for a public repository."},
					"node":     tools.Str("The machine: a node's name, id or tag, when the person asked for one; leave it out to let fridolin pick."),
					"instance": tools.Str("The FrIDE instance to add it to, by name, for a repository of its trust domain (started first when it does not run); not with node."),
				}, "name"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callFrideRepoAdd),
	})
	b.registerFrideInstanceTools(r)
}

// frideArgs reads a FrIDE tool's arguments.
type frideArgs struct {
	Message  string   `json:"message"`
	Repos    []string `json:"repos"`
	Node     string   `json:"node"`
	Instance string   `json:"instance"`
	Thread   int64    `json:"thread"`
	Text     string   `json:"text"`
}

func readFrideArgs(raw json.RawMessage, needThread bool) (frideArgs, *mcp.CallResult) {
	var a frideArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, mcp.ErrorResult("unreadable arguments: " + err.Error())
	}
	if needThread && a.Thread <= 0 {
		return a, mcp.ErrorResult("thread is missing: the number of the FrIDE thread (FrIDE #3 is 3)")
	}
	a.Node, a.Instance = strings.TrimSpace(a.Node), strings.TrimSpace(a.Instance)
	return a, nil
}

func (b *App) callFrideTask(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, false)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Message) == "" {
		return mcp.ErrorResult("message is empty: say what FrIDE is to build or change"), nil
	}
	// Policy (PLAN.md, M5): fride_task is a write action, checked by
	// tools.Guarded (registerFrideTools) before anything leaves the core: a
	// lockdown or a run whose budget is read gets its error result.
	if a.Node != "" && a.Instance != "" {
		return mcp.ErrorResult("give node or instance, not both: an instance runs where fride.instances lets it"), nil
	}
	chatJID := b.frideChat(c)
	if chatJID == "" {
		return mcp.ErrorResult("this run answers no chat, so FrIDE's questions and news would have nowhere to go"), nil
	}
	t, placed, why, started, err := b.fride.newWork(ctx, chatJID, c.run.Brain, a.Message, a.Repos, a.Node, a.Instance)
	if err != nil {
		if apperr.KindOf(err) == apperr.NotFound {
			return mcp.ErrorResult(err.Error()), nil
		}
		return mcp.ErrorResult("recording the work failed: " + err.Error()), nil
	}
	if !placed && a.Instance != "" {
		return mcp.TextResult(fmt.Sprintf("FrIDE #%d waits: the FrIDE instance %s cannot take it now (%s). The chat has been told, and hears when it starts; you need not repeat it.", t.ID, a.Instance, why)), nil
	}
	if !placed {
		return mcp.TextResult(fmt.Sprintf("FrIDE #%d waits: no machine with FrIDE can take it now. The chat has been told, and hears when it starts; you need not repeat it.", t.ID)), nil
	}
	where := b.fride.where(t.Node, t.Instance)
	note := ""
	if a.Instance != "" && !started.Already {
		note = " " + started.Text()
		if started.less() {
			note += " The chat has been told it got less memory."
		}
	}
	if chat.IsScheduleChat(chatJID) {
		return mcp.TextResult(fmt.Sprintf("Handed to FrIDE on %s as FrIDE #%d (FrIDE's status: %s). Its news does not go to WhatsApp: when FrIDE asks, or the work is done or failed, a run of this agent starts with FrIDE's text, to answer it (fride_reply) or follow up (fride_close).%s", where, t.ID, orDefault(t.FrideStatus, "queued"), note)), nil
	}
	return mcp.TextResult(fmt.Sprintf("Handed to FrIDE on %s as FrIDE #%d (FrIDE's status: %s). Its questions and news will arrive in %s as messages naming FrIDE #%d.%s", where, t.ID, orDefault(t.FrideStatus, "queued"), newsWhere(c, chatJID), t.ID, note)), nil
}

// frideChat is where the news of FrIDE work a run hands over goes: a
// scheduled agent's run keeps it to itself, in its own chat
// (schedule:<agent>), where FrIDE's questions start a run of the agent
// and never reach WhatsApp; any other run, newsChat.
func (b *App) frideChat(c mcpCaller) string {
	if chat.IsScheduleChat(c.run.Chat) && (c.run.Policy == nil || !c.run.Policy.Silent) && b.agents.lookup(c.run.Message.ID) != nil {
		return c.run.Chat
	}
	return b.newsChat(c)
}

// newsWhere names chatJID, where a tool's news go, to the run: "this
// chat" when the run answers it.
func newsWhere(c mcpCaller, chatJID string) string {
	if chatJID == c.run.Chat {
		return "this chat"
	}
	return chatLabel(chatJID)
}

func (b *App) callFrideStatus(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, false)
	if bad != nil {
		return bad, nil
	}
	var out any
	if a.Thread <= 0 {
		all, err := b.store.FrideThreads(ctx, "")
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		list := []map[string]any{}
		for _, t := range all {
			e := map[string]any{"thread": t.ID, "state": t.State, "message": truncateRunes(t.Message, 200)}
			for k, v := range map[string]string{"node": t.Node, "instance": t.Instance, "task": t.TaskID, "branch": t.Branch, "fride_status": t.FrideStatus, "archive": t.Archive} {
				if v != "" {
					e[k] = v
				}
			}
			if t.Node != "" {
				e["node"] = b.fride.nodeName(t.Node)
			}
			list = append(list, e)
		}
		out = map[string]any{"threads": list}
	} else {
		t, err := b.fride.thread(ctx, a.Thread)
		if err != nil {
			return mcp.ErrorResult(err.Error()), nil
		}
		out = b.fride.status(ctx, t)
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return mcp.TextResult(string(data)), nil
}

// repoAddArgs reads fride_repo_add's arguments; GitHub and Private are
// true unless given.
type repoAddArgs struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	New      bool   `json:"new"`
	GitHub   *bool  `json:"github"`
	Private  *bool  `json:"private"`
	Node     string `json:"node"`
	Instance string `json:"instance"`
}

func (b *App) callFrideRepoAdd(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var a repoAddArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.ErrorResult("unreadable arguments: " + err.Error()), nil
	}
	a.Name, a.URL, a.Node, a.Instance = strings.TrimSpace(a.Name), strings.TrimSpace(a.URL), strings.TrimSpace(a.Node), strings.TrimSpace(a.Instance)
	switch {
	case a.Name == "":
		return mcp.ErrorResult("name is missing: the repository's folder name"), nil
	case a.New == (a.URL != ""):
		return mcp.ErrorResult("give either url, to clone a repository, or new, to make one"), nil
	case a.Node != "" && a.Instance != "":
		return mcp.ErrorResult("give node or instance, not both"), nil
	}
	text, err := b.fride.addRepo(ctx, a)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (b *App) callFrideReply(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	if strings.TrimSpace(a.Text) == "" {
		return mcp.ErrorResult("text is empty"), nil
	}
	text, err := b.fride.reply(ctx, a.Thread, a.Text)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

func (b *App) callFrideMove(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	if a.Node == "" {
		return mcp.ErrorResult("node is missing: the machine to move it to"), nil
	}
	text, err := b.fride.move(ctx, a.Thread, a.Node)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}

func (b *App) callFrideClose(ctx context.Context, _ mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	a, bad := readFrideArgs(raw, true)
	if bad != nil {
		return bad, nil
	}
	text, err := b.fride.close(ctx, a.Thread)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	return mcp.TextResult(text), nil
}
