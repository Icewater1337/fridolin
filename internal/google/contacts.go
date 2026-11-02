package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Google Contacts, for the core brain's errands (docs/CALENDAR.md, "Google
// Contacts"): a sign-in of the brain's own, apart from its calendar's,
// that grants reading the account's contacts through the People API and
// nothing else. The calendar's sign-in stays as it is: a brain that
// connected its calendar before keeps working, and one that never needs
// contacts is never asked for them.
//
// The token is written next to the calendar's, in the same format,
//
//	<credentials dir>/google_contacts_token_<brain>.json
//
// and only the bridge reads it: find_contact asks Google with it for each
// lookup, and nothing it finds is kept. contacts.readonly does not let the
// bridge read the account's address, so the token names no account, and
// it may share its grant at Google with any other token from the same
// client: a disconnect, of the calendar or of the contacts, does not
// revoke a grant another token may hold (contactsSharing).

// ContactsScope is what a brain's contacts sign-in asks for: reading the
// account's contacts, and no more.
const ContactsScope = "https://www.googleapis.com/auth/contacts.readonly"

// contactsTokenPrefix starts the name of a brain's Contacts token, which
// ends as a calendar token's does.
const contactsTokenPrefix = "google_contacts_token_"

// MaxContactMatches is the most people SearchContacts answers.
const MaxContactMatches = 10

// ContactsTokenFile is a brain's Google Contacts token in dir, the
// credentials dir. Only the bridge reads and writes it.
func ContactsTokenFile(dir, brainID string) string {
	return filepath.Join(dir, contactsTokenPrefix+brainID+calendarTokenSuffix)
}

// ContactsTokenPath resolves the brain's Contacts token in the current
// credentials directory.
func (g *Auth) ContactsTokenPath(brainID string) string {
	return ContactsTokenFile(g.CredsDir(), brainID)
}

// contactsLock is the key of brainID's Contacts token in tokenLocks. A
// brain's id has no NUL, so it is never a calendar token's key.
func contactsLock(brainID string) string { return "contacts\x00" + brainID }

func (g *Auth) lockContacts(ctx context.Context, brainID string) (unlock func(), err error) {
	return g.lockFile(ctx, contactsLock(brainID), "brain "+brainID+"'s Google Contacts token")
}

// StartContactsSignIn returns the Google URL that connects brainID's
// Google Contacts. It comes back through the bridge's own redirect, as the
// calendar's does, and the browser then goes to the brain's page.
func (g *Auth) StartContactsSignIn(brainID string) (string, error) {
	c := g.Client()
	if c == nil {
		return "", ErrNoClient
	}
	g.mu.Lock()
	delete(g.contactsError, brainID)
	g.mu.Unlock()
	return g.StartSignInFor(c, []string{ContactsScope}, "/#/brains/"+url.PathEscape(brainID), func(tok *Token, err error) {
		g.contactsSignedIn(brainID, tok, err)
	})
}

// contactsSignedIn saves the token of brainID's Contacts sign-in, or
// records why there is none for its page. A token it replaces is not
// revoked: it may well be of the same grant as the new one, which would go
// with it.
func (g *Auth) contactsSignedIn(brainID string, tok *Token, err error) {
	if err == nil && !g.configured(brainID) {
		// One left behind would count as connected for a later brain of
		// the same id.
		err = errors.New("the brain was removed while it signed in")
	}
	if err == nil {
		err = g.saveContactsToken(brainID, tok)
	}
	if err != nil {
		g.mu.Lock()
		g.contactsError[brainID] = err.Error()
		g.mu.Unlock()
		// It may hold what the browser brought back (Google's error).
		log.Printf("brain %s: Google Contacts sign-in failed: %s", brainID, strings.NewReplacer("\n", " ", "\r", " ").Replace(err.Error()))
		return
	}
	log.Printf("brain %s: Google Contacts connected", brainID)
}

