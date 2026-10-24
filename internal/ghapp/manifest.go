package ghapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
)

// ManifestOptions are what differs between installs.
type ManifestOptions struct {
	Name        string // fridolin-<random>: NewName
	HomeURL     string // the app's homepage, which GitHub requires
	RedirectURL string // where GitHub sends the browser with the code
	SetupURL    string // where GitHub sends it after an install
	// PullRequests adds pull_requests: write, for FrIDE (docs/GITHUB.md).
	PullRequests bool
}

// Permissions are what the app may ever do. Each token narrows them to
// one use.
func (o ManifestOptions) Permissions() map[string]string {
	p := map[string]string{"contents": "read", "issues": "write", "metadata": "read"}
	if o.PullRequests {
		p["pull_requests"] = "write"
	}
	return p
}

// Manifest is the JSON GitHub's manifest flow takes in its form field
// "manifest": a private app with no webhook.
func Manifest(o ManifestOptions) ([]byte, error) {
	if o.Name == "" || o.HomeURL == "" || o.RedirectURL == "" {
		return nil, errors.New("a manifest needs a name, a homepage and a redirect")
	}
	m := map[string]any{
		"name":                o.Name,
		"url":                 o.HomeURL,
		"description":         "Fridolin's own GitHub App: short-lived tokens for its runs, one repository and permission at a time.",
		"redirect_url":        o.RedirectURL,
		"public":              false,
		"hook_attributes":     map[string]any{"url": o.HomeURL, "active": false},
		"default_events":      []string{},
		"default_permissions": o.Permissions(),
	}
	if o.SetupURL != "" {
		m["setup_url"] = o.SetupURL
		m["setup_on_update"] = true
	}
	return json.Marshal(m)
}

// NewName is fridolin-<8 hex>: an app's name is unique on all of GitHub.
func NewName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "fridolin-" + hex.EncodeToString(b[:])
}

// accountRE is a GitHub account's login.
var accountRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// CheckAccount refuses what cannot be a login.
func CheckAccount(login string) error {
	if !accountRE.MatchString(login) {
		return errors.New("not a GitHub account name")
	}
	return nil
}

func web(base string) string {
	if base == "" {
		return DefaultWeb
	}
	return base
}

// NewAppURL is the form's target: the person's own apps, or org's.
func NewAppURL(base, org, state string) string {
	u := web(base) + "/settings/apps/new"
	if org != "" {
		u = web(base) + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return u + "?state=" + url.QueryEscape(state)
}

// InstallURL is where the owner picks the repositories.
func InstallURL(base, slug string) string {
	return web(base) + "/apps/" + url.PathEscape(slug) + "/installations/new"
}

// SettingsURL is the app's settings page; its Advanced tab deletes it.
func SettingsURL(base string, c Credentials) string {
	if c.OwnerType == "Organization" {
		return web(base) + "/organizations/" + url.PathEscape(c.Owner) + "/settings/apps/" + url.PathEscape(c.Slug)
	}
	return web(base) + "/settings/apps/" + url.PathEscape(c.Slug)
}

// DeleteURL is the page with the app's Delete button.
func DeleteURL(base string, c Credentials) string { return SettingsURL(base, c) + "/advanced" }
