package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

func (b *Bridge) apiPlaudResend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := b.plaud.Resend(req.ID); {
	case errors.Is(err, plaud.ErrUnknownRecording):
		httpapi.WriteErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, plaud.ErrAlreadyWaiting):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "waiting"})
	}
}

func (b *Bridge) apiPlaudStatus(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, b.plaud.Status())
}

func (b *Bridge) apiPlaudConnect(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.Connect(); {
	case errors.Is(err, plaud.ErrNotInstalled):
		httpapi.WriteErr(w, http.StatusPreconditionFailed, "Install Plaud's CLI first: npm install -g @plaud-ai/cli")
	case errors.Is(err, plaud.ErrSigningIn):
		httpapi.WriteErr(w, http.StatusConflict, err.Error())
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
	}
}

func (b *Bridge) apiPlaudDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := b.plaud.Disconnect(); err != nil {
		httpapi.WriteErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (b *Bridge) apiPlaudSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled     *bool   `json:"enabled"`
		Brain       *string `json:"brain"`
		Polished    *bool   `json:"polished"`
		Message     *string `json:"message"`
		BrainAccess *bool   `json:"brain_access"`
		PhoneUpload *bool   `json:"phone_upload"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only a newly picked brain must exist: one that went away since must
	// not keep the other settings from being saved.
	if req.Brain != nil && *req.Brain != "" && *req.Brain != b.cfgs.Get().Plaud.Brain && b.brainMgr.Get(*req.Brain) == nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("there is no approved brain %q", *req.Brain))
		return
	}
	_, err := b.cfgs.Update("", func(c *config.Config) error {
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
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiPlaudSync(w http.ResponseWriter, r *http.Request) {
	started, err := b.plaud.CheckNow()
	switch {
	case errors.Is(err, plaud.ErrNotConnected):
		httpapi.WriteErr(w, http.StatusPreconditionFailed, "Plaud is not connected")
	case errors.Is(err, plaud.ErrSendingOff):
		httpapi.WriteErr(w, http.StatusConflict, "Turn on “Send new recordings to a brain” first: a check hands new recordings to the brain")
	case errors.Is(err, plaud.ErrStopping):
		httpapi.WriteErr(w, http.StatusServiceUnavailable, "Fridolin is stopping: the next start checks Plaud")
	case err != nil:
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
	case !started:
		// A check that is already running covers this one; say so rather
		// than claim to start one.
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "already checking"})
	default:
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
	}
}
