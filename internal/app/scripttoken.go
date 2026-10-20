package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// errTokenRequired answers a request without a token where one is needed.
var errTokenRequired = errors.New("a token is required: send the one in <data_dir>/scripts.token as Authorization: Bearer …")

// errBadToken answers a request whose token is not one the bridge gave out.
var errBadToken = errors.New("unknown token: send the one in <data_dir>/scripts.token, or in a brain's run the one in the file FRIDOLIN_BRAIN_TOKEN_FILE names, as Authorization: Bearer …")

// scriptAuth holds the script API's tokens.
type scriptAuth struct {
	scripts *httpapi.Token // <data_dir>/scripts.token
	// brainOf is the brain a run's token belongs to, "" for none: the
	// bridge's runTokens, so the token is gone once the run is over.
	brainOf func(tok string) string
	exists  func(id string) bool
}

// newScriptAuth loads or creates the scripts token in dataDir. brainOf
// names the brain of a run's token; exists reports whether that brain is
// still configured, so a removed brain's run cannot call.
func newScriptAuth(dataDir string, brainOf func(tok string) string, exists func(id string) bool) (*scriptAuth, error) {
	tok, err := httpapi.LoadToken(config.Layout{Root: dataDir}.ScriptsToken())
	if err != nil {
		return nil, err
	}
	return &scriptAuth{scripts: tok, brainOf: brainOf, exists: exists}, nil
}

// authorize says who sent r, from its Authorization header. No header is
// CallerAnonymous; a token the bridge did not give out, one of a run that
// is over or one of a brain that is gone, is errBadToken rather than a
// fall back to anonymous.
func (a *scriptAuth) authorize(r *http.Request) (chat.Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return chat.Principal{Kind: chat.CallerAnonymous}, nil
	}
	// The scheme is case-insensitive (RFC 7235): some clients send "bearer".
	scheme, tok, _ := strings.Cut(strings.TrimSpace(h), " ")
	tok = strings.TrimSpace(tok)
	if !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return chat.Principal{}, errBadToken
	}
	if a.scripts.Matches(tok) {
		return chat.Principal{Kind: chat.CallerScript}, nil
	}
	id := a.brainOf(tok)
	if id == "" || !a.exists(id) {
		return chat.Principal{}, errBadToken
	}
	return chat.Principal{Kind: chat.CallerBrain, Brain: id}, nil
}
