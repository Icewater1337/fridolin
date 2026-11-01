package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// A brain whose brain.json says "live_session": "owner_dm" keeps one
// interactive Claude Code running and wakes it with the messages its owner
// writes in a direct chat (docs/BRAINS.md, "Live session"), where every
// other message runs `claude -p` as ever.
//
// The turn runs in the brain's queue like any run: commandRunner.Run asks
// the liveHost first, which hands the message to the session when it is up
// and ready, and otherwise says no, so that the run goes ahead as a
// one-off. A session starts in the background the first time a message
// could use it, and is ready once it answered a check of its own: that
// proves the CLI accepted the channel (Claude Code's channels are a
// research preview that an org or an account can have off) and the login.
// One that does not, or that dies, is left alone for a while
// (liveBackoff), and the one-off runs go on.
//
// The message reaches the session as a channel notification of the
// session's MCP server (fridolin mcp --live, which asks the bridge for it:
// claudecli.LiveHub), and the turn's end and its answer come back through
// the session's Stop hook (fridolin live-turn-end). A turn takes the
// policy's admission as a run does, its token speaks for it (runTokens.replace),
// and its answer is delivered as a run's.
//
// A session that dies while a turn waits ends that turn in an error, as a
// run that failed would: the turn may have done something already, so it
// is not run a second time. A session is started anew after liveTurns
// turns, liveAge, or once it idled for liveIdle: Claude Code compacts
// its context itself meanwhile, and a new session is told the chat's
// history as a one-off run is.

// liveMaxFails is how often the backoff after a session that did not come
// up doubles. The limits are in waits (liveReady, liveIdle, liveBackoff,
// liveTurns, liveAge).
const liveMaxFails = 3

// liveProbe is what a session is asked once it is up, to see that
// channels work: it answers READY and the turn's text is thrown away.
const liveProbe = "Fridolin session check: no message has come in. Reply with the single word READY."

// liveSession is one brain's session.
type liveSession struct {
	brain  string
	hub    *claudecli.LiveHub
	access runAccess
	record string // the run record's file, to remove
	// chat is the direct chat the session serves: another of the owner's
	// chats runs one-off runs meanwhile, so that no session mixes two.
	chat string
	// fingerprint is liveFingerprint of what it was started with.
	fingerprint string
	// gh is the folder of the session's gh config (GH_CONFIG_DIR), which
	// holds a login only while a turn is under way; "" for a brain
	// without a GitHub grant.
	gh string

	// Under liveHost.mu.
	proc      *claudecli.LiveProc
	ready     bool
	stopped   bool
	busy      bool // a turn is under way
	turns     int
	started   time.Time
	idleSince time.Time
}

// liveFailure is how often a brain's session failed to come up, and until
// when it is left alone.
type liveFailure struct {
	n     int
	until time.Time
}

// liveHost keeps the live sessions of the brains.
type liveHost struct {
	b      *App
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	sess  map[string]*liveSession // by brain
	token map[string]*liveSession // by brain API token
	fails map[string]liveFailure
}

func newLiveHost(b *App) *liveHost {
	ctx, cancel := context.WithCancel(context.Background())
	h := &liveHost{b: b, ctx: ctx, cancel: cancel, sess: map[string]*liveSession{}, token: map[string]*liveSession{}, fails: map[string]liveFailure{}}
	h.wg.Go(h.reap)
	return h
}

