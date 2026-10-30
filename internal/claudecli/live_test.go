package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLiveHubTurn(t *testing.T) {
	h := NewLiveHub()
	answer, err := h.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Begin(); !errors.Is(err, ErrLiveBusy) {
		t.Errorf("a second turn while one is armed: %v", err)
	}
	if err := h.Deliver(LiveEvent{Content: "hello", Meta: map[string]string{"kind": "message"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Connected():
		t.Fatal("connected before anyone asked for an event")
	default:
	}
	ev, ok := h.Next(context.Background(), time.Second)
	if !ok || ev.Content != "hello" || ev.Meta["kind"] != "message" {
		t.Fatalf("next: %+v %v", ev, ok)
	}
	select {
	case <-h.Connected():
	default:
		t.Error("not connected after the MCP server asked")
	}
	if !h.End("done") {
		t.Fatal("End found no turn waiting")
	}
	if got := <-answer; got != "done" {
		t.Errorf("answer %q", got)
	}
	if h.End("again") {
		t.Error("a stop with no turn waiting counted as an answer")
	}
	if _, ok := h.Next(context.Background(), 10*time.Millisecond); ok {
		t.Error("an event from nowhere")
	}
	if _, err := h.Begin(); err != nil {
		t.Errorf("a turn after the last ended: %v", err)
	}
	h.Abort()
	if _, err := h.Begin(); err != nil {
		t.Errorf("a turn after an abort: %v", err)
	}
}

func TestLiveHubFull(t *testing.T) {
	h := NewLiveHub()
	var err error
	for range 10 {
		if err = h.Deliver(LiveEvent{Content: "x"}); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrLiveFull) {
		t.Errorf("a channel with no room: %v", err)
	}
}

// StartLive answers the development-channels dialog with Enter and reads
// what the CLI shows, without its escape sequences and its spacing.
func TestStartLiveAnswersTheDialog(t *testing.T) {
	script := `printf '\033[1mWARNING\033[0m\r\n 1. I am  using this for\033[2C local development\r\n 2. Exit\r\n'; read answer; echo "got:[$answer]"; sleep 30`
	p, err := StartLive(context.Background(), Spawn{Binary: "sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	defer p.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.Screen(), "got:[") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := p.Screen(); !strings.Contains(got, "got:[]") || strings.Contains(got, "\x1b") {
		t.Errorf("screen %q", got)
	}
	if p.Answered() != 1 {
		t.Errorf("answered %d dialogs, want 1", p.Answered())
	}
	p.Kill()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not end after Kill")
	}
}

func TestStartLiveEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := StartLive(ctx, Spawn{Binary: "sleep", Args: []string{"30"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process outlived its context")
	}
}

func TestLiveArgs(t *testing.T) {
	got := strings.Join(LiveArgs(Options{Dangerously: true, Model: "opus", DisallowedTools: []string{"WebFetch"}}, "/b/.mcp.json", `{"x":1}`), " ")
	want := "--mcp-config /b/.mcp.json --dangerously-skip-permissions --model opus --disallowed-tools WebFetch,AskUserQuestion --setting-sources project,local --strict-mcp-config --settings {\"x\":1}"
	if got != want {
		t.Errorf("args\n got %s\nwant %s", got, want)
	}
	for _, a := range LiveArgs(Options{}, "", "") {
		if a == "-p" || a == "--output-format" {
			t.Errorf("a live session is no print run: %s", a)
		}
	}
	if got := strings.Join(LiveArgs(Options{InheritUser: true}, "/b/.mcp.json", "{}"), " "); strings.Contains(got, "--setting-sources") || strings.Contains(got, "--mcp-config") {
		t.Errorf("inheriting the user's config: %s", got)
	}
}
