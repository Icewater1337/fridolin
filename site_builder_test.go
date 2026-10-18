package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestBuilderSite is a test site whose one brain, "web", has a folder
// and runs without a sandbox (tests cannot nest sandbox-exec).
func newTestBuilderSite(t *testing.T) (*SiteManager, string) {
	t.Helper()
	m := newTestSite(t)
	brainDir := filepath.Join(t.TempDir(), "web-brain")
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m.hooks = builderHooks{
		brainDir: func(id string) string {
			if id == "web" {
				return brainDir
			}
			return ""
		},
		sandbox:    func(_, bin string, args []string) (string, []string, []string, error) { return bin, args, nil, nil },
		bridgeURL:  func() string { return "http://127.0.0.1:1" },
		executable: func() (string, error) { return "/usr/bin/true", nil },
	}
	m.workspaceInstall = func(ctx context.Context, brainID, dir string) error { return m.npmInstall(ctx, dir, false) }
	return m, brainDir
}

func TestWithArgsBeforePrint(t *testing.T) {
	args := []string{"--output-format", "stream-json", "--mcp-config", "a.json", "-p"}
	got := strings.Join(withArgsBeforePrint(args, []string{"--plugin-dir", "x", "--mcp-config", "b.json"}), " ")
	if got != "--output-format stream-json --mcp-config a.json --plugin-dir x --mcp-config b.json -p" {
		t.Errorf("got %s", got)
	}
	if got := withArgsBeforePrint(args, nil); len(got) != len(args) {
		t.Errorf("nothing to add changed the args: %v", got)
	}
}

func TestSiteBuilderFlow(t *testing.T) {
	if _, err := findNode(); err != nil {
		t.Skip("node not installed")
	}
	if !fileExists(filepath.Join(repoDir, "site", "node_modules")) {
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
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote}); err != nil {
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
		if !fileExists(filepath.Join(base, f)) {
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
	if srv.Args[0] != "mcp-site" || token == "" || m.brainForToken(token) != "web" || m.brainForToken("wrong") != "" {
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
	if !fileExists(filepath.Join(m.modulesDirOf(head), "team", "module.json")) {
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
	if m.brainForToken(token) != "" || m.BuilderArgs("web") != nil || m.previewOf("web") != nil {
		t.Error("a former builder still has access")
	}
	if !fileExists(filepath.Join(ws, "modules", "team")) {
		t.Error("the workspace was removed")
	}
}

func TestBuilderHandlerNeedsTheBrainsToken(t *testing.T) {
	m, _ := newTestBuilderSite(t)
	b := &Bridge{site: m}
	h := b.builderHandler()
	for _, auth := range []string{"", "Bearer nope"} {
		req := httptest.NewRequest("GET", "/builder/context", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%q: %d", auth, rec.Code)
		}
	}
	// A token whose brain is no longer a builder is refused too.
	tok := m.builderToken("web")
	req := httptest.NewRequest("GET", "/builder/context", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a non-builder's token: %d", rec.Code)
	}
}

func TestSiteMCPServer(t *testing.T) {
	var gotAuth, gotBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		gotBody = string(body[:n])
		switch r.URL.Path {
		case "/builder/context":
			writeJSON(w, 200, map[string]any{"site": map[string]any{"title": "Test"}})
		case "/builder/propose":
			writeErr(w, 400, "commit your work first")
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"site_context","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"site_propose","arguments":{"title":"T","summary":"S"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		`not json`,
	}, "\n") + "\n"
	var out strings.Builder
	serveSiteMCP(strings.NewReader(in), &out, api.URL, "tok123")

	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("not JSON: %s", sc.Text())
		}
		resps = append(resps, r)
	}
	if len(resps) != 6 {
		t.Fatalf("%d responses (the notification must get none):\n%s", len(resps), out.String())
	}
	if v := resps[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("protocol version %v", v)
	}
	var names []string
	for _, tool := range resps[1]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "site_context,site_check,site_preview,site_sync,site_propose" {
		t.Errorf("tools = %v", names)
	}
	text := func(i int) (string, bool) {
		res := resps[i]["result"].(map[string]any)
		return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"].(bool)
	}
	if s, isErr := text(2); isErr || !strings.Contains(s, `"title": "Test"`) {
		t.Errorf("site_context = %s %v", s, isErr)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if s, isErr := text(3); !isErr || s != "commit your work first" || !strings.Contains(gotBody, `"title":"T"`) {
		t.Errorf("site_propose = %s %v (body %s)", s, isErr, gotBody)
	}
	if resps[4]["error"].(map[string]any)["code"].(float64) != -32601 || resps[5]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Errorf("errors: %v %v", resps[4], resps[5])
	}
}
