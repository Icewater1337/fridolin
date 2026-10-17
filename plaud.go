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
	"sort"
	"strings"
	"sync"
	"time"
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
	errPlaudAuth    = errors.New("the Plaud login expired")
	plaudIDRe       = regexp.MustCompile(`\b(of_[0-9a-f]{16,})\b`)
	ansiRe          = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	plaudKVRe       = regexp.MustCompile(`^\s*([a-z_]+):\s*(.*)$`)
	transcriptRowRe = regexp.MustCompile(`\[\d`)
)

const (
	plaudPageSize      = 20
	plaudSeedPages     = 25
	plaudPendingMaxAge = 3 * 24 * time.Hour
	plaudSeenKeep      = 2000
)

// plaudState is <data_dir>/plaud/state.json.
type plaudState struct {
	// Account is the Plaud account the state was seeded for; its seen list
	// means nothing for another one.
	Account      string             `json:"account,omitempty"`
	Seen         []string           `json:"seen"`
	Pending      map[string]float64 `json:"pending"` // id → unix time first seen
	AuthNotified float64            `json:"auth_notified"`
}

// seededFor reports whether st was seeded for the account acct. A state from
// before the account was recorded counts for whoever is signed in as long as
// it remembers any recording; one that remembers none was only written for
// an expiry notice and never seeded. When the CLI does not say who is signed
// in (acct is ""), the state is taken as it is.
func (st *plaudState) seededFor(acct string) bool {
	switch {
	case st.Account == "":
		return len(st.Seen) > 0 || len(st.Pending) > 0
	case acct == "":
		return true
	default:
		return st.Account == acct
	}
}

// PlaudForward describes the last recording handed to a brain.
type PlaudForward struct {
	Name  string    `json:"name"`
	Brain string    `json:"brain"`
	At    time.Time `json:"at"`
}

// PlaudManager runs the CLI, the poller and the connect flow.
type PlaudManager struct {
	b *Bridge

	mu            sync.Mutex
	connect       *connectJob
	lastPoll      time.Time
	lastErr       string
	lastForwarded *PlaudForward
	busy          bool
	stop          context.CancelFunc
	// life is the poller's context from Start, which Stop cancels; checks
	// started from the dashboard run on it too.
	life context.Context
	// authNotified is when the expiry notice went out while there was no
	// state to keep that time in (the login expired before the first sync).
	authNotified time.Time
	// checkedLogin is the login (see loginStamp) whose account the state was
	// last checked against. Only polls use it, one at a time.
	checkedLogin string

	// run executes the CLI; tests replace it.
	run func(ctx context.Context, args ...string) (string, error)
}

// NewPlaudManager creates the manager.
func NewPlaudManager(b *Bridge) *PlaudManager {
	p := &PlaudManager{b: b}
	p.run = p.runCLI
	return p
}

func (p *PlaudManager) cfg() *Config { return p.b.cfgs.Get() }

func (p *PlaudManager) dir() string { return filepath.Join(p.cfg().DataDir, "plaud") }

// cliHome is the HOME the CLI runs with, which decides where its login lives.
func (p *PlaudManager) cliHome() string {
	if p.cfg().Plaud.BrainAccess {
		return homeDir()
	}
	return filepath.Join(p.dir(), "home")
}

func (p *PlaudManager) tokenPath() string { return filepath.Join(p.cliHome(), ".plaud", "tokens.json") }

func (p *PlaudManager) connected() bool { return fileExists(p.tokenPath()) }

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

