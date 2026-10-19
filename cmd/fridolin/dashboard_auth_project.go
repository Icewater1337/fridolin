package main

func (b *Bridge) newDashboardAuth() *DashboardAuth {
	return &DashboardAuth{
		Sessions:   func() DashboardSessionStore { return b.sessions },
		Token:      func() DashboardAuthToken { return b.token },
		CookieName: func() string { return b.sessionCookie },
	}
}
