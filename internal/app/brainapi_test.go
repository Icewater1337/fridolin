package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/toolcli"
)

const (
	apiTestDM     = "15551234567@s.whatsapp.net"
	apiTestGroup  = "120363000000000000@g.us"
	apiTestGroup2 = "120363000000000001@g.us"
)

// serveTestBridge is newTestBridge with its HTTP server listening on a
// free loopback port, where runs reach the brain API. It returns the
// server's URL.
func serveTestBridge(t *testing.T, extra string) (*testBridge, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	tb := newTestBridge(t, `, "http_listen": "`+addr+`"`+extra)
	srv := &http.Server{Handler: tb.newHTTPHandler(addr), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return tb, "http://" + addr
}

// brainTool runs a tool subcommand as a run of brain would, with a token
// minted for it in its token file, and returns its exit status and its
// JSON decoded.
func brainTool(t *testing.T, cmd func([]string, toolcli.IO) int, url, token string, args ...string) (int, map[string]any) {
	t.Helper()
	file, err := writeRunToken(t.TempDir(), token)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FRIDOLIN_API": url, "FRIDOLIN_BRAIN_TOKEN_FILE": file, "CALENDAR_TZ": "Europe/Zurich"}
	code, out, errs := runTool(t, cmd, t.TempDir(), env, time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC), args...)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("exit %d, stdout is not one JSON object: %q (stderr %q)", code, out, errs)
	}
	return code, doc
}

func savePoll(t *testing.T, tb *testBridge, id, chatJID, question string, options []string, votes map[string][]string) {
	t.Helper()
	if err := tb.store.SavePoll(id, chatJID, "", question, options); err != nil {
		t.Fatal(err)
	}
	for voter, chosen := range votes {
		if err := tb.store.SavePollVote(id, voter, chosen); err != nil {
			t.Fatal(err)
		}
	}
}

func pollQuestions(doc map[string]any) []string {
	var out []string
	polls, _ := doc["polls"].([]any)
	for _, p := range polls {
		out = append(out, p.(map[string]any)["question"].(string))
	}
	return out
}