// runCLI runs `plaud args…` with what childEnv gives a helper program on
// ctx (see helperScope), the chosen home, telemetry off, and node (which the
// CLI needs) on PATH. child_env_passthrough is for brains: this third-party
// CLI needs nothing but its own login. Exit status 2 means the login
// expired.
func (p *PlaudManager) runCLI(ctx context.Context, args ...string) (string, error) {
	bin, err := p.cliPath()
	if err != nil {
		return "", err
	}
	home := p.cliHome()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = childEnv(helperScope(ctx), nil, "HOME="+home, "PATH="+plaudPATH(bin),
		"DO_NOT_TRACK=1", "PLAUD_TELEMETRY_DISABLED=1", "PLAUD_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if f, ok := ctx.Value(plaudOutputKey{}).(func([]byte)); ok {
		w := writerFunc(f)
		cmd.Stdout, cmd.Stderr = io.MultiWriter(&stdout, w), io.MultiWriter(&stderr, w)
	}
	err = cmd.Run()
	out := ansiRe.ReplaceAllString(stdout.String(), "")
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return out, errPlaudAuth
	}
	if err != nil {
		return out, fmt.Errorf("plaud %s: %v %s", args[0], err, truncate(strings.TrimSpace(ansiRe.ReplaceAllString(stderr.String(), "")), 300))
	}
	return out, nil
}

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

func parseKV(out string) map[string]string {
	d := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := plaudKVRe.FindStringSubmatch(line); m != nil {
			d[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return d
}

// me returns what `plaud me` says about the signed-in user.
func (p *PlaudManager) me(ctx context.Context) (map[string]string, error) {
	out, err := p.run(ctx, "me")
	if err != nil {
		return nil, err
	}
	return parseKV(out), nil
}

// account returns the signed-in Plaud user, for the dashboard.
func (p *PlaudManager) account(ctx context.Context) (string, error) {
	d, err := p.me(ctx)
	if err != nil {
		return "", err
	}
	if d["email"] != "" {
		return d["email"], nil
	}
	return d["nickname"], nil
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

// forward hands one recording to br, whose answer goes to jid. It reports
// false while Plaud has not finished the transcript.
func (p *PlaudManager) forward(ctx context.Context, id string, br *Brain, jid string) (bool, error) {
	cfg := p.cfg()
	detailsOut, err := p.run(ctx, "file", id)
	if err != nil {
		return false, err
	}
	d := parseKV(detailsOut)
	if d["transcript"] != "available" {
		return false, nil
	}
	args := []string{"transcript", id}
	if cfg.Plaud.Polished {
		args = append(args, "--polished")
	}
	transcript, err := p.run(ctx, args...)
	if err != nil {
		return false, err
	}
	if i := strings.Index(transcript, "Transcript:"); i >= 0 {
		transcript = transcript[i:]
		if nl := strings.Index(transcript, "\n"); nl >= 0 {
			transcript = transcript[nl+1:] // drop the "Transcript: <name>" header
		}
	}
	if !transcriptRowRe.MatchString(transcript) {
		return false, nil // the header is there, the segments not yet
	}
	summary := ""
	if d["summary"] == "available" {
		if s, err := p.run(ctx, "summary", id); err == nil {
			summary = strings.TrimSpace(s)
			if i := strings.Index(summary, "Fetching"); i >= 0 {
				if nl := strings.Index(summary[i:], "\n"); nl >= 0 {
					summary = strings.TrimSpace(summary[i+nl+1:])
				}
			}
			summary = strings.TrimSpace(strings.TrimPrefix(summary, "Summary:"))
		} else if errors.Is(err, errPlaudAuth) {
			return false, err
		} else {
			log.Printf("plaud %s: summary: %v", id, err)
		}
	}

	name := d["name"]
	if name == "" {
		name = id
	}
	day, when := time.Now().Format("02.01.2006"), d["start_at"]
	start := d["start_at"]
	if start == "" {
		start = d["created_at"]
	}
	// Plaud reports UTC without an offset.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, strings.TrimSuffix(start, "Z"), time.UTC); err == nil {
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
		return false, err
	}
	path := plaudTranscriptPath(dir, day, name, id)
	var body strings.Builder
	fmt.Fprintf(&body, "Plaud recording: %s\nRecorded: %s\nDuration: %s\nPlaud ID: %s\n\n", name, when, duration, id)
	if summary != "" {
		body.WriteString("=== Plaud AI summary ===\n" + summary + "\n\n")
	}
	body.WriteString("=== Transcript ===\n" + strings.TrimSpace(transcript) + "\n")
	if err := writeFileAtomic(path, []byte(body.String()), 0o600, false); err != nil {
		return false, err
	}

	tpl := cfg.Plaud.Message
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultPlaudMessage
	}
	text := strings.NewReplacer("{name}", name, "{when}", when, "{duration}", duration).Replace(tpl)
	p.b.handOff(br, jid, "plaud", text, []string{path})
	p.mu.Lock()
	p.lastForwarded = &PlaudForward{Name: name, Brain: br.ID(), At: time.Now()}
	p.mu.Unlock()
	log.Printf("plaud %s: handed %q to brain %s", id, name, br.ID())
	return true, nil
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
		return p.b.brainMgr.Get(id)
	}
	return p.b.brainMgr.BrainFor("", false)
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
		return cfg.AllowedNumbers[0] + "@s.whatsapp.net"
	}
	return ""
}

