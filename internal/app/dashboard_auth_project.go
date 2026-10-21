package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newDashboardAuth() *httpapi.DashboardAuth {
	return &httpapi.DashboardAuth{
		Sessions:   func() httpapi.DashboardSessionStore { return b.sessions },
		Token:      func() httpapi.DashboardAuthToken { return b.token },
		CookieName: func() string { return b.sessionCookie },
		TailnetURL: b.tailnetDashboardURL,
	}
}

// tailnetDashboardURL is where the dashboard answers on the tailnet, ""
// while the core is not on it (core.tailnet) or has not joined yet.
func (b *App) tailnetDashboardURL() string {
	if st := b.tailnetStatus(); st != nil && st.State == "up" {
		return st.URL
	}
	return ""
}
