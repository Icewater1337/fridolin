package google

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// ClientCheck is what CheckClient found out about an OAuth client, item by
// item, for the dashboard to show right after the client was pasted.
type ClientCheck struct {
	// Source is SourceOwn or SourceBuiltin.
	Source string `json:"source"`
	// Type is installed (Desktop app) or web.
	Type string `json:"type"`
	// Status is the worst of the items' statuses.
	Status string            `json:"status"`
	Items  []ClientCheckItem `json:"items"`
}

// ClientCheckItem is one thing CheckClient looked at.
type ClientCheckItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// The statuses of a ClientCheck and its items.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckBad  = "bad"
)

// probeCode is the made-up authorization code CheckClient trades in. No
// sign-in ever returns it, so Google refuses it; how it refuses says
// whether it knows the client.
const probeCode = "fridolin-client-check"

// CheckClient checks the client sign-ins use (Client): that its id and
// secret look like Google's, which redirect it needs, and whether Google
// knows it. For the last it asks Google's token endpoint to trade in a
// made-up code: Google answers invalid_client to an id or secret it does
// not know (or a client that was deleted), and invalid_grant to a known
// client with a bad code. Nothing is granted or changed at Google by it.
// Google checks a Web application client's redirect URIs only at a real
// sign-in, so for one the redirect is a reminder, not a check. The result
// never holds the secret. ErrNoClient when there is no client.
func (g *Auth) CheckClient(ctx context.Context) (ClientCheck, error) {
	c := g.Client()
	if c == nil {
		return ClientCheck{}, ErrNoClient
	}
	out := ClientCheck{Source: c.Source, Type: c.Type}
	add := func(name, status, detail string) {
		out.Items = append(out.Items, ClientCheckItem{Name: name, Status: status, Detail: detail})
	}
	if strings.HasSuffix(c.ClientID, ".apps.googleusercontent.com") && !strings.ContainsAny(c.ClientID, " \t\n") {
		add("Client ID", CheckOK, "looks like a Google OAuth client ID")
	} else {
		add("Client ID", CheckBad, "does not look like a Google OAuth client ID, which ends in .apps.googleusercontent.com: copy it again from Google Cloud Console → Clients")
	}
	add("Client secret", CheckOK, "present (never shown)")
	redirect := g.RedirectURI()
	if c.Type == "web" {
		add("Redirect", CheckWarn, "a Web application client: "+redirect+" must be one of its Authorised redirect URIs, or sign-in fails with redirect_uri_mismatch. Google checks that only at the first sign-in. A Desktop app client needs none")
	} else {
		add("Redirect", CheckOK, "a Desktop app client: Google sends the browser back to "+redirect+" on this machine, with no redirect URI to register")
	}

	_, err := g.postToken(ctx, c, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {probeCode},
		"code_verifier": {randomString(48)},
		"redirect_uri":  {redirect},
	})
	var oe *oauthError
	switch {
	case err == nil:
		add("Known to Google", CheckOK, "Google accepted the client")
	case errors.As(err, &oe) && oe.code == "invalid_grant":
		add("Known to Google", CheckOK, "Google recognised the client ID and secret")
	case errors.As(err, &oe) && (oe.code == "invalid_client" || oe.code == "unauthorized_client"):
		add("Known to Google", CheckBad, "Google does not accept this client ID and secret ("+oe.message+"): copy both again from Google Cloud Console → Clients, or create a new client")
	case errors.As(err, &oe) && oe.code == "redirect_uri_mismatch":
		add("Known to Google", CheckBad, "Google refuses the redirect "+redirect+": add it to the client's Authorised redirect URIs, or create a Desktop app client instead")
	case errors.As(err, &oe):
		add("Known to Google", CheckWarn, "Google gave an answer the check does not know ("+oe.message+"); try a sign-in")
	default:
		add("Known to Google", CheckWarn, "could not ask Google: "+err.Error())
	}

	out.Status = CheckOK
	for _, it := range out.Items {
		switch {
		case it.Status == CheckBad:
			out.Status = CheckBad
		case it.Status == CheckWarn && out.Status == CheckOK:
			out.Status = CheckWarn
		}
	}
	return out, nil
}
