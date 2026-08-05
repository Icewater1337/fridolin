package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
)

// isJunkMessage reports whether a message is too trivial to be worth a Claude
// invocation — a bare emoji, a single character, or a one-word acknowledgement.
// Only applied to untagged group chatter, never to a message addressed to the
// bridge.
func isJunkMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len([]rune(trimmed)) <= 1 {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "ok", "ok!", "okay", "thanks", "thank you", "yes", "no", "yep", "nope", "lol", "haha", "hehe":
		return true
	}
	// No letters and no digits: emoji, punctuation, or similar.
	return !strings.ContainsAny(trimmed, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
}

// Bridge connects WhatsApp to the Claude runner.
type Bridge struct {
	cfg      *Config
	store    *Store
	wa       *WhatsAppClient
	claude   *ClaudeRunner
	brainMgr *BrainManager // non-nil when cfg.BrainMode is true
}

// NewBridge creates and wires up all components.
func NewBridge(cfg *Config) (*Bridge, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}

	b := &Bridge{
		cfg:    cfg,
		store:  store,
		claude: NewClaudeRunner(cfg, store),
	}

	if cfg.BrainMode {
		mgr, err := NewBrainManager(cfg, store)
		if err != nil {
			return nil, fmt.Errorf("creating brain manager: %w", err)
		}
		b.brainMgr = mgr
	}

	wa, err := NewWhatsAppClient(cfg, store, b.handleMessage)
	if err != nil {
		return nil, fmt.Errorf("creating whatsapp client: %w", err)
	}
	b.wa = wa

	// "Still working" notification for the classic runner.
	globalWorkingNotify = func(jid string) {
		if err := b.wa.SendMessage(context.Background(), jid, "⏳ Working on it..."); err != nil {
			log.Printf("failed to send working notification to %s: %v", jid, err)
		}
	}

	// Mid-stream question forwarding for the classic runner.
	b.claude.IntermediateSendFn = func(jid, text string) {
		if err := b.wa.SendChunked(context.Background(), jid, "❓ "+text); err != nil {
			log.Printf("failed to send intermediate question to %s: %v", jid, err)
		}
	}

	// Mid-stream messages for brains (questions and working notices).
	if b.brainMgr != nil {
		b.brainMgr.SetIntermediateSendFn(func(jid, text string) {
			msg := text
			if !strings.HasPrefix(text, "⏳") {
				msg = "❓ " + text
			}
			if err := b.wa.SendChunked(context.Background(), jid, msg); err != nil {
				log.Printf("failed to send brain intermediate message to %s: %v", jid, err)
			}
		})
	}

	return b, nil
}

// Start connects to WhatsApp, starts the optional HTTP server, then blocks
// until ctx is cancelled.
func (b *Bridge) Start(ctx context.Context) error {
	if err := b.wa.Connect(); err != nil {
		return fmt.Errorf("connecting to WhatsApp: %w", err)
	}
	if b.cfg.HTTPListen != "" {
		b.startHTTPServer(b.cfg.HTTPListen)
	}
	log.Println("Bridge started. Waiting for messages…")
	<-ctx.Done()
	return nil
}

// Stop disconnects WhatsApp and closes the store.
func (b *Bridge) Stop() {
	if b.brainMgr != nil {
		b.brainMgr.Stop()
	}
	b.wa.Disconnect()
	if err := b.store.Close(); err != nil {
		log.Printf("closing store: %v", err)
	}
	log.Println("Bridge stopped.")
}

// handleMessage is the MessageHandler callback from the WhatsApp layer.
// Access control already happened there: groups are checked against
// allowed_groups plus the trigger, DMs against allowed_numbers.
func (b *Bridge) handleMessage(ctx context.Context, jid, senderNumber, text string, mediaPath string, silent bool) {
	text = strings.TrimSpace(text)
	isGroup := strings.HasSuffix(jid, "@g.us")

	switch strings.ToLower(text) {
	case "!reset":
		var resetErr error
		if b.brainMgr != nil {
			resetErr = b.brainMgr.ResetSession(jid, isGroup)
		} else {
			resetErr = b.store.DeleteSession(jid)
		}
		if resetErr != nil {
			log.Printf("error resetting session for %s: %v", jid, resetErr)
			_ = b.wa.SendMessage(ctx, jid, "❌ Failed to reset session.")
		} else {
			_ = b.wa.SendMessage(ctx, jid, "✅ Session reset. The next message starts a fresh conversation.")
		}
		return

	case "!help":
		help := "🤖 *WhatsApp–Claude Bridge*\n\n" +
			"Send a message to talk to Claude. Images, documents, audio, and video are passed along as files.\n\n" +
			"*Commands:*\n" +
			"• `!help` — show this help\n" +
			"• `!reset` — clear the current session and start fresh"
		if isGroup && len(b.cfg.Triggers) > 0 {
			help += "\n\nIn this group, start your message with `" + b.cfg.Triggers[0] + "` to address me."
		}
		_ = b.wa.SendMessage(ctx, jid, help)
		return
	}

	if text == "" && mediaPath == "" {
		return
	}

	// Untagged group chatter that carries no content is not worth a run.
	if silent && mediaPath == "" && isJunkMessage(text) {
		log.Printf("Junk message from %s: skipping Claude invocation", senderNumber)
		return
	}

	if b.brainMgr != nil {
		b.handleBrainMessage(ctx, isGroup, jid, senderNumber, text, mediaPath, silent)
		return
	}

	b.handleClassicMessage(ctx, jid, text, mediaPath)
}