// isDirectChat reports whether jid is a one-to-one chat (a phone number or a
// LID), not a group, a broadcast list or a channel.
func isDirectChat(jid string) bool {
	return strings.HasSuffix(jid, "@s.whatsapp.net") || strings.HasSuffix(jid, "@lid")
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
		st.Pending = map[string]float64{}
	}
	return st, nil
}

func (p *PlaudManager) saveState(st *plaudState) error {
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
		p.authNotified = time.Time{}
	}
	p.mu.Unlock()
	if errors.Is(err, errPlaudAuth) {
		p.notifyAuthExpired()
	}
	return err
}

func (p *PlaudManager) poll(ctx context.Context) error {
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return err
	}
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
	if stamp := p.loginStamp(); st == nil || stamp != p.checkedLogin {
		acct, err := p.accountID(ctx)
		if err != nil {
			return err
		}
		if st == nil || !st.seededFor(acct) {
			if st != nil {
				why := "signed in to another Plaud account"
				if st.Account == "" {
					why = "the state only held an expiry notice and was never seeded"
				}
				log.Printf("plaud: %s, starting over", why)
			}
			if err := p.seed(ctx, acct); err != nil {
				return err
			}
			p.checkedLogin = stamp
			return nil
		}
		if st.Account == "" && acct != "" {
			st.Account = acct
			if err := p.saveState(st); err != nil {
				return err
			}
		}
		p.checkedLogin = stamp
	}

	seen := map[string]bool{}
	for _, id := range st.Seen {
		seen[id] = true
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
	for _, id := range ids {
		if _, pending := st.Pending[id]; !seen[id] && !pending {
			st.Pending[id] = float64(time.Now().Unix())
			log.Printf("plaud %s: new recording, waiting for the transcript", id)
		}
	}
	// Resolved before anything is fetched or written: without a brain or a
	// private chat the recordings wait, and the error shows on the dashboard.
	// With nothing waiting nothing is blocked, so the poll stays quiet rather
	// than log the same error every interval; the status call reports it.
	br, jid, err := p.destination()
	if err != nil && len(st.Pending) > 0 {
		if serr := p.saveState(st); serr != nil {
			return serr
		}
		return err
	}
	pendingIDs := make([]string, 0, len(st.Pending))
	for id := range st.Pending {
		pendingIDs = append(pendingIDs, id)
	}
	sort.Strings(pendingIDs)
	// A recording that fails is retried next time, until it is given up,
	// and does not hold up the others; its error is the check's, so the card
	// shows it instead of counting the recording as waiting for Plaud.
	var failed []error
	maxAge := fmt.Sprintf("%d days", plaudPendingMaxAge/(24*time.Hour))
	for _, id := range pendingIDs {
		done, err := p.forward(ctx, id, br, jid)
		if errors.Is(err, errPlaudAuth) {
			_ = p.saveState(st)
			return err
		}
		since := time.Unix(int64(st.Pending[id]), 0)
		giveUp := !done && time.Since(since) > plaudPendingMaxAge
		if err != nil {
			next := "will retry"
			if giveUp {
				next = "given up after " + maxAge
			}
			failed = append(failed, fmt.Errorf("recording %s (%s): %w", id, next, err))
		}
		if done || giveUp {
			// A recording that still fails may well have been transcribed
			// long ago, so its line does not blame Plaud.
			switch {
			case giveUp && err != nil:
				log.Printf("plaud %s: still failing after %s, giving up", id, maxAge)
			case giveUp:
				log.Printf("plaud %s: no transcript after %s, giving up", id, maxAge)
			}
			delete(st.Pending, id)
			st.Seen = append(st.Seen, id)
		}
		if err := p.saveState(st); err != nil {
			return err
		}
	}
	st.AuthNotified = 0
	if err := p.saveState(st); err != nil {
		return err
	}
	return errors.Join(failed...)
}

