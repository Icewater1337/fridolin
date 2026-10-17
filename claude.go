package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ClaudeRequest is a single request to be processed by the Claude CLI.
type ClaudeRequest struct {
	JID       string
	Text      string
	Media     []string // downloaded attachments
	WorkDir   string   // working directory (empty = cfg.WorkDir)
	ReplyChan chan ClaudeResult
}

// ClaudeResult holds the outcome of a single Claude CLI invocation.
type ClaudeResult struct {
	Text      string
	SessionID string
	Error     error
	// AlreadySent is true when an intermediate question was sent to WhatsApp
	// mid-stream, so the caller must not send the final result again.
	AlreadySent bool
}

// ── ClaudeRunner ─────────────────────────────────────────────────────────

// ClaudeRunner is the classic (non-brain) runner: one resumable Claude session
// per chat, with a serial queue so a chat never runs two invocations at once.
type ClaudeRunner struct {
	cfg   func() *Config
	store *Store

	// IntermediateSendFn is called when Claude emits an AskUserQuestion tool
	// use, so the question reaches WhatsApp before the process finishes.
	IntermediateSendFn func(jid, text string)

	mu    sync.Mutex
	busy  map[string]bool
	queue map[string][]ClaudeRequest
}

// NewClaudeRunner creates a runner.
func NewClaudeRunner(cfg func() *Config, store *Store) *ClaudeRunner {
	return &ClaudeRunner{
		cfg:   cfg,
		store: store,
		busy:  make(map[string]bool),
		queue: make(map[string][]ClaudeRequest),
	}
}

// Enqueue adds a request to the per-chat queue, starting a worker if idle.
func (r *ClaudeRunner) Enqueue(req ClaudeRequest) {
	r.mu.Lock()
	if r.busy[req.JID] {
		r.queue[req.JID] = append(r.queue[req.JID], req)
		r.mu.Unlock()
		return
	}
	r.busy[req.JID] = true
	r.mu.Unlock()

	go r.processLoop(req)
}

// processLoop runs the current request then drains the queue for that chat.
func (r *ClaudeRunner) processLoop(first ClaudeRequest) {
	current := first
	for {
		result := r.Run(context.Background(), current)
		current.ReplyChan <- result

		r.mu.Lock()
		q := r.queue[current.JID]
		if len(q) == 0 {
			r.busy[current.JID] = false
			r.mu.Unlock()
			return
		}
		next := q[0]
		r.queue[current.JID] = q[1:]
		r.mu.Unlock()

		current = next
	}
}

// Run executes the Claude CLI for a single request.
func (r *ClaudeRunner) Run(ctx context.Context, req ClaudeRequest) ClaudeResult {
	result, retry := r.runOnce(ctx, req, false)
	if retry {
		// The stored session is unusable (expired or in use elsewhere) — drop
		// it and start a fresh conversation rather than failing the message.
		_ = r.store.DeleteSession(req.JID)
		result, _ = r.runOnce(ctx, req, true)
	}
	return result
}

// runOnce runs the Claude CLI once. Returns (result, needsRetry).
func (r *ClaudeRunner) runOnce(ctx context.Context, req ClaudeRequest, skipResume bool) (ClaudeResult, bool) {
	sessionID, err := r.store.GetSession(req.JID)
	if err != nil {
		return ClaudeResult{Error: fmt.Errorf("getting session: %w", err)}, false
	}

	cfg := r.cfg()
	prompt := req.Text
	switch len(req.Media) {
	case 0:
	case 1:
		prompt = fmt.Sprintf("A file was shared via WhatsApp and saved to: %s\n\n%s", req.Media[0], req.Text)
	default:
		prompt = fmt.Sprintf("These files were shared via WhatsApp and saved to:\n- %s\n\n%s", strings.Join(req.Media, "\n- "), req.Text)
	}

	args := []string{"--output-format", "stream-json", "--verbose"}
	if cfg.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if sessionID != "" && !skipResume {
		args = append(args, "--resume", sessionID)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	args = append(args, "-p", prompt)

	cmd := exec.CommandContext(ctx, cfg.ClaudePath, args...)
	cmd.Dir = req.WorkDir
	if cmd.Dir == "" {
		cmd.Dir = cfg.WorkDir
	}

	// stdin must be /dev/null or the CLI blocks waiting for input.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return ClaudeResult{Error: fmt.Errorf("opening /dev/null: %w", err)}, false
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ClaudeResult{Error: fmt.Errorf("creating stdout pipe: %w", err)}, false
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return ClaudeResult{Error: fmt.Errorf("starting claude: %w", err)}, false
	}

	type parseResult struct {
		result ClaudeResult
		retry  bool
	}
	parseDone := make(chan parseResult, 1)
	go func() {
		res, retry := r.parseStream(req.JID, stdout)
		parseDone <- parseResult{res, retry}
	}()

	// Reassure the sender if Claude takes more than 10 seconds.
	var pr parseResult
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	select {
	case pr = <-parseDone:
	case <-timer.C:
		globalWorkingNotify(req.JID)
		pr = <-parseDone
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		// Only surface a process-level error if the stream didn't already
		// produce an error or a usable result.
		if pr.result.Error == nil && pr.result.Text == "" && !pr.result.AlreadySent {
			if strings.Contains(stderr, "session") && strings.Contains(stderr, "use") {
				return ClaudeResult{Error: fmt.Errorf("session in use: %s", stderr)}, true
			}
			return ClaudeResult{Error: fmt.Errorf("claude exited: %w — stderr: %s", waitErr, stderr)}, false
		}
	}

	return pr.result, pr.retry
}

// parseStream reads the stream-json events, forwards AskUserQuestion prompts to
// WhatsApp as they appear, and returns the final result.
func (r *ClaudeRunner) parseStream(jid string, reader io.Reader) (ClaudeResult, bool) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	var finalResult string
	var finalSessionID string
	questionSent := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "assistant":
			if event.Message == nil {
				continue
			}
			for _, content := range event.Message.Content {
				if content.Type == "tool_use" && content.Name == "AskUserQuestion" {
					question := extractAskQuestion(content.Input)
					if question != "" && r.IntermediateSendFn != nil {
						r.IntermediateSendFn(jid, question)
						questionSent = true
					}
				}
			}

		case "result":
			finalSessionID = event.SessionID
			if event.IsError || event.Error != "" {
				lower := strings.ToLower(event.Error)
				// A session error (or a bare is_error with no message, which
				// usually means the resumed session expired) is retryable.
				if strings.Contains(lower, "session") || (event.IsError && event.Error == "") {
					return ClaudeResult{Error: fmt.Errorf("claude error: %s", event.Error)}, true
				}
				return ClaudeResult{Error: fmt.Errorf("claude error: %s", event.Error)}, false
			}
			finalResult = event.Result
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return ClaudeResult{Error: fmt.Errorf("reading claude output: %w", err)}, false
	}

	// Persist the session so the next message can --resume it.
	if finalSessionID != "" {
		_ = r.store.SaveSession(jid, finalSessionID)
	}

	if questionSent {
		// The question already went to WhatsApp. The final text would restate
		// it, so skip it; the session is saved and the user's reply resumes.
		return ClaudeResult{SessionID: finalSessionID, AlreadySent: true}, false
	}

	if finalResult == "" {
		return ClaudeResult{Error: fmt.Errorf("no result from claude (empty output)")}, false
	}

	return ClaudeResult{Text: finalResult, SessionID: finalSessionID}, false
}

// globalWorkingNotify is replaced by the bridge with a real WhatsApp send.
var globalWorkingNotify = func(jid string) {}
