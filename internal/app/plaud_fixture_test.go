package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// These fixtures describe the persisted version-2 state contract. Command
// integration tests arrange state between service operations; they do not
// reach into the service's decoder, state lock or login bookkeeping.
type plaudFixtureState struct {
	Version int `json:"version"`
	// Seeded means the first sync for Account ran: Seen then held every
	// recording the account had, so none of them is sent. A state that is
	// not seeded only keeps the times below.
	Seeded bool `json:"seeded"`
	// Account is the Plaud account the state was seeded for; its seen list
	// means nothing for another one.
	Account string   `json:"account,omitempty"`
	Seen    []string `json:"seen"`
	// Pending holds the recordings that are new and not handed over yet.
	Pending map[string]*plaudFixturePending `json:"pending"`
	// Forwarded lists the latest recordings handed to a brain, one entry
	// each, the latest last, with how their runs went, for the card.
	Forwarded []plaud.Forward `json:"forwarded,omitempty"`
	// GivenUp lists the latest recordings Fridolin gave up on, oldest
	// first, for the card.
	GivenUp []plaudFixtureGivenUp `json:"given_up,omitempty"`
	// AuthNotified is when the chat was last told the login expired.
	AuthNotified time.Time `json:"auth_notified,omitzero"`
	// Login is the sign-in Fridolin made, kept until Disconnect. Plaud's CLI
	// deletes a login that expired (its refresh token ran out, or Plaud
	// refused it), so a login recorded here whose tokens.json is gone has
	// expired, rather than never been made.
	Login *plaudFixtureLogin `json:"login,omitempty"`
	// Disconnected is set by Disconnect and cleared by the next sign-in.
	// Until then no login counts that Fridolin did not record, and nothing
	// records one, as a check or a look-up of the account otherwise would
	// for a login from before sign-ins were recorded. A check still running
	// when you disconnected must not bring the login back, as expired, and
	// one your terminal's plaud makes in ~/.plaud (with brain_access) is
	// not Fridolin's.
	Disconnected bool `json:"disconnected,omitempty"`
}

// plaudFixtureLogin is a sign-in to Plaud.
type plaudFixtureLogin struct {
	// Dir is the CLI home it was made in (see cliHome).
	Dir   string    `json:"dir"`
	Since time.Time `json:"since"`
	// Account names who signed in (an email, else a nickname), for the
	// card, as `plaud me` last said at Checked.
	Account string    `json:"account,omitempty"`
	Checked time.Time `json:"checked,omitzero"`
}

// plaudFixturePending is a recording waiting to be handed over.
type plaudFixturePending struct {
	FirstSeen time.Time `json:"first_seen"`
	// Name is the recording's name, once Plaud said it.
	Name string `json:"name,omitempty"`
	// Tries counts the checks that found it not ready, or failed to fetch
	// it. The next try is not before NextTry (see plaudRetryDelay).
	Tries   int       `json:"tries,omitempty"`
	NextTry time.Time `json:"next_try,omitzero"`
	// Handed is when it was handed to a brain whose run has not ended yet.
	// A recording stays here until its run answered it, so one whose run is
	// lost (the bridge restarted meanwhile) is handed over again. Brain is
	// the brain it went to, also once the card's entry for it is trimmed;
	// Runs counts the runs that failed.
	Handed time.Time `json:"handed,omitzero"`
	Brain  string    `json:"brain,omitempty"`
	Runs   int       `json:"runs,omitempty"`
}

// plaudFixtureGivenUp is a recording Fridolin stopped trying to hand over.
type plaudFixtureGivenUp struct {
	ID     string    `json:"id"`
	Name   string    `json:"name,omitempty"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

func (tb *testBridge) readPlaudState() (*plaudFixtureState, error) {
	data, err := os.ReadFile(filepath.Join(tb.cfgs.Boot().PlaudDir(), "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := &plaudFixtureState{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Pending == nil {
		st.Pending = map[string]*plaudFixturePending{}
	}
	return st, nil
}
func (tb *testBridge) writePlaudState(st *plaudFixtureState) error {
	st.Version = 2
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := tb.cfgs.Boot().PlaudDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, "state.json"), data, 0600, false)
}
func (tb *testBridge) updatePlaudState(fn func(*plaudFixtureState) error) error {
	st, err := tb.readPlaudState()
	if err != nil {
		return err
	}
	if st == nil {
		st = newPlaudFixtureState()
	}
	if err := fn(st); err != nil {
		return err
	}
	return tb.writePlaudState(st)
}
func newPlaudFixtureState() *plaudFixtureState {
	return &plaudFixtureState{Version: 2, Pending: map[string]*plaudFixturePending{}}
}
func (tb *testBridge) plaudTokenPath() string {
	return filepath.Join(tb.plaudFixtureHome(), ".plaud", "tokens.json")
}
func (tb *testBridge) replacePlaud(t *testing.T, change func(*plaud.Options)) {
	t.Helper()
	tb.plaud.Stop()
	options := plaudOptions(tb.cfgs, tb.brainMgr, tb.submitHandOff, tb.submitAsk, tb.wa.SendMessage, nil, plaudFleet{})
	change(&options)
	tb.plaud = plaud.New(options)
	// App.Stop owns the current manager; keep its original cleanup order.
}

// usePlaudRecorder replaces only the runtime submit callback, preserving the
// real settings/selection/projection and notification adapters for HTTP cases.
func (tb *testBridge) usePlaudRecorder(t *testing.T, rec *plaudRecorder) {
	tb.replacePlaud(t, func(o *plaud.Options) {
		target := o.Target
		o.Target = func(id string) *plaud.Target {
			br := target(id)
			if br == nil {
				return nil
			}
			br.Submit = func(jid, sender, text string, media []string, done func(error)) error {
				return rec.submit(br, jid, sender, text, media, done)
			}
			return br
		}
	})
}

// Fixture locations come from startup paths and current settings, without
// asking Status (which may schedule an account refresh for a legacy login).
func (tb *testBridge) plaudFixtureHome() string {
	if tb.cfgs.Get().Plaud.BrainAccess {
		return tb.cfgs.Boot().Home
	}
	return filepath.Join(tb.cfgs.Boot().PlaudDir(), "home")
}