// seed starts the state for acct with the recordings it already has, all
// marked as seen.
func (p *PlaudManager) seed(ctx context.Context, acct string) error {
	st := &plaudState{Account: acct, Pending: map[string]float64{}}
	seen := map[string]bool{}
	for page := 1; page <= plaudSeedPages; page++ {
		ids, err := p.listIDs(ctx, page)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				st.Seen = append(st.Seen, id)
			}
		}
		if len(ids) < plaudPageSize {
			break
		}
	}
	log.Printf("plaud: first sync — %d existing recordings marked as seen", len(st.Seen))
	return p.saveState(st)
}

// notifyAuthExpired tells the private chat Plaud uses once a day that Plaud
// needs a new sign-in. Without one only the dashboard shows it.
//
// The notice time is kept in the state. Before the first sync there is none,
// and the notice must not write one: a state is what tells the next check
// that the existing recordings were already seen, so an empty one would make
// it forward all of them. Until then the time is kept in memory.
func (p *PlaudManager) notifyAuthExpired() {
	st, _ := p.loadState()
	p.mu.Lock()
	last := p.authNotified
	p.mu.Unlock()
	if st != nil {
		last = time.Unix(int64(st.AuthNotified), 0)
	}
	if time.Since(last) < 24*time.Hour {
		return
	}
	_, jid, err := p.destination()
	if err != nil {
		return
	}
	if err := p.b.wa.SendMessage(context.Background(), jid, "⚠️ Plaud: the login expired, so new recordings are not arriving. Reconnect in the Fridolin dashboard (Integrations → Plaud)."); err != nil {
		log.Printf("plaud: could not send the expiry notice: %v", err)
		return
	}
	now := time.Now()
	if st == nil {
		p.mu.Lock()
		p.authNotified = now
		p.mu.Unlock()
		return
	}
	st.AuthNotified = float64(now.Unix())
	_ = p.saveState(st)
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
			cfg := p.cfg()
			if !cfg.Plaud.Enabled || !p.connected() || time.Now().Before(next) {
				continue
			}
			interval := time.Minute
			if cfg.Plaud.IntervalSeconds > 0 {
				interval = time.Duration(cfg.Plaud.IntervalSeconds) * time.Second
			}
			next = time.Now().Add(interval)
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Minute)
			if err := p.Poll(pctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("plaud: %v", err)
			}
			pcancel()
		}
	}()
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

// Stop ends the poller, a running check and a running sign-in.
func (p *PlaudManager) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		p.stop()
	}
	if p.connect != nil && p.connect.cancel != nil {
		p.connect.cancel()
	}
}

// ── API ──────────────────────────────────────────────────────────────────

var plaudURLRe = regexp.MustCompile(`https?://[^\s'"]+`)

// plaudOutputKey carries a func([]byte) in a context; runCLI hands it the
// CLI's output as it arrives (the login prints its sign-in URL, then waits).
type plaudOutputKey struct{}

type writerFunc func([]byte)

func (f writerFunc) Write(b []byte) (int, error) { f(b); return len(b), nil }

