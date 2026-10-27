package httpapi

import (
	"context"
	"net/http"
)

// GitIdentitiesAPI serves the git identities Fridolin hands the nodes'
// FrIDEs (docs/CONNECTIONS.md, "GitLab"; docs/NODES.md, "Git identities
// for FrIDE"): the GitLab connection, whose token goes in and never comes
// back, the GitHub identity's commit name and email, and what each node's
// FrIDE got.
type GitIdentitiesAPI struct {
	// GitLab is GET /api/gitlab.
	GitLab func(context.Context) (any, error)
	// SetGitLab saves the GitLab connection and answers it.
	SetGitLab func(ctx context.Context, req GitLabRequest) (any, error)
	// RemoveGitLab deletes it.
	RemoveGitLab func(context.Context) error
	// CheckGitLab asks GitLab who the token is.
	CheckGitLab func(context.Context) (any, error)
	// SetGitHubIdentity saves the GitHub identity's commit name and email.
	SetGitHubIdentity func(ctx context.Context, req GitCommitRequest) (any, error)
	// View is GET /api/git-identities.
	View func(context.Context) (any, error)
}

// GitLabRequest is the body of PUT /api/gitlab. A blank token keeps the
// token there is; a field left out keeps its value.
type GitLabRequest struct {
	BaseURL *string `json:"base_url"`
	Token   string  `json:"token"`
	Name    *string `json:"name"`
	Email   *string `json:"email"`
}

// GitCommitRequest is the body of PUT /api/github/identity: both empty
// leave it to git's own config.
type GitCommitRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Register adds the routes.
func (a *GitIdentitiesAPI) Register(mux *http.ServeMux) {
	get := func(f func(context.Context) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			out, err := f(r.Context())
			if err != nil {
				writeStatusErr(w, err)
				return
			}
			WriteJSONIfChanged(w, r, out)
		}
	}
	mux.HandleFunc("GET /api/gitlab", get(a.GitLab))
	mux.HandleFunc("GET /api/git-identities", get(a.View))
	mux.HandleFunc("PUT /api/gitlab", func(w http.ResponseWriter, r *http.Request) {
		var req GitLabRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.SetGitLab(r.Context(), req)
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /api/gitlab", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RemoveGitLab(r.Context()); err != nil {
			writeStatusErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/gitlab/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if err := DecodeOptionalJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.CheckGitLab(r.Context())
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT /api/github/identity", func(w http.ResponseWriter, r *http.Request) {
		var req GitCommitRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.SetGitHubIdentity(r.Context(), req)
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
}
