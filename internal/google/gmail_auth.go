package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Gmail's sign-in (docs/GMAIL.md): a sign-in of the brain's own, apart from
// its calendar's and its contacts', as each of those is apart from the
// other. It asks for GmailScope and nothing else, so a brain connects Gmail
// without touching its calendar, and disconnects it the same way. A brain
// that never needs mail is never asked for it.
//
// The token is written next to the calendar's, in the same format,
//
//	<credentials dir>/google_gmail_token_<brain>.json
//
// and only the bridge reads it: the brain's `fridolin gmail` commands go
// through the bridge, which makes each Gmail call with it. It names the
// account (the mailbox's address, from Gmail's profile), so a disconnect
// can tell a token of another account from one that may share its grant.

// GmailScope is what a brain's Gmail sign-in asks for: the full mailbox,
// which permanent delete needs (gmail.modify only reaches the trash).
const GmailScope = "https://mail.google.com/"

// HasGmail reports whether granted holds GmailScope.
func HasGmail(granted []string) bool { return slices.Contains(granted, GmailScope) }

// gmailTokenPrefix starts the name of a brain's Gmail token, which ends as
// a calendar token's does.
const gmailTokenPrefix = "google_gmail_token_"

// GmailTokenFile is a brain's Gmail token in dir, the credentials dir.
// Only the bridge reads and writes it.
func GmailTokenFile(dir, brainID string) string {
	return filepath.Join(dir, gmailTokenPrefix+brainID+calendarTokenSuffix)
}

// GmailTokenPath resolves the brain's Gmail token in the current
// credentials directory.
func (g *Auth) GmailTokenPath(brainID string) string {
	return GmailTokenFile(g.CredsDir(), brainID)
}

// gmailLock is the key of brainID's Gmail token in tokenLocks. A brain's id
// has no NUL, so it is never another token's key.
func gmailLock(brainID string) string { return "gmail\x00" + brainID }

func (g *Auth) lockGmail(ctx context.Context, brainID string) (unlock func(), err error) {
	return g.lockFile(ctx, gmailLock(brainID), "brain "+brainID+"'s Gmail token")
}

// StartGmailSignIn returns the Google URL that connects brainID's Gmail. It
// comes back through the bridge's own redirect, as the calendar's does, and
// the browser then goes to the brain's page.
func (g *Auth) StartGmailSignIn(brainID string) (string, error) {
	c := g.Client()
	if c == nil {
		return "", ErrNoClient
	}
	g.mu.Lock()
	delete(g.gmailError, brainID)
	g.mu.Unlock()
	return g.StartSignInFor(c, []string{GmailScope}, "/#/brains/"+url.PathEscape(brainID), func(tok *Token, err error) {
		g.gmailSignedIn(brainID, tok, err)
	})
}

// gmailSignedIn saves the token of brainID's Gmail sign-in, with the
// mailbox's address when Gmail tells it, or records why there is none for
// its page. A token it replaces is not revoked: it may well be of the same
// grant as the new one, which would go with it.
func (g *Auth) gmailSignedIn(brainID string, tok *Token, err error) {
	if err == nil && !g.configured(brainID) {
		// One left behind would count as connected for a later brain of
		// the same id.
		err = errors.New("the brain was removed while it signed in")
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if addr, perr := g.gmailAddress(ctx, tok); perr == nil {
			tok.Account = addr
		} else {
			// The page's check says why (the Gmail API not enabled, say).
			log.Printf("brain %s: reading the Gmail address: %s", brainID, oneLine(perr.Error()))
		}
		cancel()
		err = g.saveGmailToken(brainID, tok)
	}
	if err != nil {
		g.mu.Lock()
		g.gmailError[brainID] = err.Error()
		g.mu.Unlock()
		// It may hold what the browser brought back (Google's error).
		log.Printf("brain %s: Gmail sign-in failed: %s", brainID, oneLine(err.Error()))
		return
	}
	log.Printf("brain %s: Gmail connected", brainID)
}

