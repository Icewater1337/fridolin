package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Plaud integration: recordings from a Plaud voice recorder reach a brain as
// finished transcripts, without anyone forwarding them.
//
// Fridolin drives Plaud's official CLI (npm i -g @plaud-ai/cli). "Connect"
// runs `plaud login`, Plaud's OAuth sign-in in the browser; Fridolin then
// polls for new recordings, waits until Plaud has transcribed them, writes
// transcript + Plaud's AI summary to the media inbox and hands the file to the
// chosen brain like an attachment.
//
// The CLI keeps its login in $HOME/.plaud/. By default Fridolin runs it with a
// private home inside the data folder, which the sandbox denies to brains.
// With plaud.brain_access the login stays in ~/.plaud/ instead, so brains can
// run the CLI themselves (search, look up a transcript) — at the cost of
// being able to read the token.

// PlaudConfig controls the Plaud integration.
type PlaudConfig struct {
	// Enabled turns on polling for new recordings.
	Enabled bool `json:"enabled"`
	// Brain receives the recordings (default: the brain that handles DMs,
	// else the default brain).
	Brain string `json:"brain,omitempty"`
	// CLIPath overrides the plaud binary (default: found on PATH, else in
	// the newest nvm node version, else in /opt/homebrew or /usr/local).
	CLIPath string `json:"cli_path,omitempty"`
	// IntervalSeconds between polls (default 60, minimum 30).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// Polished uses Plaud's AI-cleaned transcript instead of the raw one.
	Polished bool `json:"polished,omitempty"`
	// Message is the instruction sent to the brain with each recording,
	// after a fixed line that says what arrived. {name}, {when} and
	// {duration} are filled in. Empty uses defaultPlaudMessage.
	Message string `json:"message,omitempty"`
	// BrainAccess keeps the login in ~/.plaud/ so brains can use the CLI.
	BrainAccess bool `json:"brain_access,omitempty"`
}

// interval is how often Plaud is checked.
func (pc PlaudConfig) interval() time.Duration {
	if pc.IntervalSeconds > 0 {
		return time.Duration(pc.IntervalSeconds) * time.Second
	}
	return time.Minute
}

func (pc PlaudConfig) validate() error {
	if pc.Brain != "" && !brainIDRe.MatchString(pc.Brain) {
		return fmt.Errorf("plaud.brain %q is not a brain id", pc.Brain)
	}
	if pc.IntervalSeconds != 0 && pc.IntervalSeconds < 30 {
		return fmt.Errorf("plaud.interval_seconds must be at least 30")
	}
	return nil
}

// plaudMessageHead starts every message a recording comes with: what
// arrived. Skills and brain prompts can recognise it whatever instruction
// follows. How the brain files a recording is the plaud skill's business
// (examples/skills/plaud.md), not the binary's.
const plaudMessageHead = `[Plaud] New recording "{name}" ({when}, {duration}). The attached .txt is Plaud's finished transcript, with Plaud's AI summary when there is one: it is text already, do not transcribe it again.`

// defaultPlaudMessage is the instruction after plaudMessageHead when
// plaud.message is empty.
const defaultPlaudMessage = `Handle it as skills/plaud.md says. Without that skill: keep it with this brain's notes, then reply with a short summary and the action items.`

var (
	errPlaudAuth     = errors.New("the Plaud login expired")
	errPlaudNotFound = errors.New("the recording is not in Plaud any more")
	// plaudRowRe is a row of `plaud files`: the id in the first column,
	// padded, then two spaces before the name. Only the first column
	// counts: a name may well contain something that looks like an id.
	plaudRowRe = regexp.MustCompile(`(?m)^ {2}(of_[0-9a-f]{16,}) {2}`)
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	plaudKVRe  = regexp.MustCompile(`^\s*([a-z_]+):\s*(.*)$`)
)

const (
	// plaudPageSize is how many recordings one `plaud files` lists, the
	// most the CLI allows. A check reads page after page, newest first,
	// until it reaches a recording it knows, so a gap in checking (the
	// bridge down, the login expired, sending turned off) misses nothing
	// up to plaudListPages pages back.
	plaudPageSize  = 100
	plaudListPages = plaudSeenKeep / plaudPageSize
	// plaudHandedMax caps the recordings with brains at once, waiting in a
	// queue or running (see plaudHandedLimit); the rest wait their turn.
	// Each is a run of its own, and a brain's queue also takes your own
	// messages, which it turns away once it is full: after a gap, a backlog
	// must arrive a few at a time, as runs end, rather than fill it.
	plaudHandedMax     = 5
	plaudPendingMaxAge = 3 * 24 * time.Hour
	// plaudGivenUpKeep is how many given-up recordings the card lists, and
	// plaudForwardedKeep how many handed over.
	plaudGivenUpKeep   = 20
	plaudForwardedKeep = 10
	// plaudRunTries is how many brain runs a recording gets before it is
	// given up; plaudRunRetryDelay is the wait after the first that failed,
	// doubled after each.
	plaudRunTries      = 3
	plaudRunRetryDelay = 5 * time.Minute
	// plaudPolishedWait is how long a recording waits for Plaud's cleaned-up
	// transcript (plaud.polished) before the raw one is sent instead. Plaud
	// makes the raw one first, and may never make the other.
	plaudPolishedWait = time.Hour
	// plaudAccountEvery is how often, at most, the card's status asks Plaud
	// who is signed in. Checks ask whenever the login changes; the card
	// shows what was asked last.
	plaudAccountEvery = 10 * time.Minute
	plaudSeenKeep     = 2000
)

// plaudStateVersion is the layout of state.json that saveState writes.
// Version 1 had no version field and kept times as Unix seconds.
const plaudStateVersion = 2

// plaudState is <data_dir>/plaud/state.json.
type plaudState struct {
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
	Pending map[string]*plaudPending `json:"pending"`
	// Forwarded lists the latest recordings handed to a brain, one entry
	// each, the latest last, with how their runs went, for the card.
	Forwarded []PlaudForward `json:"forwarded,omitempty"`
	// GivenUp lists the latest recordings Fridolin gave up on, oldest
	// first, for the card.
	GivenUp []plaudGivenUp `json:"given_up,omitempty"`
	// AuthNotified is when the chat was last told the login expired.
	AuthNotified time.Time `json:"auth_notified,omitzero"`
	// Login is the sign-in Fridolin made, kept until Disconnect. Plaud's CLI
	// deletes a login that expired (its refresh token ran out, or Plaud
	// refused it), so a login recorded here whose tokens.json is gone has
	// expired, rather than never been made.
	Login *plaudLogin `json:"login,omitempty"`
	// Disconnected is set by Disconnect and cleared by the next sign-in.
	// Until then no login counts that Fridolin did not record, and nothing
	// records one, as a check or a look-up of the account otherwise would
	// for a login from before sign-ins were recorded. A check still running
	// when you disconnected must not bring the login back, as expired, and
	// one your terminal's plaud makes in ~/.plaud (with brain_access) is
	// not Fridolin's.
	Disconnected bool `json:"disconnected,omitempty"`
}

// plaudLogin is a sign-in to Plaud.
type plaudLogin struct {
	// Dir is the CLI home it was made in (see cliHome).
	Dir   string    `json:"dir"`
	Since time.Time `json:"since"`
	// Account names who signed in (an email, else a nickname), for the
	// card, as `plaud me` last said at Checked.
	Account string    `json:"account,omitempty"`
	Checked time.Time `json:"checked,omitzero"`
}

