package app

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// multipartFiles is an upload body: one part named file per name.
func multipartFiles(t *testing.T, named map[string]string, order ...string) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range order {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(named[name]))
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

// upload posts files to the dashboard's upload call.
func upload(t *testing.T, h *harness, query string, named map[string]string, order ...string) *httptest.ResponseRecorder {
	t.Helper()
	ct, body := multipartFiles(t, named, order...)
	return h.DoRaw("POST", "/api/files/upload?"+query, ct, body.Bytes())
}

func viewSHA(t *testing.T, h *harness, root, path string) string {
	t.Helper()
	var v struct {
		SHA      string `json:"sha"`
		Editable bool   `json:"editable"`
	}
	w := h.Do("GET", "/api/files/view?root="+root+"&path="+path, nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Editable {
		t.Fatalf("view %s: %d %s", path, w.Code, w.Body.String())
	}
	return v.SHA
}

// TestGoldenFilesChanges pins what the changes answer.
func TestGoldenFilesChanges(t *testing.T) {
	h, s, _ := goldenBridge(t)
	dir := filepath.Join(h.home, "main")
	writeBrainFiles(t, dir)
	s = s.maskKeys("mod_time", "at")
	sha := viewSHA(t, h, "brain:main", "docs/plan.txt")
	ct, body := multipartFiles(t, map[string]string{"scan one.pdf": "%PDF-1", "../.env": "x"}, "scan one.pdf", "../.env")
	for _, tc := range []struct {
		name, method, path string
		body               any
		ct                 string
		raw                []byte
	}{
		{name: "text-edit", method: "PUT", path: "/api/files/text", body: map[string]string{"root": "brain:main", "path": "docs/plan.txt", "text": "The new plan.\n", "sha": sha}},
		{name: "text-stale", method: "PUT", path: "/api/files/text", body: map[string]string{"root": "brain:main", "path": "docs/plan.txt", "text": "Lost.\n", "sha": sha}},
		{name: "text-create", method: "PUT", path: "/api/files/text", body: map[string]string{"root": "brain:main", "path": "wiki/people/sam.md", "text": "# Sam\n"}},
		{name: "text-hidden", method: "PUT", path: "/api/files/text", body: map[string]string{"root": "brain:main", "path": ".env", "text": "API_KEY=mine\n", "sha": files.SHA([]byte("API_KEY=secret\n"))}},
		{name: "text-brain-json", method: "PUT", path: "/api/files/text", body: map[string]string{"root": "brain:main", "path": "brain.json", "text": "{}", "sha": "x"}},
		{name: "upload", method: "POST", path: "/api/files/upload?root=brain:main&path=docs", ct: ct, raw: body.Bytes()},
		{name: "upload-excluded", method: "POST", path: "/api/files/upload?root=brain:main&path=node_modules", ct: ct, raw: body.Bytes()},
		{name: "move", method: "POST", path: "/api/files/move", body: map[string]string{"root": "brain:main", "path": "docs/plan.txt", "to": "wiki/plan.txt"}},
		{name: "move-out", method: "POST", path: "/api/files/move", body: map[string]string{"root": "brain:main", "path": "wiki/plan.txt", "to": "../plan.txt"}},
		{name: "delete", method: "POST", path: "/api/files/delete", body: map[string]string{"root": "brain:main", "path": "wiki/plan.txt"}},
		{name: "upload-rule", method: "PUT", path: "/api/files/upload-rule", body: map[string]string{"root": "brain:main", "path": "docs", "mode": "brain"}},
		{name: "history", method: "GET", path: "/api/files/history?root=brain:main&path=wiki/plan.txt"},
		{name: "list-rule", method: "GET", path: "/api/files/list?root=brain:main&path=docs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if tc.raw != nil {
				w = h.DoRaw(tc.method, tc.path, tc.ct, tc.raw)
			} else {
				w = h.Do(tc.method, tc.path, tc.body)
			}
			var mask []string
			switch tc.name {
			case "delete":
				mask = []string{"path"} // where in the trash: the time of day
			case "history":
				mask = []string{"to"}
			}
			assertGolden(t, "api/files-change-"+tc.name+".golden", append([]byte(tc.method+" "+tc.path+"\n"), s.with(mask...).response(t, w)...))
		})
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "docs", "scan one.pdf")); string(got) != "%PDF-1" {
		t.Errorf("the upload holds %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".env")); string(got) != "API_KEY=secret\n" {
		t.Errorf(".env became %q", got)
	}
}

