package testutil

import (
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
)

// FakeGitIdentity is a git identity the companion set on a FakeFride,
// with its token, as FrIDE keeps it in <FRIDE_HOME>/secrets/git/<id>.token.
type FakeGitIdentity struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Token string `json:"-"`
}

// fakeGitID is an identity id as FrIDE takes it; "none" is reserved.
var fakeGitID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// SetGitIdentitiesSupported makes the fake a FrIDE with (the default) or
// without git identities.
func (f *FakeFride) SetGitIdentitiesSupported(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noGitIDs = !v
}

// GitIdentities are the identities the companion set, by id, with their
// tokens.
func (f *FakeFride) GitIdentities() map[string]FakeGitIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.gitIDs)
}

// gitIdentity answers the companion's git identity routes (FrIDE's
// docs/COMPANION.md): GET lists them without tokens, PUT upserts one (a
// blank token keeps the stored one), DELETE removes one (404 for one the
// companion never set). Called with mu held.
func (f *FakeFride) gitIdentity(w http.ResponseWriter, r *http.Request, id string, reply func(int, any), fail func(int, string)) {
	view := func(g FakeGitIdentity) map[string]any {
		return map[string]any{"id": g.ID, "label": g.Label, "kind": g.Kind, "host": g.Host, "name": g.Name, "email": g.Email, "managed": true, "has_token": g.Token != ""}
	}
	if f.gitIDs == nil {
		f.gitIDs = map[string]FakeGitIdentity{}
	}
	switch {
	case id == "" && r.Method == http.MethodGet:
		list := []map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(f.gitIDs)) {
			list = append(list, view(f.gitIDs[k]))
		}
		reply(http.StatusOK, map[string]any{"identities": list, "default": ""})
	case id != "" && r.Method == http.MethodPut:
		var p struct {
			Label string `json:"label"`
			Kind  string `json:"kind"`
			Host  string `json:"host"`
			Name  string `json:"name"`
			Email string `json:"email"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			fail(http.StatusBadRequest, "not JSON")
			return
		}
		if !fakeGitID.MatchString(id) || id == "none" || p.Host == "" || (p.Kind != "github" && p.Kind != "gitlab" && p.Kind != "generic") {
			fail(http.StatusBadRequest, "bad identity")
			return
		}
		g := FakeGitIdentity{ID: id, Label: p.Label, Kind: p.Kind, Host: p.Host, Name: p.Name, Email: p.Email, Token: p.Token}
		if g.Token == "" {
			g.Token = f.gitIDs[id].Token
		}
		f.gitIDs[id] = g
		reply(http.StatusOK, view(g))
	case id != "" && r.Method == http.MethodDelete:
		if _, ok := f.gitIDs[id]; !ok {
			fail(http.StatusNotFound, "no managed identity "+id)
			return
		}
		delete(f.gitIDs, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(http.StatusMethodNotAllowed, "no such route")
	}
}