// TestPollsAreTheBrainsOwn: a brain reads the polls of its own chats from
// the bridge (the sandbox denies it bridge.db), and not those of another
// brain's, whose voters are other people. Its chats are the ones that
// route to it and the one it replies in: brain r gets files through
// /ingest only and answers in the DM, so the polls it sends go there.
func TestPollsAreTheBrainsOwn(t *testing.T) {
	t.Parallel()
	tb, url := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "g", Name: "G", Routing: manifest.Routing{Groups: []string{apiTestGroup}}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "r", Name: "R", ReplyChat: apiTestDM})
	savePoll(t, tb, "P1", apiTestDM, "Lunch?", []string{"Yes", "No"}, map[string][]string{apiTestDM: {"Yes"}})
	savePoll(t, tb, "P2", apiTestGroup, "When?", []string{"Mon", "Tue", "Wed"}, map[string][]string{
		"15551234567@s.whatsapp.net": {"Mon", "Tue"},
		"15559876543@s.whatsapp.net": nil, // took the vote back
	})
	// A group no brain lists goes to the first brain, a.
	savePoll(t, tb, "P3", apiTestGroup2, "Where?", []string{"Here", "There"}, nil)
	tokA, revokeA := tb.runTokens.mint("a")
	tokG, revokeG := tb.runTokens.mint("g")
	defer revokeG()
	tokR, revokeR := tb.runTokens.mint("r")
	defer revokeR()

	cases := []struct {
		name  string
		token string
		args  []string
		want  []string // questions, newest first; nil for an error
		err   string
	}{
		{"the DM brain", tokA, nil, []string{"Where?", "Lunch?"}, ""},
		{"the group brain", tokG, nil, []string{"When?"}, ""},
		{"one chat", tokA, []string{"--chat", apiTestDM}, []string{"Lunch?"}, ""},
		{"the newest", tokA, []string{"--recent", "1"}, []string{"Where?"}, ""},
		{"a brain that replies in the DM", tokR, nil, []string{"Lunch?"}, ""},
		{"its reply chat", tokR, []string{"--chat", apiTestDM}, []string{"Lunch?"}, ""},
		{"another brain's chat", tokG, []string{"--chat", apiTestDM}, nil, "not one of this brain's chats"},
		{"a chat the reply brain does not reply in", tokR, []string{"--chat", apiTestGroup}, nil, "not one of this brain's chats"},
		{"another brain's poll", tokA, []string{"--id", "P2"}, nil, "not found in this brain's chats"},
		{"no such poll", tokG, []string{"--id", "P9"}, nil, "not found"},
		{"--id with --recent", tokG, []string{"--id", "P2", "--recent", "3"}, nil, "does not combine"},
		{"a token the bridge did not make", "forged", nil, nil, "does not know this run's token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, doc := brainTool(t, toolcli.Polls, url, tc.token, tc.args...)
			if tc.err != "" {
				if msg, _ := doc["error"].(string); code != 1 || !strings.Contains(msg, tc.err) {
					t.Errorf("exit %d, %v; want an error with %q", code, doc, tc.err)
				}
				return
			}
			if got := pollQuestions(doc); code != 0 || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("exit %d, polls %q; want %q (%v)", code, got, tc.want, doc)
			}
		})
	}

	t.Run("one poll's tally", func(t *testing.T) {
		code, doc := brainTool(t, toolcli.Polls, url, tokG, "--id", "P2")
		data, _ := json.Marshal(doc["results"])
		want := `[{"option":"Mon","voters":["15551234567@s.whatsapp.net"],"votes":1},{"option":"Tue","voters":["15551234567@s.whatsapp.net"],"votes":1},{"option":"Wed","voters":[],"votes":0}]`
		if code != 0 || string(data) != want || doc["total_voters"] != float64(1) || doc["chat_jid"] != apiTestGroup {
			t.Errorf("exit %d, %v\nresults %s\nwant    %s", code, doc, data, want)
		}
	})

	t.Run("after the run", func(t *testing.T) {
		revokeA()
		if code, doc := brainTool(t, toolcli.Polls, url, tokA); code != 1 || !strings.Contains(doc["error"].(string), "does not know this run's token") {
			t.Errorf("exit %d, %v; want the revoked token refused", code, doc)
		}
	})
}

// TestRunsGetATokenForTheirRunOnly: every run is told where the bridge's
// binary and the brain API are, and gets a token of its own in a file,
// which works while the run lasts and not after it.
func TestRunsGetATokenForTheirRunOnly(t *testing.T) {
	t.Parallel()
	tb, url := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	savePoll(t, tb, "P1", apiTestDM, "Lunch?", []string{"Yes", "No"}, nil)
	envLog := filepath.Join(t.TempDir(), "brain.env")
	setenv(t, "FRIDOLIN_FAKE_ENV_LOG", envLog)
	setenv(t, "FRIDOLIN_FAKE_POLLS", "1")

	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: apiTestDM, Sender: "15551234567", Text: "polls?", Source: "whatsapp"}})
	testutil.WaitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.sentTo(apiTestDM)) > 0 })
	reply := tb.sentTo(apiTestDM)[0]
	if !strings.Contains(reply, `polls=0 { "count": 1, "polls": [`) || !strings.Contains(reply, `"question": "Lunch?"`) {
		t.Errorf("the run's fridolin polls answered:\n%s", reply)
	}

	logged, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	file := env["FRIDOLIN_BRAIN_TOKEN_FILE"]
	if env["FRIDOLIN_API"] != url || env["FRIDOLIN_BRAIN_ID"] != "a" || filepath.Dir(file) != runTokenDir(tb.cfgs.Get().DataDir, "a") {
		t.Errorf("run env: FRIDOLIN_API=%q FRIDOLIN_BRAIN_ID=%q FRIDOLIN_BRAIN_TOKEN_FILE=%q", env["FRIDOLIN_API"], env["FRIDOLIN_BRAIN_ID"], file)
	}
	// Prompts and skills run "$FRIDOLIN_BIN" calendar, wherever the bridge
	// is (TestBrainRunsKnowWhereTheToolsAre has FRIDOLIN_TOOLS_DIR).
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if env["FRIDOLIN_BIN"] != exe {
		t.Errorf("run env: FRIDOLIN_BIN=%q, want %q", env["FRIDOLIN_BIN"], exe)
	}
	testutil.WaitFor(t, 5*time.Second, "the run's token to be revoked and its file deleted", func() bool {
		_, err := os.Stat(file)
		tb.runTokens.mu.Lock()
		defer tb.runTokens.mu.Unlock()
		return errors.Is(err, fs.ErrNotExist) && len(tb.runTokens.brains) == 0
	})
}

