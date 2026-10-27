package gitident

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of what is saved.
const (
	maxBaseURL = 512
	maxName    = 128
	maxEmail   = 254
)

// CheckBaseURL is GitLab's address as it is kept: https, a host, maybe a
// port and a path (a GitLab under https://example.com/gitlab), never a
// user, a query or a fragment, without a trailing slash.
func CheckBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter GitLab's address, such as https://gitlab.example.com")
	}
	if len(raw) > maxBaseURL {
		return "", fmt.Errorf("GitLab's address is longer than %d characters", maxBaseURL)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return "", fmt.Errorf("GitLab's address must be an https URL, such as https://gitlab.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", errors.New("GitLab's address must not hold a user, a password, a query or a fragment")
	}
	if err := CheckHost(u.Host); err != nil {
		return "", err
	}
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/"), nil
}

// hostLabel is one label of a host name.
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CheckHost reports what is wrong with host, a host name or IP address
// with an optional port, as FrIDE takes an identity's host.
func CheckHost(host string) error {
	h, port := strings.ToLower(host), ""
	if strings.Contains(h, ":") && !strings.HasSuffix(h, "]") {
		var err error
		if h, port, err = net.SplitHostPort(h); err != nil {
			return fmt.Errorf("the host %q: %v", host, err)
		}
		if n, ok := portNumber(port); !ok || n == 0 {
			return fmt.Errorf("the host %q has no valid port", host)
		}
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return nil
	}
	if h == "" || len(h) > 253 {
		return fmt.Errorf("the host %q is not a host name", host)
	}
	for _, l := range strings.Split(h, ".") {
		if !hostLabel.MatchString(l) {
			return fmt.Errorf("the host %q is not a host name", host)
		}
	}
	return nil
}

func portNumber(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n <= 65535
}

// validToken reports whether t can be an access token: 8 to 512
// printable ASCII characters, without spaces.
func validToken(t string) bool {
	if len(t) < 8 || len(t) > 512 {
		return false
	}
	return !strings.ContainsFunc(t, func(r rune) bool { return r <= ' ' || r > '~' })
}

// emailRE is an address git takes: something@something, without spaces
// or the brackets git puts around it.
var emailRE = regexp.MustCompile(`^[^\s@<>]+@[^\s@<>]+$`)

// checkCommit is a commit identity as it is kept: trimmed, a name without
// control characters or brackets, and an address. Both or neither.
func checkCommit(name, email string) (Commit, error) {
	c := Commit{Name: strings.TrimSpace(name), Email: strings.TrimSpace(email)}
	if utf8.RuneCountInString(c.Name) > maxName || !utf8.ValidString(c.Name) ||
		strings.ContainsFunc(c.Name, func(r rune) bool { return unicode.IsControl(r) || r == '<' || r == '>' }) {
		return Commit{}, bad("the commit name must be at most %d characters, without control characters or < >", maxName)
	}
	if c.Email != "" && (len(c.Email) > maxEmail || !emailRE.MatchString(c.Email)) {
		return Commit{}, bad("the commit email %q is not an address such as you@example.com", c.Email)
	}
	if (c.Name == "") != (c.Email == "") {
		return Commit{}, bad("give the commit name and email together, or neither (git's own then count)")
	}
	return c, nil
}

// writeAtomic writes data to path, 0600, through a temporary file in the
// same 0700 folder, so that a reader sees the old file or the new.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
