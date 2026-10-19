package google

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

var repoDir string

func TestMain(m *testing.M) {
	testutil.Main(m, testutil.MainOptions{Ready: func(p testutil.Paths) { repoDir = p.RepoDir }})
}

var fakeGoogle = testutil.FakeGoogle

const testClientJSON = `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"%s/token","redirect_uris":["http://localhost"]}}`

func googleTestOptions(options Options, srv *httptest.Server) Options {
	options.Endpoints = Endpoints{Token: srv.URL + "/token", Revoke: srv.URL + "/revoke", APIBase: srv.URL}
	client := *srv.Client()
	client.Timeout = 20 * time.Second
	options.HTTP = &client
	return options
}