// TestRunTokenStaysOutOfTheEnvironment: a run's environment names the file
// with its token, never the token. macOS shows a process's environment to
// every process of the same user, sandboxed or not, so another brain
// running at the same time could read a token there and call the brain
// API as this brain (TestSandboxKeepsRunTokensToTheirBrain).
func TestRunTokenStaysOutOfTheEnvironment(t *testing.T) {
	t.Parallel()
	tb, _ := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true}})
	access := tb.brainRunEnv(runInfo{Brain: "a"})
	env, done := access.Env, access.Done
	var file string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "FRIDOLIN_BRAIN_TOKEN_FILE="); ok {
			file = v
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the run's token file %q: %v", file, err)
	}
	tok := string(data)
	if tb.runTokens.brainOf(tok) != "a" {
		t.Fatalf("the file holds %q, not a token of brain a", tok)
	}
	for _, kv := range env {
		if strings.Contains(kv, tok) {
			t.Errorf("the token is in the run's environment: %s", kv)
		}
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the token file is %v (%v), want readable by its owner only", fi.Mode(), err)
	}
	// The run's MCP server is started from a file next to the token's,
	// which names the token's file, never the token.
	cfg, err := os.ReadFile(access.MCPConfig)
	if err != nil || filepath.Dir(access.MCPConfig) != filepath.Dir(file) || strings.Contains(string(cfg), tok) || !strings.Contains(string(cfg), file) {
		t.Errorf("the run's --mcp-config %s (%v):\n%s", access.MCPConfig, err, cfg)
	}
	done()
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) || tb.runTokens.brainOf(tok) != "" {
		t.Errorf("after the run the file is still there (%v) or the token still works", err)
	}
	if _, err := os.Stat(access.MCPConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after the run its --mcp-config is still there (%v)", err)
	}
}

