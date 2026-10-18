package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
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

// A chat's file is named only by a valid brain id.
func TestBuildChatPathNeedsABrainID(t *testing.T) {
	c := &siteChats{m: newTestSite(t)}
	for _, bad := range []string{"../../etc/passwd", "..", "web/../x", "", "Web", "a b"} {
		if _, err := c.path(bad); err == nil {
			t.Errorf("path(%q) was accepted", bad)
		}
		if err := c.add(bad, "user", "hi"); err == nil {
			t.Errorf("add(%q) wrote a chat", bad)
		}
	}
	p, err := c.path("web")
	if err != nil || filepath.Dir(p) != c.m.dir("build") {
		t.Errorf("path(web) = %q, %v", p, err)
	}
}

// The Build page shows that a run is working on its own, so the working
// notice is left out of a build chat. Every other notice reaches it, those
// that start with the same ⏳ too, such as the one about Claude's usage
// limit.
func TestBuildChatLeavesOutOnlyTheWorkingNotice(t *testing.T) {
	c := &siteChats{m: newTestSite(t)}
	s := siteChatMessenger{chats: c}
	for _, tc := range []struct {
		text  string
		shown bool
	}{
		{workingNotice, false},
		{chatErrLimit, true},
		{"⏳ Queued (#2) — I'll get to this right after the current task.", true},
		{"Here is the new page.", true},
	} {
		before := len(c.get("web").Messages)
		if err := s.SendMessage(context.Background(), buildChatJID("web", 1), tc.text); err != nil {
			t.Fatal(err)
		}
		if shown := len(c.get("web").Messages) > before; shown != tc.shown {
			t.Errorf("%q in the build chat: %v, want %v", tc.text, shown, tc.shown)
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
		if src := filepath.Join(repoDir, "site", "node_modules"); fsx.Exists(src) {
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

// A long answer in a build chat reaches the Build page as one message,
// as it did before replies went through the outbox, which split it into
// "[1/5]" to "[5/5]" for a page that has no length limit. WhatsApp
// still gets it in parts.
func TestLongBuildChatAnswerStaysWhole(t *testing.T) {
	long := strings.Repeat("This is one sentence of a long answer. ", 24)
	cases := []struct {
		name  string
		send  func(o *Outbox, chat string) error
		build bool // to a build chat
		want  int  // messages
	}{
		{"a reply to a build chat", func(o *Outbox, chat string) error { return o.Reply(t.Context(), "web", chat, long, 200) }, true, 1},
		{"a question in a build chat", func(o *Outbox, chat string) error { return o.Text(t.Context(), chat, long, 200) }, true, 1},
		{"a reply to WhatsApp", func(o *Outbox, chat string) error { return o.Reply(t.Context(), "web", chat, long, 200) }, false, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := newTestBridge(t, `, "max_chunk_size": 200`)
			tb.addFolderBrain(t, BrainManifest{ID: "web", Name: "Web"})
			chat := testDMChat
			if c.build {
				chat = buildChatJID("web", 1)
			}
			if err := c.send(tb.outbox, chat); err != nil {
				t.Fatal(err)
			}
			received := func() []string {
				if !c.build {
					return tb.sentTo(chat)
				}
				var out []string
				for _, m := range tb.site.chats.get("web").Messages {
					out = append(out, m.Text)
				}
				return out
			}
			waitFor(t, 10*time.Second, "the answer", func() bool { return len(received()) >= c.want })
			time.Sleep(100 * time.Millisecond) // for a part too many
			got := received()
			if c.build {
				if len(got) != 1 || strings.TrimSpace(got[0]) != strings.TrimSpace(long) {
					t.Errorf("the Build page got %d messages %q, want the answer whole", len(got), got)
				}
				return
			}
			if len(got) != c.want || !strings.HasPrefix(got[0], "[1/5] ") {
				t.Errorf("WhatsApp got %q, want it in %d numbered parts", got, c.want)
			}
		})
	}
}

// The Build page shows the log of the run in flight: from the heading that
// names its pid to the next run's, never the lines of the runs before it.
func TestCurrentRunLogIsTheRunsOwn(t *testing.T) {
	chat := Inbound{InboundContent: InboundContent{JID: buildChatJID("web", 1), Sender: "dashboard"}}
	s := &brainSettings{model: "sonnet"}
	heading := func(pid int) string { return "[10:00:00] " + runHeading(chat, s, pid) }
	twoRuns := []string{
		heading(101),
		"[10:00:01] 🔧 Tool: Read",
		"[10:00:02] 💬 The first answer",
		heading(202),
		"[10:05:01] 🔧 Tool: Edit",
		"[10:05:02] 💬 Working on it",
	}
	// Tools a long run used, n of them.
	tools := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("[10:06:00] 🔧 Tool: Bash %04d %s", i, strings.Repeat("x", 40)))
		}
		return out
	}
	busy := append([]string{heading(101), "[10:00:01] 💬 Before", heading(303)}, tools(100)...)
	// More than currentRunLog reads of the log's end, heading and all.
	long := append([]string{heading(101), "[10:00:01] 💬 Before", heading(303)}, tools(2000)...)
	// Lines so long that fewer than 40 fit in what is read. The first line
	// read is the end of one, which is left out.
	var wide []string
	for i := range 30 {
		wide = append(wide, fmt.Sprintf("[10:06:00] 🔧 Tool: Bash %04d %s", i, strings.Repeat("y", 3000)))
	}
	wide = append([]string{heading(101), heading(303)}, wide...)
	whole := func(lines []string) []string { // those wholly in what is read
		size := 0
		for i := len(lines) - 1; i >= 0; i-- {
			if size += len(lines[i]) + 1; size >= runLogWindow {
				return lines[i+1:]
			}
		}
		return lines
	}
	crossing := []string{heading(101), heading(303), strings.Repeat("y", runLogWindow-20), "tail"}
	oversized := []string{heading(303), strings.Repeat("y", 2*runLogWindow), "tail"}
	cases := []struct {
		name  string
		lines []string
		pid   int
		want  []string
	}{
		{"the run in flight", twoRuns, 202, []string{"[10:05:01] 🔧 Tool: Edit", "[10:05:02] 💬 Working on it"}},
		{"a run before it", twoRuns, 101, []string{"[10:00:01] 🔧 Tool: Read", "[10:00:02] 💬 The first answer"}},
		{"a run that has not written its heading yet", twoRuns, 404, []string{}},
		{"a run without its process yet", twoRuns, 0, []string{}},
		{"its last 40 lines", busy, 303, busy[len(busy)-40:]},
		{"a run longer than what is read", long, 303, long[len(long)-40:]},
		{"a new run after a long previous run", long, 404, []string{}},
		{"a long log without a run heading", tools(2000), 303, []string{}},
		{"its whole lines only", wide, 303, whole(wide)},
		{"heading split by the window", crossing, 303, crossing[2:]},
		{"oversized tool line", oversized, 303, []string{"tail"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), streamLogName)
			if err := os.WriteFile(path, []byte(strings.Join(c.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := currentRunLog(path, c.pid); !slices.Equal(got, c.want) {
				t.Errorf("currentRunLog(pid %d) = %q, want %q", c.pid, got, c.want)
			}
		})
	}
	if got := currentRunLog(filepath.Join(t.TempDir(), "none.log"), 202); got == nil || len(got) != 0 {
		t.Errorf("without a log: %q, want an empty list", got)
	}

	// The Build page asks every second or so, also while the run starts
	// and has no pid yet: no file may stay open after an answer.
	t.Run("no file left open", func(t *testing.T) {
		openFiles := func() int {
			fds, err := os.ReadDir("/dev/fd")
			if err != nil {
				t.Skipf("cannot count open files: %v", err)
			}
			return len(fds)
		}
		path := filepath.Join(t.TempDir(), streamLogName)
		if err := os.WriteFile(path, []byte(strings.Join(twoRuns, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A collection would close what was left open, and hide it.
		defer debug.SetGCPercent(debug.SetGCPercent(-1))
		before := openFiles()
		for _, pid := range []int{0, 202, 404} {
			for range 50 {
				currentRunLog(path, pid)
			}
		}
		if left := openFiles() - before; left > 25 {
			t.Errorf("%d files left open after 150 answers", left)
		}
	})
}
