package google

import "testing"

// TestBuiltinClientFromTheBuild: the client a release build bakes in with
// -ldflags -X is a Desktop app client, and a build with neither value, or
// only one, has none.
func TestBuiltinClientFromTheBuild(t *testing.T) {
	// Serial: it sets the package variables -ldflags would.
	defer func(id, secret string) { builtinClientID, builtinClientSecret = id, secret }(builtinClientID, builtinClientSecret)
	for _, tc := range []struct{ id, secret string }{{"", ""}, {"1-x.apps.googleusercontent.com", ""}, {"", "s"}} {
		builtinClientID, builtinClientSecret = tc.id, tc.secret
		if c := BuiltinClient(); c != nil {
			t.Errorf("%q/%q: a built-in client %+v", tc.id, tc.secret, c)
		}
	}
	builtinClientID, builtinClientSecret = " 1-x.apps.googleusercontent.com\n", "GOCSPX-s"
	c := BuiltinClient()
	if c == nil || c.Type != "installed" || c.ClientID != "1-x.apps.googleusercontent.com" || c.Source != SourceBuiltin {
		t.Errorf("built-in client = %+v", c)
	}
}
