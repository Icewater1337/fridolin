package main

import (
	"errors"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *Bridge) apiPlaudAppCredentials(w http.ResponseWriter, r *http.Request) {
	var d plaudDeveloper
	if err := httpapi.DecodeJSON(r, &d); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.plaud.SaveDeveloper(d); err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiPlaudAppBuild(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.BuildApp(); {
	case errors.Is(err, errPlaudAppBuilding):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		httpapi.WriteErr(w, http.StatusPreconditionFailed, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "building"})
	}
}

func (b *Bridge) apiPlaudAppDownload(w http.ResponseWriter, r *http.Request) {
	if !fsx.Exists(b.plaud.AppFile()) {
		httpapi.WriteErr(w, http.StatusNotFound, "no phone app built yet")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="plaud-sync.apk"`)
	http.ServeFile(w, r, b.plaud.AppFile())
}

func (b *Bridge) apiPlaudRevokeDevice(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.RevokeDevice(r.PathValue("id")); {
	case errors.Is(err, errPlaudUnknownDevice):
		httpapi.WriteErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
	}
}
