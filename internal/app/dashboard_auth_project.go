package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newDashboardAuth() *httpapi.DashboardAuth {
	return &httpapi.DashboardAuth{
		Sessions:   func() httpapi.DashboardSessionStore { return b.sessions },
		Token:      func() httpapi.DashboardAuthToken { return b.token },
		CookieName: func() string { return b.sessionCookie },
	}
}