func (g *Auth) saveContactsToken(brainID string, tok *Token) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := g.lockContacts(ctx, brainID)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(g.ContactsTokenPath(brainID), data, 0o600, false); err != nil {
		return fmt.Errorf("saving the token failed: %w", err)
	}
	return nil
}

// ContactsConnected reports whether brainID has a Google Contacts token: a
// file of that name, which find_contact uses, or says why it cannot. A
// symlink counts, so that the brain hears why it is not used.
func (g *Auth) ContactsConnected(brainID string) bool {
	_, err := os.Lstat(g.ContactsTokenPath(brainID))
	return err == nil
}

// ContactsAccessToken returns a valid access token of brainID's Contacts
// sign-in, refreshing (and saving) it when it has expired, as
// AccessToken does the calendar's.
func (g *Auth) ContactsAccessToken(ctx context.Context, brainID string) (*Token, error) {
	unlock, err := g.lockContacts(ctx, brainID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return g.freshToken(ctx, g.ContactsTokenPath(brainID), brainID, "contacts")
}

// ContactsSnapshot is a brain's Google Contacts sign-in as its page shows it.
type ContactsSnapshot struct {
	ClientConfigured bool `json:"client_configured"`
	Connected        bool `json:"connected"`
	// Works is whether Google answered a search with the sign-in, when the
	// page asked for that check; CheckError then says why not.
	Works      *bool  `json:"works,omitempty"`
	CheckError string `json:"check_error,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	// Scope is what the sign-in asks for, to add on the consent screen.
	Scope     string `json:"scope"`
	TokenPath string `json:"token_path"`
}

// ContactsStatus returns brainID's Contacts sign-in; check asks Google
// whether it works, with the warm-up request every search starts with.
func (g *Auth) ContactsStatus(ctx context.Context, brainID string, check bool) ContactsSnapshot {
	resp := ContactsSnapshot{ClientConfigured: g.Client() != nil, Connected: g.ContactsConnected(brainID), Scope: ContactsScope, TokenPath: g.ContactsTokenPath(brainID)}
	g.mu.Lock()
	resp.LastError = g.contactsError[brainID]
	g.mu.Unlock()
	if resp.Connected && check {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		tok, err := g.ContactsAccessToken(ctx, brainID)
		if err == nil {
			err = g.peopleAPI(ctx, tok, searchContactsPath, warmUp(1), nil)
		}
		works := err == nil
		resp.Works = &works
		if err != nil {
			resp.CheckError = ContactsProblem(err)
		}
	}
	return resp
}

// DisconnectContacts ends brainID's Contacts access and deletes its token.
// It revokes the refresh token at Google first, unless another token in
// the credentials dir is from the same client and may be for the same
// account, the calendar's included: Google revokes the app's whole grant
// for the account, which that token would lose too; note then says why it
// did not. When Google does not confirm the revocation, the token stays
// and the error wraps ErrNotRevoked, unless localOnly asks to delete it
// anyway.
func (g *Auth) DisconnectContacts(ctx context.Context, brainID string, localOnly bool) (revoked bool, note string, err error) {
	unlock, err := g.lockContacts(ctx, brainID)
	if err != nil {
		return false, "", err
	}
	defer unlock()
	path := g.ContactsTokenPath(brainID)
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
		if other := g.contactsSharing(brainID, t); other != "" {
			note = contactsSharingNote(other)
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
	delete(g.contactsError, brainID)
	g.mu.Unlock()
	return revoked, note, nil
}

// contactsSharingNote says why a disconnect did not revoke a grant that
// brain other's Contacts token may hold.
func contactsSharingNote(other string) string {
	return fmt.Sprintf("the access was not revoked at Google, since brain %s's Google Contacts sign-in is from the same client and may be for the same Google account: "+
		"it would lose its access too. Once neither needs it, remove it at myaccount.google.com/permissions", other)
}

// contactsSharing names a brain, other than skip, whose Contacts token may
// hold the grant t belongs to: one from the same client. A Contacts token
// names no account, so it may be for t's.
func (g *Auth) contactsSharing(skip string, t *Token) string {
	return g.holderOf(contactsTokenPrefix, skip, t)
}

// holderOf names the brain, other than skip, of a token in the credentials
// dir whose name starts with prefix, from t's client, for t's account or
// for one either does not name; "" for none.
func (g *Auth) holderOf(prefix, skip string, t *Token) string {
	entries, _ := os.ReadDir(g.CredsDir()) // sorted by name
	for _, e := range entries {
		other, ok := strings.CutPrefix(e.Name(), prefix)
		if ok {
			other, ok = strings.CutSuffix(other, calendarTokenSuffix)
		}
		if !ok || other == "" || other == skip {
			continue
		}
		data, err := readCredentialFile(filepath.Join(g.CredsDir(), e.Name()))
		if err != nil {
			continue
		}
		var o Token
		if json.Unmarshal(data, &o) != nil || o.RefreshToken == "" || o.ClientID != t.ClientID {
			continue
		}
		if o.Account == "" || t.Account == "" || strings.EqualFold(o.Account, t.Account) {
			return other
		}
	}
	return ""
}

// Contact is a person of the account's contacts, as SearchContacts finds
// them.
type Contact struct {
	Name   string         `json:"name"`
	Phones []ContactPhone `json:"phones"`
}

// ContactPhone is one of a contact's phone numbers.
type ContactPhone struct {
	// Value is the number as the contact holds it.
	Value string `json:"value"`
	// Canonical is Google's E.164 form of it ("+15551234567"), "" when
	// Google has none.
	Canonical string `json:"canonical,omitempty"`
	// Type is its label: Mobile, Home, Work, or one of the contact's own.
	Type string `json:"type,omitempty"`
}

const searchContactsPath = "/v1/people:searchContacts"

// warmUp is the request a search starts with: an empty query, which brings
// the cache Google searches up to date.
func warmUp(limit int) url.Values {
	return url.Values{"query": {""}, "readMask": {"names,phoneNumbers"}, "pageSize": {strconv.Itoa(limit)}}
}

// SearchContacts finds the account's contacts whose names, nicknames,
// addresses, numbers or organizations match query, at most limit of them
// (MaxContactMatches when it is not between 1 and that), with their names
// and phone numbers. Google answers a search from a cache that a request
// with an empty query brings up to date, so that goes first.
func (g *Auth) SearchContacts(ctx context.Context, tok *Token, query string, limit int) ([]Contact, error) {
	if limit <= 0 || limit > MaxContactMatches {
		limit = MaxContactMatches
	}
	q := warmUp(limit)
	if err := g.peopleAPI(ctx, tok, searchContactsPath, q, nil); err != nil {
		return nil, err
	}
	q.Set("query", query)
	var page struct {
		Results []struct {
			Person struct {
				Names []struct {
					DisplayName string `json:"displayName"`
					Metadata    struct {
						Primary bool `json:"primary"`
					} `json:"metadata"`
				} `json:"names"`
				PhoneNumbers []struct {
					Value         string `json:"value"`
					CanonicalForm string `json:"canonicalForm"`
					Type          string `json:"type"`
					FormattedType string `json:"formattedType"`
				} `json:"phoneNumbers"`
			} `json:"person"`
		} `json:"results"`
	}
	if err := g.peopleAPI(ctx, tok, searchContactsPath, q, &page); err != nil {
		return nil, err
	}
	out := []Contact{}
	for _, r := range page.Results {
		if len(out) == limit {
			break
		}
		c := Contact{Phones: []ContactPhone{}}
		for i, n := range r.Person.Names {
			if i == 0 || n.Metadata.Primary {
				c.Name = strings.TrimSpace(n.DisplayName)
			}
			if n.Metadata.Primary {
				break
			}
		}
		for _, p := range r.Person.PhoneNumbers {
			value := strings.TrimSpace(p.Value)
			if value == "" {
				continue
			}
			typ := p.FormattedType
			if typ == "" {
				typ = p.Type
			}
			c.Phones = append(c.Phones, ContactPhone{Value: value, Canonical: p.CanonicalForm, Type: strings.TrimSpace(typ)})
		}
		out = append(out, c)
	}
	return out, nil
}

// peopleAPIError is an error answer of the People API.
type peopleAPIError struct {
	status  int
	message string
	// reason is Google's reason for it, when it gave one: SERVICE_DISABLED,
	// ACCESS_TOKEN_SCOPE_INSUFFICIENT, …
	reason string
}

func (e *peopleAPIError) Error() string {
	return fmt.Sprintf("Google's People API %d: %s", e.status, strings.TrimSuffix(e.message, "."))
}

// disabled reports whether e says the People API is not enabled in the
// OAuth client's project.
func (e *peopleAPIError) disabled() bool {
	m := strings.ToLower(e.message)
	return e.reason == "SERVICE_DISABLED" || (e.status == http.StatusForbidden && (strings.Contains(m, "has not been used") || strings.Contains(m, "is disabled")))
}

// hint is what to do about e, or "".
func (e *peopleAPIError) hint() string {
	switch {
	case e.disabled():
		return "The People API is not enabled in the Google Cloud project of the bridge's OAuth client: the owner enables it there (docs/CALENDAR.md, \"Google Contacts\")."
	case e.status == http.StatusUnauthorized, e.status == http.StatusForbidden:
		return contactsReconnectHint
	case e.status == http.StatusTooManyRequests:
		return "Google limits how often the contacts are searched: try again in a minute."
	}
	return ""
}

// contactsReconnectHint is what a brain tells its owner when Google no
// longer takes its Contacts sign-in.
const contactsReconnectHint = "Ask the owner to connect Google Contacts again in the dashboard: Brains → this brain → Google Contacts."

// ContactsProblem is what a lookup tells the brain for err, which came
// from getting the brain's Contacts token or from Google.
func ContactsProblem(err error) string {
	var se *signInError
	var oe *oauthError
	var pe *peopleAPIError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "Google Contacts is not connected for this brain. Ask the owner to connect it in the dashboard: Brains → this brain → Google Contacts → Connect Google Contacts."
	case errors.As(err, &se):
		return "the brain's Google Contacts sign-in cannot be used (" + err.Error() + "). " + contactsReconnectHint
	case errors.As(err, &oe) && oe.temporary():
		return "Google could not refresh the Google Contacts sign-in just now (" + oe.message + "). This is usually temporary; try again in a minute."
	case errors.As(err, &oe):
		return "Google refused to refresh the Google Contacts sign-in (" + oe.message + "): it was revoked or has expired. " + contactsReconnectHint
	case errors.As(err, &pe):
		if h := pe.hint(); h != "" {
			return err.Error() + ". " + h
		}
	}
	return err.Error()
}

// peopleAPI calls the People API with tok: GET path with query, the answer
// decoded into out when it is not nil.
func (g *Auth) peopleAPI(ctx context.Context, tok *Token, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.options.Endpoints.People+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := g.options.HTTP.Do(req)
	if err != nil {
		// Not the URL, which holds what was searched for.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("could not reach Google's People API (%w); this is usually temporary, try again in a minute", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Details []struct {
					Reason string `json:"reason"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		pe := &peopleAPIError{status: resp.StatusCode, message: e.Error.Message}
		if pe.message == "" {
			pe.message = http.StatusText(resp.StatusCode)
		}
		for _, d := range e.Error.Details {
			if d.Reason != "" {
				pe.reason = d.Reason
				break
			}
		}
		return pe
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("reading the People API's answer: %w", err)
	}
	return nil
}
