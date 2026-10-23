package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/ghapp"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// Where the GitHub App's credentials are kept (docs/GITHUB.md, "Where the
// keys are"). The dashboard stores them when the owner connects GitHub, so
// they go where the bridge itself can write a secret:
//
//   - inside the core container, which has no host secret store, in
//     secrets/github-app.age in the data dir, encrypted to the core's
//     post-quantum age identity (FRIDOLIN_AGE_IDENTITY_PQ), like a node
//     target's password: it moves with the core's export, and opens
//     wherever that identity is, which every core host has;
//   - on a native install, in the host's secret store as
//     fridolin.github.app (the keychain, the Credential Manager, 0600
//     files on Linux), base64 of the JSON, which the keychain helper
//     passes on unchanged.
//
// Never in config.json, a log or the audit trail.

// githubHostStore is the host's secret store for the app's credentials;
// the tests replace it, so that none reaches the login keychain.
var githubHostStore = hostSecretStore

// githubSecrets reads, keeps and forgets the app's credentials.
type githubSecrets struct {
	getenv     func(string) string
	store      hostsecrets.Store // nil in the container
	secretsDir string
}

func (s githubSecrets) ageFile() string { return filepath.Join(s.secretsDir, "github-app.age") }

func (s githubSecrets) identities() ([]age.Identity, age.Recipient, error) {
	return targetSecrets{getenv: s.getenv}.identities()
}

// where names the place, for the dashboard and the docs.
func (s githubSecrets) where() string {
	if s.store != nil {
		return "the host's secret store, as " + hostsecrets.GitHubApp
	}
	return "the core's secrets, " + s.ageFile() + ", encrypted to its age identity"
}

// load returns the credentials, ok false when none are kept.
func (s githubSecrets) load() (ghapp.Credentials, bool, error) {
	var c ghapp.Credentials
	if s.store != nil {
		v, err := s.store.Get(hostsecrets.GitHubApp)
		if errors.Is(err, hostsecrets.ErrNotFound) {
			return c, false, nil
		}
		if err != nil {
			return c, false, fmt.Errorf("reading %s from the host's secrets: %w", hostsecrets.GitHubApp, err)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return c, false, fmt.Errorf("%s is not what the dashboard stored", hostsecrets.GitHubApp)
		}
		return c, true, decodeGitHubCreds(raw, &c, hostsecrets.GitHubApp)
	}
	data, err := os.ReadFile(s.ageFile())
	if errors.Is(err, fs.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	ids, _, err := s.identities()
	if err != nil || len(ids) == 0 {
		return c, false, fmt.Errorf("%s needs the core's age identity (FRIDOLIN_AGE_IDENTITY_PQ) to be read", s.ageFile())
	}
	r, err := age.Decrypt(bytes.NewReader(data), ids...)
	if err != nil {
		return c, false, fmt.Errorf("decrypting %s: %w", s.ageFile(), err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return c, false, err
	}
	return c, true, decodeGitHubCreds(raw, &c, s.ageFile())
}

func decodeGitHubCreds(raw []byte, c *ghapp.Credentials, where string) error {
	if err := json.Unmarshal(raw, c); err != nil {
		return fmt.Errorf("%s does not hold a GitHub app", where)
	}
	if err := c.Check(); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	return nil
}

// save keeps c, replacing what was kept.
func (s githubSecrets) save(c ghapp.Credentials) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if s.store != nil {
		return s.store.Set(hostsecrets.GitHubApp, base64.StdEncoding.EncodeToString(raw))
	}
	_, recipient, err := s.identities()
	if err != nil {
		return err
	}
	if recipient == nil {
		return errors.New("there is no host secret store here and no post-quantum age identity (FRIDOLIN_AGE_IDENTITY_PQ) to keep the GitHub app's key with")
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.secretsDir, 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.ageFile(), buf.Bytes(), 0o600, false)
}

// forget removes what is kept; nothing kept is no error.
func (s githubSecrets) forget() error {
	if s.store != nil {
		d, ok := s.store.(hostsecrets.Deleter)
		if !ok {
			return fmt.Errorf("this host's secret store cannot delete; remove %s yourself", hostsecrets.GitHubApp)
		}
		return d.Delete(hostsecrets.GitHubApp)
	}
	if err := os.Remove(s.ageFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// typedToken is the second choice to an app: a token the owner typed in,
// FRIDOLIN_GITHUB_TOKEN (core start passes it from the host secret
// fridolin.github.token), or that host secret on a native install.
func (s githubSecrets) typedToken() string {
	if v := strings.TrimSpace(s.getenv("FRIDOLIN_GITHUB_TOKEN")); v != "" {
		return v
	}
	if s.store == nil {
		return ""
	}
	v, err := s.store.Get(hostsecrets.GitHubToken)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
