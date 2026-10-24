package google

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The health of each brain's calendar grant: when Google last answered a
// calendar call with it, and whether Google refused it since. Every call
// a brain makes, the dashboard's look at the brain's calendars and a check
// every few hours (CheckGrants) note what happened (Note). Once a grant
// breaks, Options.OnBroken hears it once, and the brain's page, the
// Overview and doctor say so, until the brain connects again or a call
// works again. The record is kept in Options.HealthPath, beside the
// client file, so that it lasts a restart and doctor can read it.

// The states of a grant, GrantHealth.State.
const (
	// GrantValid: Google answered the last call with it.
	GrantValid = "valid"
	// GrantExpiring: it works, but Google ends it soon (an app in Testing,
	// or access granted for a limited time): connect again before then.
	GrantExpiring = "expiring"
	// GrantRevoked: Google refused it (invalid_grant): the access was
	// removed, it expired, or the account's password changed.
	GrantRevoked = "revoked"
	// GrantReconsent: the bridge cannot use it as it is (no refresh
	// token, from another client, a scope missing, an unreadable file), or
	// Google refused the client it was issued to: only a new consent helps.
	GrantReconsent = "needs_reconsent"
	// GrantUnchecked: no call has used it since it was saved.
	GrantUnchecked = "unchecked"
)

// expiringWithin is how long before Google ends a sign-in it counts as
// expiring.
const expiringWithin = 48 * time.Hour

// grantRecord is a brain's entry in the health file.
type grantRecord struct {
	LastOK   time.Time `json:"last_ok,omitzero"`
	Broken   string    `json:"broken,omitempty"` // GrantRevoked or GrantReconsent
	Detail   string    `json:"detail,omitempty"`
	Since    time.Time `json:"since,omitzero"`
	Notified bool      `json:"notified,omitempty"`
}

// GrantHealth is a brain's calendar grant as the dashboard and doctor see
// it. Detail never holds a token.
type GrantHealth struct {
	State string `json:"state"`
	// Detail says what Google or the bridge said, for a broken grant.
	Detail string `json:"detail,omitempty"`
	// LastOK is the last time Google answered a calendar call with it.
	LastOK *time.Time `json:"last_ok,omitempty"`
	// Since is when it broke.
	Since *time.Time `json:"since,omitempty"`
	// Expires is when Google ends the sign-in, for one it gave a limit.
	Expires *time.Time `json:"expires,omitempty"`
}

// Broken reports whether the grant needs the owner: revoked or needing a
// new consent.
func (h GrantHealth) Broken() bool { return h.State == GrantRevoked || h.State == GrantReconsent }

// ReadGrantHealth reads the health file at path: brain id → record, as
// GrantHealth, without what the token says (Expires). doctor reads it so;
// a missing file is no brain.
func ReadGrantHealth(data []byte) (map[string]GrantHealth, error) {
	var recs map[string]grantRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	out := map[string]GrantHealth{}
	for id, r := range recs {
		out[id] = r.health(nil, time.Now())
	}
	return out, nil
}

func (r grantRecord) health(expires *time.Time, now time.Time) GrantHealth {
	h := GrantHealth{State: GrantUnchecked, Expires: expires}
	if !r.LastOK.IsZero() {
		t := r.LastOK
		h.LastOK, h.State = &t, GrantValid
	}
	switch {
	case r.Broken != "":
		since := r.Since
		h.State, h.Detail, h.Since = r.Broken, r.Detail, &since
	case expires != nil && !expires.After(now):
		h.State, h.Detail = GrantReconsent, "Google ended this sign-in on "+expires.Local().Format("2 Jan 2006 15:04")
	case expires != nil && expires.Sub(now) < expiringWithin && h.State == GrantValid:
		h.State, h.Detail = GrantExpiring, "Google ends this sign-in on "+expires.Local().Format("2 Jan 2006 15:04")+": connect again before then"
	}
	return h
}

// loadHealthLocked reads the health file into g.health once. g.mu is held.
func (g *Auth) loadHealthLocked() {
	if g.health != nil {
		return
	}
	g.health = map[string]grantRecord{}
	if g.options.HealthPath == "" {
		return
	}
	data, err := os.ReadFile(g.options.HealthPath)
	if err == nil {
		err = json.Unmarshal(data, &g.health)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("google: reading the calendar health file: %v", err)
		g.health = map[string]grantRecord{}
	}
}