func TestUploadToTheBrainHandsTheFilesOver(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", ReplyChat: "15551234567@s.whatsapp.net",
		Files: &manifest.Files{Uploads: []manifest.UploadFolder{{Folder: "docs", ToBrain: true}}}})
	writeBrainFiles(t, dir)
	w := upload(t, h, "root=brain:main&path=docs", map[string]string{"bill.pdf": "%PDF-bill"}, "bill.pdf")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"to":"brain"`) {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "bill.pdf")); !os.IsNotExist(err) {
		t.Error("a file for the brain landed in the folder")
	}
	inbox := h.b.fileInbox("main")
	if got, _ := os.ReadFile(filepath.Join(inbox, "bill.pdf")); string(got) != "%PDF-bill" {
		t.Errorf("the inbox holds %q", got)
	}
	reply := h.WaitSent("15551234567@s.whatsapp.net", "uploaded on the dashboard")
	if !strings.Contains(reply, "bill.pdf") || !strings.Contains(reply, "docs/") {
		t.Errorf("the brain was told %q", reply)
	}
}

func TestSiteRolesChangeWhatTheirRoleAllows(t *testing.T) {
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", ReplyChat: "15551234567@s.whatsapp.net",
		Files: &manifest.Files{Uploads: []manifest.UploadFolder{{Folder: "docs"}}}})
	writeBrainFiles(t, dir)
	writeBrainFiles(t, h.AddBrain(manifest.Manifest{ID: "other", Name: "Other"}))
	if w := h.Do("PUT", "/api/site/roles", map[string]any{"roles": []string{"member", "reader"},
		"brains": map[string][]string{"member": {"main", "other"}, "reader": {"main"}}, "uploads": map[string][]string{"member": {"main"}}}); w.Code != http.StatusOK {
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
	reader := add(map[string]any{"kind": "password", "username": "kim", "password": "correct horse battery", "role": "reader"})
	url, token, err := h.b.filesBridge()
	if err != nil {
		t.Fatal(err)
	}
	call := func(account, method, path, ct string, body []byte) (int, string) {
		req, _ := http.NewRequest(method, url+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(siteUserHeader, account)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	js := func(v any) []byte { b, _ := json.Marshal(v); return b }
	sha := files.SHA([]byte("The plan.\n"))
	up := func(name string) (string, []byte) {
		ct, body := multipartFiles(t, map[string]string{name: "data"}, name)
		return ct, body.Bytes()
	}
	ct1, b1 := up("member.txt")
	ct2, b2 := up("wiki.md")
	ct3, b3 := up("reader.txt")
	ct4, b4 := up("other.txt")
	for _, c := range []struct {
		name, account, method, path, ct string
		body                            []byte
		want                            int
	}{
		{"a member edits", member, "PUT", "/site-api/files/text", "application/json", js(map[string]string{"root": "brain:main", "path": "docs/plan.txt", "text": "x", "sha": sha}), http.StatusForbidden},
		{"a member deletes", member, "POST", "/site-api/files/delete", "application/json", js(map[string]string{"root": "brain:main", "path": "docs/plan.txt"}), http.StatusForbidden},
		{"a member moves", member, "POST", "/site-api/files/move", "application/json", js(map[string]string{"root": "brain:main", "path": "docs/plan.txt", "to": "plan.txt"}), http.StatusForbidden},
		{"a member uploads into docs", member, "POST", "/site-api/files/upload?root=brain:main&path=docs", ct1, b1, http.StatusOK},
		{"a member uploads into wiki", member, "POST", "/site-api/files/upload?root=brain:main&path=wiki", ct2, b2, http.StatusForbidden},
		{"a reader uploads", reader, "POST", "/site-api/files/upload?root=brain:main&path=docs", ct3, b3, http.StatusForbidden},
		{"a member uploads to another brain", member, "POST", "/site-api/files/upload?root=brain:other&path=docs", ct4, b4, http.StatusForbidden},
		{"a member reads the history", member, "GET", "/site-api/files/history?root=brain:main&path=docs/plan.txt", "", nil, http.StatusNotFound},
		{"an admin edits", admin, "PUT", "/site-api/files/text", "application/json", js(map[string]string{"root": "brain:main", "path": "docs/plan.txt", "text": "Admin's plan.\n", "sha": sha}), http.StatusOK},
		{"an admin reads the history", admin, "GET", "/site-api/files/history?root=brain:main&path=docs/plan.txt", "", nil, http.StatusOK},
		{"an admin edits a media inbox", admin, "PUT", "/site-api/files/text", "application/json", js(map[string]string{"root": "media:main", "path": "x.txt", "text": "x"}), http.StatusNotFound},
		{"an admin, a form body", admin, "POST", "/site-api/files/move", "application/x-www-form-urlencoded", []byte("a=b"), http.StatusUnsupportedMediaType},
		{"a dashboard-only call", admin, "PUT", "/site-api/files/upload-rule", "application/json", js(map[string]string{"root": "brain:main", "path": "docs", "mode": ""}), http.StatusMethodNotAllowed},
	} {
		if code, body := call(c.account, c.method, c.path, c.ct, c.body); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, body, c.want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "docs", "member.txt")); string(got) != "data" {
		t.Errorf("the member's upload holds %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "docs", "plan.txt")); string(got) != "Admin's plan.\n" {
		t.Errorf("plan.txt holds %q", got)
	}
	_, hist := call(admin, "GET", "/site-api/files/history?root=brain:main&path=docs/plan.txt", "", nil)
	if !strings.Contains(hist, `"by":"site:you@example.com"`) || !strings.Contains(hist, `"action":"edit"`) {
		t.Errorf("history: %s", hist)
	}
	var audit []struct{ Kind, By, Detail, Outcome string }
	_ = json.Unmarshal(h.Do("GET", "/api/audit?kind=file_change", nil).Body.Bytes(), &audit)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.By+" "+e.Detail+" "+strings.SplitN(e.Outcome, ":", 2)[0]] = true
	}
	for _, want := range []string{"site:sam upload brain:main docs/member.txt ok", "site:sam edit brain:main docs/plan.txt refused", "site:sam upload brain:main wiki refused"} {
		if !seen[want] {
			t.Errorf("the audit log lacks %q: %v", want, seen)
		}
	}
}

func TestEditsAreCommittedInARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main"})
	writeBrainFiles(t, dir)
	for _, args := range [][]string{{"init", "-q"}, {"add", "wiki"}, {"-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-qm", "start"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	sha := viewSHA(t, h, "brain:main", "wiki/index.md")
	w := h.Do("PUT", "/api/files/text", map[string]string{"root": "brain:main", "path": "wiki/index.md", "text": "# Home again\n", "sha": sha})
	var out struct{ Commit, GitError string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if out.Commit == "" {
		t.Fatalf("not committed: %s", w.Body.String())
	}
	cmd := exec.Command("git", "log", "-1", "--format=%an|%s")
	cmd.Dir = dir
	got, _ := cmd.Output()
	if s := strings.TrimSpace(string(got)); s != "Fridolin|Edit wiki/index.md (Fridolin, by dashboard)" {
		t.Errorf("the commit is %q", s)
	}
	if w := h.Do("POST", "/api/files/move", map[string]string{"root": "brain:main", "path": "wiki/people/alex.md", "to": "wiki/alex.md"}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"commit"`) {
		t.Errorf("move: %d %s", w.Code, w.Body.String())
	}
	cmd = exec.Command("git", "status", "--porcelain", "wiki")
	cmd.Dir = dir
	if st, _ := cmd.Output(); len(st) != 0 {
		t.Errorf("wiki/ is not clean after the move: %s", st)
	}
}
