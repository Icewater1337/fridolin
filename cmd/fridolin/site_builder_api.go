package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// builderHandler serves /builder/ for the MCP servers of builder brains.
// Each brain's token works only for its own workspace.
func (b *Bridge) builderHandler() http.Handler {
	mux := http.NewServeMux()
	m := b.site
	call := func(fn func(r *http.Request, brainID string) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			brainID := m.brainForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if brainID == "" {
				httpapi.WriteErr(w, http.StatusUnauthorized, "this brain may not build the site (Fridolin dashboard → Site → Builders)")
				return
			}
			out, err := fn(r, brainID)
			if err != nil {
				httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpapi.WriteJSON(w, http.StatusOK, out)
		}
	}
	mux.HandleFunc("GET /builder/context", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderContext(id), nil
	}))
	mux.HandleFunc("POST /builder/check", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderCheck(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/preview", call(func(r *http.Request, id string) (any, error) {
		return m.BuilderPreview(r.Context(), id)
	}))
	mux.HandleFunc("POST /builder/sync", call(func(r *http.Request, id string) (any, error) {
		out, err := m.BuilderSync(r.Context(), id)
		return map[string]string{"result": out}, err
	}))
	mux.HandleFunc("POST /builder/propose", call(func(r *http.Request, id string) (any, error) {
		var req struct{ Title, Summary string }
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024)).Decode(&req); err != nil {
			return nil, err
		}
		p, changes, err := m.BuilderPropose(r.Context(), id, req.Title, req.Summary)
		return map[string]any{"proposal": p, "changes": changes}, err
	}))
	return mux
}
