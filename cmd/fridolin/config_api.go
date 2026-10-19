package main

import (
	"encoding/json"
	"errors"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
)

type ConfigStore interface {
	Snapshot() config.Snapshot
	Path() string
	UpdateTx(string, func(*config.Config, *config.Tx) error) (config.UpdateResult, error)
}
type ConfigAPI struct{ Store func() ConfigStore }

func (a *ConfigAPI) Get(w http.ResponseWriter, r *http.Request) {
	// One snapshot: the etag must be that of the config sent with it, or a
	// save that comes back with it would pass the conflict check over a
	// write the page never saw.
	snap := a.Store().Snapshot()
	file := config.MustClone(snap.File)
	file.BrainDirs = nil
	eff := config.MustClone(snap.Effective)
	eff.BrainDirs = nil
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"etag":      snap.ETag,
		"path":      a.Store().Path(),
		"config":    file,
		"effective": eff,
	})
}

// apiPutConfig replaces the settings. Brains are managed through their own
// endpoints and are carried over untouched.
//
// It replaces the whole file layer, so it takes only the whole of it, as
// GET /api/config returned it (see DecodeReplacement), and only with
// the etag it came with: a script that sends one setting, or a tab that
// saves over a newer change, would otherwise reset the rest to defaults —
// the sandbox off, nobody allowed, another data dir.
func (a *ConfigAPI) Put(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ETag   string          `json:"etag"`
		Config json.RawMessage `json:"config"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ETag == "" {
		httpapi.WriteErr(w, http.StatusPreconditionRequired, "etag required: send the etag GET /api/config returned along with the config")
		return
	}
	incoming, err := config.DecodeReplacement(req.Config)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := a.Store().UpdateTx(req.ETag, func(c *config.Config, _ *config.Tx) error {
		incoming.BrainDirs = c.BrainDirs
		*c = *incoming
		return nil
	})
	switch {
	case errors.Is(err, config.ErrConflict):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The etag of what this save wrote, not ETag(), which may be a later
	// save's: the page would take that write for the one it saved.
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"etag": res.ETag, "restart_required": nonNil(res.Restart)})
}

// apiRestart restarts the bridge in place (same binary, same arguments): it
// answers, then asks whoever runs the bridge to stop it and run it again
// (BridgeOptions.Restart). The stop is the one SIGTERM makes, in main, and
// main runs the bridge again once it is over.
