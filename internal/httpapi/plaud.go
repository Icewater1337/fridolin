package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// PlaudService is the recorder workflow surface used by HTTP handlers.
type PlaudService interface {
	Resend(string) error
	Status() plaud.Status
	Connect() error
	Disconnect() error
	CheckNow() (bool, error)
}

type PlaudAPI struct {
	Plaud        func() PlaudService
	CurrentBrain func() string
	HasBrain     func(string) bool
	UpdateConfig func(func(*config.File) error) error
}

func (a *PlaudAPI) Resend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := a.Plaud().Resend(req.ID); {
	case errors.Is(err, plaud.ErrUnknownRecording):
		WriteErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, plaud.ErrAlreadyWaiting):
		WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "waiting"})
	}
}

func (a *PlaudAPI) Status(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.Plaud().Status())
}

func (a *PlaudAPI) Connect(w http.ResponseWriter, r *http.Request) {
	switch err := a.Plaud().Connect(); {
	case errors.Is(err, plaud.ErrNotInstalled):
		WriteErr(w, http.StatusPreconditionFailed, "Install Plaud's CLI first: npm install -g @plaud-ai/cli")
	case errors.Is(err, plaud.ErrSigningIn):
		WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "connecting"})
	}
}

func (a *PlaudAPI) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.Plaud().Disconnect(); err != nil {
		WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "disconnected"})
}

func (a *PlaudAPI) Settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled     *bool   `json:"enabled"`
		Brain       *string `json:"brain"`
		Polished    *bool   `json:"polished"`
		Message     *string `json:"message"`
		BrainAccess *bool   `json:"brain_access"`
		PhoneUpload *bool   `json:"phone_upload"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only a newly picked brain must exist: one that went away since must
	// not keep the other settings from being saved.
	if req.Brain != nil && *req.Brain != "" && *req.Brain != a.CurrentBrain() && !a.HasBrain(*req.Brain) {
		WriteErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("there is no approved brain %q", *req.Brain))
		return
	}
	err := a.UpdateConfig(func(c *config.File) error {
		if req.Enabled != nil {
			c.Plaud.Enabled = *req.Enabled
		}
		if req.Brain != nil {
			c.Plaud.Brain = *req.Brain
		}
		if req.Polished != nil {
			c.Plaud.Polished = *req.Polished
		}
		if req.Message != nil {
			c.Plaud.Message = *req.Message
		}
		if req.BrainAccess != nil {
			c.Plaud.BrainAccess = *req.BrainAccess
		}
		if req.PhoneUpload != nil {
			c.Plaud.PhoneUpload = *req.PhoneUpload
		}
		return nil
	})
	if err != nil {
		WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

func (a *PlaudAPI) Sync(w http.ResponseWriter, r *http.Request) {
	started, err := a.Plaud().CheckNow()
	switch {
	case errors.Is(err, plaud.ErrNotConnected):
		WriteErr(w, http.StatusPreconditionFailed, "Plaud is not connected")
	case errors.Is(err, plaud.ErrSendingOff):
		WriteErr(w, http.StatusConflict, "Turn on “Send new recordings to a brain” first: a check hands new recordings to the brain")
	case errors.Is(err, plaud.ErrStopping):
		WriteErr(w, http.StatusServiceUnavailable, "Fridolin is stopping: the next start checks Plaud")
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, err.Error())
	case !started:
		// A check that is already running covers this one; say so rather
		// than claim to start one.
		WriteJSON(w, http.StatusOK, OperationResponse{Status: "already checking"})
	default:
		WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "syncing"})
	}
}
