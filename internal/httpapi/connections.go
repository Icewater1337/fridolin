package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// ConnectionsAPI serves Fridolin's connections to OpenAI and ElevenLabs
// on the Integrations page (docs/CONNECTIONS.md): the keys, which brains
// may use each and this month's usage. A key goes in and never comes
// back: the page learns whether one is set and its last characters.
type ConnectionsAPI struct {
	// View is GET /api/connections.
	View func(context.Context) (any, error)
	// SetKey saves a connection's key, and OpenAI's organization and
	// project, and answers its entry.
	SetKey func(ctx context.Context, id string, req ConnectionKeyRequest) (any, error)
	// RemoveKey deletes a connection's key.
	RemoveKey func(ctx context.Context, id string) error
	// SetBrains writes connections.<id> in config.json and answers the
	// connection's entry.
	SetBrains func(ctx context.Context, id string, req ConnectionBrainsRequest) (any, error)
	// Check asks the provider whether the key works.
	Check func(ctx context.Context, id string) (any, error)
}

// ConnectionKeyRequest is the body of PUT /api/connections/{id}. A blank
// api_key keeps the key there is; organization and project, OpenAI's
// only, keep theirs when left out.
type ConnectionKeyRequest struct {
	APIKey       string  `json:"api_key"`
	Organization *string `json:"organization"`
	Project      *string `json:"project"`
}

// ConnectionBrainsRequest is the body of PUT /api/connections/{id}/brains.
// What is left out stays as it is; voice is ElevenLabs' only.
type ConnectionBrainsRequest struct {
	Brains          *[]string `json:"brains"`
	BrainMonthlyUSD *float64  `json:"brain_monthly_usd"`
	Voice           *string   `json:"voice"`
}

// writeStatusErr answers err with the status it carries
// (HTTPStatus() int), else its kind's.
func writeStatusErr(w http.ResponseWriter, err error) {
	var s interface{ HTTPStatus() int }
	if errors.As(err, &s) {
		WriteErr(w, s.HTTPStatus(), err.Error())
		return
	}
	WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
}

// Register adds the routes.
func (a *ConnectionsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/connections", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.View(r.Context())
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("PUT /api/connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req ConnectionKeyRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.SetKey(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /api/connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RemoveKey(r.Context(), r.PathValue("id")); err != nil {
			writeStatusErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /api/connections/{id}/brains", func(w http.ResponseWriter, r *http.Request) {
		var req ConnectionBrainsRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.SetBrains(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /api/connections/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if err := DecodeOptionalJSON(r, &req); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.Check(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
}
