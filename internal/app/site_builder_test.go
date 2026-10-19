package app

import (
	"bufio"
	"context"
	"encoding/json"

	"github.com/Icewater1337/fridolin/internal/toolcli"

	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
)

// newTestBuilderSite is a test site whose one brain, "web", has a folder
// and runs without a sandbox (tests cannot nest sandbox-exec).
func newTestBuilderSite(t *testing.T) (*sitesvc.SiteManager, string) {
	t.Helper()
	opts := testSiteOptions(t)
	brainDir := filepath.Join(t.TempDir(), "web-brain")
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts.Builder = sitesvc.SiteBuilderHooks{
		BrainDir: func(id string) string {
			if id == "web" {
				return brainDir
			}
			return ""
		},
		Sandbox:    func(_, bin string, args []string) (string, []string, []string, error) { return bin, args, nil, nil },
		BridgeURL:  func() string { return "http://127.0.0.1:1" },
		Executable: func() (string, error) { return "/usr/bin/true", nil },
	}
	opts.WorkspaceInstall = func(ctx context.Context, brainID, dir string) error { return opts.NPMInstall(ctx, dir, false) }
	return NewSiteManager(opts), brainDir
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

func TestBuilderHandlerNeedsTheBrainsToken(t *testing.T) {
	m, _ := newTestBuilderSite(t)
	b := &App{site: m}
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
			httpapi.WriteJSON(w, 200, map[string]any{"site": map[string]any{"title": "Test"}})
		case "/builder/propose":
			httpapi.WriteErr(w, 400, "commit your work first")
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
	toolcli.ServeSiteMCP(strings.NewReader(in), &out, api.URL, "tok123")

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
