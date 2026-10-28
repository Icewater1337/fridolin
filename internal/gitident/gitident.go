// Package gitident holds Fridolin's git identities for FrIDE
// (docs/CONNECTIONS.md, "GitLab"; docs/NODES.md, "Git identities for
// FrIDE"): the GitLab connection, a base URL with an access token, and the
// commit name and email of the GitHub identity.
//
// They live in the data dir's core/local-secrets folder, which stays on
// the machines: no export, restic snapshot, vault cold tier or Drive
// backup takes it. A core move carries it end to end, sealed beside the
// host secrets (internal/secretxfer). The owner's rule is that work data
// never goes to the cloud, and a work GitLab token is work data.
//
// The package uses the standard library only.
package gitident

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// The files in the folder, each 0600 in a 0700 folder.
const (
	// GitLabFile is the GitLab connection.
	GitLabFile = "gitlab.json"
	// GitHubFile is the GitHub identity's commit name and email.
	GitHubFile = "github.json"
)

// The identities' ids, as FrIDE knows them.
const (
	GitHub = "github"
	GitLab = "gitlab"
)

// IDs are the identities Fridolin can hand a FrIDE.
func IDs() []string { return []string{GitHub, GitLab} }

// Store is the folder the identities are kept in:
// config.Layout.LocalSecrets().
type Store struct {
	Dir string
}

// Error is a request the store refuses, with the HTTP status the bridge
// answers it with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// HTTPStatus is the status the bridge answers the error with.
func (e *Error) HTTPStatus() int { return e.Status }

func bad(format string, a ...any) *Error {
	return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, a...)}
}

// Connection is the GitLab connection: where GitLab is, the access token,
// and the commit identity of work done with it.
type Connection struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Host is the host of the base URL, with its port: what git's credential
// helper and FrIDE key the identity by.
func (c Connection) Host() string {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// Commit is a commit identity; both empty leaves it to git's own config.
type Commit struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Update is what the dashboard saves of the GitLab connection. A blank
// Token keeps the stored token; a nil field keeps its value.
type Update struct {
	BaseURL *string
	Token   string
	Name    *string
	Email   *string
}

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// GitLab reads the connection; ok is false when none is set.
func (s Store) GitLab() (Connection, bool, error) {
	var c Connection
	ok, err := s.read(GitLabFile, &c)
	if err != nil || !ok || c.Token == "" || c.BaseURL == "" {
		return Connection{}, false, err
	}
	return c, true, nil
}

// SetGitLab saves the connection as u says and returns it.
func (s Store) SetGitLab(u Update) (Connection, error) {
	old, had, err := s.GitLab()
	if err != nil {
		return Connection{}, err
	}
	next := old
	if u.BaseURL != nil {
		b, err := CheckBaseURL(*u.BaseURL)
		if err != nil {
			return Connection{}, bad("%v", err)
		}
		next.BaseURL = b
	} else if !had {
		return Connection{}, bad("enter GitLab's address, such as https://gitlab.example.com")
	}
	if t := strings.TrimSpace(u.Token); t != "" {
		if !validToken(t) {
			return Connection{}, bad("that is not a token: it must be 8 to 512 characters, letters, digits and punctuation, no spaces")
		}
		next.Token = t
	} else if !had {
		return Connection{}, bad("enter the GitLab access token")
	}
	c, err := checkCommit(valueOr(u.Name, next.Name), valueOr(u.Email, next.Email))
	if err != nil {
		return Connection{}, err
	}
	next.Name, next.Email = c.Name, c.Email
	return next, s.write(GitLabFile, next)
}

func valueOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// RemoveGitLab deletes the connection.
func (s Store) RemoveGitLab() error { return s.remove(GitLabFile) }

// GitHubCommit is the GitHub identity's commit name and email.
func (s Store) GitHubCommit() (Commit, error) {
	var c Commit
	_, err := s.read(GitHubFile, &c)
	return c, err
}

// SetGitHubCommit saves them; both empty removes the file.
func (s Store) SetGitHubCommit(name, email string) (Commit, error) {
	c, err := checkCommit(name, email)
	if err != nil {
		return Commit{}, err
	}
	if c == (Commit{}) {
		return c, s.remove(GitHubFile)
	}
	return c, s.write(GitHubFile, c)
}

// read decodes file name into v; ok is false when there is none.
func (s Store) read(name string, v any) (bool, error) {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s does not read: %w", name, err)
	}
	return true, nil
}

func (s Store) write(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path(name), append(data, '\n'))
}

func (s Store) remove(name string) error {
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
