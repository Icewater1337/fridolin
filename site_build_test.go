package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildChatJIDs(t *testing.T) {
	for jid, want := range map[string]string{
		buildChatJID("web", 1):              "web",
		buildChatJID("my-site", 12):         "my-site",
		"15551234567@s.whatsapp.net":        "",
		"site-build-web-1@s.whatsapp.net":   "",
		"site-build--1" + buildChatSuffix:   "",
		"something-web-1" + buildChatSuffix: "",
		"120363000000000000@g.us":           "",
	} {
		if got := brainOfBuildJID(jid); got != want {
			t.Errorf("brainOfBuildJID(%q) = %q, want %q", jid, got, want)
		}
	}
}

// A message on the Build page runs the builder brain in a chat of its own;
// its answer lands in the page, not in WhatsApp.
func TestSiteBuildChat(t *testing.T) {
	tb := newTestBridge(t, "")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FRIDOLIN_FAKE_ARGS_LOG", argsLog)
	tb.addFolderBrain(t, BrainManifest{ID: "web", Name: "Web"})
	m := tb.site
	link := func(ctx context.Context, dir string, _ bool) error {
		if src := filepath.Join(repoDir, "site", "node_modules"); fileExists(src) {
			return os.Symlink(src, filepath.Join(dir, "node_modules"))
		}
		return nil
	}
	m.npmInstall = link
	m.workspaceInstall = func(ctx context.Context, _, dir string) error { return link(ctx, dir, false) }

	ctx := context.Background()
	if err := m.ConnectRepo(ctx, SiteRepo{URL: newBareRepo(t)}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddStarter(ctx); err != nil {
		t.Fatal(err)
	}
	if w := tb.do("GET", "/api/site/build/web", nil); w.Code != 404 {
		t.Errorf("a brain that is not a builder has a build chat: %d", w.Code)
	}
	if w := tb.do("PUT", "/api/site/builders/web", map[string]bool{"on": true}); w.Code != 200 {
		t.Fatalf("allow builder: %d %s", w.Code, w.Body.String())
	}

	if w := tb.do("POST", "/api/site/build/web", map[string]string{"text": "  "}); w.Code != 400 {
		t.Errorf("an empty message: %d", w.Code)
	}
	if w := tb.do("POST", "/api/site/build/web", map[string]string{"text": "add a page for the team"}); w.Code != 202 {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	var chat struct {
		Messages []BuildMessage `json:"messages"`
		Thread   int            `json:"thread"`
		Running  bool           `json:"running"`
	}
	waitFor(t, 20*time.Second, "the brain's answer", func() bool {
		w := tb.do("GET", "/api/site/build/web", nil)
		json.Unmarshal(w.Body.Bytes(), &chat)
		return len(chat.Messages) == 2 && !chat.Running
	})
	if chat.Messages[0].Role != "user" || chat.Messages[1].Role != "assistant" || !strings.Contains(chat.Messages[1].Text, "add a page for the team") {
		t.Fatalf("chat = %+v", chat.Messages)
	}
	for _, msg := range tb.dev.Outbox() {
		if strings.Contains(msg.Text, "add a page for the team") {
			t.Errorf("the answer also went to WhatsApp (%s)", msg.JID)
		}
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--plugin-dir") || !strings.Contains(string(args), filepath.Join(".fridolin-site", "mcp.json")) {
		t.Errorf("the run did not get the builder's plugin and tools:\n%s", args)
	}

	// A new conversation is a new chat, so a new Claude session.
	if w := tb.do("POST", "/api/site/build/web/new", map[string]any{}); w.Code != 200 {
		t.Fatalf("new: %d", w.Code)
	}
	w := tb.do("GET", "/api/site/build/web", nil)
	json.Unmarshal(w.Body.Bytes(), &chat)
	if chat.Thread != 2 || len(chat.Messages) != 0 {
		t.Errorf("after a new conversation: %+v", chat)
	}
	if w := tb.do("POST", "/api/site/build/web/stop", map[string]any{}); w.Code != 409 {
		t.Errorf("stopping an idle chat: %d", w.Code)
	}

	// Other chats still reach WhatsApp.
	if err := tb.wa.SendMessage(ctx, "15551234567@s.whatsapp.net", "plain message"); err != nil {
		t.Fatal(err)
	}
	if out := tb.dev.Outbox(); len(out) == 0 || out[0].Text != "plain message" {
		t.Errorf("outbox = %+v", out)
	}
}
