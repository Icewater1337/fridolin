// Package ghapp is a small client for a GitHub App of fridolin's own
// (docs/GITHUB.md): the manifest that registers one, the exchange of
// GitHub's code for its credentials, its installations, and installation
// access tokens narrowed to the repositories and permissions of one use.
//
// It never logs, and its errors never carry a key, a client secret or a
// token: GitHub's answers are quoted by their message only.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI and DefaultWeb are github.com's API and site.
const (
	DefaultAPI = "https://api.github.com"
	DefaultWeb = "https://github.com"
)

// Credentials are what GitHub hands back for a new app, and all the bridge
// keeps of it. PEM and ClientSecret are secrets; the rest is not.
type Credentials struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Owner        string `json:"owner"`
	OwnerType    string `json:"owner_type"` // "User" or "Organization"
	HTMLURL      string `json:"html_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	PEM          string `json:"pem"`
}

// String never shows the secrets, whatever prints the value.
func (c Credentials) String() string { return fmt.Sprintf("GitHub app %s (%d)", c.Slug, c.ID) }

// GoString is String, for %#v.
func (c Credentials) GoString() string { return c.String() }

// Check says whether c has what minting tokens needs.
func (c Credentials) Check() error {
	switch {
	case c.ID <= 0:
		return errors.New("the GitHub app has no id")
	case c.Slug == "":
		return errors.New("the GitHub app has no slug")
	}
	_, err := parseKey(c.PEM)
	return err
}

// Token is an installation access token. Value is the secret.
type Token struct {
	Value          string
	ExpiresAt      time.Time
	InstallationID int64
	Repos          []string          // owner/name
	Permissions    map[string]string // as GitHub granted them
}

// String never shows the token.
func (t Token) String() string {
	return fmt.Sprintf("GitHub token for %s, until %s", strings.Join(t.Repos, ", "), t.ExpiresAt.Format(time.RFC3339))
}

// GoString is String, for %#v.
func (t Token) GoString() string { return t.String() }

// Installation is the app installed on an account.
type Installation struct {
	ID                  int64             `json:"id"`
	Account             string            `json:"account"`
	AccountType         string            `json:"account_type"`
	HTMLURL             string            `json:"html_url"`
	RepositorySelection string            `json:"repository_selection"` // "all" or "selected"
	Permissions         map[string]string `json:"permissions"`
	Suspended           bool              `json:"suspended"`
}

// Client talks to GitHub's API. The zero value talks to github.com.
type Client struct {
	API  string // default DefaultAPI
	HTTP *http.Client
	Now  func() time.Time
}

func (c *Client) api() string {
	if c.API != "" {
		return strings.TrimRight(c.API, "/")
	}
	return DefaultAPI
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// APIError is an answer of GitHub's other than the one expected.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub answered %d", e.Status)
	}
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message)
}

// IsStatus reports whether err is GitHub's answer with that status.
func IsStatus(err error, status int) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && e.Status == status
}

// do sends a request with auth (a Bearer value, or none) and decodes a
// JSON answer of status want into out (nil to skip it).
func (c *Client) do(ctx context.Context, method, path, auth string, body any, want int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fridolin")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		// url.Error quotes the URL, which holds no secret: codes and
		// tokens never go into one here, but the code of a conversion
		// does, so it is cut.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			return fmt.Errorf("reaching GitHub: %w", ue.Err)
		}
		return fmt.Errorf("reaching GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("reading GitHub's answer: %w", err)
	}
	if resp.StatusCode != want {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return &APIError{Status: resp.StatusCode, Message: oneLine(e.Message)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub's answer to %s %s is not what was expected: %w", method, cutCode(path), err)
	}
	return nil
}

// cutCode leaves a conversion's code out of a path quoted in an error.
func cutCode(path string) string {
	if strings.HasPrefix(path, "/app-manifests/") {
		return "/app-manifests/…/conversions"
	}
	return path
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// codeRE is what a manifest code looks like: GitHub's are hex; the
// pattern only keeps a code from changing the path.
var codeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Convert exchanges the code GitHub's manifest flow sent back for the new
// app's credentials (POST /app-manifests/{code}/conversions). The code
// works once, within an hour.
func (c *Client) Convert(ctx context.Context, code string) (Credentials, error) {
	if !codeRE.MatchString(code) {
		return Credentials{}, errors.New("GitHub sent back no usable code")
	}
	var out struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		HTMLURL string `json:"html_url"`
		Owner   struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		PEM          string `json:"pem"`
	}
	if err := c.do(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, http.StatusCreated, &out); err != nil {
		return Credentials{}, err
	}
	creds := Credentials{ID: out.ID, Slug: out.Slug, Name: out.Name, HTMLURL: out.HTMLURL,
		Owner: out.Owner.Login, OwnerType: out.Owner.Type, ClientID: out.ClientID, ClientSecret: out.ClientSecret, PEM: out.PEM}
	if err := creds.Check(); err != nil {
		return Credentials{}, fmt.Errorf("GitHub's new app: %w", err)
	}
	return creds, nil
}

func parseKey(p string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, errors.New("the GitHub app's private key is no PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the GitHub app's private key is no RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the GitHub app's private key is no RSA key")
	}
	return rk, nil
}

// JWT is the app's own token, RS256, valid for nine minutes from a minute
// ago (GitHub allows ten and clocks drift).
func (c *Client) JWT(creds Credentials) (string, error) {
	key, err := parseKey(creds.PEM)
	if err != nil {
		return "", err
	}
	now := c.now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(creds.ID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

type apiInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	HTMLURL             string            `json:"html_url"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	SuspendedAt         *string           `json:"suspended_at"`
}

