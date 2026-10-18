package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ingestCalls records what the site handed to brains.
type ingestCalls struct {
	mu    sync.Mutex
	calls []string // "brain|sender|text|file,file"
}

func (c *ingestCalls) hook(brainID, sender, text string, files []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	c.calls = append(c.calls, strings.Join([]string{brainID, sender, text, strings.Join(names, ",")}, "|"))
	return nil
}

func (c *ingestCalls) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.calls...)
}

// spoolEntry writes a request the way ctx.ingest does.
func spoolEntry(t *testing.T, m *SiteManager, id string, req spoolRequest, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(m.spoolDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "files", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func spoolAnswer(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("no answer in %s: %v", dir, err)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	return out
}

func TestSiteSpoolHandoff(t *testing.T) {
	m := newTestSite(t)
	media := t.TempDir()
	var got ingestCalls
	m.hooks.ingest, m.hooks.mediaDir = got.hook, func() string { return media }

	// A live commit whose "inbox" module may hand files to brain web.
	commit := strings.Repeat("a", 40)
	m.settings.Approved = commit
	mods := m.modulesDirOf(commit)
	for id, caps := range map[string]map[string]any{
		"inbox": {"ingest": []string{"web"}},
		"other": {},
	} {
		os.MkdirAll(filepath.Join(mods, id), 0o700)
		data, _ := json.Marshal(map[string]any{"version": 1, "id": id, "capabilities": caps})
		os.WriteFile(filepath.Join(mods, id, "module.json"), data, 0o600)
	}

	ok := spoolEntry(t, m, strings.Repeat("1", 24), spoolRequest{Module: "inbox", Brain: "web", Files: []string{"Brief ü.pdf"}, Text: "filed", Sender: "sam"}, map[string]string{"Brief ü.pdf": "pdf"})
	wrongBrain := spoolEntry(t, m, strings.Repeat("2", 24), spoolRequest{Module: "inbox", Brain: "private", Files: []string{"a.txt"}}, map[string]string{"a.txt": "a"})
	notDeclared := spoolEntry(t, m, strings.Repeat("3", 24), spoolRequest{Module: "other", Brain: "web", Files: []string{"b.txt"}}, map[string]string{"b.txt": "b"})
	notLive := spoolEntry(t, m, strings.Repeat("4", 24), spoolRequest{Module: "ghost", Brain: "web", Text: "hi"}, nil)
	badName := spoolEntry(t, m, strings.Repeat("5", 24), spoolRequest{Module: "inbox", Brain: "web", Files: []string{"../escape.txt"}}, nil)
	link := spoolEntry(t, m, strings.Repeat("6", 24), spoolRequest{Module: "inbox", Brain: "web", Files: []string{"link"}}, nil)
	os.Symlink("/etc/hosts", filepath.Join(link, "files", "link"))
	// Not a folder ctx.ingest makes: left alone.
	stray := filepath.Join(m.spoolDir(), "not-ours")
	os.MkdirAll(stray, 0o700)
	os.WriteFile(filepath.Join(stray, "request.json"), []byte(`{"module":"inbox","brain":"web","text":"x"}`), 0o600)

	m.processSpool()

	if a := spoolAnswer(t, ok); a["ok"] != true {
		t.Fatalf("the declared handoff was refused: %v", a)
	}
	if calls := got.list(); len(calls) != 1 || calls[0] != "web|sam|filed|Brief ü.pdf" {
		t.Fatalf("brains got %v", calls)
	}
	if data, err := os.ReadFile(filepath.Join(media, "Brief ü.pdf")); err != nil || string(data) != "pdf" {
		t.Errorf("the file did not reach the media folder: %v", err)
	}
	for dir, want := range map[string]string{
		wrongBrain:  "does not list brain",
		notDeclared: "does not list brain",
		notLive:     "is not live",
		badName:     "not a plain file name",
		link:        "not a regular file",
	} {
		a := spoolAnswer(t, dir)
		if a["ok"] != false || !strings.Contains(a["error"].(string), want) {
			t.Errorf("%s: %v, want an error with %q", filepath.Base(dir), a, want)
		}
	}
	if fileExists(filepath.Join(stray, "result.json")) {
		t.Error("a folder ctx.ingest did not make was answered")
	}
	// A refused file stays where it was.
	if !fileExists(filepath.Join(wrongBrain, "files", "a.txt")) {
		t.Error("a refused file was moved")
	}

	// Answered requests go after ten minutes.
	old := time.Now().Add(-11 * time.Minute)
	os.Chtimes(filepath.Join(ok, "result.json"), old, old)
	m.processSpool()
	if fileExists(ok) {
		t.Error("an old answered request was kept")
	}
	if !fileExists(wrongBrain) {
		t.Error("a recent answered request was removed")
	}
}

// TestSiteModuleDataAndIngest runs the live site with a module that writes
// to its own data folder and hands a file to a brain through ctx.ingest, under
// the site's sandbox on macOS, where Fridolin's media folder stays closed.
func TestSiteModuleDataAndIngest(t *testing.T) {
	requireSiteRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestSite(t)
	media := filepath.Join(m.dataDir, "media")
	os.MkdirAll(media, 0o700)
	var got ingestCalls
	m.hooks.ingest, m.hooks.mediaDir = got.hook, func() string { return media }

	remote := newBareRepo(t)
	if err := m.ConnectRepo(ctx, SiteRepo{URL: remote}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	gitT(t, filepath.Dir(work), "clone", "-q", remote, work)
	writeSiteModule(t, work, "inbox", map[string]any{"capabilities": map[string]any{"ingest": []string{"web"}}}, `import express from 'express'
import { writeFileSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
const attempt = async (f) => { try { return String(await f()).trim() } catch (e) { return 'error ' + (e.code ?? e.message) } }
export default (ctx) => express.Router().get('/probe', async (_req, res) => {
  const upload = join(ctx.dataDir, 'Rechnung.pdf')
  res.json({
    data: await attempt(() => (writeFileSync(join(ctx.dataDir, 'state.json'), 'kept'), readFileSync(join(ctx.dataDir, 'state.json'), 'utf8'))),
    ingest: await attempt(async () => { writeFileSync(upload, 'pdf'); return (await ctx.ingest({ brain: 'web', files: [upload], text: 'file it', sender: 'sam' })).status }),
    otherBrain: await attempt(() => ctx.ingest({ brain: 'private', files: [], text: 'x' })),
    media: await attempt(() => readdirSync(`+jsString(media)+`).length),
  })
})
`)
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", "Add inbox")
	gitT(t, work, "push", "-q", "origin", "main")

	if _, err := m.AddUser(SiteUserInput{Kind: "password", Username: "sam", Password: "long enough pw", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	up, err := m.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Listen: &listen}); err != nil {
		t.Fatal(err)
	}
	m.Start(ctx)
	defer m.Stop()
	if err := m.Deploy(ctx, up.Head); err != nil {
		t.Fatal(err)
	}
	waitDeploy(t, m)
	on := true
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the site to answer", func() bool { return siteHealthy(listen) })

	body, _ := json.Marshal(map[string]string{"username": "sam", "password": "long enough pw"})
	resp, err := http.Post("http://"+listen+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("GET", "http://"+listen+"/inbox/api/probe", nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	json.NewDecoder(r.Body).Decode(&out)
	r.Body.Close()

	if out["data"] != "kept" || out["ingest"] != "accepted" || !strings.Contains(out["otherBrain"], "capabilities.ingest") {
		t.Fatalf("probe = %v\n%s", out, strings.Join(m.site.status().Log, "\n"))
	}
	if runtime.GOOS == "darwin" && out["media"] != "error EPERM" {
		t.Errorf("the module could read the media folder: %q", out["media"])
	}
	if calls := got.list(); len(calls) != 1 || calls[0] != "web|sam|file it|Rechnung.pdf" {
		t.Errorf("brains got %v", calls)
	}
	if data, err := os.ReadFile(filepath.Join(media, "Rechnung.pdf")); err != nil || string(data) != "pdf" {
		t.Errorf("the upload did not reach the media folder: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(m.dir("data"), "inbox", "state.json")); string(data) != "kept" {
		t.Errorf("the module's data folder holds %q", data)
	}
}
