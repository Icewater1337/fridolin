package ghapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/ghapp"
	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
)

func fake(t *testing.T) (*ghapptest.Server, *ghapp.Client) {
	t.Helper()
	s := ghapptest.New()
	t.Cleanup(s.Close)
	return s, &ghapp.Client{API: s.URL}
}

// The whole life of a token: convert the manifest's code, find the
// installation, mint a token narrowed to one repository and permission,
// use it, revoke it.
func TestConvertMintRevoke(t *testing.T) {
	s, c := fake(t)
	ctx := context.Background()
	creds, err := c.Convert(ctx, ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if creds.ID != s.AppID || creds.Slug != s.Slug || creds.Owner != "you" || creds.ClientSecret == "" {
		t.Fatalf("credentials: %v", creds)
	}
	insts, err := c.Installations(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 1 || insts[0].ID != s.InstallationID || insts[0].Account != "you" {
		t.Fatalf("installations: %+v", insts)
	}
	tok, err := c.Mint(ctx, creds, insts[0].ID, []string{"you/fridolin"}, map[string]string{"issues": "read"})
	if err != nil {
		t.Fatal(err)
	}
	if m := s.LastMinted(); fmt.Sprint(m.Repos) != "[fridolin]" || m.Permissions["issues"] != "read" || len(m.Permissions) != 1 {
		t.Errorf("asked GitHub for %+v", m)
	}
	repos, err := c.Repositories(ctx, tok.Value)
	if err != nil || fmt.Sprint(repos) != "[you/fridolin]" {
		t.Errorf("repositories: %v, %v", repos, err)
	}
	issue, err := c.LatestIssue(ctx, tok.Value, "you/fridolin")
	if err != nil || issue == nil || issue.Number != 7 {
		t.Errorf("issue: %+v, %v", issue, err)
	}
	if err := c.Revoke(ctx, tok.Value); err != nil {
		t.Fatal(err)
	}
	if len(s.Live()) != 0 {
		t.Errorf("live after revoking: %v", s.Live())
	}
	// Revoking twice is no error: the token is gone either way.
	if err := c.Revoke(ctx, tok.Value); err != nil {
		t.Errorf("second revoke: %v", err)
	}
}

// Nothing that prints a credential or a token shows its secret.
func TestSecretsNeverPrint(t *testing.T) {
	s, c := fake(t)
	creds, err := c.Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	tok := ghapp.Token{Value: "ghs_secretvalue", Repos: []string{"you/x"}}
	for _, out := range []string{fmt.Sprint(creds), fmt.Sprintf("%v %+v %#v", creds, creds, creds), fmt.Sprintf("%v %+v %#v", tok, tok, tok)} {
		for _, secret := range []string{"fake-client-secret", "PRIVATE KEY", "ghs_secretvalue", s.PEM()[40:80]} {
			if strings.Contains(out, secret) {
				t.Errorf("%q shows %q", out, secret)
			}
		}
	}
}

func TestErrors(t *testing.T) {
	_, c := fake(t)
	ctx := context.Background()
	if _, err := c.Convert(ctx, "wrong"); !ghapp.IsStatus(err, 404) {
		t.Errorf("a wrong code: %v", err)
	}
	if _, err := c.Convert(ctx, "../../x"); err == nil {
		t.Error("a code that changes the path was used")
	}
	creds, err := c.Convert(ctx, ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Mint(ctx, creds, 77, []string{"you/elsewhere"}, nil); !ghapp.IsStatus(err, 422) {
		t.Errorf("a repository the installation lacks: %v", err)
	}
	for _, perms := range []map[string]string{{"issues": "everything"}, {"Issues!": "read"}} {
		if _, err := c.Mint(ctx, creds, 77, nil, perms); err == nil {
			t.Errorf("%v was asked for", perms)
		}
	}
	if _, err := c.Mint(ctx, creds, 77, []string{"no-owner"}, nil); err == nil {
		t.Error("a repository without its owner was asked for")
	}
	bad := creds
	bad.PEM = "nope"
	if _, err := c.Installations(ctx, bad); err == nil || strings.Contains(err.Error(), "nope") {
		t.Errorf("a broken key: %v", err)
	}
}

func TestManifest(t *testing.T) {
	for _, prs := range []bool{false, true} {
		raw, err := ghapp.Manifest(ghapp.ManifestOptions{Name: "fridolin-abcd", HomeURL: "https://example.com/fridolin",
			RedirectURL: "http://127.0.0.1:8765/github/created", SetupURL: "http://127.0.0.1:8765/github/installed", PullRequests: prs})
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Name        string            `json:"name"`
			Public      bool              `json:"public"`
			Redirect    string            `json:"redirect_url"`
			Setup       string            `json:"setup_url"`
			Hook        map[string]any    `json:"hook_attributes"`
			Permissions map[string]string `json:"default_permissions"`
			Events      []string          `json:"default_events"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		want := "map[contents:read issues:write metadata:read]"
		if prs {
			want = "map[contents:read issues:write metadata:read pull_requests:write]"
		}
		if fmt.Sprint(m.Permissions) != want || m.Public || m.Hook["active"] != false || len(m.Events) != 0 ||
			m.Redirect != "http://127.0.0.1:8765/github/created" || m.Setup == "" {
			t.Errorf("pull requests %v: %s", prs, raw)
		}
	}
	if a, b := ghapp.NewName(), ghapp.NewName(); !strings.HasPrefix(a, "fridolin-") || a == b {
		t.Error("names are not fridolin-<random>")
	}
	if got := ghapp.NewAppURL("", "acme", "s t"); got != "https://github.com/organizations/acme/settings/apps/new?state=s+t" {
		t.Errorf("org URL: %s", got)
	}
	c := ghapp.Credentials{Slug: "fridolin-x", Owner: "acme", OwnerType: "Organization"}
	if got := ghapp.DeleteURL("", c); got != "https://github.com/organizations/acme/settings/apps/fridolin-x/advanced" {
		t.Errorf("delete URL: %s", got)
	}
}
