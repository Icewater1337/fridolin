package claudecli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

const DefaultCodexModel = "gpt-6.1-sol"

func AgentName(provider string) string {
	if provider == "codex" {
		return "codex"
	}
	return "claude"
}

// Codex's combined web tool covers both Claude web tools. Turning it off
// honors either restriction. Other Claude tool names have no safe mapping.
func ValidateCodexTools(blocked []string) error {
	for _, tool := range blocked {
		if tool != "WebFetch" && tool != "WebSearch" {
			return fmt.Errorf("the Codex CLI cannot enforce Claude's blocked tools entry %q; use Claude mode while it is configured", tool)
		}
	}
	return nil
}

// codexArgs starts a fresh turn with Fridolin's history on stdin. User
// configuration and ancestor instructions must not bring personal tools,
// hooks or conversations into a brain. The brain prompt is supplied by
// Fridolin, and its files remain available in the working directory.
func CodexArgs(s Options, dir string, externalSandbox bool) []string {
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--color", "never", "--model", cmp.Or(s.Model, DefaultCodexModel),
		"-c", "model_reasoning_effort=" + strconv.Quote(cmp.Or(s.Effort, "high")),
		"-c", "approval_policy=\"never\"",
		"-c", "project_doc_max_bytes=0",
		"-c", "projects." + strconv.Quote(dir) + ".trust_level=\"untrusted\"",
		"-c", "cli_auth_credentials_store=\"file\"",
		"-c", "shell_environment_policy.inherit=\"all\"",
		"-c", "shell_environment_policy.ignore_default_excludes=true",
	}
	if slices.Contains(s.DisallowedTools, "WebFetch") || slices.Contains(s.DisallowedTools, "WebSearch") {
		args = append(args, "-c", "web_search=\"disabled\"")
	}
	if s.Dangerously && externalSandbox {
		// The outer Fridolin sandbox constrains the CLI itself and every
		// child. macOS cannot nest a second sandbox-exec inside it.
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else if s.Dangerously {
		args = append(args, "--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=true",
			"--add-dir", s.StateDir)
		if s.MediaInbox != "" {
			args = append(args, "--add-dir", s.MediaInbox)
		}
	} else {
		args = append(args, "--sandbox", "read-only")
	}
	return append(args, "-")
}

// codexStream adapts Codex JSONL to the run reader's common events. Only a
// completed turn is a reply; progress and a partial answer before a failure
// must never become a successful WhatsApp reply.
type codexStream struct {
	text string
	err  string
}

func (s *codexStream) event(line []byte) streamEvent {
	var e struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &e) != nil {
		return streamEvent{}
	}
	switch e.Type {
	case "item.completed":
		if e.Item.Type == "agent_message" {
			if e.Item.Phase != "commentary" {
				s.text = e.Item.Text
			}
			return streamEvent{Type: "assistant", Message: &streamMsg{Content: []streamContent{{Type: "text", Text: e.Item.Text}}}}
		}
		return streamEvent{Type: "assistant", Message: &streamMsg{Content: []streamContent{{Type: "tool_use", Name: e.Item.Type}}}}
	case "turn.completed":
		return streamEvent{Type: "result", Result: s.text}
	case "error":
		// Connection retries can emit error events and still complete.
		// Keep the diagnostic for a failed/truncated turn, but let the CLI retry.
		s.err = cmp.Or(e.Message, e.Error.Message, "Codex run failed")
	case "turn.failed":
		return streamEvent{Type: "result", IsError: true, Error: cmp.Or(e.Error.Message, e.Message, s.err, "Codex run failed")}
	}
	return streamEvent{}
}
