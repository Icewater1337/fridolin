package nodeproto

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Git identities for FrIDE (docs/NODES.md, "Git identities for FrIDE"):
// the core hands a node whose fride is approved the identities its FrIDE's
// workspaces may be created under, with their tokens, and the node passes
// them on to FrIDE's companion API (PUT /api/companion/git/identities/<id>).

// MethodFrideIdentities hands the node all the git identities of its FrIDE
// (FrideIdentities → FrideIdentitiesResult). Like every fride method it
// needs fride approved.
const MethodFrideIdentities = "fride.identities"

// EventFrideIdentities is the node asking the core for its identities
// again (a FrideIdentitiesAsk): a GitHub token runs out soon, or FrIDE
// was paired anew. The core answers with fride.identities.
const EventFrideIdentities = "fride.identities"

// The kinds of identity FrIDE knows.
const (
	GitKindGitHub  = "github"
	GitKindGitLab  = "gitlab"
	GitKindGeneric = "generic"
)

// Bounds of the identities.
const (
	MaxGitIdentities = 8
	maxGitLabel      = 64
	maxGitName       = 128
	maxGitEmail      = 254
	maxGitToken      = 4096
	maxGitHost       = 255
	maxGitAskReason  = 64
)

// gitIdentityID is an identity's id, as FrIDE takes it.
var gitIdentityID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// gitHostLabel is one label of a host name.
var gitHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// GitIdentity is one identity with its token.
type GitIdentity struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Host is the forge's host, with its port when it has one; HTTPS.
	Host string `json:"host"`
	// Name and Email are the commit identity; both empty leave it to git.
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	// Token is what FrIDE's git and forge tools authenticate with on
	// Host. It never goes into a log, the audit or an answer.
	Token string `json:"token"`
	// Expires is when Token ends, for a GitHub App's installation token
	// (an hour); zero for one that does not. The node asks for a new one
	// before then (EventFrideIdentities).
	Expires time.Time `json:"expires,omitzero"`
}

// FrideIdentities are all the identities the node's FrIDE gets: one that
// FrIDE holds from Fridolin and that is not here, the node removes.
type FrideIdentities struct {
	Identities []GitIdentity `json:"identities"`
}

// FrideIdentitiesResult says what the node did with them.
type FrideIdentitiesResult struct {
	// Supported says FrIDE takes identities (git_identities in its
	// /api/companion/info); without, the node keeps them and passes them
	// on once a FrIDE that does answers.
	Supported bool `json:"supported"`
	// Pending says FrIDE does not answer now: the same.
	Pending bool `json:"pending,omitempty"`
	// Pushed are the ids FrIDE took, Removed those it no longer has.
	Pushed  []string `json:"pushed"`
	Removed []string `json:"removed"`
	// Held says why the node keeps them from its FrIDE on purpose, such
	// as the FrIDE in a Docker Sandbox, which must never hold a real
	// token; the core does not send them again until they change.
	Held string `json:"held,omitempty"`
	// Error says what went wrong, without a token.
	Error string `json:"error,omitempty"`
}

// FrideIdentitiesAsk is the node's EventFrideIdentities.
type FrideIdentitiesAsk struct {
	// Reason is why, in a word or two: "expiring", "paired".
	Reason string `json:"reason"`
}

// Check reports what is wrong with the identities.
func (f FrideIdentities) Check() error {
	if len(f.Identities) > MaxGitIdentities {
		return fmt.Errorf("at most %d git identities", MaxGitIdentities)
	}
	seen := map[string]bool{}
	for _, id := range f.Identities {
		if err := id.Check(); err != nil {
			return err
		}
		if seen[id.ID] {
			return fmt.Errorf("the git identity %q comes twice", id.ID)
		}
		seen[id.ID] = true
	}
	return nil
}

// ValidGitIdentityID reports whether id can be an identity's id: FrIDE's
// form, and not "none", which FrIDE reserves for a task without one.
func ValidGitIdentityID(id string) bool { return gitIdentityID.MatchString(id) && id != "none" }

// Check reports what is wrong with the identity, never naming its token.
func (g GitIdentity) Check() error {
	if !ValidGitIdentityID(g.ID) {
		return fmt.Errorf("git identity id %q: lowercase letters, digits, _ and -, up to 32, and not none", g.ID)
	}
	switch g.Kind {
	case GitKindGitHub, GitKindGitLab, GitKindGeneric:
	default:
		return fmt.Errorf("git identity %s: kind %q", g.ID, g.Kind)
	}
	if err := CheckGitHost(g.Host); err != nil {
		return fmt.Errorf("git identity %s: %w", g.ID, err)
	}
	for _, f := range []struct {
		what, v string
		max     int
	}{{"label", g.Label, maxGitLabel}, {"name", g.Name, maxGitName}, {"email", g.Email, maxGitEmail}} {
		if utf8.RuneCountInString(f.v) > f.max || !utf8.ValidString(f.v) || strings.ContainsFunc(f.v, unicode.IsControl) {
			return fmt.Errorf("git identity %s: the %s is longer than %d characters or holds control characters", g.ID, f.what, f.max)
		}
	}
	if g.Email != "" && (strings.ContainsAny(g.Email, " <>") || strings.Count(g.Email, "@") != 1) {
		return fmt.Errorf("git identity %s: the email is no address", g.ID)
	}
	if g.Token == "" || len(g.Token) > maxGitToken || strings.ContainsFunc(g.Token, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return fmt.Errorf("git identity %s: the token is empty, too long or not printable", g.ID)
	}
	return nil
}

// CheckGitHost reports what is wrong with host, a host name or IP address
// with an optional port.
func CheckGitHost(host string) error {
	if host == "" || len(host) > maxGitHost {
		return errors.New("the host is empty or too long")
	}
	h := strings.ToLower(host)
	if h != host {
		return fmt.Errorf("the host %q must be lowercase", host)
	}
	if strings.Contains(h, ":") && !strings.HasSuffix(h, "]") {
		var port string
		var err error
		if h, port, err = net.SplitHostPort(h); err != nil {
			return fmt.Errorf("the host %q: %v", host, err)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return fmt.Errorf("the host %q has no valid port", host)
		}
	}
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return nil
	}
	for _, l := range strings.Split(h, ".") {
		if !gitHostLabel.MatchString(l) {
			return fmt.Errorf("the host %q is not a host name", host)
		}
	}
	return nil
}

// Check reports what is wrong with the ask.
func (a FrideIdentitiesAsk) Check() error {
	if len(a.Reason) > maxGitAskReason || strings.ContainsFunc(a.Reason, unicode.IsControl) {
		return errors.New("the reason is too long or holds control characters")
	}
	return nil
}
