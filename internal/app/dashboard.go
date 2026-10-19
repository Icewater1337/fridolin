package app

import (
	"net/http"

	"github.com/Icewater1337/fridolin/internal/version"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/web"
)

// requiresToken reports whether API calls need the token. Turning
// dashboard.require_token off waits for a restart, while the dashboard
// warns about it: one saved setting must not drop the only thing keeping
// other programs on this computer out of the API at once. Turning it on
// applies at once.
func (b *App) requiresToken() bool {
	return b.tokenAtStart || b.cfgs.Effective().DashboardRequiresToken()
}

func (b *App) newHTTPHandler(listen string) http.Handler {
	b.sessionCookie = httpapi.SessionCookieName(listen)
	return httpapi.New(httpapi.Deps{
		Listen: listen, Ready: func(w http.ResponseWriter, r *http.Request) {
			httpapi.ReadyAPI{Connection: b.wa, DevMode: b.dev != nil, Version: version.String, StartedAt: b.started}.ServeHTTP(w, r)
		}, Send: b.newScriptAPI().Send, Ingest: b.newScriptAPI().Ingest,
		Builder: b.builderHandler, Brain: b.brainAPIHandler,
		DashboardDisabled: func() bool { return b.cfgs.Effective().Dashboard.Disabled }, Static: web.FS,
		GoogleCallback: func(w http.ResponseWriter, r *http.Request) { b.newGoogleAPI().Callback(w, r) },
		Login:          b.newDashboardAuth().Login, Token: b.token, RequiresToken: b.requiresToken,
		SignedIn: b.newDashboardAuth().SignedIn, API: b.apiMux,
	})
}
