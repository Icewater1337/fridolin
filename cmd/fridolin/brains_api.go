package main

import (
	"encoding/json"
	"errors"
	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/registry"
	"net/http"
	"strings"
)

type BrainView struct {
	registry.View
	Runtime *brain.Runtime `json:"runtime,omitempty"`
	// Fallback marks the brain that takes the chats no other brain
	// claims (BrainManager.Fallback). Without a default brain that is the
	// first one, which no brain.json says, so the dashboard names it.
	Fallback bool `json:"fallback"`
}

type BrainRegistry interface {
	Inspect(string) (map[string]any, error)
	Add(registry.AddInput) (registry.AddResult, error)
	Update(string, manifest.Manifest) error
	Approve(string, manifest.Manifest) error
	Remove(string) error
	RemoveDir(string) error
}
type BrainsAPI struct {
	Registry func() BrainRegistry
	Views    func() []BrainView
}

func (a *BrainsAPI) List(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSONIfChanged(w, r, a.Views())
}

// apiBrain answers GET /api/brains/{id} with that brain's entry of
// /api/brains, so the Brain page can follow its brain without fetching
// every other one with it. Two folders whose brain.json claim one id are
// both listed there, and here the first is, as the page showed it when it
// picked its brain out of the list. It builds the whole list on purpose:
// reading only this brain's folder would be cheaper, but could tell the
// page something other than the list does (which folder wins, its issues),
// and saving the download is what matters.
func (a *BrainsAPI) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, v := range a.Views() {
		if v.ID == id {
			httpapi.WriteJSONIfChanged(w, r, v)
			return
		}
	}
	httpapi.WriteErr(w, http.StatusNotFound, "no such brain")
}

func (a *BrainsAPI) Inspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := a.Registry().Inspect(req.Path)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

// apiAddBrain turns a folder into a brain: scaffold what is missing, write
// brain.json, register it, and start routing to it.
func (a *BrainsAPI) Add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		// Manifest is decoded below, where a backup setting left out must
		// be told from one set to false.
		Manifest  json.RawMessage `json:"manifest"`
		CreateDir bool            `json:"create_dir"`
		// UseExisting adopts a brain.json already in the folder (after the
		// dashboard showed it to you) instead of writing Manifest.
		UseExisting bool `json:"use_existing"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var m manifest.Manifest
	backupGiven := false
	if !req.UseExisting {
		if len(req.Manifest) == 0 {
			httpapi.WriteErr(w, http.StatusBadRequest, "manifest required (or use_existing, to add the brain.json in the folder)")
			return
		}
		if err := manifest.JSONError(httpapi.DecodeStrict(req.Manifest, &m)); err != nil {
			httpapi.WriteErr(w, http.StatusBadRequest, "manifest: "+err.Error())
			return
		}
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(req.Manifest, &keys)
		backupGiven = keys["backup"] != nil && string(keys["backup"]) != "null"
	}
	result, err := a.Registry().Add(registry.AddInput{Path: req.Path, Manifest: m, BackupGiven: backupGiven, CreateDir: req.CreateDir, UseExisting: req.UseExisting})
	if err != nil {
		status := http.StatusUnprocessableEntity
		switch apperr.KindOf(err) {
		case apperr.Conflict:
			status = http.StatusConflict
		case apperr.Unknown:
			status = http.StatusInternalServerError
		}
		httpapi.WriteErr(w, status, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"id": result.ID, "path": result.Path, "created": result.Created})
}

// apiUpdateBrain saves edited settings for a brain.
func (a *BrainsAPI) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Manifest manifest.Manifest `json:"manifest"`
	}
	if err := manifest.JSONError(httpapi.DecodeJSON(r, &req)); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.Registry().Update(id, req.Manifest)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiApproveBrain accepts the brain.json on disk, provided it is still the
// one the dashboard showed you: the request carries that manifest. A brain
// can rewrite its own brain.json at any moment, and approving whatever is
// on disk when the click arrives would grant it a change, routing say, that
// you never saw, without the confirmation such a change asks for.
func (a *BrainsAPI) Approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Manifest *manifest.Manifest `json:"manifest"`
	}
	if err := manifest.JSONError(httpapi.DecodeJSON(r, &req)); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Manifest == nil {
		httpapi.WriteErr(w, http.StatusBadRequest, "manifest required: send the brain.json you reviewed")
		return
	}
	err := a.Registry().Approve(id, *req.Manifest)
	switch {
	case errors.Is(err, registry.ErrReviewedManifestChanged):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// apiRemoveBrain stops routing to a brain. Its folder is left exactly as it is.
func (a *BrainsAPI) Remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Registry().Remove(id)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// apiRemoveBrainDir removes the brain_dirs entry for the folder ?path= that
// has no approved manifest. Such an entry has no id, so it cannot be
// removed as a brain; it comes from a hand-edited config.json. Its folder
// is left exactly as it is.
func (a *BrainsAPI) RemoveDir(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "path required")
		return
	}
	err := a.Registry().RemoveDir(path)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
