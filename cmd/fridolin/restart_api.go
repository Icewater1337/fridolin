package main

import (
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"log"
	"net/http"
)

type RestartAPI struct {
	Available func() bool
	Restart   func()
}

func (a *RestartAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.Available() {
		httpapi.WriteErr(w, http.StatusServiceUnavailable, "this bridge cannot restart itself")
		return
	}
	log.Printf("Restarting on request from the dashboard…")
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	a.Restart()
}
