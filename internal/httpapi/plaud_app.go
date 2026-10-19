package httpapi

import (
	"errors"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

type PlaudAppService interface {
	SaveDeveloper(plaud.Developer) error
	BuildApp() error
	AppFile() string
	RevokeDevice(string) error
}
type PlaudAppAPI struct{ Plaud func() PlaudAppService }

func (a *PlaudAppAPI) Credentials(w http.ResponseWriter, r *http.Request) {
	var d plaud.Developer
	if err := DecodeJSON(r, &d); err != nil {
		WriteErr(w, http.StatusBadRequest, plaud.DecodeError(err).Error())
		return
	}
	if err := a.Plaud().SaveDeveloper(d); err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

func (a *PlaudAppAPI) Build(w http.ResponseWriter, r *http.Request) {
	switch err := a.Plaud().BuildApp(); {
	case errors.Is(err, plaud.ErrAppBuilding):
		WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		WriteErr(w, http.StatusPreconditionFailed, err.Error())
	default:
		WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "building"})
	}
}

func (a *PlaudAppAPI) Download(w http.ResponseWriter, r *http.Request) {
	if !fsx.Exists(a.Plaud().AppFile()) {
		WriteErr(w, http.StatusNotFound, "no phone app built yet")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="plaud-sync.apk"`)
	http.ServeFile(w, r, a.Plaud().AppFile())
}

func (a *PlaudAppAPI) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	switch err := a.Plaud().RevokeDevice(r.PathValue("id")); {
	case errors.Is(err, plaud.ErrUnknownDevice):
		WriteErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		WriteJSON(w, http.StatusOK, OperationResponse{Status: "unpaired"})
	}
}
