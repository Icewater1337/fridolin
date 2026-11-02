package testutil

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FakeGoogle is a token, calendar-list, revocation and People search
// endpoint for OAuth tests.
func FakeGoogle(t *testing.T) (*httptest.Server, *map[string]string) {
	t.Helper()
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				got[k] = v[0]
			}
			// The made-up code google.CheckClient trades in: Google knows
			// the client unless its secret is WRONG-SECRET.
			if r.PostForm.Get("code") == "fridolin-client-check" {
				if r.PostForm.Get("client_secret") == "WRONG-SECRET" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":"invalid_client","error_description":"Unauthorized"}`)
					return
				}
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Malformed auth code."}`)
				return
			}
			if r.PostForm.Get("code") == "bad" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
				return
			}
			switch r.PostForm.Get("refresh_token") {
			case "REVOKED":
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			case "GOOGLE-DOWN":
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"backend_error"}`)
				return
			case "CLIENT-GONE":
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"ACCESS-1","refresh_token":"RT-1","expires_in":3600}`)
		case "/calendar/v3/users/me/calendarList":
			if r.Header.Get("Authorization") != "Bearer ACCESS-1" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"message":"bad token"}}`)
				return
			}
			fmt.Fprint(w, `{"items":[{"id":"team@example.com","summary":"Team","accessRole":"writer"},{"id":"you@example.com","summary":"you@example.com","primary":true,"accessRole":"owner"}]}`)
		case "/revoke":
			// Tokens named so steer the answer: Google failing, or one it
			// revoked already.
			switch tok := r.FormValue("token"); tok {
			case "FAIL-REVOKE":
				w.WriteHeader(503)
			case "GONE-REVOKE":
				got["revoked"] = tok
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_token","error_description":"Token expired or revoked"}`)
			default:
				got["revoked"] = tok
			}
		case "/v1/people:searchContacts":
			// The People API's search, for Google Contacts: each query
			// goes into people.queries, the warm-up's empty one too.
			if r.Header.Get("Authorization") != "Bearer ACCESS-1" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
				return
			}
			q := r.URL.Query().Get("query")
			got["people.queries"] += q + "|"
			if q != "Alex" {
				fmt.Fprint(w, `{}`)
				return
			}
			fmt.Fprint(w, `{"results":[
				{"person":{"names":[{"displayName":"Alex Example","metadata":{"primary":true}}],
					"phoneNumbers":[{"value":"+1 555-987-6543","type":"mobile","formattedType":"Mobile"},{"value":"555 0100","type":"work","formattedType":"Work"}]}},
				{"person":{"names":[{"displayName":"Alex Other"}],"phoneNumbers":[{"value":"001 555 123 4567","type":"home","formattedType":"Home"}]}}
			]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}
