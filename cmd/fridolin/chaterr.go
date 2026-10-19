package main

import (
	"context"
	"errors"
	"strings"
)

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
		return chatErrSandbox(errSandboxUnavailable)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errRunTimedOut) {
		return chatErrTimeout
	}
	text := strings.ToLower(err.Error())
	var ce *cliError
	fromCLI := errors.As(err, &ce)
	codex := (fromCLI && ce.Provider == "codex") || strings.HasPrefix(text, "codex ")
	if codex && containsAny(text, "not logged in", "not signed in", "401", "invalid api key", "authentication", "access token", "refresh token") {
		return chatErrCodexNotSignedIn
	}
	switch {
	case containsAny(text, "not logged in", "please run /login", "invalid api key", "failed to authenticate",
		"authentication_error", "oauth token has expired", "api error: 401"):
		return chatErrNotSignedIn
	case containsAny(text, "usage limit", "rate limit", "rate_limit", "quota", "api error: 429", "overloaded"):
		if codex {
			return chatErrCodexLimit
		}
		return chatErrLimit
	case containsAny(text, "timed out", "timeout"):
		return chatErrTimeout
	}
	if inDM && fromCLI {
		if codex {
			return chatErrCodex(ce.Message)
		}
		return chatErrClaude(ce.Message)
	}
	return chatErrGeneric
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// capitalize upper-cases the first letter of an ASCII sentence.
func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