// saveHealthLocked writes g.health. g.mu is held.
func (g *Auth) saveHealthLocked() {
	if g.options.HealthPath == "" {
		return
	}
	data, err := json.MarshalIndent(g.health, "", "  ")
	if err == nil {
		err = fsx.WriteFileAtomic(g.options.HealthPath, data, 0o600, false)
	}
	if err != nil {
		log.Printf("google: saving the calendar health file: %v", err)
	}
}

// classify says what err, from getting a brain's token or from a call to
// Google with it, means for the grant: broken (and how), or nothing new
// (a Google outage, a calendar it cannot read, the network).
func classify(err error) (state, detail string) {
	var se *signInError
	var oe *oauthError
	var ge *googleAPIError
	switch {
	case errors.As(err, &se):
		return GrantReconsent, se.Error()
	case errors.As(err, &oe) && oe.revoked():
		return GrantRevoked, "Google no longer accepts this sign-in (" + oe.message + ")"
	case errors.As(err, &oe) && (oe.code == "invalid_client" || oe.code == "unauthorized_client"):
		return GrantReconsent, "Google refuses the OAuth client this brain signed in with (" + oe.message + "): check Settings → Google sign-in, then connect again"
	case errors.As(err, &ge) && (ge.status == http.StatusUnauthorized || (ge.status == http.StatusForbidden && ge.scopeMissing())):
		return GrantReconsent, "Google refused the calendar access the brain has (" + ge.Error() + "): connect again and leave every box ticked"
	}
	return "", ""
}

// Note records what a calendar call of brainID came to: err nil is a call
// Google answered. A grant that breaks is told to Options.OnBroken, once
// until it works again or the brain connects again. A brain that is not
// connected, or an error that says nothing about the grant, changes
// nothing.
func (g *Auth) Note(brainID string, err error) {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, context.Canceled) {
		return
	}
	state, detail := "", ""
	if err != nil {
		if state, detail = classify(err); state == "" {
			return
		}
	}
	now := time.Now()
	g.mu.Lock()
	g.loadHealthLocked()
	r := g.health[brainID]
	tell := false
	switch {
	case err == nil:
		healed := r.Broken != ""
		// A call a minute is plenty for "last used", and spares the disk.
		if !healed && now.Sub(r.LastOK) < time.Minute {
			g.mu.Unlock()
			return
		}
		r = grantRecord{LastOK: now}
	case r.Broken == state && r.Detail == detail:
		g.mu.Unlock()
		return
	default:
		if r.Broken == "" {
			r.Since = now
		}
		r.Broken, r.Detail = state, detail
		tell = !r.Notified
		r.Notified = true
	}
	g.health[brainID] = r
	g.saveHealthLocked()
	g.mu.Unlock()
	if tell {
		log.Printf("brain %s: its Google Calendar sign-in stopped working: %s", brainID, detail)
		if g.options.OnBroken != nil {
			g.options.OnBroken(brainID, state)
		}
	}
}

// forgetHealth drops brainID's record: it disconnected.
func (g *Auth) forgetHealth(brainID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadHealthLocked()
	if _, ok := g.health[brainID]; ok {
		delete(g.health, brainID)
		g.saveHealthLocked()
	}
}

// signedIn starts brainID's record afresh: a new sign-in that worked.
func (g *Auth) signedIn(brainID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadHealthLocked()
	g.health[brainID] = grantRecord{LastOK: time.Now()}
	g.saveHealthLocked()
}

// Health is brainID's grant, or nil when it is not connected.
func (g *Auth) Health(brainID string) *GrantHealth {
	if _, err := os.Lstat(g.TokenPath(brainID)); err != nil {
		return nil
	}
	var expires *time.Time
	if t, err := g.loadToken(brainID); err == nil && t.RefreshExpiry != "" {
		if e, err := time.Parse(time.RFC3339Nano, t.RefreshExpiry); err == nil {
			expires = &e
		}
	}
	g.mu.Lock()
	g.loadHealthLocked()
	r := g.health[brainID]
	g.mu.Unlock()
	h := r.health(expires, time.Now())
	return &h
}

// CheckGrants asks Google, for each brain in ids that is connected, for
// its calendar list, and notes the answer: a grant revoked at Google is
// found without waiting for the brain to use its calendar.
func (g *Auth) CheckGrants(ctx context.Context, ids []string) {
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := os.Lstat(g.TokenPath(id)); err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tok, err := g.AccessToken(cctx, id)
		if err == nil {
			_, err = g.CalendarList(cctx, tok)
		}
		cancel()
		g.Note(id, err)
	}
}
