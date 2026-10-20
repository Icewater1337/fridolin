package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Icewater1337/fridolin/internal/apperr"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/intent"
)

// BrainMemoryService is the memory index and the standing intents, as a
// brain's run reaches them.
type BrainMemoryService interface {
	// Search searches br's memory index: every provenance, each hit
	// labelled with its own.
	Search(ctx context.Context, br *brainruntime.Brain, q string, limit int) (any, int, error)
	// Intents lists br's standing intents.
	Intents(br *brainruntime.Brain) (any, error)
	// AddIntent sets one, for the run tok, which must answer the owner.
	AddIntent(tok string, br *brainruntime.Brain, req intent.Request) (any, error)
	// RemoveIntent removes one, for the run tok, which must answer the
	// owner.
	RemoveIntent(tok string, br *brainruntime.Brain, id string) error
}

// MemorySearchResponse is GET /brain/memory/search.
type MemorySearchResponse struct {
	Hits  any `json:"hits"`
	Count int `json:"count"`
}

// IntentsResponse lists standing intents.
type IntentsResponse struct {
	Intents any `json:"intents"`
}

func writeKindErr(w http.ResponseWriter, err error) {
	WriteErr(w, statusFor(apperr.KindOf(err)), err.Error())
}

// brainMemorySearch answers GET /brain/memory/search?q=QUERY&limit=N.
func (a *BrainAPI) brainMemorySearch(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		WriteErr(w, http.StatusBadRequest, "q is required: what to search for")
		return
	}
	limit := 10
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			WriteErr(w, http.StatusBadRequest, "limit must be a number from 1 to 100")
			return
		}
		limit = n
	}
	hits, count, err := a.Memory.Search(r.Context(), br, q, limit)
	if err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, MemorySearchResponse{Hits: hits, Count: count})
}

func (a *BrainAPI) brainIntents(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	list, err := a.Memory.Intents(br)
	if err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, IntentsResponse{Intents: list})
}

func (a *BrainAPI) brainAddIntent(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	var req intent.Request
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in, err := a.Memory.AddIntent(runToken(r), br, req)
	if err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, in)
}

func (a *BrainAPI) brainRemoveIntent(w http.ResponseWriter, r *http.Request, br *brainruntime.Brain) {
	if err := a.Memory.RemoveIntent(runToken(r), br, r.PathValue("id")); err != nil {
		writeKindErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "removed"})
}

// runToken is the run's token a brain API call carries.
func runToken(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok
}
