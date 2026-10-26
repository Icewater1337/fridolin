package app

import (
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain's workstation tools (docs/NODES.md, "Workstation";
// #159): an agent session or a terminal in a folder a node granted. A
// core run has the agent tools only while some node has agent approved,
// the terminal tools only while some node has terminal approved; a
// sub-brain and a delegated run never do. Starting, answering and typing
// are write (M5: refused in a lockdown and to a read budget); reading
// and stopping are not.

func (b *App) agentTools(c mcpCaller) bool {
	return c.Core() && b.nodesEnabled && b.work.anyWorkNode("agent")
}

func (b *App) terminalTools(c mcpCaller) bool {
	return c.Core() && b.nodesEnabled && b.work.anyWorkNode("terminal")
}

func sessionProp() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": "The session's number, as node_agent_run returned it and its messages in the chat name it (Agent #2 is 2)."}
}

func termProp() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": "The terminal's number, as node_terminal_open returned it."}
}

func nodeProp() map[string]any {
	return tools.Str("The machine: a node's name, id or tag; leave it out when only one node offers this.")
}

func folderProp() map[string]any {
	return tools.Str("The folder on the node: an absolute path under a folder the node granted, or a path relative to its first granted folder; leave it out for that folder.")
}

func (b *App) registerWorkstationTools(r *tools.Set[mcpCaller]) {
	b.registerAgentTools(r)
	b.registerTerminalTools(r)
}

func (b *App) registerAgentTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_agent_run",
		Available: b.agentTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Start a Claude Code (or Codex) session in a folder on one of the owner's machines, to just do something there: look into a build, fix a small thing, tidy a folder. Use FrIDE (fride_task) instead for real coding work in a repository (a feature, a refactor, a branch to review). The session runs with the node's own login and permission rules; its questions, the steps it needs approved, and its end with what changed arrive in this chat as messages naming the session (Agent #2). Returns the session's number.",
				InputSchema: tools.Schema(map[string]any{
					"prompt":         tools.Str("What the session is to do, in full: it cannot see this chat."),
					"node":           nodeProp(),
					"folder":         folderProp(),
					"provider":       map[string]any{"type": "string", "enum": []string{"claude", "codex"}, "description": "claude or codex; leave it out for the node's first."},
					"model":          tools.Str("The provider's model, only when the person named one."),
					"use_core_login": map[string]any{"type": "boolean", "description": "Lend the core's Claude login to this one session instead of the node's own. Only when the person explicitly asked for it for this session; default false."},
				}, "prompt"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callAgentRun),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_agent_status",
		Available: b.agentTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "What an agent session is doing: its state, what it asks, its recent events, and once done its summary and the files it changed. Without a session, lists the sessions.",
				InputSchema: tools.Schema(map[string]any{"session": sessionProp()}),
			}
		},
		Call: b.callAgentStatus,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_agent_reply",
		Available: b.agentTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Pass the person's answer to an agent session's question, or more instructions once it is done. Steps that need approval are answered by the person's vote on the approval poll, never here.",
				InputSchema: tools.Schema(map[string]any{"session": sessionProp(), "text": tools.Str("The answer or instruction, in the person's words.")}, "session", "text"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callAgentReply),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_agent_stop",
		Available: b.agentTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Stop an agent session; returns what it changed so far.",
				InputSchema: tools.Schema(map[string]any{"session": sessionProp()}, "session"),
			}
		},
		Call: b.callAgentStop,
	})
}

func (b *App) registerTerminalTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_terminal_open",
		Available: b.terminalTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Open a shell in a folder on one of the owner's machines, for commands a node's exec allowlist does not cover. Every command line typed is in the audit log. Returns the terminal's number; close it when done.",
				InputSchema: tools.Schema(map[string]any{"node": nodeProp(), "folder": folderProp()}),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callTerminalOpen),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_terminal_send",
		Available: b.terminalTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Type into a terminal. A command needs its Enter: end it with a newline (the default adds one). Then read its output with node_terminal_read.",
				InputSchema: tools.Schema(map[string]any{
					"terminal": termProp(),
					"text":     tools.Str("What to type."),
					"enter":    map[string]any{"type": "boolean", "description": "Press Enter after text (default true)."},
				}, "terminal", "text"),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callTerminalSend),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_terminal_read",
		Available: b.terminalTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Read what a terminal printed since the last read, waiting up to wait_seconds for output when there is none yet.",
				InputSchema: tools.Schema(map[string]any{
					"terminal":     termProp(),
					"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "description": "How long to wait for output (default 2)."},
				}, "terminal"),
			}
		},
		Call: b.callTerminalRead,
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "node_terminal_close",
		Available: b.terminalTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Close a terminal: its shell and what it started end.",
				InputSchema: tools.Schema(map[string]any{"terminal": termProp()}, "terminal"),
			}
		},
		Call: b.callTerminalClose,
	})
}
