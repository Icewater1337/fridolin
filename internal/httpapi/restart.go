package httpapi

import (
	"log"
	"net/http"
)

type RestartAPI struct {
	Available func() bool
	Restart   func()
}

func (a *RestartAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.Available() {
		WriteErr(w, http.StatusServiceUnavailable, "this bridge cannot restart itself")
		return
	}
	log.Printf("Restarting on request from the dashboard…")
	WriteJSON(w, http.StatusAccepted, OperationResponse{Status: "restarting"})
	a.Restart()
}