func (b *Bridge) apiPlaudStatus(w http.ResponseWriter, r *http.Request) {
	p := b.plaud
	cfg := b.cfgs.Get()
	cli := map[string]any{"found": false, "install_hint": "npm install -g @plaud-ai/cli"}
	if path, err := p.cliPath(); err == nil {
		cli["found"], cli["path"] = true, path
	}
	resp := map[string]any{
		"cli":       cli,
		"connected": p.connected(),
		"settings":  cfg.Plaud,
		"login_dir": filepath.Dir(p.tokenPath()),
	}
	if br := p.targetBrain(); br != nil {
		resp["target_brain"] = br.ID()
	} else if cfg.Plaud.Brain != "" {
		resp["brain_missing"] = true
	}
	// Where summaries go, for the card: a chat that belongs to another brain
	// gets a warning, since what you write back there goes to that brain.
	if br, jid, err := p.destination(); err != nil {
		resp["summary_error"] = err.Error()
	} else {
		resp["summary_chat"] = jid
		resp["summary_to_you"] = len(cfg.AllowedNumbers) > 0 && jid == cfg.AllowedNumbers[0]+"@s.whatsapp.net"
		if owner := b.brainMgr.BrainFor(jid, false); owner != nil && owner != br {
			resp["summary_chat_brain"] = owner.ID()
		}
	}
	if st, _ := p.loadState(); st != nil {
		resp["seen"], resp["pending"] = len(st.Seen), len(st.Pending)
	}
	p.mu.Lock()
	if p.connect != nil {
		job := *p.connect // a copy: the sign-in goroutine keeps writing to the original
		resp["connect"] = job
	}
	if !p.lastPoll.IsZero() {
		resp["last_poll"] = p.lastPoll
	}
	if p.lastErr != "" {
		resp["last_error"] = p.lastErr
	}
	if p.lastForwarded != nil {
		resp["last_forwarded"] = p.lastForwarded
	}
	p.mu.Unlock()
	if p.connected() && cli["found"] == true && r.URL.Query().Get("account") != "0" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		if acct, err := p.account(ctx); err == nil {
			resp["account"] = acct
		} else if errors.Is(err, errPlaudAuth) {
			resp["expired"] = true
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (b *Bridge) apiPlaudConnect(w http.ResponseWriter, r *http.Request) {
	p := b.plaud
	if _, err := p.cliPath(); err != nil {
		writeErr(w, http.StatusPreconditionFailed, "Install Plaud's CLI first: npm install -g @plaud-ai/cli")
		return
	}
	p.mu.Lock()
	if p.connect != nil && p.connect.State == "running" {
		p.mu.Unlock()
		writeErr(w, http.StatusConflict, "already signing in — finish in the browser tab Plaud opened")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	job := &connectJob{State: "running", cancel: cancel}
	p.connect = job
	p.mu.Unlock()

	go func() {
		defer cancel()
		var seen bytes.Buffer
		ctx := context.WithValue(ctx, plaudOutputKey{}, func(b []byte) {
			p.mu.Lock()
			defer p.mu.Unlock()
			seen.Write(b)
			if i := bytes.LastIndexByte(seen.Bytes(), '\n'); i >= 0 && job.AuthURL == "" {
				job.AuthURL = plaudURLRe.FindString(ansiRe.ReplaceAllString(string(seen.Bytes()[:i]), "")) // whole lines only
			}
		})
		// The CLI opens the sign-in page in your browser itself.
		_, err := p.run(withDesktop(ctx), "login")
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil || !p.connected() {
			job.State = "error"
			if err != nil {
				job.Error = err.Error()
			} else {
				job.Error = "Plaud sign-in did not finish"
			}
			log.Printf("plaud: sign-in failed: %s", job.Error)
			return
		}
		job.State = "done"
		log.Printf("plaud: connected")
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "connecting"})
}

func (b *Bridge) apiPlaudDisconnect(w http.ResponseWriter, r *http.Request) {
	p := b.plaud
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := p.run(ctx, "logout"); err != nil && !errors.Is(err, errPlaudAuth) {
		log.Printf("plaud: logout: %v", err)
	}
	if !b.cfgs.Get().Plaud.BrainAccess {
		_ = os.Remove(p.tokenPath()) // our private copy; the shared one is the CLI's to manage
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
	if !b.plaud.connected() {
		writeErr(w, http.StatusPreconditionFailed, "Plaud is not connected")
		return
	}
	if !b.cfgs.Get().Plaud.Enabled {
		writeErr(w, http.StatusConflict, "Turn on “Send new recordings to a brain” first: a check hands new recordings to the brain")
		return
	}
	// A check that is already running covers this one; say so rather than
	// claim to start one.
	if !b.plaud.beginPoll() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already checking"})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(b.plaud.lifetime(), 10*time.Minute)
		defer cancel()
		if err := b.plaud.runPoll(ctx); err != nil {
			log.Printf("plaud: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}