func (i apiInstallation) installation() Installation {
	return Installation{ID: i.ID, Account: i.Account.Login, AccountType: i.Account.Type, HTMLURL: i.HTMLURL,
		RepositorySelection: i.RepositorySelection, Permissions: i.Permissions, Suspended: i.SuspendedAt != nil}
}

// Installations lists where the app is installed (GET /app/installations).
func (c *Client) Installations(ctx context.Context, creds Credentials) ([]Installation, error) {
	jwt, err := c.JWT(creds)
	if err != nil {
		return nil, err
	}
	var out []Installation
	for page := 1; page <= 10; page++ {
		var got []apiInstallation
		if err := c.do(ctx, http.MethodGet, "/app/installations?per_page=100&page="+strconv.Itoa(page), jwt, nil, http.StatusOK, &got); err != nil {
			return nil, err
		}
		for _, i := range got {
			out = append(out, i.installation())
		}
		if len(got) < 100 {
			break
		}
	}
	return out, nil
}

// Repositories lists the repositories an installation token reaches
// (GET /installation/repositories), as owner/name.
func (c *Client) Repositories(ctx context.Context, token string) ([]string, error) {
	var out []string
	for page := 1; page <= 10; page++ {
		var got struct {
			Total int `json:"total_count"`
			Repos []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, "/installation/repositories?per_page=100&page="+strconv.Itoa(page), token, nil, http.StatusOK, &got); err != nil {
			return nil, err
		}
		for _, r := range got.Repos {
			out = append(out, r.FullName)
		}
		if len(got.Repos) < 100 || len(out) >= got.Total {
			break
		}
	}
	slices.Sort(out)
	return out, nil
}

// Permission levels a token may ask for.
var levels = map[string]bool{"read": true, "write": true, "admin": true}

// permissionRE is a permission's name as GitHub spells them.
var permissionRE = regexp.MustCompile(`^[a-z][a-z_]{1,63}$`)

// CheckPermissions refuses a permission that is no name or level.
func CheckPermissions(perms map[string]string) error {
	for name, level := range perms {
		if !permissionRE.MatchString(name) {
			return fmt.Errorf("%q is not a GitHub permission", name)
		}
		if !levels[level] {
			return fmt.Errorf("permission %s: %q is not read, write or admin", name, level)
		}
	}
	return nil
}

// repoRE is owner/name.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

// SplitRepo splits owner/name.
func SplitRepo(full string) (owner, name string, err error) {
	if !repoRE.MatchString(full) {
		return "", "", fmt.Errorf("%q is not owner/name", full)
	}
	owner, name, _ = strings.Cut(full, "/")
	return owner, name, nil
}

// Mint makes an installation access token of installation id, for repos
// (owner/name, all of the installation's account; none: every repository
// the installation reaches) and perms (none: everything the app may). It
// lasts an hour; revoke it with Revoke once its use is over.
func (c *Client) Mint(ctx context.Context, creds Credentials, id int64, repos []string, perms map[string]string) (Token, error) {
	if err := CheckPermissions(perms); err != nil {
		return Token{}, err
	}
	body := map[string]any{}
	if len(repos) > 0 {
		names := make([]string, 0, len(repos))
		for _, r := range repos {
			_, name, err := SplitRepo(r)
			if err != nil {
				return Token{}, err
			}
			names = append(names, name)
		}
		body["repositories"] = names
	}
	if len(perms) > 0 {
		body["permissions"] = perms
	}
	jwt, err := c.JWT(creds)
	if err != nil {
		return Token{}, err
	}
	var out struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := c.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(id, 10)+"/access_tokens", jwt, body, http.StatusCreated, &out); err != nil {
		return Token{}, err
	}
	if out.Token == "" {
		return Token{}, errors.New("GitHub sent no token")
	}
	t := Token{Value: out.Token, ExpiresAt: out.ExpiresAt, InstallationID: id, Permissions: out.Permissions}
	for _, r := range out.Repositories {
		t.Repos = append(t.Repos, r.FullName)
	}
	if len(t.Repos) == 0 {
		t.Repos = slices.Clone(repos)
	}
	return t, nil
}

// Revoke ends an installation token before its hour is up (DELETE
// /installation/token). A token that is no longer valid is no error.
func (c *Client) Revoke(ctx context.Context, token string) error {
	err := c.do(ctx, http.MethodDelete, "/installation/token", token, nil, http.StatusNoContent, nil)
	if IsStatus(err, http.StatusUnauthorized) {
		return nil
	}
	return err
}

// Issue is one issue, as Test reads it.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
}

// LatestIssue reads repo's newest issue or pull request (GET
// /repos/{owner}/{repo}/issues), none when it has none.
func (c *Client) LatestIssue(ctx context.Context, token, repo string) (*Issue, error) {
	if _, _, err := SplitRepo(repo); err != nil {
		return nil, err
	}
	var got []Issue
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/issues?state=all&per_page=1", token, nil, http.StatusOK, &got); err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, nil
	}
	return &got[0], nil
}
