package main

import (
	"context"
	"encoding/json"
	"fmt"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
	"net/http"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

func TestSiteBuilderFlow(t *testing.T) {
	if _, err := findNode(); err != nil {
		t.Skip("node not installed")
	}
	if !fsx.Exists(filepath.Join(repoDir, "site", "node_modules")) {
		t.Skip("run npm install in site/ first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, brainDir := newTestBuilderSite(t)
	m.Start(ctx)
	defer m.Stop()

	remote := newBareRepo(t)
	if err := m.SetBuilder(ctx, "web", true); err == nil {
		t.Fatal("a builder was allowed before a repository was connected")
	}
	if err := m.ConnectRepo(ctx, sitesvc.SiteRepo{URL: remote}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	up, _ := m.Fetch(ctx)
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)

	if err := m.SetBuilder(ctx, "nobody", true); err == nil {
		t.Error("a brain that does not exist was allowed")
	}
	if got := m.BuilderArgs("web"); got != nil {
		t.Errorf("a brain that is not a builder gets %v", got)
	}
	if err := m.SetBuilder(ctx, "web", true); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(brainDir, ".fridolin-site")
	for _, f := range []string{"plugin/.claude-plugin/plugin.json", "plugin/skills/site-builder/SKILL.md", "mcp.json", "shell/server/check.ts", "workspace/modules/welcome/module.json"} {
		if !fsx.Exists(filepath.Join(base, f)) {
			t.Errorf("%s is missing", f)
		}
	}
	args := strings.Join(m.BuilderArgs("web"), " ")
	if !strings.Contains(args, "--plugin-dir "+filepath.Join(base, "plugin")) || !strings.Contains(args, "--mcp-config "+filepath.Join(base, "mcp.json")) {
		t.Errorf("builder args = %s", args)
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(base, "mcp.json"))
	json.Unmarshal(data, &mcp)
	srv := mcp.MCPServers["fridolin-site"]
	token := srv.Env["FRIDOLIN_BUILDER_TOKEN"]
	if srv.Args[0] != "mcp-site" || token == "" || m.BrainForToken(token) != "web" || m.BrainForToken("wrong") != "" {
		t.Fatalf("mcp.json = %s", data)
	}
	codexArgs, codexEnv, codexPrompt, err := m.CodexBuilder("web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(codexArgs, " "), "mcp_servers.fridolin-site.command=") || strings.Contains(strings.Join(codexArgs, " "), token) {
		t.Fatal("Codex must receive the MCP server but no token in argv")
	}
	if !strings.Contains(strings.Join(codexEnv, "\n"), "FRIDOLIN_BUILDER_TOKEN="+token) || !strings.Contains(codexPrompt, "site-builder") {
		t.Fatal("Codex builder is missing its tools or instructions")
	}
	ws := filepath.Join(base, "workspace")
	if branch := gitT(t, ws, "rev-parse", "--abbrev-ref", "HEAD"); branch != "builder/web" {
		t.Errorf("workspace branch = %s", branch)
	}

	bc := m.BuilderContext("web")
	live := bc["live"].(map[string]any)["modules"].([]map[string]any)
	if len(live) != 1 || live[0]["id"] != "welcome" || bc["workspace"].(map[string]any)["path"] != ws {
		t.Errorf("context = %+v", bc)
	}

	// The brain adds a page with a small API, and a broken module.
	note := filepath.Join(t.TempDir(), "note.txt")
	os.WriteFile(note, []byte("from a declared file"), 0o644)
	team := filepath.Join(ws, "modules", "team")
	os.MkdirAll(filepath.Join(team, "app"), 0o755)
	os.WriteFile(filepath.Join(team, "app", "index.html"), []byte("<!doctype html><html><head></head><body>team</body></html>"), 0o644)
	manifest, _ := json.Marshal(map[string]any{
		"version": 1, "id": "team", "title": "Team", "route": "/team",
		"access": map[string]any{"roles": []string{"admin", "member"}},
		"app":    map[string]any{"dist": "app", "base": "inject"},
		"server": map[string]any{"entry": "server.ts"},
		"capabilities": map[string]any{"files": map[string]any{
			"note": map[string]any{"path": note, "access": "read"},
		}},
	})
	os.WriteFile(filepath.Join(team, "module.json"), manifest, 0o644)
	os.WriteFile(filepath.Join(team, "server.ts"), []byte(`import express from 'express'
import { readFileSync } from 'node:fs'
export default (ctx) => express.Router().get('/note', (_req, res) => res.json({ note: readFileSync(ctx.file('note'), 'utf8') }))
`), 0o644)
	os.MkdirAll(filepath.Join(ws, "modules", "oops"), 0o755)
	os.WriteFile(filepath.Join(ws, "modules", "oops", "module.json"), []byte(`{"version": 1, "id": "oops", "title": "Oops", "route": "/oops", "app": {"dist": "x", "base": "built"}}`), 0o644)

	res, err := m.BuilderCheck(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Mounted, ",") != "team,welcome" || len(res.Broken) != 1 || res.Broken[0].ID != "oops" || !strings.Contains(res.Broken[0].Error, "access") {
		t.Fatalf("check = %+v", res)
	}
	var reviewed []string
	for _, c := range res.Review {
		reviewed = append(reviewed, c.ID+":"+c.Status)
	}
	if strings.Join(reviewed, ",") != "oops:added,team:added" {
		t.Errorf("review = %v", reviewed)
	}

	// The preview knows no accounts: only its token opens it.
	urls, err := m.BuilderPreview(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	p := m.previewOf("web")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(urls["local"])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) != 1 {
		t.Fatalf("preview login: %d %v", resp.StatusCode, resp.Cookies())
	}
	if r, _ := http.Get("http://" + p.Listen + "/api/modules"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("the preview answers without its token: %d", r.StatusCode)
	}
	// Its home page, from inside the brain's .fridolin-site folder.
	if r, _ := http.Get("http://" + p.Listen + "/"); r.StatusCode != http.StatusOK {
		t.Errorf("the preview's home page: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", "http://"+p.Listen+"/team/api/note", nil)
	req.AddCookie(resp.Cookies()[0])
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if got["note"] != "from a declared file" {
		t.Errorf("preview API = %v", got)
	}
	previews, _ := os.ReadFile(m.dir("previews.json"))
	if !strings.Contains(string(previews), p.Token) {
		t.Errorf("previews.json does not list the preview: %s", previews)
	}

	// Proposing needs a commit; then it is pushed as builder/web.
	if _, _, err := m.BuilderPropose(ctx, "web", "Add the team page", "x"); err == nil {
		t.Error("an uncommitted workspace was proposed")
	}
	os.RemoveAll(filepath.Join(ws, "modules", "oops"))
	gitT(t, ws, "add", "-A")
	gitT(t, ws, "commit", "-q", "-m", "Add the team page")
	prop, changes, err := m.BuilderPropose(ctx, "web", "Add the team page", "A page for the team, reading one note file.")
	if err != nil {
		t.Fatal(err)
	}
	if prop.Branch != "builder/web" || len(changes) != 1 || changes[0].ID != "team" {
		t.Fatalf("proposal %+v changes %+v", prop, changes)
	}
	if got := gitT(t, t.TempDir(), "--git-dir", remote, "rev-parse", "builder/web"); got != prop.Commit {
		t.Errorf("remote builder/web = %s, want %s", got, prop.Commit)
	}
	if list := m.Proposals(ctx); len(list) != 1 {
		t.Errorf("proposals = %v", list)
	}

	// Merging pushes the branch and deploys it.
	if err := m.MergeProposal(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	head := gitT(t, t.TempDir(), "--git-dir", remote, "rev-parse", "main")
	if m.Settings().Approved != head || len(m.Settings().Proposals) != 0 {
		t.Errorf("after merging: approved %s, main %s, proposals %v", m.Settings().Approved, head, m.Settings().Proposals)
	}
	if !fsx.Exists(filepath.Join(m.modulesDirOf(head), "team", "module.json")) {
		t.Error("the merged module is not deployed")
	}

	// Someone else changes main; the workspace syncs onto it.
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", "-q", remote, other)
	os.WriteFile(filepath.Join(other, "CLAUDE.md"), []byte("# Conventions\n\n- Pages in English.\n"), 0o644)
	gitT(t, other, "commit", "-q", "-am", "Say the pages are in English")
	gitT(t, other, "push", "-q", "origin", "main")
	if _, err := m.BuilderSync(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if log := gitT(t, ws, "log", "--format=%s", "-3"); !strings.Contains(log, "Say the pages are in English") {
		t.Errorf("workspace after sync:\n%s", log)
	}

	// Turning the builder off revokes the token and keeps the workspace.
	if err := m.SetBuilder(ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	if m.BrainForToken(token) != "" || m.BuilderArgs("web") != nil || m.previewOf("web") != nil {
		t.Error("a former builder still has access")
	}
	if !fsx.Exists(filepath.Join(ws, "modules", "team")) {
		t.Error("the workspace was removed")
	}
}

// TestSiteBuilderCommandsLeadASessionOfTheirOwn: the commands the site
// builder runs as a brain lead a session of their own, without a terminal,
// as brain runs do (platform_test.go checks those): one that shared the
// session of a bridge started in your terminal could push keystrokes into
// your shell. They used to share the bridge's process group too, so a
// timeout left what the command started running.
func TestSiteBuilderCommandsLeadASessionOfTheirOwn(t *testing.T) {
	m, dir := newTestBuilderSite(t)
	m.hooks.Sandbox = func(_, bin string, args []string) (string, []string, []string, error) {
		return bin, args, []string{"FRIDOLIN_FAKE_SESSION=1"}, nil
	}
	out, err := m.sandboxed(context.Background(), "web", dir, 30*time.Second, os.Args[0])
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var pid, sid int
	if _, err := fmt.Sscanf(out, "pid=%d sid=%d", &pid, &sid); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if sid != pid {
		t.Errorf("the command ran in session %d, want one of its own (%d)", sid, pid)
	}
}

// A known run token is still refused when its brain is not a builder.
func TestBuilderTokenRequiresActiveBuilder(t *testing.T) {
	m, _ := newTestBuilderSite(t)
	tok := m.builderToken("web")
	if got := m.BrainForToken(tok); got != "" {
		t.Errorf("a non-builder's token resolved to %q", got)
	}
}
