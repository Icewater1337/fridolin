package main

import (
	"context"
	"errors"
	"strings"
)

// cliError is the error the CLI itself reported in its result event, as
// opposed to a failure of the process or of the bridge. Its text is the
// CLI's own short account, such as "Credit balance is too low".
type cliError struct{ msg string }

func (e *cliError) Error() string { return "claude error: " + e.msg }

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
	switch {
	case containsAny(text, "not logged in", "please run /login", "invalid api key", "failed to authenticate",
		"authentication_error", "oauth token has expired", "api error: 401"):
		return chatErrNotSignedIn
	case containsAny(text, "usage limit", "rate limit", "rate_limit", "quota", "api error: 429", "overloaded"):
		return chatErrLimit
	case containsAny(text, "timed out", "timeout"):
		return chatErrTimeout
	}
	var ce *cliError
	if inDM && errors.As(err, &ce) {
		return chatErrClaude(ce.msg)
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