// Stop ends every session and what starts them.
func (h *liveHost) Stop() {
	if h == nil {
		return
	}
	h.cancel()
	h.mu.Lock()
	for _, s := range h.sess {
		h.stopLocked(s)
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// up reports whether the brain's live session runs and is ready for a
// turn, for the core brain's system_status.
func (h *liveHost) up(brainID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sess[brainID]
	return s != nil && s.ready && !s.stopped
}

// eligible reports whether spec's message is one a live session takes:
// the brain has one, and it is its owner's direct message to it, from
// WhatsApp, in a brain that runs the CLI as the session needs it (without
// asking for permissions, which a session has nobody to answer, and in its
// own sandboxed state). A brain's own GitHub grant goes with the session
// as with a one-off run: each turn gets a token of its own (loginGitHub).
func (h *liveHost) eligible(spec brain.RunSpec) bool {
	s, m := spec.Settings, spec.Message
	switch {
	case s.Manifest.LiveSession != manifest.LiveSessionOwnerDM, s.Provider == "codex", !s.Dangerously, s.InheritUser:
		return false
	case m.Silent, m.IsGroup(), m.HandedIn(), m.GitHub != nil, chat.IsInternalChat(m.JID):
		return false
	case m.Source != chat.SourceWhatsApp && m.Source != chat.SourceDev && m.Source != "":
		return false
	}
	return h.b.isOwner(m.Sender)
}

// run takes spec's message into the brain's session when there is one
// ready. It reports false, with nothing done, when the message is not for
// a session or there is none ready.
func (h *liveHost) run(r *commandRunner, ctx context.Context, spec brain.RunSpec, cb claudecli.Callbacks, pol *runPolicy) (claudecli.Result, bool) {
	if h == nil {
		return claudecli.Result{}, false
	}
	if !h.eligible(spec) {
		h.endIfAnswersInChat(spec)
		return claudecli.Result{}, false
	}
	s := h.ready(r, spec)
	if s == nil {
		return claudecli.Result{}, false
	}
	return h.turn(ctx, r, s, spec, cb, pol), true
}

// endIfAnswersInChat ends the brain's session when a one-off run is about
// to answer in the chat it serves: its later turns only get the new message,
// so they would never see this exchange. The next message starts a session
// that is given the chat's history.
func (h *liveHost) endIfAnswersInChat(spec brain.RunSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sess[spec.ID]; s != nil && !s.stopped && !s.busy && s.chat == spec.Message.JID {
		log.Printf("brain %s: ending the live session, as a one-off run answers in its chat", spec.ID)
		h.stopLocked(s)
	}
}

// ready is the brain's session if it is ready for a turn of spec's
// message. Without one under way, or in place of one that ended, had its
// turns or its time, idled too long or was started with other settings,
// it starts one in the background, unless the last failed lately.
func (h *liveHost) ready(r *commandRunner, spec brain.RunSpec) *liveSession {
	fingerprint := liveFingerprint(r, spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	id, chat := spec.ID, spec.Message.JID
	if s := h.sess[id]; s != nil {
		var why string
		switch {
		case s.stopped:
			// On its way out (stopped, timed out, interrupted, reaped); its
			// watcher has not cleared it yet.
		case !s.ready, s.busy:
			return nil // still starting
		case s.chat != chat:
			return nil // another of the owner's chats'
		case s.fingerprint != fingerprint:
			why = "the brain's settings changed"
		case s.turns >= h.b.waits.liveTurns:
			why = fmt.Sprintf("it had its %d turns", s.turns)
		case time.Since(s.started) >= h.b.waits.liveAge:
			why = "it had its time"
		case time.Since(s.idleSince) >= h.b.waits.liveIdle:
			why = "it idled too long"
		default:
			return s
		}
		if why != "" {
			log.Printf("brain %s: ending the live session, as %s: starting a new one", id, why)
			h.stopLocked(s)
		}
	}
	if f := h.fails[id]; time.Now().Before(f.until) {
		return nil
	}
	s := &liveSession{brain: id, chat: chat, fingerprint: fingerprint, hub: claudecli.NewLiveHub(), started: time.Now()}
	h.sess[id] = s
	h.wg.Go(func() { h.start(r, spec, s) })
	return nil
}

// stopLocked ends s: its process dies, and the watcher cleans up.
func (h *liveHost) stopLocked(s *liveSession) {
	s.stopped = true
	if s.proc != nil {
		s.proc.Kill()
	}
}

// start brings s up: the process, then the check.
func (h *liveHost) start(r *commandRunner, spec brain.RunSpec, s *liveSession) {
	err := h.launch(r, spec, s)
	if err == nil {
		err = h.check(s)
	}
	if err == nil {
		h.mu.Lock()
		s.ready, s.idleSince = true, time.Now()
		delete(h.fails, s.brain)
		h.mu.Unlock()
		log.Printf("brain %s: the live session is ready", s.brain)
		return
	}
	h.mu.Lock()
	screen := ""
	if s.proc != nil {
		screen = textx.Tail(s.proc.Screen(), 600)
	} else if h.sess[s.brain] == s {
		delete(h.sess, s.brain) // no process, so no watcher to do it
	}
	h.stopLocked(s)
	f := h.fails[s.brain]
	f.n = min(f.n+1, liveMaxFails)
	backoff := h.b.waits.liveBackoff << (f.n - 1)
	f.until = time.Now().Add(backoff)
	h.fails[s.brain] = f
	h.mu.Unlock()
	log.Printf("brain %s: the live session did not come up (%v); messages run as one-off runs for %s. What it showed: %q", s.brain, err, backoff.Round(time.Second), screen)
}

// launch starts the process of s for spec's brain.
func (h *liveHost) launch(r *commandRunner, spec brain.RunSpec, s *liveSession) error {
	st := spec.Settings
	if r.hooks == nil || r.hooks.RunEnv == nil {
		return errors.New("the brain API is off")
	}
	sandboxed := r.sandbox != nil && r.sandbox.Enabled()
	run := runInfoFor(spec.ID, spec.Message)
	run.Live = true
	access := r.hooks.RunEnv(run)
	if access.Token == "" || access.MCPConfig == "" {
		access.Done()
		return errors.New("the brain API has no token or tools for the session (the HTTP server is off, or the bridge has no binary)")
	}
	if st.Manifest.GitHub != nil && h.b.github != nil {
		// gh reads its login anew each time it runs: loginGitHub puts one
		// there for each turn, which outlives no turn.
		gh, err := newGitHubConfigDir(access.TokenDir)
		if err != nil {
			access.Done()
			return fmt.Errorf("the session's gh folder: %w", err)
		}
		access.Env = append(access.Env, "GH_CONFIG_DIR="+gh, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
		prev := access.Done
		access.Done = func() {
			if err := os.RemoveAll(gh); err != nil {
				log.Printf("brain %s: removing the live session's gh folder: %v", spec.ID, err)
			}
			prev()
		}
		s.gh = gh
	}
	s.access = access
	fail := func(err error) error {
		access.Done()
		return err
	}
	args := claudecli.LiveArgs(cliOptionsOf(st), mcpConfigOf(spec.ID, spec.Dir), liveSettings(spec.Dir, r.home))
	args = append(args, runMCPArgs("claude", access)...)
	// Last: the flag takes every argument after it that is no flag.
	args = append(args, "--dangerously-load-development-channels", "server:"+mcpServerName)
	binary, finalArgs, env, err := r.command(spec, st, args, nil, append(slices.Clone(access.Env), "TERM=xterm-256color"), access.TokenDir)
	if err != nil {
		return fail(err)
	}
	if sandboxed {
		if err := seedLiveState(brainStateOf(st.DataDir, spec.ID).claude, spec.Dir); err != nil {
			log.Printf("brain %s: %v", spec.ID, err)
			return fail(brain.ErrSandboxUnavailable)
		}
	}
	spec.BeforeStart()
	proc, err := claudecli.StartLive(h.ctx, claudecli.Spawn{Binary: binary, Args: finalArgs, Dir: spec.Dir, Env: env})
	if err != nil {
		return fail(fmt.Errorf("starting claude: %w", err))
	}
	log.Printf("brain %s: the live session's CLI started (pid %d)", spec.ID, proc.PID())
	s.record = recordRun(st.DataDir, spec.ID, proc.PID())
	h.mu.Lock()
	s.proc = proc
	h.token[access.Token] = s
	stopped := s.stopped
	h.mu.Unlock()
	h.wg.Go(func() { h.watch(s) })
	if stopped {
		proc.Kill()
		return errors.New("stopped while it started")
	}
	return nil
}

// watch cleans up after s once its process is gone.
func (h *liveHost) watch(s *liveSession) {
	<-s.proc.Done()
	h.mu.Lock()
	if h.sess[s.brain] == s {
		delete(h.sess, s.brain)
	}
	delete(h.token, s.access.Token)
	s.ready, s.stopped = false, true
	turns := s.turns
	h.mu.Unlock()
	s.access.Done()
	if s.record != "" {
		_ = os.Remove(s.record)
	}
	log.Printf("brain %s: the live session ended after %d turn(s)", s.brain, turns)
}

// check asks s to answer a message of its own, which shows that channels
// work for it. The wait is liveReady.
func (h *liveHost) check(s *liveSession) error {
	limit := time.NewTimer(h.b.waits.liveReady)
	defer limit.Stop()
	select {
	case <-s.hub.Connected():
		// Claude Code takes channel messages only once it has registered the
		// server's channel, a moment after it connected: one sent before is lost.
		select {
		case <-time.After(h.b.waits.liveSettle):
		case <-s.proc.Done():
			return errors.New("claude exited at the start")
		case <-h.ctx.Done():
			return h.ctx.Err()
		}
	case <-s.proc.Done():
		return errors.New("claude exited at the start")
	case <-limit.C:
		return errors.New("claude did not start the session's MCP server")
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
	answer, err := s.hub.Begin()
	if err != nil {
		return err
	}
	defer s.hub.Abort()
	if err := s.hub.Deliver(claudecli.LiveEvent{Content: liveProbe, Meta: map[string]string{"kind": "check"}}); err != nil {
		return err
	}
	select {
	case <-answer:
		return nil
	case <-s.proc.Done():
		return errors.New("claude exited before it answered the check")
	case <-limit.C:
		return errors.New("claude did not answer the check: channels may be off for this account or organisation, or the login was not accepted")
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

// turn gives s spec's message and waits for the answer.
func (h *liveHost) turn(ctx context.Context, r *commandRunner, s *liveSession, spec brain.RunSpec, cb claudecli.Callbacks, pol *runPolicy) claudecli.Result {
	m := spec.Message
	run := runInfoFor(spec.ID, m)
	run.Policy = pol
	if pol != nil {
		run.ID = pol.ID
	}
	h.b.runTokens.replace(s.access.Token, run)

	h.mu.Lock()
	kind := prompttext.LiveFollow
	if s.turns == 0 {
		kind = prompttext.LiveStart
	}
	s.turns++
	n := s.turns
	proc := s.proc
	s.busy = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		s.busy, s.idleSince = false, time.Now()
		h.mu.Unlock()
	}()

	// Before the turn waits for its answer: a Stop that comes while the
	// token is minted then finds no turn waiting, rather than ending this
	// one before its message went in.
	if s.gh != "" {
		defer h.loginGitHub(ctx, s, spec)()
	}
	answer, err := s.hub.Begin()
	if err != nil {
		return claudecli.Result{Err: err}
	}
	defer s.hub.Abort()

	pid := proc.PID()
	if cleanup := cb.Started(pid); cleanup != nil {
		defer cleanup()
	}
	defer cb.Reaped()
	sink, closeStream := cb.OpenStream(pid, func(claudecli.Result) {})
	defer closeStream()
	sink.Log("live session, turn " + strconv.Itoa(n))
	notice := time.AfterFunc(r.waits.workingNotice, cb.Notice)
	defer notice.Stop()

	if err := s.hub.Deliver(claudecli.LiveEvent{Content: spec.LivePrompt(kind), Meta: map[string]string{"kind": "message", "turn": strconv.Itoa(n)}}); err != nil {
		return claudecli.Result{Err: err}
	}
	var res claudecli.Result
	select {
	case text := <-answer:
		res.Text = strings.TrimSpace(text)
	case <-proc.Done():
		res.Err = errors.New("the live session ended before it answered; the next message starts a new one")
	case <-ctx.Done():
		// Its turn is half done and a Stop hook could still come for it:
		// the session goes, and the next message starts a new one.
		h.mu.Lock()
		h.stopLocked(s)
		h.mu.Unlock()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.Err = claudecli.TimedOut(spec.Settings.RunTimeout)
		} else {
			res.Err = claudecli.ErrInterrupted
		}
	}
	sink.Result(res)
	return res
}

// loginGitHub logs the gh of s in for the turn of spec's message, with a
// token of the brain's grant minted for it, as a one-off run's gh is
// (withGitHubToken); what it returns logs gh out and revokes the token,
// once the turn is over. A token that cannot be minted leaves the turn
// without one, and the log says why; a session on its way out gets none.
func (h *liveHost) loginGitHub(ctx context.Context, s *liveSession, spec brain.RunSpec) func() {
	run := runInfoFor(spec.ID, spec.Message)
	run.OwnerGitHub = spec.Settings.Manifest.GitHub
	g := githubGrantOf(run, h.b.cfgs.Get())
	h.mu.Lock()
	stopped := s.stopped
	h.mu.Unlock()
	if g == nil || stopped {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, done, err := h.b.github.Mint(ctx, *g)
	if err != nil {
		log.Printf("brain %s: github: no token for this turn of the live session (repos %s): %v", spec.ID, strings.Join(g.Repos, ","), err)
		return func() {}
	}
	if err := writeGitHubHosts(s.gh, tok); err != nil {
		done()
		log.Printf("brain %s: github: no token for this turn of the live session: %v", spec.ID, err)
		return func() {}
	}
	return func() {
		if err := os.Remove(filepath.Join(s.gh, "hosts.yml")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("brain %s: logging the live session's gh out: %v", spec.ID, err)
		}
		done()
	}
}

// reap ends the sessions that idled for liveIdle.
func (h *liveHost) reap() {
	every := min(h.b.waits.liveIdle/2, time.Minute)
	t := time.NewTicker(max(every, time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			h.mu.Lock()
			for _, s := range h.sess {
				if s.ready && !s.stopped && !s.busy && time.Since(s.idleSince) >= h.b.waits.liveIdle {
					log.Printf("brain %s: the live session idled for %s: ending it", s.brain, h.b.waits.liveIdle)
					h.stopLocked(s)
				}
			}
			h.mu.Unlock()
		}
	}
}

// next is GET /brain/live/next: the next message for the session whose
// token it is.
func (h *liveHost) next(ctx context.Context, token string, wait time.Duration) (claudecli.LiveEvent, bool, error) {
	s := h.byToken(token)
	if s == nil {
		return claudecli.LiveEvent{}, false, apperr.New(apperr.NotFound, "this token is no live session's")
	}
	ev, ok := s.hub.Next(ctx, wait)
	return ev, ok, nil
}

// end is POST /brain/live/turn-end: the Stop hook of the session whose
// token it is. A Stop with no turn waiting is the session going on after
// its answer, as when a background task it started ends: what it says then
// goes to its chat, as a later message of the run would. One of a session
// on its way out, or of its check, goes nowhere.
func (h *liveHost) end(ctx context.Context, token, text string) error {
	s := h.byToken(token)
	if s == nil {
		return apperr.New(apperr.NotFound, "this token is no live session's")
	}
	if s.hub.End(text) {
		return nil
	}
	h.mu.Lock()
	gone := s.stopped || !s.ready
	h.mu.Unlock()
	text = strings.TrimSpace(text)
	if gone || text == "" || h.b.pipeline == nil {
		log.Printf("brain %s: the live session stopped with no turn waiting (%d characters not sent)", s.brain, len(text))
		return nil
	}
	log.Printf("brain %s: the live session went on after its turn: sending what it said (%d chars) to %s", s.brain, len(text), s.chat)
	if err := h.b.pipeline.Reply(context.WithoutCancel(ctx), s.brain, s.chat, text, h.b.pipeline.Settings().MaxChunkSize); err != nil {
		log.Printf("brain %s: %v", s.brain, err)
	}
	return nil
}

// liveFingerprint sums up what a session of spec's brain is started with
// that can change while it runs: the brain's folder, its settings and its
// sandbox profile (deny_paths, allow_paths and the like). The session of
// another is replaced, so that a tool taken away, a path denied or another
// model holds from the next message on, as it does for a run.
func liveFingerprint(r *commandRunner, spec brain.RunSpec) string {
	sum := sha256.New()
	if st := spec.Settings; st != nil {
		s := *st
		// What only the first turn's prompt tells, and a zone has no JSON.
		s.Zone, s.FrideInstances = nil, nil
		_ = json.NewEncoder(sum).Encode(s)
	}
	_, _ = io.WriteString(sum, spec.Dir+"\x00")
	if r.sandbox != nil && r.sandbox.Enabled() {
		profile, err := r.sandbox.Profile(spec.ID)
		if err != nil {
			profile = "error: " + err.Error()
		}
		_, _ = io.WriteString(sum, profile)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (h *liveHost) byToken(token string) *liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token[token]
}

// liveSettings are the session's --settings: what a run's are (the
// instructions above the folder it does not read), the Stop hook that
// brings each turn's answer to the bridge, and no warning about the
// permissions it runs without, which the bridge's setting already chose.
func liveSettings(dir, home string) string {
	data, err := json.Marshal(map[string]any{
		"claudeMdExcludes":                  instructionsAbove(dir, home),
		"skipDangerousModePermissionPrompt": true,
		"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": `"$FRIDOLIN_BIN" live-turn-end`, "timeout": 30},
		}}}},
	})
	if err != nil {
		return ""
	}
	return string(data)
}

// seedLiveState has the brain's own Claude Code config in configDir say
// that onboarding is done and the brain's folder is trusted, as a
// one-off run (-p) never asks, so that the interactive session meets no
// dialog of that kind. What else the file holds stays.
func seedLiveState(configDir, folder string) error {
	path := filepath.Join(configDir, ".claude.json")
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			doc = map[string]any{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading the session's Claude Code state: %w", err)
	}
	doc["hasCompletedOnboarding"] = true
	if _, ok := doc["theme"]; !ok {
		doc["theme"] = "dark"
	}
	projects, _ := doc["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	for _, dir := range []string{AbsPath(folder), fsx.Resolve(AbsPath(folder))} {
		p, _ := projects[dir].(map[string]any)
		if p == nil {
			p = map[string]any{}
		}
		p["hasTrustDialogAccepted"] = true
		projects[dir] = p
	}
	doc["projects"] = projects
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(path, append(data, '\n'), 0o600, false); err != nil {
		return fmt.Errorf("writing the session's Claude Code state: %w", err)
	}
	return nil
}