// plaudPending is a recording waiting to be handed over.
type plaudPending struct {
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

// plaudGivenUp is a recording Fridolin stopped trying to hand over.
type plaudGivenUp struct {
	ID     string    `json:"id"`
	Name   string    `json:"name,omitempty"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// plaudRetryDelay is how long a recording waits before the next try after
// tries failed ones: none after the first, so the next check tries again,
// then twice as long each time, up to 15 minutes. A recording Plaud takes
// three days over costs about 300 calls, not one a minute.
func plaudRetryDelay(tries int) time.Duration {
	if tries <= 1 {
		return 0
	}
	if tries > 5 {
		return 15 * time.Minute
	}
	return min(time.Minute<<(tries-1), 15*time.Minute)
}

// plaudHandedLimit is how many recordings may be with brains at once under
// cfg: plaudHandedMax, or half of queue.max_pending when that is less, so
// that the queue keeps room for your own messages. One always may.
func plaudHandedLimit(cfg *Config) int {
	return max(1, min(plaudHandedMax, cfg.Queue.MaxPending/2))
}

// UnmarshalJSON reads both layouts of state.json.
//
// compat: version 1 (no version field) kept the times as Unix seconds,
// and a state counted as seeded when it named the account or remembered
// any recording; one that did neither was only written for an expiry
// notice.
func (st *plaudState) UnmarshalJSON(data []byte) error {
	type current plaudState // without this method
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Version >= 2 {
		return json.Unmarshal(data, (*current)(st))
	}
	var v1 struct {
		Account      string             `json:"account"`
		Seen         []string           `json:"seen"`
		Pending      map[string]float64 `json:"pending"`
		AuthNotified float64            `json:"auth_notified"`
	}
	if err := json.Unmarshal(data, &v1); err != nil {
		return err
	}
	*st = plaudState{Version: 1, Account: v1.Account, Seen: v1.Seen, Pending: map[string]*plaudPending{},
		Seeded: v1.Account != "" || len(v1.Seen) > 0 || len(v1.Pending) > 0}
	for id, at := range v1.Pending {
		st.Pending[id] = &plaudPending{FirstSeen: time.Unix(int64(at), 0)}
	}
	if v1.AuthNotified > 0 {
		st.AuthNotified = time.Unix(int64(v1.AuthNotified), 0)
	}
	return nil
}

// seededFor reports whether st was seeded for the account acct. A state
// seeded before the account was recorded counts for whoever is signed in.
// When the CLI does not say who is signed in (acct is ""), a seeded state
// is taken as it is.
func (st *plaudState) seededFor(acct string) bool {
	return st != nil && st.Seeded && (acct == "" || st.Account == "" || st.Account == acct)
}

// pendingInOrder returns the waiting recordings, the one seen first first.
func (st *plaudState) pendingInOrder() []string {
	ids := make([]string, 0, len(st.Pending))
	for id := range st.Pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := st.Pending[ids[i]].FirstSeen, st.Pending[ids[j]].FirstSeen
		if !a.Equal(b) {
			return a.Before(b)
		}
		return ids[i] < ids[j]
	})
	return ids
}

// PlaudForward is a recording handed to a brain, and how its run went.
type PlaudForward struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Brain string    `json:"brain"`
	At    time.Time `json:"at"`
	// Status is handed (the brain has it: waiting or running), done,
	// stopped (by !stop, the dashboard, or the brain's removal; not sent
	// again unless you send it again) or failed (tried again, up to
	// plaudRunTries runs).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// The statuses of a PlaudForward.
const (
	plaudRunHanded  = "handed"
	plaudRunDone    = "done"
	plaudRunStopped = "stopped"
	plaudRunFailed  = "failed"
)

// forwardOf returns the entry for recording id in st.Forwarded, or nil.
func (st *plaudState) forwardOf(id string) *PlaudForward {
	for i := range st.Forwarded {
		if st.Forwarded[i].ID == id {
			return &st.Forwarded[i]
		}
	}
	return nil
}

// PlaudManager runs the CLI, the poller and the connect flow. It knows the
// rest of the bridge only through the configuration, the brains and two
// functions, like the backup manager, so it can be tested without a bridge.
type PlaudManager struct {
	cfgs   *ConfigStore
	brains *BrainManager
	// submit gives a recording's transcript to br, whose answer goes to
	// jid. An error means br did not take it; done then hears how its run
	// ended (see Inbound.Done).
	submit func(br *Brain, jid, sender, text string, media []string, done func(error)) error
	// notify sends text to a chat (the expiry notice).
	notify func(ctx context.Context, jid, text string) error

	// stMu makes every change to state.json a read, change and write of
	// its own (see update): the poller, the sign-in and the dashboard all
	// change it.
	stMu sync.Mutex
	// cliMu runs the CLI's commands one at a time, all but the sign-in,
	// which waits for the browser for minutes. The CLI rewrites tokens.json
	// when it refreshes the login, without a lock, so two at once could
	// read a half-written login, which reads as signed out, or refresh it
	// twice.
	cliMu sync.Mutex

	mu       sync.Mutex
	connect  *connectJob
	lastPoll time.Time
	lastErr  string
	// refused is set while Plaud refuses a login whose tokens.json is still
	// there (revoked on Plaud's side); the next sign-in or check that works
	// clears it.
	refused bool
	busy    bool
	// inFlight holds the recordings handed to a brain since this start
	// whose runs have not ended. stopping is set by Stop: the runs that end
	// then were stopped with Fridolin, and are handed over again at the
	// next start.
	inFlight map[string]bool
	stopping bool
	// accountBusy and accountTried keep refreshAccount to one run at a
	// time and one try per plaudAccountEvery.
	accountBusy  bool
	accountTried time.Time
	// bg is the background work Stop waits for.
	bg   sync.WaitGroup
	stop context.CancelFunc
	// life is the poller's context from Start, which Stop cancels; checks
	// started from the dashboard run on it too.
	life context.Context
	// checkedLogin is the login (see loginStamp) whose account the state was
	// last checked against. Only polls use it, one at a time.
	checkedLogin string

	// run executes the CLI in the CLI home in use, for everything but the
	// sign-in and the sign-out of the private login (see Disconnect). It is
	// runCLI; tests wrap it to act between two commands.
	run func(ctx context.Context, args ...string) (string, error)
}

// NewPlaudManager creates the manager. The caller sets submit and notify.
func NewPlaudManager(cfgs *ConfigStore, brains *BrainManager) *PlaudManager {
	p := &PlaudManager{cfgs: cfgs, brains: brains, inFlight: map[string]bool{}}
	p.run = p.runCLI
	return p
}

func (p *PlaudManager) cfg() *Config { return p.cfgs.Get() }

func (p *PlaudManager) dir() string { return filepath.Join(p.cfg().DataDir, "plaud") }

// cliHome is the HOME the CLI runs with, which decides where its login lives.
func (p *PlaudManager) cliHome() string {
	if p.cfg().Plaud.BrainAccess {
		return homeDir()
	}
	return p.privateHome()
}

// privateHome is the CLI home without plaud.brain_access, in the data
// folder: only Fridolin uses it.
func (p *PlaudManager) privateHome() string { return filepath.Join(p.dir(), "home") }

func (p *PlaudManager) tokenPath() string { return filepath.Join(p.cliHome(), ".plaud", "tokens.json") }

// The states of the Plaud login, as loginStatus reports them.
const (
	plaudSignedOut = "signed out"
	plaudConnected = "connected"
	plaudExpired   = "expired"
	// plaudMoved: Fridolin signed in with the other CLI home, before
	// plaud.brain_access changed (see cliHome).
	plaudMoved = "moved"
)

// loginStatus says whether Plaud is connected. A login is there while
// tokens.json is, and expired when it is gone but Fridolin's sign-in is
// still on record (the CLI deleted it). A login from before sign-ins were
// recorded counts while its tokens.json is there, unless you disconnected
// since.
//
// A sign-in made with the other CLI home counts for nothing until you
// connect again: plaud.brain_access moves where the CLI keeps its login,
// and ~/.plaud may hold your own terminal's login, maybe of another
// account, which Fridolin must not take over unasked.
func (p *PlaudManager) loginStatus() string {
	st, _ := p.loadState()
	var dir string
	disconnected := false
	if st != nil {
		disconnected = st.Disconnected
		if st.Login != nil {
			dir = st.Login.Dir
		}
	}
	switch {
	case dir != "" && dir != p.cliHome():
		return plaudMoved
	case p.hasLogin() && (dir != "" || !disconnected):
		return plaudConnected
	case dir != "":
		return plaudExpired
	}
	return plaudSignedOut
}

func (p *PlaudManager) connected() bool { return p.loginStatus() == plaudConnected }

// hasLogin reports whether the CLI has a login in the CLI home in use.
func (p *PlaudManager) hasLogin() bool { return fileExists(p.tokenPath()) }

// loginStamp changes whenever the login file does: a sign-in, a sign-out, a
// token refresh, or brain_access switching to the other login. "" means
// there is none.
func (p *PlaudManager) loginStamp() string {
	fi, err := os.Stat(p.tokenPath())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s %d %d", p.tokenPath(), fi.Size(), fi.ModTime().UnixNano())
}

// cliPath finds the plaud binary.
func (p *PlaudManager) cliPath() (string, error) {
	return findPlaudCLI(p.cfg().Plaud.CLIPath)
}

// nvmNodeVersions is where nvm installs node versions, relative to HOME.
const nvmNodeVersions = ".nvm/versions/node"

// findPlaudCLI finds the plaud binary, given plaud.cli_path. The sandbox
// profile uses it too, to keep brains from replacing the CLI.
func findPlaudCLI(c string) (string, error) {
	if c != "" {
		if _, err := os.Stat(c); err != nil {
			return "", fmt.Errorf("plaud.cli_path %s: %w", c, err)
		}
		return c, nil
	}
	if found, err := exec.LookPath("plaud"); err == nil {
		return found, nil
	}
	candidates, _ := filepath.Glob(filepath.Join(homeDir(), nvmNodeVersions, "*/bin/plaud"))
	// The newest node first: by version, not by name, which would put
	// v20.9.0 before v20.10.0, and v9 before v24.
	slices.SortStableFunc(candidates, func(a, b string) int {
		return -compareNodeVersions(filepath.Base(filepath.Dir(filepath.Dir(a))), filepath.Base(filepath.Dir(filepath.Dir(b))))
	})
	candidates = append(candidates, "/opt/homebrew/bin/plaud", "/usr/local/bin/plaud")
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}
	return "", errors.New("the Plaud CLI is not installed")
}

// brainPlaudPATH is the PATH brains run with when plaud.brain_access lets
// them use Plaud's CLI, or "" to keep the bridge's own. The bridge finds
// the CLI in nvm when it is not on its PATH, which under the service it
// often is not, so brains get the CLI's folder first, as the bridge's own
// runs do: `plaud` and the node its #!/usr/bin/env node needs. The sandbox
// already keeps brains from writing to every folder on that PATH (see
// plantablePrograms).
func brainPlaudPATH(cfg *Config) string {
	if !cfg.Plaud.BrainAccess {
		return ""
	}
	bin, err := findPlaudCLI(cfg.Plaud.CLIPath)
	if err != nil {
		return ""
	}
	return plaudPATH(bin)
}

// plaudOptOut keeps Plaud's CLI from sending usage statistics to Plaud's
// analytics and from asking npm for updates. Fridolin's own runs get it,
// and so does every brain's run: with plaud.brain_access brains run the
// CLI themselves, and the bridge's environment has neither variable. It
// names Plaud's own variables, not DO_NOT_TRACK, which other tools in a
// brain's run read too, Claude Code among them.
var plaudOptOut = []string{"PLAUD_TELEMETRY_DISABLED=1", "PLAUD_NO_UPDATE_NOTIFIER=1"}

// compareNodeVersions compares two nvm version folder names ("v20.10.0")
// number by number. A name that is not a version sorts before every one.
func compareNodeVersions(a, b string) int {
	parse := func(v string) []int {
		var n []int
		for _, part := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			i, err := strconv.Atoi(part)
			if err != nil {
				return nil
			}
			n = append(n, i)
		}
		return n
	}
	return slices.Compare(parse(a), parse(b))
}

// plaudCLITested is the version of @plaud-ai/cli whose output Fridolin
// reads (testdata/plaud holds what it prints). The CLI is not 1.0 yet, so
// another 0.x may print otherwise.
const plaudCLITested = "0.3.15"

// plaudCLIVersion reads the version of the CLI at bin from the package.json
// of the npm package it belongs to, without running it: `plaud version`
// asks npm for updates. "" means it cannot tell (a wrapper script, say).
func plaudCLIVersion(bin string) string {
	pkg := nodePackage(resolvePath(bin))
	if pkg == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &m) != nil || m.Name != "@plaud-ai/cli" {
		return ""
	}
	return m.Version
}

// plaudCLIWarning says what to do about a CLI of version v, or "" when it
// is the tested version or a later 0.3 one, or the version is unknown.
func plaudCLIWarning(v string) string {
	var major, minor, patch int
	if _, err := fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return ""
	}
	var tMajor, tMinor, tPatch int
	_, _ = fmt.Sscanf(plaudCLITested, "%d.%d.%d", &tMajor, &tMinor, &tPatch)
	switch {
	case major == tMajor && minor == tMinor && patch >= tPatch:
		return ""
	case major < tMajor || major == tMajor && (minor < tMinor || minor == tMinor && patch < tPatch):
		return fmt.Sprintf("Plaud's CLI %s is older than %s, the version Fridolin reads; update it: npm install -g @plaud-ai/cli@%s", v, plaudCLITested, plaudCLITested)
	}
	return fmt.Sprintf("Plaud's CLI %s is newer than %s, the version Fridolin was tested with, and may print things Fridolin does not read; if recordings stop arriving, install that one: npm install -g @plaud-ai/cli@%s", v, plaudCLITested, plaudCLITested)
}

// plaudPATH is the PATH the Plaud CLI runs with: its own folder first, so
// that its #!/usr/bin/env node finds the node it was installed with.
func plaudPATH(bin string) string {
	return filepath.Dir(bin) + ":" + os.Getenv("PATH") + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
}

// plaudOutputLimit caps what Fridolin keeps of the CLI's stdout: a
// listing or a details page is a few KB, and transcripts go to a file.
// plaudErrorLimit caps stderr, of which errors show the start.
const (
	plaudOutputLimit = 4 << 20
	plaudErrorLimit  = 64 << 10
)

// runCLI runs `plaud args…` with what childEnv gives a helper program on
// ctx (see helperScope), the chosen home, telemetry off, and node (which the
// CLI needs) on PATH. child_env_passthrough is for brains: this third-party
// CLI needs nothing but its own login. Exit status 2 means the login
// expired.
func (p *PlaudManager) runCLI(ctx context.Context, args ...string) (string, error) {
	return p.runCLIOut(ctx, nil, args...)
}

// runCLIOut is runCLI that also copies the CLI's output to out as it
// arrives, when out is not nil: the sign-in prints its link, then waits.
func (p *PlaudManager) runCLIOut(ctx context.Context, out io.Writer, args ...string) (string, error) {
	return p.runCLIIn(ctx, "", out, args...)
}

// runCLIIn is runCLIOut with the CLI home home, or "" for the one in use
// once the command may run.
func (p *PlaudManager) runCLIIn(ctx context.Context, home string, out io.Writer, args ...string) (string, error) {
	bin, err := p.cliPath()
	if err != nil {
		return "", err
	}
	if args[0] != "login" {
		p.cliMu.Lock()
		defer p.cliMu.Unlock()
	}
	if home == "" {
		home = p.cliHome()
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = childEnv(helperScope(ctx), nil, "HOME="+home, "PATH="+plaudPATH(bin),
		"DO_NOT_TRACK=1", "PLAUD_TELEMETRY_DISABLED=1", "PLAUD_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	// The CLI loads a .env from its working folder (dotenv), and the
	// bridge's is its data folder under the service: the CLI's home is
	// the CLI's own.
	cmd.Dir = home
	// Like a brain run: its own process group, all of which a cancel
	// kills, and a bounded wait for anything still holding its output.
	isolateProcessGroup(cmd)
	stdout, stderr := &cappedBuffer{limit: plaudOutputLimit}, &cappedBuffer{limit: plaudErrorLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if out != nil {
		cmd.Stdout, cmd.Stderr = io.MultiWriter(stdout, out), io.MultiWriter(stderr, out)
	}
	err = cmd.Run()
	text := ansiRe.ReplaceAllString(stdout.String(), "")
	if err != nil {
		return text, plaudRunError(args[0], err, ansiRe.ReplaceAllString(stderr.String(), ""))
	}
	return text, nil
}

// plaudRunError says why `plaud cmd` failed. The CLI's exit status names
// the cause: 2 a login Plaud refuses (for a sign-in: one Plaud did not
// grant), 3 Plaud unreachable, 4 a timeout, 1 anything else.
func plaudRunError(cmd string, err error, stderr string) error {
	var ee *exec.ExitError
	code := -1
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	// Without a terminal the CLI's spinner leaves "- Fetching file..." and
	// the like on stderr, which says nothing about what went wrong.
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "- ") {
			lines = append(lines, l)
		}
	}
	detail := truncate(strings.Join(lines, " "), 300)
	switch {
	case cmd == "login" && code == 2:
		return fmt.Errorf("access was not granted: it was denied on Plaud's page, or the sign-in failed (%s)", detail)
	case cmd == "login" && code == 4:
		return errors.New("the sign-in timed out: Plaud's tool waits two minutes for the approval in the browser")
	case code == 2:
		return errPlaudAuth
	case code == 1 && strings.Contains(stderr, "[NOT_FOUND]"):
		return fmt.Errorf("plaud %s: %w", cmd, errPlaudNotFound)
	case code == 3:
		return fmt.Errorf("plaud %s: cannot reach Plaud, check the network", cmd)
	case code == 4:
		return fmt.Errorf("plaud %s: Plaud did not answer in time", cmd)
	}
	return fmt.Errorf("plaud %s: %v %s", cmd, err, detail)
}

// cappedBuffer keeps the first limit bytes written to it and drops the
// rest, so a CLI that floods its output cannot fill the bridge's memory.
// It must not embed bytes.Buffer: io.Copy would use the buffer's ReadFrom
// and never call Write.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(b []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// listIDs returns the ids on one page of `plaud files`, newest first.
func (p *PlaudManager) listIDs(ctx context.Context, page int) ([]string, error) {
	out, err := p.run(ctx, "files", "--page", fmt.Sprint(page), "--page-size", fmt.Sprint(plaudPageSize))
	if err != nil {
		return nil, err
	}
	return plaudListedIDs(out), nil
}

// plaudListedIDs reads the ids out of what `plaud files` prints.
func plaudListedIDs(out string) []string {
	var ids []string
	for _, m := range plaudRowRe.FindAllStringSubmatch(out, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// listUntil lists recordings newest first, page by page, and stops after
// the first page with one that known says it knows, a page that is not
// full, or plaudListPages pages.
func (p *PlaudManager) listUntil(ctx context.Context, known func(id string) bool) ([]string, error) {
	var all []string
	for page := 1; page <= plaudListPages; page++ {
		ids, err := p.listIDs(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, ids...)
		if len(ids) < plaudPageSize || slices.ContainsFunc(ids, known) {
			break
		}
	}
	return all, nil
}

// parseKV reads the "  key: value" lines `plaud file` and `plaud me`
// print. A value the CLI has not got is left out: it prints "-" for a
// missing start_at or serial_number, and "undefined" or "null" for a field
// Plaud did not send at all.
func parseKV(out string) map[string]string {
	d := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := plaudKVRe.FindStringSubmatch(line); m != nil {
			switch v := strings.TrimSpace(m[2]); v {
			case "", "-", "undefined", "null":
			default:
				d[m[1]] = v
			}
		}
	}
	return d
}

// plaudTime reads a time from `plaud file`: Plaud's API sends UTC, often
// without an offset, which the CLI then reads as UTC too.
func plaudTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// fetchBody runs `plaud args… -o <file>` and returns what it wrote, or ""
// when it wrote nothing. With -o the CLI writes only the transcript or
// the summary, without its header, and nothing at all when Plaud has none
// (it says so on stdout and exits 0), so there is no output to pick apart.
// The file goes in dir, which brains cannot read.
func (p *PlaudManager) fetchBody(ctx context.Context, dir string, args ...string) (string, error) {
	out := filepath.Join(dir, args[0]+".txt")
	_ = os.Remove(out)
	if _, err := p.run(ctx, append(slices.Clip(args), "-o", out)...); err != nil {
		return "", err
	}
	data, err := os.ReadFile(out)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

// plaudEmptyNote starts what `plaud summary` writes for a summary note
// Plaud has not filled in yet.
const plaudEmptyNote = "(this note has no content yet"

// me returns what `plaud me` says about the signed-in user.
// It also records who that is with the sign-in, for the card.
func (p *PlaudManager) me(ctx context.Context) (map[string]string, error) {
	out, err := p.run(ctx, "me")
	if err != nil {
		return nil, err
	}
	d := parseKV(out)
	name := d["email"]
	if name == "" {
		name = d["nickname"]
	}
	err = p.update(func(st *plaudState) error {
		switch {
		case st.Disconnected:
			return nil // signed out while `me` ran
		case st.Login == nil:
			// A sign-in from before they were recorded.
			st.Login = &plaudLogin{Dir: p.cliHome(), Since: time.Now()}
		case st.Login.Dir != p.cliHome():
			// brain_access moved the login while `me` ran. The login in the
			// new home is not Fridolin's until you connect again.
			return nil
		}
		st.Login.Account, st.Login.Checked = name, time.Now()
		return nil
	})
	return d, err
}

// refreshAccount asks Plaud in the background who is signed in, when no
// one did for plaudAccountEvery, so that the status never runs the CLI
// while the dashboard waits. A refusal shows the login as expired. A
// sign-in that is still running asks itself.
func (p *PlaudManager) refreshAccount(checked time.Time) {
	p.mu.Lock()
	due := time.Since(checked) >= plaudAccountEvery && time.Since(p.accountTried) >= plaudAccountEvery
	if !due || p.accountBusy || p.connect != nil && p.connect.State == "running" {
		p.mu.Unlock()
		return
	}
	p.accountBusy, p.accountTried = true, time.Now()
	p.mu.Unlock()
	p.bg.Add(1)
	go func() {
		defer p.bg.Done()
		ctx, cancel := context.WithTimeout(p.lifetime(), 30*time.Second)
		defer cancel()
		_, err := p.me(ctx)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.accountBusy = false
		if errors.Is(err, errPlaudAuth) {
			p.refused = p.loginStatus() == plaudConnected
		} else if err == nil {
			p.refused = false
		}
	}()
}

// accountID identifies the signed-in Plaud account: its user id, else its
// email or nickname, else "" when the CLI does not say.
func (p *PlaudManager) accountID(ctx context.Context) (string, error) {
	d, err := p.me(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range []string{"id", "email", "nickname"} {
		if d[k] != "" {
			return d[k], nil
		}
	}
	return "", nil
}

// forward hands one waiting recording to br, whose answer goes to jid, and
// says what it handed over: nil while Plaud has not finished the
// transcript. It notes the recording's name in item once Plaud said it.
func (p *PlaudManager) forward(ctx context.Context, cfg *Config, id string, item *plaudPending, br *Brain, jid string) (*PlaudForward, error) {
	since := item.FirstSeen
	detailsOut, err := p.run(ctx, "file", id)
	if err != nil {
		return nil, err
	}
	d := parseKV(detailsOut)
	if d["name"] != "" {
		item.Name = d["name"]
	}
	if d["transcript"] != "available" {
		return nil, nil
	}
	tmp, err := os.MkdirTemp(p.dir(), plaudFetchPrefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// `plaud file` says "transcript: available" for the raw transcript
	// only; whether there is a cleaned-up one shows when it is fetched.
	var transcript, note string
	if cfg.Plaud.Polished {
		if transcript, err = p.fetchBody(ctx, tmp, "transcript", id, "--polished"); err != nil {
			return nil, err
		}
		if transcript == "" && time.Since(since) < plaudPolishedWait {
			return nil, nil
		}
		if transcript == "" {
			note = fmt.Sprintf("Plaud's cleaned-up transcript was not there %v after the recording arrived, so this is the raw one.\n", plaudPolishedWait)
			log.Printf("plaud %s: no cleaned-up transcript after %v, sending the raw one", id, plaudPolishedWait)
		}
	}
	if transcript == "" {
		if transcript, err = p.fetchBody(ctx, tmp, "transcript", id); err != nil {
			return nil, err
		}
	}
	if transcript == "" {
		return nil, nil
	}
	summary := ""
	if d["summary"] == "available" {
		if s, err := p.fetchBody(ctx, tmp, "summary", id); err == nil {
			if !strings.HasPrefix(s, plaudEmptyNote) {
				summary = s
			}
		} else if errors.Is(err, errPlaudAuth) {
			return nil, err
		} else {
			log.Printf("plaud %s: summary: %v", id, err)
		}
	}

	name := d["name"]
	if name == "" {
		name = id
	}
	// When it was recorded, else when it reached Plaud, else today.
	// Dates go year first, so that files sort by date.
	day, when := time.Now().Format("2006-01-02"), "unknown"
	for _, k := range []string{"start_at", "created_at"} {
		if t, ok := plaudTime(d[k]); ok {
			local := t.Local()
			day, when = local.Format("2006-01-02"), local.Format("2006-01-02 15:04")
			break
		}
	}
	duration := d["duration"]
	if duration == "" {
		duration = "?"
	}

	dir := filepath.Join(cfg.MediaDir, "plaud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := plaudTranscriptPath(dir, day, name, id)
	var body strings.Builder
	fmt.Fprintf(&body, "Plaud recording: %s\nRecorded: %s\nDuration: %s\nPlaud ID: %s\n%s\n", name, when, duration, id, note)
	if summary != "" {
		body.WriteString("=== Plaud AI summary ===\n" + summary + "\n\n")
	}
	body.WriteString("=== Transcript ===\n" + strings.TrimSpace(transcript) + "\n")
	if err := writeFileAtomic(path, []byte(body.String()), 0o600, false); err != nil {
		return nil, err
	}

	instruction := cfg.Plaud.Message
	if strings.TrimSpace(instruction) == "" {
		instruction = defaultPlaudMessage
	}
	text := strings.NewReplacer("{name}", name, "{when}", when, "{duration}", duration).Replace(plaudMessageHead + "\n\n" + strings.TrimSpace(instruction))
	// Marked as handed before the brain gets it, so that a run that ends
	// at once finds it so.
	fw := PlaudForward{ID: id, Name: name, Brain: br.ID(), At: time.Now(), Status: plaudRunHanded}
	if err := p.markHanded(fw); err != nil {
		return nil, err
	}
	if err := p.submit(br, jid, "plaud", text, []string{path}, p.runEnded(id)); err != nil {
		p.unmarkHanded(id)
		return nil, fmt.Errorf("brain %s did not take it: %w", br.ID(), err)
	}
	log.Printf("plaud %s: handed to brain %s", id, br.ID()) // its name is personal data, the id is not
	return &fw, nil
}

// markHanded notes that the recording fw.ID is about to go to a brain.
func (p *PlaudManager) markHanded(fw PlaudForward) error {
	p.mu.Lock()
	p.inFlight[fw.ID] = true
	p.mu.Unlock()
	return p.update(func(st *plaudState) error {
		if cur := st.Pending[fw.ID]; cur != nil {
			cur.Handed, cur.Brain = fw.At, fw.Brain
		}
		st.Forwarded = append(slices.DeleteFunc(st.Forwarded, func(f PlaudForward) bool { return f.ID == fw.ID }), fw)
		if len(st.Forwarded) > plaudForwardedKeep {
			st.Forwarded = st.Forwarded[len(st.Forwarded)-plaudForwardedKeep:]
		}
		return nil
	})
}

// unmarkHanded undoes markHanded for a recording the brain did not take.
func (p *PlaudManager) unmarkHanded(id string) {
	p.mu.Lock()
	delete(p.inFlight, id)
	p.mu.Unlock()
	if err := p.update(func(st *plaudState) error {
		if cur := st.Pending[id]; cur != nil {
			cur.Handed = time.Time{}
		}
		st.Forwarded = slices.DeleteFunc(st.Forwarded, func(f PlaudForward) bool { return f.ID == id })
		return nil
	}); err != nil {
		log.Printf("plaud %s: %v", id, err)
	}
}

// runEnded returns what recording id's hand-off calls when its run ends:
// done, it is seen; stopped, it is seen too, and you can send it again
// from the dashboard; failed, it is handed over again later, up to
// plaudRunTries runs, and then given up. A run that ends because Fridolin
// is stopping changes nothing, so the next start hands it over again.
func (p *PlaudManager) runEnded(id string) func(error) {
	return func(runErr error) {
		p.mu.Lock()
		stopping := p.stopping
		delete(p.inFlight, id)
		if !stopping {
			// Stop waits for what this records: the brain's worker calls
			// it after the reply went out, and stopping does not wait for
			// the worker.
			p.bg.Add(1)
		}
		p.mu.Unlock()
		if stopping {
			return
		}
		defer p.bg.Done()
		var gaveUp *plaudGivenUp
		var brain string
		err := p.update(func(st *plaudState) error {
			cur := st.Pending[id]
			if cur == nil || cur.Handed.IsZero() {
				return nil
			}
			fw := st.forwardOf(id)
			if fw == nil {
				st.Forwarded = append(st.Forwarded, PlaudForward{ID: id, Name: cur.Name, Brain: cur.Brain, At: cur.Handed})
				fw = &st.Forwarded[len(st.Forwarded)-1]
			}
			brain = fw.Brain
			switch {
			case runErr == nil:
				fw.Status, fw.Error = plaudRunDone, ""
			case errors.Is(runErr, errBrainInterrupted) || errors.Is(runErr, errBrainDropped) || errors.Is(runErr, context.Canceled):
				fw.Status, fw.Error = plaudRunStopped, ""
				log.Printf("plaud %s: stopped before brain %s answered it; it is not sent again unless you send it again from the dashboard", id, brain)
			default:
				cur.Runs++
				fw.Status, fw.Error = plaudRunFailed, runErr.Error()
				if cur.Runs < plaudRunTries {
					wait := plaudRunRetryDelay << (cur.Runs - 1)
					cur.Handed, cur.NextTry = time.Time{}, time.Now().Add(wait)
					log.Printf("plaud %s: brain %s's run failed (%v), handing it over again in %v", id, brain, runErr, wait)
					return nil
				}
				g := plaudGivenUp{ID: id, Name: cur.Name, At: time.Now(), Reason: fmt.Sprintf("the brain's run failed %d times (%v)", cur.Runs, runErr)}
				st.GivenUp = append(st.GivenUp, g)
				if len(st.GivenUp) > plaudGivenUpKeep {
					st.GivenUp = st.GivenUp[len(st.GivenUp)-plaudGivenUpKeep:]
				}
				gaveUp = &g
				log.Printf("plaud %s: brain %s's run failed %d times, giving up", id, brain, cur.Runs)
			}
			delete(st.Pending, id)
			st.Seen = append(st.Seen, id)
			return nil
		})
		if err != nil {
			log.Printf("plaud %s: %v", id, err)
		}
		if gaveUp != nil {
			// The brain's worker calls this; the notice must not hold it up.
			p.bg.Add(1)
			go func() {
				defer p.bg.Done()
				if _, jid, err := p.destination(p.cfg()); err == nil {
					p.noticeGivenUp(brain, jid, cmp.Or(gaveUp.Name, id), gaveUp.Reason)
				}
			}()
		}
	}
}

// plaudTranscriptPath is where the transcript of recording id goes in dir.
// The name ends in the Plaud id, so a retry finds the file it wrote before,
// even after the recording was renamed, and rewrites it instead of adding
// a copy. (Writing it atomically also replaces, rather than follows, a
// symlink a brain may have put there.) The folder is read rather than
// globbed, since media_dir may contain '[' or '*'.
func plaudTranscriptPath(dir, day, name, id string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), " - "+id+".txt") {
			return filepath.Join(dir, e.Name())
		}
	}
	return filepath.Join(dir, day+" - "+safePlaudName(name)+" - "+id+".txt")
}

func safePlaudName(s string) string {
	s = truncate(strings.TrimSpace(strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\n", " ", "\r", " ", "\t", " ").Replace(s)), 120)
	if s == "" {
		s = "Plaud recording"
	}
	return s
}

// targetBrain is the configured brain, else the DM brain, else the default.
func (p *PlaudManager) targetBrain(cfg *Config) *Brain {
	if id := cfg.Plaud.Brain; id != "" {
		return p.brains.Get(id)
	}
	return p.brains.BrainFor("", false)
}

// summaryChat is where br's answer to a recording and the expiry notice go.
// Recordings are private (a doctor's visit, an HR talk), so it is never a
// group, whatever chats the brain serves: its reply_chat when that is a
// direct chat, else your own DM (the first allowed number). "" means there
// is no private chat to use.
func (p *PlaudManager) summaryChat(cfg *Config, br *Brain) string {
	if rc := br.settings.Load().manifest.ReplyChat; isDirectChat(rc) {
		return rc
	}
	if len(cfg.AllowedNumbers) > 0 {
		return userJID(cfg.AllowedNumbers[0])
	}
	return ""
}

// isDirectChat reports whether jid is a one-to-one chat (a phone number or a
// LID), not a group, a broadcast list or a channel.
func isDirectChat(jid string) bool {
	return strings.HasSuffix(jid, "@"+types.DefaultUserServer) || strings.HasSuffix(jid, "@"+types.HiddenUserServer)
}

// destination returns the brain that receives recordings and the private
// chat its answers go to, under cfg. A check reads the configuration once
// and passes it on, so that it never mixes two versions of it.
func (p *PlaudManager) destination(cfg *Config) (*Brain, string, error) {
	br := p.targetBrain(cfg)
	if br == nil {
		if id := cfg.Plaud.Brain; id != "" {
			return nil, "", fmt.Errorf("brain %s, picked for Plaud recordings, was removed or is not approved — pick another one under Integrations → Plaud", id)
		}
		return nil, "", errors.New("no brain to send Plaud recordings to")
	}
	jid := p.summaryChat(cfg, br)
	if jid == "" {
		return br, "", fmt.Errorf("no private chat for Plaud summaries — add your number under Chats → Allowed numbers, or set brain %s's reply chat to a direct chat", br.ID())
	}
	return br, jid, nil
}

func (p *PlaudManager) loadState() (*plaudState, error) {
	data, err := os.ReadFile(filepath.Join(p.dir(), "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := &plaudState{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Pending == nil {
		st.Pending = map[string]*plaudPending{}
	}
	return st, nil
}

// update changes state.json with fn: it reads the file (a new state when
// there is none), lets fn change it and saves it, all under stMu. Every
// change goes through it, so the poller, which works through recordings
// for minutes, never writes back a state another change has moved past.
// An error from fn saves nothing.
func (p *PlaudManager) update(fn func(st *plaudState) error) error {
	p.stMu.Lock()
	defer p.stMu.Unlock()
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return err
	}
	st, err := p.loadState()
	if err != nil {
		return err
	}
	if st == nil {
		st = newPlaudState()
	}
	if err := fn(st); err != nil {
		return err
	}
	return p.saveState(st)
}

// newPlaudState is the state before the first sync.
func newPlaudState() *plaudState {
	return &plaudState{Version: plaudStateVersion, Pending: map[string]*plaudPending{}}
}

func (p *PlaudManager) saveState(st *plaudState) error {
	st.Version = plaudStateVersion
	if len(st.Seen) > plaudSeenKeep {
		st.Seen = st.Seen[len(st.Seen)-plaudSeenKeep:]
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.dir(), "state.json"), data, 0o600, false)
}

// Poll checks for new recordings once. The first poll for an account only
// remembers what already exists, so connecting never floods a brain with
// old recordings. It does nothing while "Send new recordings to a brain"
// (plaud.enabled) is off; see runPoll.
func (p *PlaudManager) Poll(ctx context.Context) error {
	if !p.beginPoll() {
		return nil
	}
	return p.runPoll(ctx)
}

// beginPoll marks a check as running; false means one already is.
func (p *PlaudManager) beginPoll() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return false
	}
	p.busy = true
	return true
}

// runPoll does the check beginPoll started, and ends it. With "Send new
// recordings to a brain" off (plaud.enabled) it does nothing, whoever asked
// for it, in case the switch went off while the check was on its way.
func (p *PlaudManager) runPoll(ctx context.Context) error {
	defer func() {
		p.mu.Lock()
		p.busy = false
		p.mu.Unlock()
	}()
	if !p.cfg().Plaud.Enabled {
		return nil
	}

	err := p.poll(ctx)
	p.mu.Lock()
	p.lastPoll = time.Now()
	if err != nil {
		p.lastErr = err.Error()
	} else {
		p.lastErr = ""
	}
	// Plaud refusing a login it did not delete: revoked on Plaud's side.
	p.refused = errors.Is(err, errPlaudAuth) && p.loginStatus() == plaudConnected
	p.mu.Unlock()
	if errors.Is(err, errPlaudAuth) {
		p.notifyAuthExpired()
	}
	return err
}

func (p *PlaudManager) poll(ctx context.Context) error {
	cfg := p.cfg()
	st, err := p.loadState()
	if err != nil {
		return err
	}
	// The account is looked up when there is no state yet and whenever the
	// login changed, not on every poll: signing in with another account
	// must not make its recordings look new. The same account signing in
	// again keeps its state, so what it recorded in between still arrives.
	// The login counts as checked as it was before `me` ran: one that
	// changes while the account is looked up is looked up again.
	if stamp := p.loginStamp(); !st.seededFor("") || stamp != p.checkedLogin {
		acct, err := p.accountID(ctx)
		if err != nil {
			return err
		}
		if !st.seededFor(acct) {
			if st.seededFor("") {
				log.Printf("plaud: signed in to another Plaud account, starting over")
			}
			if err := p.seed(ctx, acct); err != nil {
				return err
			}
			p.checkedLogin = stamp
			return nil
		}
		if err := p.update(func(st *plaudState) error {
			if st.Account == "" {
				st.Account = acct
			}
			return nil
		}); err != nil {
			return err
		}
		p.checkedLogin = stamp
	}

	known := map[string]bool{}
	for _, id := range st.Seen {
		known[id] = true
	}
	for id := range st.Pending {
		known[id] = true
	}
	ids, err := p.listUntil(ctx, func(id string) bool { return known[id] })
	if err != nil {
		return err
	}
	// A login that changed since the account was checked may have listed
	// another account's recordings, which must not count as new. The next
	// poll checks the account first; a token refresh costs one interval.
	if p.loginStamp() != p.checkedLogin {
		return nil
	}
	if err := p.update(func(st *plaudState) error {
		seen := map[string]bool{}
		for _, id := range st.Seen {
			seen[id] = true
		}
		// Oldest first, each a nanosecond after the one before, so that
		// recordings that show up together go in the order they were made.
		now := time.Now()
		for i, id := range slices.Backward(ids) {
			if _, pending := st.Pending[id]; !seen[id] && !pending {
				st.Pending[id] = &plaudPending{FirstSeen: now.Add(time.Duration(len(ids) - i))}
				log.Printf("plaud %s: new recording, waiting for the transcript", id)
			}
		}
		// A login that works is not expired: the next expiry gets its notice.
		st.AuthNotified = time.Time{}
		if st.Login == nil && !st.Disconnected {
			// A sign-in from before they were recorded.
			st.Login = &plaudLogin{Dir: p.cliHome(), Since: time.Now()}
		}
		return nil
	}); err != nil {
		return err
	}
	if st, err = p.loadState(); err != nil {
		return err
	}
	// Resolved before anything is fetched or written: without a brain or a
	// private chat the recordings wait, and the error shows on the dashboard.
	// With nothing waiting nothing is blocked, so the poll stays quiet rather
	// than log the same error every interval; the status call reports it.
	br, jid, err := p.destination(cfg)
	if err != nil && len(st.Pending) > 0 {
		return err
	}
	// A recording that fails is tried again, ever less often, until it is
	// given up, and does not hold up the others; its error is the check's,
	// so the card shows it instead of counting the recording as waiting for
	// Plaud.
	var failed []error
	maxAge := fmt.Sprintf("%d days", plaudPendingMaxAge/(24*time.Hour))
	// Only as many as there is room for: the recordings still with a brain
	// count, not just those this check hands over, so a backlog never fills
	// a brain's queue however many checks it takes.
	p.mu.Lock()
	room := plaudHandedLimit(cfg) - len(p.inFlight)
	p.mu.Unlock()
	now := time.Now()
	for _, id := range st.pendingInOrder() {
		if room <= 0 {
			break
		}
		item := *st.Pending[id]
		if now.Before(item.NextTry) {
			continue
		}
		if !item.Handed.IsZero() {
			p.mu.Lock()
			running := p.inFlight[id]
			p.mu.Unlock()
			if running {
				continue
			}
			log.Printf("plaud %s: handed over before a restart and not answered, handing it over again", id)
		}
		fw, err := p.forward(ctx, cfg, id, &item, br, jid)
		if errors.Is(err, errPlaudAuth) {
			return err
		}
		// Why it is given up, if it is. A recording deleted in Plaud is
		// given up at once; the rest after plaudPendingMaxAge. A recording
		// that still fails may well have been transcribed long ago, so its
		// line does not blame Plaud.
		var why string
		old := time.Since(item.FirstSeen) > plaudPendingMaxAge
		deleted := errors.Is(err, errPlaudNotFound)
		switch {
		case fw != nil:
			room--
		case deleted:
			why = "it was deleted in Plaud"
			log.Printf("plaud %s: deleted in Plaud, giving up", id)
		case old && err != nil:
			why = fmt.Sprintf("fetching it from Plaud failed for %s (%v)", maxAge, err)
			log.Printf("plaud %s: still failing after %s, giving up", id, maxAge)
			failed = append(failed, fmt.Errorf("recording %s (given up after %s): %w", id, maxAge, err))
		case old:
			why = fmt.Sprintf("Plaud had no transcript for it after %s", maxAge)
			log.Printf("plaud %s: no transcript after %s, giving up", id, maxAge)
		case err != nil:
			failed = append(failed, fmt.Errorf("recording %s (will retry): %w", id, err))
		}
		if err := p.update(func(st *plaudState) error {
			cur := st.Pending[id]
			if cur == nil {
				return nil // handled meanwhile
			}
			cur.Name = item.Name
			switch {
			case fw != nil:
				return nil // markHanded noted it; its run decides the rest
			case why != "":
				st.GivenUp = append(st.GivenUp, plaudGivenUp{ID: id, Name: item.Name, At: time.Now(), Reason: why})
				if len(st.GivenUp) > plaudGivenUpKeep {
					st.GivenUp = st.GivenUp[len(st.GivenUp)-plaudGivenUpKeep:]
				}
			default:
				cur.Tries++
				cur.NextTry = time.Now().Add(plaudRetryDelay(cur.Tries))
				return nil
			}
			delete(st.Pending, id)
			st.Seen = append(st.Seen, id)
			return nil
		}); err != nil {
			return err
		}
		// You deleted it yourself; the rest you hear about, since a
		// recording that never arrives is otherwise easy to miss.
		if why != "" && !deleted {
			p.noticeGivenUp(br.ID(), jid, cmp.Or(item.Name, id), why)
		}
	}
	return errors.Join(failed...)
}

// noticeGivenUp tells the chat summaries go to that recording name will
// not reach brain, and why.
func (p *PlaudManager) noticeGivenUp(brain, jid, name, why string) {
	ctx, cancel := context.WithTimeout(p.lifetime(), 30*time.Second)
	defer cancel()
	text := fmt.Sprintf("⚠️ Plaud: the recording “%s” was not handed to brain %s: %s. It is still in Plaud, and the dashboard lists it (Integrations → Plaud).", name, brain, why)
	if err := p.notify(ctx, jid, text); err != nil {
		log.Printf("plaud: could not send the notice about a recording given up: %v", err)
	}
}

// seed starts the state over for acct, with the recordings it already
// has, all marked as seen. What does not belong to an account stays: the
// sign-in and the last recording handed over, for the card.
func (p *PlaudManager) seed(ctx context.Context, acct string) error {
	ids, err := p.listUntil(ctx, func(string) bool { return false })
	if err != nil {
		return err
	}
	// Oldest first, like the recordings seen later, so that keeping the
	// last plaudSeenKeep drops the oldest. A recording may show on two
	// pages when one arrives while they are listed.
	var listed []string
	seen := map[string]bool{}
	for _, id := range slices.Backward(ids) {
		if !seen[id] {
			seen[id] = true
			listed = append(listed, id)
		}
	}
	log.Printf("plaud: first sync — %d existing recordings marked as seen", len(listed))
	return p.update(func(st *plaudState) error {
		fresh := newPlaudState()
		fresh.Seeded, fresh.Account, fresh.Seen = true, acct, listed
		fresh.Forwarded, fresh.Login, fresh.Disconnected = st.Forwarded, st.Login, st.Disconnected
		if fresh.Login == nil && !fresh.Disconnected {
			fresh.Login = &plaudLogin{Dir: p.cliHome(), Since: time.Now()}
		}
		*st = *fresh
		return nil
	})
}

// notifyAuthExpired tells the private chat Plaud uses once a day that Plaud
// needs a new sign-in. Without one only the dashboard shows it. The notice
// time is kept in the state, also before the first sync: a state that is
// not seeded still gets its first sync.
//
// It is only for a login that lapsed. A check that lost its login because
// you disconnected, or moved it with brain_access, while it ran says
// nothing: its next command failed for want of a login, which is no
// expiry.
func (p *PlaudManager) notifyAuthExpired() {
	st, err := p.loadState()
	if err != nil {
		log.Printf("plaud: %v", err)
		return
	}
	if st != nil && (st.Disconnected || time.Since(st.AuthNotified) < 24*time.Hour) || p.loginStatus() == plaudMoved {
		return
	}
	_, jid, err := p.destination(p.cfg())
	if err != nil {
		return
	}
	if err := p.notify(context.Background(), jid, "⚠️ Plaud: the login expired, so new recordings are not arriving. Reconnect in the Fridolin dashboard (Integrations → Plaud)."); err != nil {
		log.Printf("plaud: could not send the expiry notice: %v", err)
		return
	}
	if err := p.update(func(st *plaudState) error { st.AuthNotified = time.Now(); return nil }); err != nil {
		log.Printf("plaud: %v", err)
	}
}

// plaudFetchPrefix starts the folders in the data folder that a fetch
// writes a recording's transcript and summary to (see forward).
const plaudFetchPrefix = "fetch-"

// removeFetchFolders removes the folders of fetches that never ended: a
// fetch removes its own, but not when the bridge is killed during it, and
// what it wrote is the recording's text.
func (p *PlaudManager) removeFetchFolders() {
	entries, _ := os.ReadDir(p.dir())
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), plaudFetchPrefix) {
			if err := os.RemoveAll(filepath.Join(p.dir(), e.Name())); err != nil {
				log.Printf("plaud: %v", err)
			}
		}
	}
}

// Start runs the poller, after removing what fetches that never ended left
// behind. The dashboard may already be serving then, but a check it
// started meanwhile has not reached a fetch yet: it looks up the account
// and lists the recordings first, each a run of the CLI.
func (p *PlaudManager) Start(ctx context.Context) {
	p.removeFetchFolders()
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.stop, p.life = cancel, ctx
	p.mu.Unlock()
	go func() {
		next := time.Now().Add(15 * time.Second) // let WhatsApp connect first
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			next = p.tick(ctx, next)
		}
	}()
}

// tick checks Plaud if a check is due at due, and returns when the next
// one is. With an expired login there is nothing to check, but the chat
// still gets its daily notice; signed out, the first check comes as soon
// as there is a login.
func (p *PlaudManager) tick(ctx context.Context, due time.Time) time.Time {
	cfg := p.cfg()
	if !cfg.Plaud.Enabled || time.Now().Before(due) {
		return due
	}
	switch p.loginStatus() {
	case plaudConnected:
		pctx, pcancel := context.WithTimeout(ctx, 10*time.Minute)
		if err := p.Poll(pctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("plaud: %v", err)
		}
		pcancel()
	case plaudExpired:
		p.notifyAuthExpired()
	default:
		return due
	}
	return time.Now().Add(cfg.Plaud.interval())
}

// lifetime is the context a check runs on: Start's, so that Stop ends it,
// or Background before Start.
func (p *PlaudManager) lifetime() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.life != nil {
		return p.life
	}
	return context.Background()
}

// Stop ends the poller, a running check and a running sign-in, and waits
// for the sign-in, a look-up of the account and the recording of a run
// that ended. Recordings whose runs it stops are handed over again at the
// next start.
func (p *PlaudManager) Stop() {
	p.mu.Lock()
	p.stopping = true
	if p.stop != nil {
		p.stop()
	}
	if p.connect != nil && p.connect.cancel != nil {
		p.connect.cancel()
	}
	p.mu.Unlock()
	p.bg.Wait()
}

// ── API ──────────────────────────────────────────────────────────────────

// plaudSignInURL returns the sign-in link `plaud login` prints when it
// cannot open a browser itself:
//
//	Could not open browser. Open this URL manually:
//	  https://web.plaud.ai/platform/oauth?client_id=…
//
// Only complete lines count, and only the link on that line or the next:
// not one from a notice or an error before it. It must be https, or http
// to this machine (a sign-in's callback), so the card never offers a
// plain-http page elsewhere as Plaud's sign-in. "" means there is none.
func plaudSignInURL(out string) string {
	lines := strings.Split(ansiRe.ReplaceAllString(out, ""), "\n")
	lines = lines[:len(lines)-1] // the last one may still be arriving
	for i, l := range lines {
		_, after, ok := strings.Cut(l, "Open this URL manually:")
		if !ok {
			continue
		}
		link := strings.TrimSpace(after)
		if link == "" && i+1 < len(lines) {
			link = strings.TrimSpace(lines[i+1])
		}
		u, err := url.Parse(link)
		if err != nil || u.Host == "" || strings.ContainsAny(link, " \t") {
			return ""
		}
		switch host := u.Hostname(); {
		case u.Scheme == "https":
			return link
		case u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()):
			return link
		}
		return ""
	}
	return ""
}

// What the dashboard's Plaud actions refuse, and why; the handlers word
// them for the card.
var (
	errPlaudUnknownRecording = errors.New("no such recording: only one Fridolin handed over or gave up can be sent again")
	errPlaudAlreadyWaiting   = errors.New("that recording is already waiting to be handed over")
	errPlaudNotInstalled     = errors.New("the Plaud CLI is not installed")
	errPlaudSigningIn        = errors.New("already signing in — finish in the browser tab Plaud opened")
	errPlaudNotConnected     = errors.New("the Plaud CLI is not signed in")
	errPlaudSendingOff       = errors.New("sending new recordings to a brain is off")
)

// PlaudCLI says whether the Plaud CLI is installed, where, and which
// version, with a warning when it is not one Fridolin reads.
type PlaudCLI struct {
	Found       bool   `json:"found"`
	Path        string `json:"path,omitempty"`
	Version     string `json:"version,omitempty"`
	Warning     string `json:"warning,omitempty"`
	InstallHint string `json:"install_hint"`
}

// PlaudStatus is what the dashboard's Plaud card shows.
type PlaudStatus struct {
	CLI       PlaudCLI    `json:"cli"`
	Connected bool        `json:"connected"`
	Settings  PlaudConfig `json:"settings"`
	LoginDir  string      `json:"login_dir"`
	// DefaultMessage is the instruction sent when plaud.message is empty,
	// and IntervalSeconds how often Plaud is checked, defaults applied.
	DefaultMessage  string `json:"default_message"`
	IntervalSeconds int    `json:"interval_seconds"`
	// TargetBrain receives recordings; BrainMissing means the picked one is
	// gone, so none does.
	TargetBrain  string `json:"target_brain,omitempty"`
	BrainMissing bool   `json:"brain_missing,omitempty"`
	// Where summaries go, or why they cannot go anywhere. SummaryChatBrain
	// names another brain that owns that chat: what you write back there
	// goes to it.
	SummaryChat      string `json:"summary_chat,omitempty"`
	SummaryToYou     bool   `json:"summary_to_you,omitempty"`
	SummaryChatBrain string `json:"summary_chat_brain,omitempty"`
	SummaryError     string `json:"summary_error,omitempty"`
	// Seen, Pending (waiting to be handed over) and Handed (with a brain,
	// waiting for its run or running) count recordings after the first
	// sync.
	Seen          *int          `json:"seen,omitempty"`
	Pending       *int          `json:"pending,omitempty"`
	Handed        *int          `json:"handed,omitempty"`
	Connect       *connectJob   `json:"connect,omitempty"`
	LastPoll      *time.Time    `json:"last_poll,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	LastForwarded *PlaudForward `json:"last_forwarded,omitempty"`
	// Forwarded lists the latest recordings handed over, the latest last.
	Forwarded []PlaudForward `json:"forwarded,omitempty"`
	// GivenUp lists the latest recordings that were not handed over, and
	// why, oldest first.
	GivenUp []plaudGivenUp `json:"given_up,omitempty"`
	// Account is who is signed in. Expired means the login lapsed: Plaud's
	// CLI deleted it, or Plaud refuses it. LoginMoved means Fridolin signed
	// in before plaud.brain_access changed where the login lives, and needs
	// to connect again.
	Account    string `json:"account,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
	LoginMoved bool   `json:"login_moved,omitempty"`
}

// Status reports the integration's state. It never runs the CLI: who is
// signed in comes from the last time Plaud was asked (see refreshAccount).
func (p *PlaudManager) Status() PlaudStatus {
	cfg := p.cfg()
	login := p.loginStatus()
	s := PlaudStatus{
		CLI:        PlaudCLI{InstallHint: "npm install -g @plaud-ai/cli"},
		Connected:  login == plaudConnected,
		Expired:    login == plaudExpired,
		LoginMoved: login == plaudMoved,
		Settings:   cfg.Plaud,
		LoginDir:   filepath.Dir(p.tokenPath()),

		DefaultMessage:  defaultPlaudMessage,
		IntervalSeconds: int(cfg.Plaud.interval() / time.Second),
	}
	if path, err := p.cliPath(); err == nil {
		s.CLI.Found, s.CLI.Path = true, path
		s.CLI.Version = plaudCLIVersion(path)
		s.CLI.Warning = plaudCLIWarning(s.CLI.Version)
	}
	if br := p.targetBrain(cfg); br != nil {
		s.TargetBrain = br.ID()
	} else if cfg.Plaud.Brain != "" {
		s.BrainMissing = true
	}
	// Where summaries go, for the card: a chat that belongs to another brain
	// gets a warning, since what you write back there goes to that brain.
	if br, jid, err := p.destination(cfg); err != nil {
		s.SummaryError = err.Error()
	} else {
		s.SummaryChat = jid
		s.SummaryToYou = len(cfg.AllowedNumbers) > 0 && jid == userJID(cfg.AllowedNumbers[0])
		if owner := p.brains.BrainFor(jid, false); owner != nil && owner != br {
			s.SummaryChatBrain = owner.ID()
		}
	}
	if st, _ := p.loadState(); st != nil {
		if st.Seeded {
			seen, pending, handed := len(st.Seen), 0, 0
			for _, it := range st.Pending {
				if it.Handed.IsZero() {
					pending++
				} else {
					handed++
				}
			}
			s.Seen, s.Pending, s.Handed = &seen, &pending, &handed
		}
		s.Forwarded, s.GivenUp = st.Forwarded, st.GivenUp
		if n := len(st.Forwarded); n > 0 {
			s.LastForwarded = &st.Forwarded[n-1]
		}
	}
	p.mu.Lock()
	if p.connect != nil {
		job := *p.connect // a copy: the sign-in goroutine keeps writing to the original
		s.Connect = &job
	}
	if !p.lastPoll.IsZero() {
		last := p.lastPoll
		s.LastPoll = &last
	}
	s.LastError = p.lastErr
	s.Expired = s.Expired || p.refused && s.Connected
	p.mu.Unlock()
	if s.Connected && s.CLI.Found {
		var checked time.Time
		if st, _ := p.loadState(); st != nil && st.Login != nil && st.Login.Dir == p.cliHome() {
			s.Account, checked = st.Login.Account, st.Login.Checked
		}
		p.refreshAccount(checked)
	}
	return s
}

// Connect starts Plaud's sign-in in the background: the CLI opens Plaud's
// page in the browser and waits for the approval. Status reports how it
// goes.
func (p *PlaudManager) Connect() error {
	bin, err := p.cliPath()
	if err != nil {
		return errPlaudNotInstalled
	}
	if w := plaudCLIWarning(plaudCLIVersion(bin)); w != "" {
		log.Printf("plaud: %s", w)
	}
	p.mu.Lock()
	if p.connect != nil && p.connect.State == "running" {
		p.mu.Unlock()
		return errPlaudSigningIn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	job := &connectJob{State: "running", cancel: cancel}
	p.connect = job
	p.mu.Unlock()

	// Stop cancels it and waits for its end: it records the sign-in in the
	// data folder, and asks who signed in.
	p.bg.Add(1)
	go func() {
		defer p.bg.Done()
		defer cancel()
		var seen bytes.Buffer
		out := writerFunc(func(b []byte) {
			p.mu.Lock()
			defer p.mu.Unlock()
			seen.Write(b)
			if job.AuthURL == "" {
				job.AuthURL = plaudSignInURL(seen.String())
			}
		})
		// The CLI opens the sign-in page in your browser itself.
		_, err := p.runCLIOut(withDesktop(ctx), out, "login")
		if err == nil && !p.hasLogin() {
			err = errors.New("the sign-in did not finish: Plaud's tool left no login")
		}
		if err == nil {
			// The login in this CLI home is Fridolin's now, also one the CLI
			// already had ("Already logged in"): you asked for it.
			err = p.update(func(st *plaudState) error {
				st.Login = &plaudLogin{Dir: p.cliHome(), Since: time.Now()}
				st.AuthNotified, st.Disconnected = time.Time{}, false
				return nil
			})
		}
		if err == nil {
			// Who signed in, for the card; a check asks again if this fails.
			if _, merr := p.me(ctx); merr != nil {
				log.Printf("plaud: could not ask who signed in: %v", merr)
			}
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil {
			job.State, job.Error = "error", err.Error()
			log.Printf("plaud: sign-in failed: %s", job.Error)
			return
		}
		p.refused = false
		job.State = "done"
		log.Printf("plaud: connected")
	}()
	return nil
}

// writerFunc is an io.Writer that hands what is written to a function.
type writerFunc func([]byte)

func (f writerFunc) Write(b []byte) (int, error) { f(b); return len(b), nil }

// Disconnect signs out of Plaud. The sign-out runs to its end even when
// whoever asked for it goes away: the CLI must not be stopped while it
// rewrites its login. With plaud.brain_access on, that is the login in
// ~/.plaud, which your terminal's plaud uses too. A login still there
// afterwards is an error: Fridolin no longer uses it, but it is still
// signed in.
//
// While the login waits to be connected again after brain_access changed
// (plaudMoved), or when there is none, ~/.plaud is not Fridolin's to sign
// out: it may hold your terminal's own login, or the one Fridolin made
// there, which the dashboard said would stay for your terminal when you
// turned brain_access off. Only the private login is signed out then.
//
// The state says so first: a check still running finds its login gone
// from then on, and must take that for a sign-out rather than an expiry.
func (p *PlaudManager) Disconnect() error {
	ctx, cancel := context.WithTimeout(p.lifetime(), 30*time.Second)
	defer cancel()
	login := p.loginStatus()
	shared := p.cfg().Plaud.BrainAccess && (login == plaudConnected || login == plaudExpired)
	// Signed out on purpose, not expired. What was seen stays, for the
	// same account signing in again.
	err := p.update(func(st *plaudState) error { st.Login, st.Disconnected = nil, true; return nil })
	p.mu.Lock()
	p.refused = false
	p.mu.Unlock()
	if err != nil {
		return err
	}
	token := filepath.Join(p.privateHome(), ".plaud", "tokens.json")
	if shared {
		token = p.tokenPath()
		_, err = p.run(ctx, "logout")
	} else {
		_, err = p.runCLIIn(ctx, p.privateHome(), nil, "logout")
		_ = os.Remove(token) // our private copy; the shared one is the CLI's to manage
	}
	if err != nil && !errors.Is(err, errPlaudAuth) {
		log.Printf("plaud: logout: %v", err)
	}
	if fileExists(token) {
		return fmt.Errorf("the sign-out did not remove %s, so Plaud's login is still there — run `plaud logout` in a terminal", token)
	}
	return nil
}

// CheckNow starts a check in the background, on the poller's lifetime so
// that stopping Fridolin ends it. false means one is already running, which
// covers this one.
func (p *PlaudManager) CheckNow() (bool, error) {
	if !p.connected() {
		return false, errPlaudNotConnected
	}
	if !p.cfg().Plaud.Enabled {
		return false, errPlaudSendingOff
	}
	if !p.beginPoll() {
		return false, nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(p.lifetime(), 10*time.Minute)
		defer cancel()
		if err := p.runPoll(ctx); err != nil {
			log.Printf("plaud: %v", err)
		}
	}()
	return true, nil
}

// Resend hands recording id over again: one handed over before (its run
// stopped, say) or given up. It waits like a new recording, and a check
// starts at once when one can.
func (p *PlaudManager) Resend(id string) error {
	err := p.update(func(st *plaudState) error {
		if st.Pending[id] != nil {
			return errPlaudAlreadyWaiting
		}
		known := func(id string) bool {
			return st.forwardOf(id) != nil || slices.ContainsFunc(st.GivenUp, func(g plaudGivenUp) bool { return g.ID == id })
		}
		if !known(id) {
			return errPlaudUnknownRecording
		}
		st.Seen = slices.DeleteFunc(st.Seen, func(s string) bool { return s == id })
		st.GivenUp = slices.DeleteFunc(st.GivenUp, func(g plaudGivenUp) bool { return g.ID == id })
		st.Pending[id] = &plaudPending{FirstSeen: time.Now()}
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("plaud %s: sending it again, as asked on the dashboard", id)
	if _, err := p.CheckNow(); err != nil {
		log.Printf("plaud %s: sent again at the next check: %v", id, err)
	}
	return nil
}

func (b *Bridge) apiPlaudResend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := b.plaud.Resend(req.ID); {
	case errors.Is(err, errPlaudUnknownRecording):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errPlaudAlreadyWaiting):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "waiting"})
	}
}

func (b *Bridge) apiPlaudStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, b.plaud.Status())
}

func (b *Bridge) apiPlaudConnect(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.Connect(); {
	case errors.Is(err, errPlaudNotInstalled):
		writeErr(w, http.StatusPreconditionFailed, "Install Plaud's CLI first: npm install -g @plaud-ai/cli")
	case errors.Is(err, errPlaudSigningIn):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
	}
}

func (b *Bridge) apiPlaudDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := b.plaud.Disconnect(); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (b *Bridge) apiPlaudSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled     *bool   `json:"enabled"`
		Brain       *string `json:"brain"`
		Polished    *bool   `json:"polished"`
		Message     *string `json:"message"`
		BrainAccess *bool   `json:"brain_access"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only a newly picked brain must exist: one that went away since must
	// not keep the other settings from being saved.
	if req.Brain != nil && *req.Brain != "" && *req.Brain != b.cfgs.Get().Plaud.Brain && b.brainMgr.Get(*req.Brain) == nil {
		writeErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("there is no approved brain %q", *req.Brain))
		return
	}
	_, err := b.cfgs.Update("", func(c *Config) error {
		if req.Enabled != nil {
			c.Plaud.Enabled = *req.Enabled
		}
		if req.Brain != nil {
			c.Plaud.Brain = *req.Brain
		}
		if req.Polished != nil {
			c.Plaud.Polished = *req.Polished
		}
		if req.Message != nil {
			c.Plaud.Message = *req.Message
		}
		if req.BrainAccess != nil {
			c.Plaud.BrainAccess = *req.BrainAccess
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiPlaudSync(w http.ResponseWriter, r *http.Request) {
	started, err := b.plaud.CheckNow()
	switch {
	case errors.Is(err, errPlaudNotConnected):
		writeErr(w, http.StatusPreconditionFailed, "Plaud is not connected")
	case errors.Is(err, errPlaudSendingOff):
		writeErr(w, http.StatusConflict, "Turn on “Send new recordings to a brain” first: a check hands new recordings to the brain")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	case !started:
		// A check that is already running covers this one; say so rather
		// than claim to start one.
		writeJSON(w, http.StatusOK, map[string]string{"status": "already checking"})
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
	}
}