func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\r", " ").Replace(s) }

// gmailAddress is the mailbox's address, from Gmail's profile.
func (g *Auth) gmailAddress(ctx context.Context, tok *Token) (string, error) {
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := g.gmail(ctx, tok, http.MethodGet, "/profile", nil, nil, &p); err != nil {
		return "", err
	}
	return p.EmailAddress, nil
}

func (g *Auth) saveGmailToken(brainID string, tok *Token) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := g.lockGmail(ctx, brainID)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(g.GmailTokenPath(brainID), data, 0o600, false); err != nil {
		return fmt.Errorf("saving the token failed: %w", err)
	}
	return nil
}

// GmailConnected reports whether brainID has a Gmail token: a file of that
// name, which the Gmail commands use, or say why they cannot. A symlink
// counts, so that the brain hears why it is not used.
func (g *Auth) GmailConnected(brainID string) bool {
	_, err := os.Lstat(g.GmailTokenPath(brainID))
	return err == nil
}

// GmailAccessToken returns a valid access token of brainID's Gmail
// sign-in, refreshing (and saving) it when it has expired, as AccessToken
// does the calendar's.
func (g *Auth) GmailAccessToken(ctx context.Context, brainID string) (*Token, error) {
	unlock, err := g.lockGmail(ctx, brainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return g.freshToken(ctx, g.GmailTokenPath(brainID), brainID, "gmail")
}

// GmailSnapshot is a brain's Gmail sign-in as its page shows it.
type GmailSnapshot struct {
	ClientConfigured bool `json:"client_configured"`
	Connected        bool `json:"connected"`
	// Account is the mailbox's address, when Gmail told it at sign-in.
	Account string `json:"account,omitempty"`
	// Works is whether Gmail answered with the sign-in, when the page
	// asked for that check; CheckError then says why not.
	Works      *bool  `json:"works,omitempty"`
	CheckError string `json:"check_error,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	// Scope is what the sign-in asks for, to add on the consent screen.
	Scope     string `json:"scope"`
	TokenPath string `json:"token_path"`
}

// GmailStatus returns brainID's Gmail sign-in; check asks Gmail whether it
// works, with a read of the mailbox's profile.
func (g *Auth) GmailStatus(ctx context.Context, brainID string, check bool) GmailSnapshot {
	resp := GmailSnapshot{ClientConfigured: g.Client() != nil, Connected: g.GmailConnected(brainID), Scope: GmailScope, TokenPath: g.GmailTokenPath(brainID)}
	g.mu.Lock()
	resp.LastError = g.gmailError[brainID]
	g.mu.Unlock()
	if !resp.Connected {
		return resp
	}
	if t, err := loadTokenAt(resp.TokenPath); err == nil {
		resp.Account = t.Account
	}
	if check {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		tok, err := g.GmailAccessToken(ctx, brainID)
		if err == nil {
			_, err = g.gmailAddress(ctx, tok)
		}
		works := err == nil
		resp.Works = &works
		if err != nil {
			_, resp.CheckError = GmailProblem(err)
		}
	}
	return resp
}

func loadTokenAt(path string) (*Token, error) {
	t, _, err := loadTokenFieldsAt(path)
	return t, err
}

// DisconnectGmail ends brainID's Gmail access and deletes its token. It
// revokes the refresh token at Google first, unless another token in the
// credentials dir is from the same client and may be for the same account,
// the brain's own calendar or contacts sign-in included: Google revokes the
// app's whole grant for the account, which that token would lose too; note
// then says why it did not. When Google does not confirm the revocation,
// the token stays and the error wraps ErrNotRevoked, unless localOnly asks
// to delete it anyway.
func (g *Auth) DisconnectGmail(ctx context.Context, brainID string, localOnly bool) (revoked bool, note string, err error) {
	unlock, err := g.lockGmail(ctx, brainID)
	if err != nil {
		return false, "", err
	}
	defer unlock()
	path := g.GmailTokenPath(brainID)
	t, _, lerr := loadTokenFieldsAt(path)
	switch {
	case lerr != nil || t.RefreshToken == "":
		// Nothing that could be revoked.
	case localOnly:
		note = "the access was not revoked at Google: remove it at myaccount.google.com/permissions"
	default:
		if other := g.holderOf(calendarTokenPrefix, "", t); other != "" {
			note = fmt.Sprintf("the access was not revoked at Google, since brain %s's calendar sign-in is from the same client and may be for the same Google account: "+
				"it would lose its access too. Once neither needs it, remove it at myaccount.google.com/permissions", other)
			break
		}
		if other := g.contactsSharing("", t); other != "" {
			note = contactsSharingNote(other)
			break
		}
		if other := g.gmailSharing(brainID, t); other != "" {
			note = gmailSharingNote(other)
			break
		}
		if err := g.revoke(ctx, t.RefreshToken); err != nil {
			return false, "", err
		}
		revoked = true
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return revoked, note, err
	}
	g.mu.Lock()
	delete(g.gmailError, brainID)
	g.mu.Unlock()
	return revoked, note, nil
}

// gmailSharing names a brain, other than skip, whose Gmail token may hold
// the grant t belongs to: one from the same client, for t's account or for
// one either does not name.
func (g *Auth) gmailSharing(skip string, t *Token) string {
	return g.holderOf(gmailTokenPrefix, skip, t)
}

// gmailSharingNote says why a disconnect did not revoke a grant that brain
// other's Gmail token may hold.
func gmailSharingNote(other string) string {
	return fmt.Sprintf("the access was not revoked at Google, since brain %s's Gmail sign-in is from the same client and may be for the same Google account: "+
		"it would lose its access too. Once neither needs it, remove it at myaccount.google.com/permissions", other)
}

// gmailReconnectHint is what a brain tells its owner when Google no longer
// takes its Gmail sign-in.
const gmailReconnectHint = "Ask the owner to connect Gmail again in the dashboard: Brains → this brain → Gmail → Reconnect."

// gmailDisabledHint is what to do when the Gmail API is off in the OAuth
// client's project.
const gmailDisabledHint = "The Gmail API is not enabled in the Google Cloud project of the bridge's OAuth client: the owner enables it there (docs/GMAIL.md)."

// GmailProblem is the status and message a brain's Gmail call gets for
// err, which came from getting the brain's Gmail token or from Gmail. As
// with the calendar (CallProblem), only a sign-in Google refuses, or one
// the bridge cannot use, tells the brain to have its owner connect again.
func GmailProblem(err error) (int, string) {
	var se *signInError
	var oe *oauthError
	var ge *googleAPIError
	var ne net.Error
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusPreconditionFailed, "Gmail is not connected for this brain. Ask the owner to connect it in the dashboard: Brains → this brain → Gmail → Connect Gmail."
	case errors.Is(err, ErrNoGmail):
		return http.StatusPreconditionFailed, err.Error()
	case errors.As(err, &se):
		return http.StatusPreconditionFailed, "the brain's Gmail sign-in cannot be used (" + err.Error() + "). " + gmailReconnectHint
	case errors.As(err, &oe) && oe.temporary():
		return http.StatusServiceUnavailable, "Google could not refresh the Gmail sign-in just now (" + oe.message + "). This is usually temporary; try again in a minute."
	case errors.As(err, &oe):
		return http.StatusPreconditionFailed, "Google refused to refresh the Gmail sign-in (" + oe.message + "): it was revoked or has expired. " + gmailReconnectHint
	case errors.As(err, &ge):
		msg := err.Error()
		if h := ge.hint(); h != "" {
			msg += ". " + h
		}
		if ge.status == http.StatusNotFound {
			return http.StatusNotFound, msg
		}
		return http.StatusBadGateway, msg
	case errors.As(err, &ne):
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusBadGateway, err.Error()
}
