package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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
	// CLIPath overrides the plaud binary (default: found on PATH or in nvm).
	CLIPath string `json:"cli_path,omitempty"`
	// IntervalSeconds between polls (default 60, minimum 30).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// Polished uses Plaud's AI-cleaned transcript instead of the raw one.
	Polished bool `json:"polished,omitempty"`
	// Message is sent to the brain with each recording. {name}, {when} and
	// {duration} are filled in. Empty uses a built-in instruction.
	Message string `json:"message,omitempty"`
	// BrainAccess keeps the login in ~/.plaud/ so brains can use the CLI.
	BrainAccess bool `json:"brain_access,omitempty"`
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

const defaultPlaudMessage = `[Plaud] New recording "{name}" ({when}, {duration}). The attached .txt is the finished transcript from the Plaud recorder, with Plaud's AI summary when there is one — it is already transcribed, do not transcribe it again. File it as your plaud skill describes (skills/plaud.md); without one: save it under transcripts/ as "DD.MM.YYYY - Short description.txt", note durable facts in memories, and reply with a short summary, key points and action items.`

var (
	errPlaudAuth = errors.New("the Plaud login expired")
	plaudIDRe    = regexp.MustCompile(`\b(of_[0-9a-f]{16,})\b`)
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	plaudKVRe    = regexp.MustCompile(`^\s*([a-z_]+):\s*(.*)$`)
)

