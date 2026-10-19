package main

import (
	"context"
	"errors"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"strings"
)

// cliError is the error the CLI itself reported in its result event, as
// opposed to a failure of the process or of the bridge. Its text is the
// CLI's own short account, such as "Credit balance is too low".
type cliError struct{ msg, provider string }

func (e *cliError) Error() string { return agentName(e.provider) + " error: " + e.msg }

// chatErrorText is what a chat is told about a run that failed with err,
// one of the chatErr messages of messages.go, or "" for a run that was
// stopped, which says nothing. inDM is true for a direct chat with an
// allowed number: there alone the CLI's own error text may show, since
// only the person who asked reads it.
func chatErrorText(err error, inDM bool) string {
	if err == nil || errors.Is(err, errBrainInterrupted) || errors.Is(err, context.Canceled) {
		return ""
	}
	if errors.Is(err, errSandboxUnavailable) {
		return outboundmsg.ChatErrSandbox(errSandboxUnavailable)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errRunTimedOut) {
		return outboundmsg.ChatErrTimeout
	}
	text := strings.ToLower(err.Error())
	var ce *cliError
	fromCLI := errors.As(err, &ce)
	codex := (fromCLI && ce.provider == "codex") || strings.HasPrefix(text, "codex ")
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
			return outboundmsg.ChatErrCodex(ce.msg)
		}
		return outboundmsg.ChatErrClaude(ce.msg)
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
