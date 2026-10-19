package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type BuilderAPI struct {
	BrainForToken func(string) string
	Context       func(string) any
	Check         func(context.Context, string) (any, error)
	Preview       func(context.Context, string) (any, error)
	Sync          func(context.Context, string) (string, error)
	Propose       func(context.Context, string, string, string) (any, any, error)
}

// builderHandler serves /builder/ for the MCP servers of builder brains.
// Each brain's token works only for its own workspace.
func (a *BuilderAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	call := func(fn func(r *http.Request, brainID string) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			brainID := a.BrainForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if brainID == "" {
				WriteErr(w, http.StatusUnauthorized, "this brain may not build the site (Fridolin dashboard → Site → Builders)")
				return
			}
			out, err := fn(r, brainID)
			if err != nil {
				WriteErr(w, http.StatusBadRequest, err.Error())
				return
			}
			WriteJSON(w, http.StatusOK, out)
		}
	}
	mux.HandleFunc("GET /builder/context", call(func(r *http.Request, id string) (any, error) {
		return a.Context(id), nil
	}))
	mux.HandleFunc("POST /builder/check", call(func(r *http.Request, id string) (any, error) {
		return a.Check(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/preview", call(func(r *http.Request, id string) (any, error) {
		return a.Preview(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/sync", call(func(r *http.Request, id string) (any, error) {
		out, err := a.Sync(r.Context(), id)
		return map[string]string{"result": out}, err
	}))
	mux.HandleFunc("POST /builder/propose", call(func(r *http.Request, id string) (any, error) {
		var req struct{ Title, Summary string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024)).Decode(&req); err != nil {
			return nil, err
		}
		p, changes, err := a.Propose(r.Context(), id, req.Title, req.Summary)
		return map[string]any{"proposal": p, "changes": changes}, err
	}))
	return mux
}