const (
	plaudPageSize      = 20
	plaudSeedPages     = 25
	plaudPendingMaxAge = 3 * 24 * time.Hour
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
	// LastForwarded is the last recording handed to a brain, for the card.
	LastForwarded *PlaudForward `json:"last_forwarded,omitempty"`
	// AuthNotified is when the chat was last told the login expired.
	AuthNotified time.Time `json:"auth_notified,omitzero"`
	// Login is the sign-in Fridolin made, kept until Disconnect. Plaud's CLI
	// deletes a login that expired (its refresh token ran out, or Plaud
	// refused it), so a login recorded here whose tokens.json is gone has
	// expired, rather than never been made.
	Login *plaudLogin `json:"login,omitempty"`
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

// PlaudForward describes the last recording handed to a brain.
type PlaudForward struct {
	Name  string    `json:"name"`
	Brain string    `json:"brain"`
	At    time.Time `json:"at"`
}

// PlaudManager runs the CLI, the poller and the connect flow. It knows the
// rest of the bridge only through the configuration, the brains and two
// functions, like the backup manager, so it can be tested without a bridge.
type PlaudManager struct {
	cfgs   *ConfigStore
	brains *BrainManager
	// handOff gives a recording's transcript to br, whose answer goes to jid.
	handOff func(br *Brain, jid, sender, text string, media []string)
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

	// run executes the CLI for everything but the sign-in. It is runCLI;
	// tests wrap it to act between two commands.
	run func(ctx context.Context, args ...string) (string, error)
}

// NewPlaudManager creates the manager. The caller sets handOff and notify.
func NewPlaudManager(cfgs *ConfigStore, brains *BrainManager) *PlaudManager {
	p := &PlaudManager{cfgs: cfgs, brains: brains}
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
	return filepath.Join(p.dir(), "home")
}

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
// recorded counts while its tokens.json is there.
//
// A sign-in made with the other CLI home counts for nothing until you
// connect again: plaud.brain_access moves where the CLI keeps its login,
// and ~/.plaud may hold your own terminal's login, maybe of another
// account, which Fridolin must not take over unasked.
func (p *PlaudManager) loginStatus() string {
	st, _ := p.loadState()
	var dir string
	if st != nil && st.Login != nil {
		dir = st.Login.Dir
	}
	switch {
	case dir != "" && dir != p.cliHome():
		return plaudMoved
	case p.hasLogin():
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
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	candidates = append(candidates, "/opt/homebrew/bin/plaud", "/usr/local/bin/plaud")
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}
	return "", errors.New("the Plaud CLI is not installed")
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
	bin, err := p.cliPath()
	if err != nil {
		return "", err
	}
	if args[0] != "login" {
		p.cliMu.Lock()
		defer p.cliMu.Unlock()
	}
	home := p.cliHome()
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
	detail := truncate(strings.TrimSpace(stderr), 300)
	switch {
	case cmd == "login" && code == 2:
		return fmt.Errorf("access was not granted: it was denied on Plaud's page, or the sign-in failed (%s)", detail)
	case cmd == "login" && code == 4:
		return errors.New("the sign-in timed out: Plaud's tool waits two minutes for the approval in the browser")
	case code == 2:
		return errPlaudAuth
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

func (p *PlaudManager) listIDs(ctx context.Context, page int) ([]string, error) {
	out, err := p.run(ctx, "files", "--page", fmt.Sprint(page), "--page-size", fmt.Sprint(plaudPageSize))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range plaudIDRe.FindAllStringSubmatch(out, -1) {
		ids = append(ids, m[1])
	}
	return ids, nil
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
		if st.Login == nil || st.Login.Dir != p.cliHome() {
			// A sign-in from before they were recorded.
			st.Login = &plaudLogin{Dir: p.cliHome(), Since: time.Now()}
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

// forward hands one recording, first seen at since, to br, whose answer
// goes to jid, and says what it handed over: nil while Plaud has not
// finished the transcript.
func (p *PlaudManager) forward(ctx context.Context, id string, since time.Time, br *Brain, jid string) (*PlaudForward, error) {
	cfg := p.cfg()
	detailsOut, err := p.run(ctx, "file", id)
	if err != nil {
		return nil, err
	}
	d := parseKV(detailsOut)
	if d["transcript"] != "available" {
		return nil, nil
	}
	tmp, err := os.MkdirTemp(p.dir(), "fetch-")
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
	day, when := time.Now().Format("02.01.2006"), "unknown"
	for _, k := range []string{"start_at", "created_at"} {
		if t, ok := plaudTime(d[k]); ok {
			local := t.Local()
			day, when = local.Format("02.01.2006"), local.Format("02.01.2006 15:04")
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

	tpl := cfg.Plaud.Message
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultPlaudMessage
	}
	text := strings.NewReplacer("{name}", name, "{when}", when, "{duration}", duration).Replace(tpl)
	p.handOff(br, jid, "plaud", text, []string{path})
	log.Printf("plaud %s: handed %q to brain %s", id, name, br.ID())
	return &PlaudForward{Name: name, Brain: br.ID(), At: time.Now()}, nil
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
	s = strings.TrimSpace(strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\n", " ", "\r", " ", "\t", " ").Replace(s))
	if len(s) > 120 {
		s = truncate(s, 120)
	}
	if s == "" {
		s = "Plaud recording"
	}
	return s
}

// targetBrain is the configured brain, else the DM brain, else the default.
func (p *PlaudManager) targetBrain() *Brain {
	if id := p.cfg().Plaud.Brain; id != "" {
		return p.brains.Get(id)
	}
	return p.brains.BrainFor("", false)
}

// summaryChat is where br's answer to a recording and the expiry notice go.
// Recordings are private (a doctor's visit, an HR talk), so it is never a
// group, whatever chats the brain serves: its reply_chat when that is a
// direct chat, else your own DM (the first allowed number). "" means there
// is no private chat to use.
func (p *PlaudManager) summaryChat(br *Brain) string {
	if rc := br.settings.Load().manifest.ReplyChat; isDirectChat(rc) {
		return rc
	}
	if cfg := p.cfg(); len(cfg.AllowedNumbers) > 0 {
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
// chat its answers go to.
func (p *PlaudManager) destination() (*Brain, string, error) {
	br := p.targetBrain()
	if br == nil {
		if id := p.cfg().Plaud.Brain; id != "" {
			return nil, "", fmt.Errorf("brain %s, picked for Plaud recordings, was removed or is not approved — pick another one under Integrations → Plaud", id)
		}
		return nil, "", errors.New("no brain to send Plaud recordings to")
	}
	jid := p.summaryChat(br)
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

	ids, err := p.listIDs(ctx, 1)
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
		for _, id := range ids {
			if _, pending := st.Pending[id]; !seen[id] && !pending {
				st.Pending[id] = &plaudPending{FirstSeen: time.Now()}
				log.Printf("plaud %s: new recording, waiting for the transcript", id)
			}
		}
		// A login that works is not expired: the next expiry gets its notice.
		st.AuthNotified = time.Time{}
		if st.Login == nil {
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
	br, jid, err := p.destination()
	if err != nil && len(st.Pending) > 0 {
		return err
	}
	// A recording that fails is retried next time, until it is given up,
	// and does not hold up the others; its error is the check's, so the card
	// shows it instead of counting the recording as waiting for Plaud.
	var failed []error
	maxAge := fmt.Sprintf("%d days", plaudPendingMaxAge/(24*time.Hour))
	for _, id := range st.pendingInOrder() {
		since := st.Pending[id].FirstSeen
		fw, err := p.forward(ctx, id, since, br, jid)
		if errors.Is(err, errPlaudAuth) {
			return err
		}
		done := fw != nil
		giveUp := !done && time.Since(since) > plaudPendingMaxAge
		if err != nil {
			next := "will retry"
			if giveUp {
				next = "given up after " + maxAge
			}
			failed = append(failed, fmt.Errorf("recording %s (%s): %w", id, next, err))
		}
		if !done && !giveUp {
			continue
		}
		// A recording that still fails may well have been transcribed
		// long ago, so its line does not blame Plaud.
		switch {
		case giveUp && err != nil:
			log.Printf("plaud %s: still failing after %s, giving up", id, maxAge)
		case giveUp:
			log.Printf("plaud %s: no transcript after %s, giving up", id, maxAge)
		}
		if err := p.update(func(st *plaudState) error {
			if _, ok := st.Pending[id]; ok {
				delete(st.Pending, id)
				st.Seen = append(st.Seen, id)
			}
			if fw != nil {
				st.LastForwarded = fw
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return errors.Join(failed...)
}

// seed starts the state over for acct, with the recordings it already
// has, all marked as seen. What does not belong to an account stays: the
// sign-in and the last recording handed over, for the card.
func (p *PlaudManager) seed(ctx context.Context, acct string) error {
	var listed []string
	seen := map[string]bool{}
	for page := 1; page <= plaudSeedPages; page++ {
		ids, err := p.listIDs(ctx, page)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				listed = append(listed, id)
			}
		}
		if len(ids) < plaudPageSize {
			break
		}
	}
	log.Printf("plaud: first sync — %d existing recordings marked as seen", len(listed))
	return p.update(func(st *plaudState) error {
		fresh := newPlaudState()
		fresh.Seeded, fresh.Account, fresh.Seen = true, acct, listed
		fresh.LastForwarded, fresh.Login = st.LastForwarded, st.Login
		if fresh.Login == nil {
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
func (p *PlaudManager) notifyAuthExpired() {
	st, err := p.loadState()
	if err != nil {
		log.Printf("plaud: %v", err)
		return
	}
	if st != nil && time.Since(st.AuthNotified) < 24*time.Hour {
		return
	}
	_, jid, err := p.destination()
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

// Start runs the poller.
func (p *PlaudManager) Start(ctx context.Context) {
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
	interval := time.Minute
	if cfg.Plaud.IntervalSeconds > 0 {
		interval = time.Duration(cfg.Plaud.IntervalSeconds) * time.Second
	}
	return time.Now().Add(interval)
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
// for a look-up of the account.
func (p *PlaudManager) Stop() {
	p.mu.Lock()
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

var plaudURLRe = regexp.MustCompile(`https?://[^\s'"]+`)

// What the dashboard's Plaud actions refuse, and why; the handlers word
// them for the card.
var (
	errPlaudNotInstalled = errors.New("the Plaud CLI is not installed")
	errPlaudSigningIn    = errors.New("already signing in — finish in the browser tab Plaud opened")
	errPlaudNotConnected = errors.New("the Plaud CLI is not signed in")
	errPlaudSendingOff   = errors.New("sending new recordings to a brain is off")
)

// PlaudCLI says whether the Plaud CLI is installed, and where.
type PlaudCLI struct {
	Found       bool   `json:"found"`
	Path        string `json:"path,omitempty"`
	InstallHint string `json:"install_hint"`
}

// PlaudStatus is what the dashboard's Plaud card shows.
type PlaudStatus struct {
	CLI       PlaudCLI    `json:"cli"`
	Connected bool        `json:"connected"`
	Settings  PlaudConfig `json:"settings"`
	LoginDir  string      `json:"login_dir"`
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
	// Seen and Pending count recordings once there is a state.
	Seen          *int          `json:"seen,omitempty"`
	Pending       *int          `json:"pending,omitempty"`
	Connect       *connectJob   `json:"connect,omitempty"`
	LastPoll      *time.Time    `json:"last_poll,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	LastForwarded *PlaudForward `json:"last_forwarded,omitempty"`
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
	}
	if path, err := p.cliPath(); err == nil {
		s.CLI.Found, s.CLI.Path = true, path
	}
	if br := p.targetBrain(); br != nil {
		s.TargetBrain = br.ID()
	} else if cfg.Plaud.Brain != "" {
		s.BrainMissing = true
	}
	// Where summaries go, for the card: a chat that belongs to another brain
	// gets a warning, since what you write back there goes to that brain.
	if br, jid, err := p.destination(); err != nil {
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
			seen, pending := len(st.Seen), len(st.Pending)
			s.Seen, s.Pending = &seen, &pending
		}
		s.LastForwarded = st.LastForwarded
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
	if _, err := p.cliPath(); err != nil {
		return errPlaudNotInstalled
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

	go func() {
		defer cancel()
		var seen bytes.Buffer
		out := writerFunc(func(b []byte) {
			p.mu.Lock()
			defer p.mu.Unlock()
			seen.Write(b)
			if i := bytes.LastIndexByte(seen.Bytes(), '\n'); i >= 0 && job.AuthURL == "" {
				job.AuthURL = plaudURLRe.FindString(ansiRe.ReplaceAllString(string(seen.Bytes()[:i]), "")) // whole lines only
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
				st.AuthNotified = time.Time{}
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
// afterwards is an error: Fridolin would go on using it.
func (p *PlaudManager) Disconnect() error {
	ctx, cancel := context.WithTimeout(p.lifetime(), 30*time.Second)
	defer cancel()
	if _, err := p.run(ctx, "logout"); err != nil && !errors.Is(err, errPlaudAuth) {
		log.Printf("plaud: logout: %v", err)
	}
	if !p.cfg().Plaud.BrainAccess {
		_ = os.Remove(p.tokenPath()) // our private copy; the shared one is the CLI's to manage
	}
	// Signed out on purpose, not expired. What was seen stays, for the
	// same account signing in again.
	err := p.update(func(st *plaudState) error { st.Login = nil; return nil })
	p.mu.Lock()
	p.refused = false
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if p.hasLogin() {
		return fmt.Errorf("the sign-out did not remove %s, so Plaud's login is still there — run `plaud logout` in a terminal", p.tokenPath())
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
