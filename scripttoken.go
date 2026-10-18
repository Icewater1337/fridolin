package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
)

// Capability tokens for the script API, /send and /ingest (docs/API.md).
//
// Brains run on this computer and can reach the API with curl, so without
// tokens any brain, one taken over by a message included, could hand work
// to another brain (which then runs with that brain's folder, memories and
// credentials) or message any chat. Each caller now says who it is:
//
//   - a brain sends its run's token, the brain API's (brainapi.go), from
//     the file FRIDOLIN_BRAIN_TOKEN_FILE names. It works only while the run
//     lasts, and the brain may send with it only to its own chats and hand
//     files only to itself;
//   - your own scripts send the token in <data_dir>/scripts.token, which the
//     sandbox denies to brains, and may do what the API allows;
//   - a caller without a token may only /send, to the allowed chats, and
//     only while script_api.tokenless_send is on (it is, for scripts written
//     before tokens).
//
// Like the dashboard token, the scripts token keeps brains out only while
// they cannot read the data dir, which takes the macOS sandbox.

// principalKind says who called the script API.
type principalKind int

const (
	// callerAnonymous sent no token.
	callerAnonymous principalKind = iota
	// callerScript sent the scripts token.
	callerScript
	// callerBrain sent a brain's token.
	callerBrain
)

// principal is who called the script API.
type principal struct {
	kind  principalKind
	brain string // for callerBrain
}

// via is how a message the caller sent is marked in history.
func (p principal) via() string {
	switch p.kind {
	case callerScript:
		return "script"
	case callerBrain:
		return "brain"
	}
	return "api"
}

// errTokenRequired answers a request without a token where one is needed.
var errTokenRequired = errors.New("a token is required: send the one in <data_dir>/scripts.token as Authorization: Bearer …")

// errBadToken answers a request whose token is not one the bridge gave out.
var errBadToken = errors.New("unknown token: send the one in <data_dir>/scripts.token, or in a brain's run the one in the file FRIDOLIN_BRAIN_TOKEN_FILE names, as Authorization: Bearer …")

// scriptAuth holds the script API's tokens.
type scriptAuth struct {
	scripts *DashboardToken // <data_dir>/scripts.token
	// brainOf is the brain a run's token belongs to, "" for none: the
	// bridge's runTokens, so the token is gone once the run is over.
	brainOf func(tok string) string
	exists  func(id string) bool
}

// newScriptAuth loads or creates the scripts token in dataDir. brainOf
// names the brain of a run's token; exists reports whether that brain is
// still configured, so a removed brain's run cannot call.
func newScriptAuth(dataDir string, brainOf func(tok string) string, exists func(id string) bool) (*scriptAuth, error) {
	tok, err := loadTokenFile(filepath.Join(dataDir, "scripts.token"))
	if err != nil {
		return nil, err
	}
	return &scriptAuth{scripts: tok, brainOf: brainOf, exists: exists}, nil
}

// authorize says who sent r, from its Authorization header. No header is
// callerAnonymous; a token the bridge did not give out, one of a run that
// is over or one of a brain that is gone, is errBadToken rather than a
// fall back to anonymous.
func (a *scriptAuth) authorize(r *http.Request) (principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return principal{kind: callerAnonymous}, nil
	}
	// The scheme is case-insensitive (RFC 7235): some clients send "bearer".
	scheme, tok, _ := strings.Cut(strings.TrimSpace(h), " ")
	tok = strings.TrimSpace(tok)
	if !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return principal{}, errBadToken
	}
	if a.scripts.Matches(tok) {
		return principal{kind: callerScript}, nil
	}
	id := a.brainOf(tok)
	if id == "" || !a.exists(id) {
		return principal{}, errBadToken
	}
	return principal{kind: callerBrain, brain: id}, nil
}
