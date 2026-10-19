package toolcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/version"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	method      string
	path        string
}

var siteMCPTools = []mcpTool{
	{
		Name:        "site_context",
		Description: "The site as it is now: its name, domain and roles, the modules that are live (id, route, who may open them), where your workspace is and its branch, your running preview and your open proposal. Call this first.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		method:      "GET", path: "/builder/context",
	},
	{
		Name:        "site_check",
		Description: "Load the workspace's modules the way the site will and report which mount and which would be skipped (with the reason), plus the module.json changes an admin will review before approving (access, server code, files, commands, public files, build commands). Run it after every change.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		method:      "POST", path: "/builder/check",
	},
	{
		Name:        "site_preview",
		Description: "Start (or restart) a preview of the workspace as it is on disk, committed or not, and return its links: 'local' opens it on this machine, 'public' lets a signed-in admin see it on the real domain. Rebuild an app (e.g. npx vite build) before previewing it.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		method:      "POST", path: "/builder/preview",
	},
	{
		Name:        "site_sync",
		Description: "Rebase your workspace's commits onto the newest commit of the site's branch. Use it when site_context or a proposal says the branch moved on. On a conflict nothing changes and you get the details.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		method:      "POST", path: "/builder/sync",
	},
	{
		Name: "site_propose",
		Description: "Propose your workspace's committed work for the live site. Commit first (git add -A && git commit -m …): only commits are proposed. " +
			"An admin reviews and deploys it in Fridolin's dashboard (Site → Proposals); you cannot deploy. Proposing again replaces your open proposal.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string", "description": "One line: what the change does, for the person approving it."},
				"summary": map[string]any{"type": "string", "description": "What changed and why, which modules, and anything the approver should look at: new access, files or server code."},
			},
			"required": []string{"title", "summary"},
		},
		method: "POST", path: "/builder/propose",
	},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeSiteMCP answers MCP requests from in until it closes.
func ServeSiteMCP(in io.Reader, out io.Writer, url, token string) {
	client := &http.Client{Timeout: 15 * time.Minute}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: nothing to answer
		}
		resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fridolin-site", "version": version.String()},
				"instructions":    "Tools for building this Fridolin site's modules in your workspace. Start with site_context; the site-builder skill has the workflow.",
			}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			resp.Result = map[string]any{"tools": siteMCPTools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp.Error = &mcpError{-32602, "invalid params"}
				break
			}
			resp.Result = callSiteTool(client, url, token, p.Name, p.Arguments)
		default:
			resp.Error = &mcpError{-32601, "method not found: " + req.Method}
		}
		_ = enc.Encode(resp)
	}
}

func callSiteTool(client *http.Client, base, token, name string, args json.RawMessage) map[string]any {
	result := func(text string, isErr bool) map[string]any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
	}
	var tool *mcpTool
	for i := range siteMCPTools {
		if siteMCPTools[i].Name == name {
			tool = &siteMCPTools[i]
		}
	}
	if tool == nil {
		return result("unknown tool "+name, true)
	}
	var body io.Reader
	if tool.method == "POST" {
		if len(args) == 0 || string(args) == "null" {
			args = []byte("{}")
		}
		body = bytes.NewReader(args)
	}
	req, err := http.NewRequest(tool.method, strings.TrimSuffix(base, "/")+tool.path, body)
	if err != nil {
		return result(err.Error(), true)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return result("Fridolin is not reachable: "+err.Error(), true)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return result(e.Error, true)
		}
		return result(fmt.Sprintf("Fridolin answered %d: %s", resp.StatusCode, data), true)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		data = pretty.Bytes()
	}
	return result(string(data), false)
}
