package main

import "github.com/Icewater1337/fridolin/internal/httpapi"

type DashboardToken = httpapi.Token

var (
	LoadDashboardToken = httpapi.LoadDashboardToken
	loadTokenFile      = httpapi.LoadToken
	readDashboardToken = httpapi.ReadToken
	loginURL           = httpapi.LoginURL
	sessionCookieName  = httpapi.SessionCookieName
	allowedHosts       = httpapi.AllowedHosts
	hostGuard          = httpapi.HostGuard
	originGuard        = httpapi.OriginGuard
	apiGuard           = httpapi.APIGuard
	checkWrite         = httpapi.CheckWrite
	securityHeaders    = httpapi.SecurityHeaders
	decodeJSON         = httpapi.DecodeJSON
	decodeOptionalJSON = httpapi.DecodeOptionalJSON
	decodeStrict       = httpapi.DecodeStrict
	writeJSON          = httpapi.WriteJSON
	writeErr           = httpapi.WriteErr
)