// handleBrainMessage runs the message through the brain that owns the chat.
func (b *Bridge) handleBrainMessage(ctx context.Context, isGroup bool, jid, senderNumber, text, mediaPath string, silent bool) {
	response, err := b.brainMgr.ProcessMessage(ctx, isGroup, jid, senderNumber, text, mediaPath, silent)
	if err != nil {
		if errors.Is(err, errBrainInterrupted) || errors.Is(err, context.Canceled) {
			log.Printf("Brain run for %s was interrupted by a newer message", jid)
			return
		}
		log.Printf("Brain error for %s: %v", jid, err)
		if !silent {
			_ = b.wa.SendMessage(ctx, jid, fmt.Sprintf("❌ Error: %v", err))
		}
		return
	}

	// An empty response means a question was already forwarded mid-stream.
	// Silent runs never reply at all.
	if response == "" || silent {
		return
	}

	log.Printf("Brain: sending response (%d chars) to %s", len(response), jid)
	b.sendResponse(ctx, jid, response)
}

// handleClassicMessage runs the message through the single-session runner.
func (b *Bridge) handleClassicMessage(ctx context.Context, jid, text, mediaPath string) {
	replyChan := make(chan ClaudeResult, 1)
	b.claude.Enqueue(ClaudeRequest{
		JID:       jid,
		Text:      text,
		MediaPath: mediaPath,
		WorkDir:   b.cfg.WorkDir,
		ReplyChan: replyChan,
	})

	result := <-replyChan

	if result.Error != nil {
		log.Printf("Claude error for %s: %v", jid, result.Error)
		_ = b.wa.SendMessage(ctx, jid, fmt.Sprintf("❌ Error: %v", result.Error))
		return
	}

	// AlreadySent means a question went out mid-stream; the final text would
	// just repeat it.
	if result.AlreadySent {
		return
	}

	b.sendResponse(ctx, jid, result.Text)
}

// sendResponse parses Claude's response for [POLL]…[/POLL] blocks, sends each
// as a real WhatsApp poll, and sends the remaining text normally.
func (b *Bridge) sendResponse(ctx context.Context, jid, text string) {
	polls, remaining := extractPolls(text)

	for _, p := range polls {
		log.Printf("Sending poll to %s: %q (%d options)", jid, p.Question, len(p.Options))
		msgID, err := b.wa.SendPoll(ctx, jid, p.Question, p.Options, p.MaxSelectable)
		if err != nil {
			log.Printf("failed to send poll to %s: %v — falling back to text", jid, err)
			fallback := "📊 *" + p.Question + "*\n"
			for i, opt := range p.Options {
				fallback += fmt.Sprintf("%d. %s\n", i+1, opt)
			}
			_ = b.wa.SendMessage(ctx, jid, strings.TrimSpace(fallback))
			continue
		}
		// Store the option names so incoming vote hashes can be matched back.
		if err := b.store.SavePoll(string(msgID), jid, p.Question, p.Options); err != nil {
			log.Printf("failed to save poll %s to DB: %v", msgID, err)
		}
	}

	if remaining != "" {
		if err := b.wa.SendChunked(ctx, jid, remaining); err != nil {
			log.Printf("failed to send response to %s: %v", jid, err)
		}
	}
}

// ── poll parsing ──────────────────────────────────────────────────────────

// pollData represents a parsed [POLL]…[/POLL] block.
type pollData struct {
	Question      string
	Options       []string
	MaxSelectable int // 1 = single choice, 0 = unlimited
}

// maxPollOptions is WhatsApp's own limit on options per poll.
const maxPollOptions = 12

// pollHeaderRe matches [POLL] or [POLL max=N].
var pollHeaderRe = regexp.MustCompile(`(?i)\[POLL(?:\s+max=(\d+))?\]`)

// extractPolls finds all [POLL]…[/POLL] blocks in text, parses them, and
// returns the remaining text with the blocks removed.
func extractPolls(text string) ([]pollData, string) {
	var polls []pollData
	remaining := text

	for {
		loc := pollHeaderRe.FindStringIndex(remaining)
		if loc == nil {
			break
		}
		end := strings.Index(remaining[loc[1]:], "[/POLL]")
		if end == -1 {
			break
		}
		end += loc[1] // absolute position of "[/POLL]"

		maxSelectable := 1
		headerMatch := pollHeaderRe.FindStringSubmatch(remaining[loc[0]:loc[1]])
		if len(headerMatch) > 1 && headerMatch[1] != "" {
			if n, err := strconv.Atoi(headerMatch[1]); err == nil {
				maxSelectable = n
			}
		}

		block := remaining[loc[1]:end]
		remaining = remaining[:loc[0]] + remaining[end+len("[/POLL]"):]

		var question string
		var options []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(line), "question:") {
				question = strings.TrimSpace(line[len("question:"):])
			} else if strings.HasPrefix(line, "- ") {
				if opt := strings.TrimSpace(line[2:]); opt != "" {
					options = append(options, opt)
				}
			}
		}

		if len(options) > maxPollOptions {
			log.Printf("poll %q had %d options; truncating to WhatsApp's limit of %d", question, len(options), maxPollOptions)
			options = options[:maxPollOptions]
		}
		if maxSelectable > len(options) {
			maxSelectable = len(options)
		}

		if question != "" && len(options) >= 2 {
			polls = append(polls, pollData{
				Question:      question,
				Options:       options,
				MaxSelectable: maxSelectable,
			})
		}
	}

	return polls, strings.TrimSpace(remaining)
}
