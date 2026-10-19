package main

import (
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
)

type APIDeps struct {
	Status     http.HandlerFunc
	Activity   http.HandlerFunc
	Logs       http.HandlerFunc
	Restart    http.HandlerFunc
	Site       func(*http.ServeMux)
	DevMode    bool
	Connection func() *httpapi.ConnectionAPI
	Chats      func() *httpapi.ChatsAPI
	Config     func() *httpapi.ConfigAPI
	Brains     func() *httpapi.BrainsAPI
	Filesystem func() *httpapi.FilesystemAPI
	Backup     func() *httpapi.BackupAPI
	Google     func() *httpapi.GoogleAPI
	Plaud      func() *httpapi.PlaudAPI
	PlaudApp   func() *httpapi.PlaudAppAPI
	Auth       func() *httpapi.DashboardAuth
	Dev        func() *httpapi.DevAPI
}

func NewAPIMux(d APIDeps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", d.Status)
	mux.HandleFunc("GET /api/whatsapp/qr", d.Connection().QR)
	mux.HandleFunc("POST /api/whatsapp/pair", d.Connection().Pair)
	mux.HandleFunc("GET /api/whatsapp/groups", d.Connection().Groups)
	mux.HandleFunc("GET /api/activity", d.Activity)
	mux.HandleFunc("GET /api/logs", d.Logs)

	mux.HandleFunc("GET /api/chats/ignored", d.Chats().Ignored)
	mux.HandleFunc("POST /api/chats/allow", d.Chats().Allow)
	mux.HandleFunc("POST /api/chats/revoke", d.Chats().Revoke)
	mux.HandleFunc("POST /api/chats/members", d.Chats().Members)
	mux.HandleFunc("POST /api/chats/dismiss", d.Chats().Dismiss)

	mux.HandleFunc("GET /api/config", d.Config().Get)
	mux.HandleFunc("PUT /api/config", d.Config().Put)
	mux.HandleFunc("POST /api/restart", d.Restart)

	mux.HandleFunc("POST /api/logout", d.Auth().Logout)
	mux.HandleFunc("POST /api/login/code", d.Auth().LoginCode)
	mux.HandleFunc("POST /api/token/rotate", d.Auth().RotateToken)

	mux.HandleFunc("GET /api/brains", d.Brains().List)
	mux.HandleFunc("GET /api/brains/{id}", d.Brains().Get)
	mux.HandleFunc("POST /api/brains/inspect", d.Brains().Inspect)
	mux.HandleFunc("POST /api/brains", d.Brains().Add)
	mux.HandleFunc("PUT /api/brains/{id}", d.Brains().Update)
	mux.HandleFunc("POST /api/brains/{id}/approve", d.Brains().Approve)
	mux.HandleFunc("DELETE /api/brains/{id}", d.Brains().Remove)
	mux.HandleFunc("POST /api/brains/{id}/stop", d.Connection().StopRun)
	mux.HandleFunc("DELETE /api/brain-dirs", d.Brains().RemoveDir)

	mux.HandleFunc("GET /api/fs/capabilities", d.Filesystem().Caps)
	mux.HandleFunc("POST /api/fs/pick", d.Filesystem().Pick)
	mux.HandleFunc("GET /api/fs/list", d.Filesystem().List)

	mux.HandleFunc("GET /api/backup/status", d.Backup().Status)
	mux.HandleFunc("POST /api/backup/connect", d.Backup().Connect)
	mux.HandleFunc("POST /api/backup/disconnect", d.Backup().Disconnect)
	mux.HandleFunc("PUT /api/backup/settings", d.Backup().Settings)
	mux.HandleFunc("POST /api/backup/run", d.Backup().Run)
	mux.HandleFunc("POST /api/backup/cancel", d.Backup().Cancel)
	mux.HandleFunc("POST /api/backup/restore", d.Backup().Restore)
	mux.HandleFunc("PUT /api/brains/{id}/backup", d.Backup().BrainSettings)
	mux.HandleFunc("GET /api/brains/{id}/backup/versions", d.Backup().Versions)

	mux.HandleFunc("GET /api/google/client", d.Google().Client)
	mux.HandleFunc("PUT /api/google/client", d.Google().SetClient)
	mux.HandleFunc("DELETE /api/google/client", d.Google().DeleteClient)
	mux.HandleFunc("GET /api/brains/{id}/calendar", d.Google().Status)
	mux.HandleFunc("POST /api/brains/{id}/calendar/connect", d.Google().Connect)
	mux.HandleFunc("PUT /api/brains/{id}/calendar", d.Google().Select)
	mux.HandleFunc("DELETE /api/brains/{id}/calendar", d.Google().Disconnect)

	mux.HandleFunc("GET /api/plaud/status", d.Plaud().Status)
	mux.HandleFunc("POST /api/plaud/connect", d.Plaud().Connect)
	mux.HandleFunc("POST /api/plaud/disconnect", d.Plaud().Disconnect)
	mux.HandleFunc("PUT /api/plaud/settings", d.Plaud().Settings)
	mux.HandleFunc("POST /api/plaud/sync", d.Plaud().Sync)
	mux.HandleFunc("POST /api/plaud/resend", d.Plaud().Resend)
	mux.HandleFunc("PUT /api/plaud/app/credentials", d.PlaudApp().Credentials)
	mux.HandleFunc("POST /api/plaud/app/build", d.PlaudApp().Build)
	mux.HandleFunc("GET /api/plaud/app.apk", d.PlaudApp().Download)
	mux.HandleFunc("DELETE /api/plaud/devices/{id}", d.PlaudApp().RevokeDevice)

	d.Site(mux)

	// Only in dev mode: outside it they are not there at all (404).
	if d.DevMode {
		mux.HandleFunc("POST /api/dev/inbound", d.Dev().Inbound)
		mux.HandleFunc("GET /api/dev/outbox", d.Dev().Outbox)
	}

	// No catch-all: the mux answers an unknown path with 404 and a known
	// one asked with the wrong method with 405 and an Allow header.
	return mux
}
