package google

import "strings"

// The built-in OAuth client. A publisher who builds Fridolin for other
// people can bake a Desktop app client of their own Google Cloud project
// into the binary, so that its users connect calendars and Drive with one
// click, without a Google Cloud project of their own:
//
//	go build -ldflags "-X github.com/Icewater1337/fridolin/internal/google.builtinClientID=… \
//	  -X github.com/Icewater1337/fridolin/internal/google.builtinClientSecret=…"
//
// `make build` and scripts/build-release.sh pass FRIDOLIN_GOOGLE_CLIENT_ID
// and FRIDOLIN_GOOGLE_CLIENT_SECRET from the environment, so the values
// never appear on a command line make prints. Both are empty in the public
// repository's builds, which then have no built-in client. A Desktop app
// client's secret is not a secret in Google's sense (an installed app
// cannot keep one), but the bridge still never shows or logs it.
//
// A client pasted under Settings → Google sign-in always wins over this
// one (Auth.Client).
var builtinClientID, builtinClientSecret string

// BuiltinClient is the client baked into this build, or nil. It is always
// a Desktop app client: only that type may return to the bridge on a
// loopback address without a redirect URI registered for it.
func BuiltinClient() *Client {
	id, secret := strings.TrimSpace(builtinClientID), strings.TrimSpace(builtinClientSecret)
	if id == "" || secret == "" {
		return nil
	}
	return &Client{Type: "installed", ClientID: id, ClientSecret: secret, Source: SourceBuiltin}
}

// Where the client a sign-in uses comes from.
const (
	// SourceOwn is the client pasted under Settings → Google sign-in.
	SourceOwn = "own"
	// SourceBuiltin is the client baked into the build (BuiltinClient).
	SourceBuiltin = "builtin"
)