// TestBridgePrunesStaleRunTokens: a bridge that stopped without cleaning up
// leaves token files behind; the next one to start deletes them, and
// nothing else. One that cannot start, since another bridge holds the data
// dir, leaves them alone: they are that bridge's runs'.
func TestBridgePrunesStaleRunTokens(t *testing.T) {
	t.Parallel()
	home := fsx.Canonical(t.TempDir())
	setenv(t, "HOME", home)
	data := filepath.Join(home, "data")
	cfgs, err := openConfigStore(t, writeConfig(t, `{"data_dir": "`+data+`", "http_listen": "off"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())
	stale, err := writeRunToken(runTokenDir(data, "a"), "old-token")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(runTokenDir(data, "a"), "notes.txt")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	running, err := platform.AcquireLock(config.Layout{Root: data}.Lock())
	if err != nil {
		t.Fatal(err)
	}
	if b, err := New(Options{Config: cfgs, NoWhatsApp: true}); err == nil {
		b.Stop()
		t.Fatal("NewBridge must refuse a data dir another bridge holds")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("a bridge that could not start took the running one's token file: %v", err)
	}
	running.Release()

	b, err := New(Options{Config: cfgs, NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the stale token file is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("pruning took a file that is no token: %v", err)
	}
}

// TestBrainClientNeedsTheRunsTokenFile: outside a run, or with a token file
// that is gone or holds no token, the commands say so.
func TestBrainClientNeedsTheRunsTokenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge")
	if err := os.WriteFile(huge, bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"outside a run", nil, "works inside a brain's run, where the bridge sets FRIDOLIN_API and FRIDOLIN_BRAIN_TOKEN_FILE"},
		{"the old variable", map[string]string{"FRIDOLIN_API": "http://127.0.0.1:9", "FRIDOLIN_BRAIN_TOKEN": "tok"}, "FRIDOLIN_BRAIN_TOKEN_FILE"},
		{"a file that is gone", map[string]string{"FRIDOLIN_API": "http://127.0.0.1:9", "FRIDOLIN_BRAIN_TOKEN_FILE": filepath.Join(dir, "gone")}, "no such file"},
		{"an empty file", map[string]string{"FRIDOLIN_API": "http://127.0.0.1:9", "FRIDOLIN_BRAIN_TOKEN_FILE": empty}, "does not hold a token"},
		{"not a token", map[string]string{"FRIDOLIN_API": "http://127.0.0.1:9", "FRIDOLIN_BRAIN_TOKEN_FILE": huge}, "does not hold a token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := toolcli.BrainClientFor(toolcli.IO{Getenv: func(k string) string { return tc.env[k] }})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("brainClientFor = %v, %v; want an error with %q", c, err, tc.want)
			}
		})
	}
}

func TestRunTokens(t *testing.T) {
	t.Parallel()
	var rt runTokens
	a1, revokeA1 := rt.mint("a")
	a2, revokeA2 := rt.mint("a")
	b, revokeB := rt.mint("b")
	defer revokeA2()
	defer revokeB()
	if a1 == a2 || a1 == b {
		t.Fatal("two runs got the same token")
	}
	if rt.brainOf(a1) != "a" || rt.brainOf(a2) != "a" || rt.brainOf(b) != "b" {
		t.Fatal("a token does not name its brain")
	}
	revokeA1()
	revokeA1() // twice is fine
	if rt.brainOf(a1) != "" || rt.brainOf(a2) != "a" {
		t.Error("revoking one run's token touched another's, or did not revoke it")
	}
	if rt.brainOf("") != "" || rt.brainOf(a2[:len(a2)-1]) != "" {
		t.Error("an empty or partial token was taken")
	}
}

func TestBrainAPIRefusals(t *testing.T) {
	t.Parallel()
	tb, _ := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	tok, revoke := tb.runTokens.mint("a")
	defer revoke()
	stale, revokeStale := tb.runTokens.mint("gone") // a brain removed since
	defer revokeStale()
	cases := []struct {
		name, auth, path string
		want             int
	}{
		{"no token", "", "/brain/polls", 401},
		{"the dashboard's header", "", "/brain/polls", 401},
		{"a removed brain's run", "Bearer " + stale, "/brain/polls", 401},
		{"unknown call", "Bearer " + tok, "/brain/nope", 404},
		{"ok", "Bearer " + tok, "/brain/polls", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tb.do("GET", tc.path, nil, func(r *http.Request) {
				r.Header.Del("X-Bridge-Token")
				if tc.name == "the dashboard's header" {
					r.Header.Set("X-Bridge-Token", tb.token.Value())
				}
				if tc.auth != "" {
					r.Header.Set("Authorization", tc.auth)
				}
			})
			if w.Code != tc.want || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Errorf("%s: %d %s; want %d with a JSON body", tc.path, w.Code, w.Body.String(), tc.want)
			}
		})
	}
}
