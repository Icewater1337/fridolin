package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// writeBrainFiles fills the brain folder dir with a small wiki, a memory
// note, documents, and what Files must never show.
func writeBrainFiles(t *testing.T, dir string) {
	t.Helper()
	for name, content := range map[string]string{
		"wiki/index.md":       "# Home\n\nSee [[alex]] and [the plan](../docs/plan.txt).\n\n## Notes\n<script>alert(1)</script>\n",
		"wiki/people/alex.md": "---\ntitle: Alex\n---\nWorks on [[index|the home page]]. Budget 2026.\n",
		"memories/team.md":    "# Team\nAlex leads the budget.\n",
		"docs/plan.txt":       "The plan.\n",
		"docs/photo.png":      "\x89PNG\r\n",
		"node_modules/x.js":   "excluded by the backup\n",
		".env":                "API_KEY=secret\n",
		"id_ed25519":          "not a real key\n", // a private key's name
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGoldenFilesEndpoints pins what the Files page and the wiki read.
func TestGoldenFilesEndpoints(t *testing.T) {
	t.Parallel()
	h, s, _ := goldenBridge(t)
	writeBrainFiles(t, filepath.Join(h.home, "main"))
	s = s.maskKeys("mod_time", "ticket", "expires")
	for _, tc := range []struct {
		name, path string
		// mask lists what depends on the machine: the size of the
		// brain's history, whose lines hold times and paths.
		mask []string
	}{
		{"files-roots", "/api/files/roots", nil},
		{"files-list", "/api/files/list?root=brain:main", []string{"size"}},
		{"files-list-folder", "/api/files/list?root=brain:main&path=docs", nil},
		{"files-list-excluded", "/api/files/list?root=brain:main&path=node_modules", nil},
		{"files-list-outside", "/api/files/list?root=brain:main&path=../", nil},
		{"files-list-unknown-root", "/api/files/list?root=brain:nope", nil},
		{"files-search", "/api/files/search?root=brain:main&q=PLAN", nil},
		{"files-view-markdown", "/api/files/view?root=brain:main&path=wiki/index.md", nil},
		{"files-view-text", "/api/files/view?root=brain:main&path=docs/plan.txt", nil},
		{"files-view-image", "/api/files/view?root=brain:main&path=docs/photo.png", nil},
		{"files-view-secret", "/api/files/view?root=brain:main&path=.env", nil},
		{"files-view-key", "/api/files/view?root=brain:main&path=id_ed25519", nil},
		{"files-ticket", "/api/files/ticket?root=brain:main", nil},
		{"wiki", "/api/wiki", nil},
		{"wiki-pages", "/api/wiki/main/pages", nil},
		{"wiki-page", "/api/wiki/main/page?path=wiki/people/alex.md", nil},
		{"wiki-page-home", "/api/wiki/main/page", nil},
		{"wiki-page-missing", "/api/wiki/main/page?path=wiki/nope.md", nil},
		{"wiki-page-unknown-brain", "/api/wiki/nope/pages", nil},
		{"wiki-search", "/api/wiki/search?q=budget", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.Do("GET", tc.path, nil)
			assertGolden(t, "api/get-"+tc.name+".golden", append([]byte("GET "+tc.path+"\n"), s.with(tc.mask...).response(t, w)...))
		})
	}
}

// fileTicket is a ticket for root, as the page gets it.
func fileTicket(t *testing.T, h *harness, root string) string {
	t.Helper()
	var r struct {
		Ticket string `json:"ticket"`
	}
	if w := h.Do("GET", "/api/files/ticket?root="+root, nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &r) != nil {
		t.Fatalf("ticket: %d %s", w.Code, w.Body.String())
	}
	return r.Ticket
}

func TestFilesRawServesWhatTheTicketOpens(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main"})
	writeBrainFiles(t, dir)
	other := h.AddBrain(manifest.Manifest{ID: "other", Name: "Other"})
	writeBrainFiles(t, other)
	tok := fileTicket(t, h, "brain:main")

	raw := func(q string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765/files/raw?"+q, nil)
		for _, m := range mutate {
			m(r)
		}
		w := httptest.NewRecorder()
		h.h.ServeHTTP(w, r)
		return w
	}
	w := raw("root=brain:main&path=docs/plan.txt&ticket=" + tok)
	if w.Code != http.StatusOK || w.Body.String() != "The plan.\n" {
		t.Fatalf("a file of the root: %d %q", w.Code, w.Body.String())
	}
	if ct, csp := w.Header().Get("Content-Type"), w.Header().Get("Content-Security-Policy"); ct != "text/plain; charset=utf-8" || !strings.Contains(csp, "sandbox") {
		t.Errorf("served as %q under %q", ct, csp)
	}
	if d := w.Header().Get("Content-Disposition"); d != "inline; filename=plan.txt" {
		t.Errorf("disposition %q", d)
	}
	if d := raw("root=brain:main&path=docs/plan.txt&download=1&ticket=" + tok).Header().Get("Content-Disposition"); d != "attachment; filename=plan.txt" {
		t.Errorf("a download: %q", d)
	}
	w = raw("root=brain:main&path=docs/plan.txt&ticket="+tok, func(r *http.Request) { r.Header.Set("Range", "bytes=4-7") })
	if w.Code != http.StatusPartialContent || w.Body.String() != "plan" {
		t.Errorf("a range: %d %q", w.Code, w.Body.String())
	}
	for name, q := range map[string]string{
		"no ticket":             "root=brain:main&path=docs/plan.txt",
		"another root's ticket": "root=brain:other&path=docs/plan.txt&ticket=" + tok,
		"a forged ticket":       "root=brain:main&path=docs/plan.txt&ticket=9999999999.AAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if w := raw(q); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}
	for _, p := range []string{".env", "id_ed25519", "node_modules/x.js", "../other/docs/plan.txt", "/etc/passwd"} {
		if w := raw("root=brain:main&ticket=" + tok + "&path=" + p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(other, "docs", "plan.txt"), filepath.Join(dir, "docs", "link.txt")); err != nil {
			t.Fatal(err)
		}
		if w := raw("root=brain:main&path=docs/link.txt&ticket=" + tok); w.Code != http.StatusNotFound {
			t.Errorf("a symlink out of the root: %d, want 404", w.Code)
		}
	}
	var audit []struct{ Kind, Brain, By, Detail, Outcome string }
	_ = json.Unmarshal(h.Do("GET", "/api/audit?kind=file_view", nil).Body.Bytes(), &audit)
	opened := 0
	for _, e := range audit {
		if e.Detail == "open brain:main docs/plan.txt" && e.By == "dashboard" && e.Brain == "main" && e.Outcome == "ok" {
			opened++
		}
	}
	if opened != 1 {
		t.Errorf("the audit log has %d openings of plan.txt, want 1 (a range request is the same opening): %+v", opened, audit)
	}
}

// siteBridgeRequest asks the site bridge for path as account (none: "").
func siteBridgeRequest(t *testing.T, url, token, account, path string) (int, string) {
	t.Helper()
	return siteBridgeCall(t, "GET", url, token, account, path, "")
}

// siteBridgeCall is a request of method with an empty body of type ct.
func siteBridgeCall(t *testing.T, method, url, token, account, path, ct string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if account != "" {
		req.Header.Set(siteUserHeader, account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestSiteRolesSeeTheirBrainsOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	writeBrainFiles(t, h.AddBrain(manifest.Manifest{ID: "main", Name: "Main"}))
	writeBrainFiles(t, h.AddBrain(manifest.Manifest{ID: "other", Name: "Other"}))
	if w := h.Do("PUT", "/api/site/roles", map[string]any{"roles": []string{"member", "guest"}, "brains": map[string][]string{"member": {"main"}}}); w.Code != http.StatusOK {
		t.Fatalf("roles: %d %s", w.Code, w.Body.String())
	}
	add := func(body map[string]any) string {
		w := h.Do("POST", "/api/site/users", body)
		var u struct{ ID string }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &u) != nil {
			t.Fatalf("add user: %d %s", w.Code, w.Body.String())
		}
		return u.ID
	}
	admin := add(map[string]any{"kind": "google", "email": "you@example.com", "role": "admin"})
	member := add(map[string]any{"kind": "password", "username": "sam", "password": "correct horse battery", "role": "member"})
	guest := add(map[string]any{"kind": "password", "username": "kim", "password": "correct horse battery", "role": "guest"})

	url, token, err := h.b.filesBridge()
	if err != nil {
		t.Fatal(err)
	}
	roots := func(account string) []string {
		code, body := siteBridgeRequest(t, url, token, account, "/site-api/files/roots")
		if code != http.StatusOK {
			t.Fatalf("roots as %s: %d %s", account, code, body)
		}
		var r struct{ Roots []struct{ ID, Path string } }
		_ = json.Unmarshal([]byte(body), &r)
		var ids []string
		for _, x := range r.Roots {
			ids = append(ids, x.ID)
			if x.Path != "" {
				t.Errorf("the site is told where %s is: %s", x.ID, x.Path)
			}
		}
		return ids
	}
	if got := strings.Join(roots(admin), ","); got != "brain:main,brain:other" {
		t.Errorf("admin sees %s", got)
	}
	if got := strings.Join(roots(member), ","); got != "brain:main" {
		t.Errorf("member sees %s", got)
	}
	if got := roots(guest); len(got) != 0 {
		t.Errorf("guest sees %v", got)
	}

	for _, c := range []struct {
		name, token, account, path string
		want                       int
	}{
		{"another brain's folder", token, member, "/site-api/files/list?root=brain:other", http.StatusNotFound},
		{"another brain's wiki", token, member, "/site-api/wiki/other/pages", http.StatusNotFound},
		{"a media inbox", token, admin, "/site-api/files/list?root=media:main", http.StatusNotFound},
		{"the history, as a member", token, member, "/site-api/files/view?root=brain:main&path=conversation-history.jsonl", http.StatusNotFound},
		{"no token", "", member, "/site-api/files/roots", http.StatusUnauthorized},
		{"the dashboard's token", h.token(), member, "/site-api/files/roots", http.StatusUnauthorized},
		{"no account", token, "", "/site-api/files/roots", http.StatusUnauthorized},
		{"an unknown account", token, "u_nobody", "/site-api/files/roots", http.StatusUnauthorized},
		{"a dashboard call", token, admin, "/api/status", http.StatusNotFound},
		{"its own brain", token, member, "/site-api/wiki/main/page?path=wiki/index.md", http.StatusOK},
	} {
		if code, body := siteBridgeRequest(t, url, c.token, c.account, c.path); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, body, c.want)
		}
	}

	// Another Host (DNS rebinding) is turned away.
	req, _ := http.NewRequest("GET", url+"/site-api/files/roots", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(siteUserHeader, admin)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("another host: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	var audit []struct{ Kind, Brain, By, Detail, Outcome string }
	_ = json.Unmarshal(h.Do("GET", "/api/audit?kind=file_view", nil).Body.Bytes(), &audit)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.By+" "+e.Detail+" "+e.Outcome] = true
	}
	for _, want := range []string{"site:sam wiki brain:main wiki/index.md ok", "site:sam view brain:main conversation-history.jsonl refused: no such file or folder: file does not exist"} {
		if !seen[want] {
			t.Errorf("the audit log lacks %q: %v", want, seen)
		}
	}
}
