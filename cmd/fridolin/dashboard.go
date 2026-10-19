package main

import (
	"github.com/Icewater1337/fridolin/internal/version"
	"net/http"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/web"
)

// The dashboard and its API are served by the bridge on http_listen, which is
// always a loopback address. Loopback is not enough on its own:
//
//   - Web pages in your browser can send requests to localhost (CSRF) and can
//     reach it under a hostname they control (DNS rebinding). The Host check,
//     the Origin / Sec-Fetch-Site check and the custom header on writes stop
//     both.
//   - Brains are local programs that can run curl. The dashboard can change
//     which chats reach which brain and whether the sandbox is on, so every
//     API call needs the token in <data_dir>/dashboard.token. That keeps
//     brains out only while they cannot read the data dir, which takes the
//     macOS sandbox: on Linux, or with the sandbox off, a brain with all
//     permissions can read the token, and the status page says so.
//
// /health and /ready need no token. /send and /ingest get the same Host and
// Origin checks, and capability tokens of their own (scripttoken.go).

// requiresToken reports whether API calls need the token. Turning
// dashboard.require_token off waits for a restart, while the dashboard
// warns about it: one saved setting must not drop the only thing keeping
// other programs on this computer out of the API at once. Turning it on
// applies at once.
func (b *Bridge) requiresToken() bool {
	return b.tokenAtStart || b.cfgs.Get().DashboardRequiresToken()
}

func (b *Bridge) newHTTPHandler(listen string) http.Handler {
	b.sessionCookie = httpapi.SessionCookieName(listen)
	return httpapi.New(httpapi.Deps{
		Listen: listen, Ready: func(w http.ResponseWriter, r *http.Request) {
			httpapi.ReadyAPI{Connection: b.wa, DevMode: b.dev != nil, Version: version.String, StartedAt: b.started}.ServeHTTP(w, r)
		}, Send: b.newScriptAPI().Send, Ingest: b.newScriptAPI().Ingest,
		Builder: b.builderHandler, Brain: b.brainAPIHandler,
		DashboardDisabled: func() bool { return b.cfgs.Get().Dashboard.Disabled }, Static: web.FS,
		GoogleCallback: func(w http.ResponseWriter, r *http.Request) { b.newGoogleAPI().Callback(w, r) },
		Login:          b.newDashboardAuth().Login, Token: b.token, RequiresToken: b.requiresToken,
		SignedIn: b.newDashboardAuth().SignedIn, API: b.apiMux,
	})
}
