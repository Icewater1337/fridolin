package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/claudecli"
)

// ChatErrorText is what a chat is told about a run that failed with err,
// one of the chatErr messages of messages.go, or "" for a run that was
// stopped, which says nothing. inDM is true for a direct chat with an
// allowed number: there alone the CLI's own error text may show, since
// only the person who asked reads it.
func ChatErrorText(err error, inDM bool) string {
	if err == nil || errors.Is(err, claudecli.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return ""
	}
	if refused, ok := errors.AsType[*brain.RunRefused](err); ok {
		return refused.Notice
	}
	if errors.Is(err, brain.ErrSandboxUnavailable) {
		return outboundmsg.ChatErrSandbox(brain.ErrSandboxUnavailable)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, claudecli.ErrTimedOut) {
		return outboundmsg.ChatErrTimeout
	}
	text := strings.ToLower(err.Error())
	var ce *claudecli.Error
	fromCLI := errors.As(err, &ce)
	codex := (fromCLI && ce.Provider == "codex") || strings.HasPrefix(text, "codex ")
	if codex && containsAny(text, "not logged in", "not signed in", "401", "invalid api key", "authentication", "access token", "refresh token") {
		return outboundmsg.ChatErrCodexNotSignedIn
	}
	switch {
	case containsAny(text, "not logged in", "please run /login", "invalid api key", "failed to authenticate",
		"authentication_error", "oauth token has expired", "api error: 401"):
		return outboundmsg.ChatErrNotSignedIn
	case containsAny(text, "usage limit", "rate limit", "rate_limit", "quota", "api error: 429", "overloaded"):
		if codex {
			return outboundmsg.ChatErrCodexLimit
		}
		return outboundmsg.ChatErrLimit
	case containsAny(text, "timed out", "timeout"):
		return outboundmsg.ChatErrTimeout
	}
	if inDM && fromCLI {
		if codex {
			return outboundmsg.ChatErrCodex(ce.Message)
		}
		return outboundmsg.ChatErrClaude(ce.Message)
	}
	return outboundmsg.ChatErrGeneric
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
