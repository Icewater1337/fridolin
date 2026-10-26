package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/examples"
	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/intent"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// scheduleTemplates are the built-in tasks of scheduled agents
// (examples/schedules), read once at startup like the brain templates.
var scheduleTemplates = mustScheduleTemplates()

func mustScheduleTemplates() []sched.Template {
	list, err := sched.Templates(examples.FS, "schedules")
	if err != nil {
		panic("the built-in schedule templates: " + err.Error())
	}
	return list
}

func scheduleTemplate(name string) (sched.Template, bool) {
	i := slices.IndexFunc(scheduleTemplates, func(t sched.Template) bool { return t.Name == name })
	if i < 0 {
		return sched.Template{}, false
	}
	return scheduleTemplates[i], true
}

// agents runs the brains' scheduled agents (docs/BRAINS.md, "Scheduled
// agents"): the scheduler's tick starts the ones that fell due, the
// dashboard and the CLI start one now, and each run's result goes where
// its agent says. Their state, history and logs are in sched.Store.
type agents struct {
	b     *App
	store *sched.Store

	mu sync.Mutex
	// running are the runs in progress, by agent key (sched.Key), and
	// byID the same by run id, which a brain run's message carries.
	running map[string]*agentRun
	byID    map[string]*agentRun
	// recovered is set once the runs a stop cut short were ended.
	recovered bool
	// frideWaiting is FrIDE's news for an agent, by agent key, that waits
	// for a run of it: one at a time, and what arrives meanwhile goes into
	// the next.
	frideWaiting map[string][]string
}

// agentRun is one run in progress.
type agentRun struct {
	run   sched.Run
	agent manifest.Schedule
	// chat is where the result goes, "" for nowhere.
	chat string
	log  *sched.Log
	// What the policy gives a brain run (admitScheduled).
	risk    skill.Risk
	timeout time.Duration
	// fride is FrIDE's news a TriggerFride run handles.
	fride []string
}

func newAgents(b *App, layout config.Layout) *agents {
	return &agents{b: b, store: sched.NewStore(layout.Schedules()),
		running: map[string]*agentRun{}, byID: map[string]*agentRun{}, frideWaiting: map[string][]string{}}
}

// recover ends, once, the runs the state says are running: the bridge
// stopped while they ran.
func (a *agents) recover(now time.Time) {
	a.mu.Lock()
	done := a.recovered
	a.recovered = true
	a.mu.Unlock()
	if done {
		return
	}
	runs, err := a.store.Interrupted(now)
	if err != nil {
		log.Printf("scheduled agents: %v", err)
	}
	for _, r := range runs {
		log.Printf("brain %s: scheduled agent %s: run %s was cut short when the bridge stopped", r.Brain, r.Agent, r.ID)
	}
}

// jitterOf is s's jitter, its own or schedules.jitter_minutes.
func jitterOf(s manifest.Schedule, cfg config.SchedulesSettings) time.Duration {
	if s.JitterMinutes != nil {
		return time.Duration(*s.JitterMinutes) * time.Minute
	}
	return cfg.Jitter
}

// tick starts the agents that fell due by now, of every brain that runs.
func (a *agents) tick(now time.Time) {
	now = now.In(a.b.zone())
	a.recover(now)
	cfg := a.b.cfgs.Get().Live.Schedules
	states, err := a.store.States()
	if err != nil {
		log.Printf("scheduled agents: %v", err)
		return
	}
	known := map[string]bool{}
	brains := map[string]bool{}
	for _, br := range a.b.brainMgr.Brains() {
		brains[br.ID()] = true
		for _, s := range br.Manifest().EffectiveSchedules() {
			key := sched.Key(br.ID(), s.Name)
			known[key] = true
			a.consider(br, s, states[key], cfg, now)
		}
	}
	// An agent taken out of a brain that runs loses its state; its
	// history stays until it ages out. A brain that does not run now
	// keeps its agents' state for when it comes back.
	if err := a.store.Forget(func(key string) bool {
		brain, _, _ := strings.Cut(key, "/")
		return known[key] || !brains[brain]
	}); err != nil {
		log.Printf("scheduled agents: %v", err)
	}
}

// consider starts agent s of br if it fell due, and moves its anchor on.
func (a *agents) consider(br *brainruntime.Brain, s manifest.Schedule, st sched.AgentState, cfg config.SchedulesSettings, now time.Time) {
	id := br.ID()
	sc, err := s.Parsed(now.Location())
	if err != nil {
		return // Validate refused it already
	}
	last := st.Last
	if last.IsZero() && s.Name == manifest.ConsolidationSchedule {
		// compat: the consolidation's last run, as the bridge kept it
		// before scheduled agents.
		last, _ = a.b.intents.JobLast(id, manifest.ConsolidationSchedule)
	}
	d := sched.Decide(sc, last, sched.Offset(id, s.Name, jitterOf(s, cfg)), s.MissedMode() == manifest.MissedSkip, now)
	if !d.Last.Equal(st.Last) {
		if err := a.store.Update(id, s.Name, func(as *sched.AgentState) bool { as.Last = d.Last; return true }); err != nil {
			log.Printf("brain %s: scheduled agent %s: %v", id, s.Name, err)
			return
		}
	}
	switch {
	case !d.Run && !d.Skipped:
		return
	case !cfg.Enabled || st.Paused || !s.IsEnabled():
		return
	case d.Skipped:
		log.Printf("brain %s: scheduled agent %s fell due at %s while the bridge was down; its missed setting skips it", id, s.Name, d.Last.Format(time.RFC3339))
		r := sched.Run{ID: sched.NewRunID(now), Brain: id, Agent: s.Name, Kind: runKind(s), Trigger: sched.TriggerMissed, Started: now, Ended: now, Status: sched.StatusSkipped,
			Result: "fell due at " + d.Last.Format("2006-01-02 15:04") + " while the bridge was down"}
		if err := a.store.Finish(r); err != nil {
			log.Printf("brain %s: scheduled agent %s: %v", id, s.Name, err)
		}
		return
	}
	trigger := sched.TriggerSchedule
	if d.Late {
		trigger = sched.TriggerMissed
	}
	if _, err := a.start(br, s, trigger, ""); err != nil {
		log.Printf("brain %s: scheduled agent %s did not start: %v", id, s.Name, err)
	}
}

func runKind(s manifest.Schedule) string {
	if s.Command != nil {
		return sched.KindCommand
	}
	return sched.KindBrain
}

// errAgentRunning is a start of an agent whose run is not over.
var errAgentRunning = apperr.New(apperr.Conflict, "this agent's last run is still going: an agent never runs twice at once")

// start starts a run of agent s of br, for trigger, by whoever asked
// for a manual one, and returns it as it starts.
func (a *agents) start(br *brainruntime.Brain, s manifest.Schedule, trigger, by string) (sched.Run, error) {
	return a.startWith(br, s, trigger, by, nil)
}

// startWith is start, with FrIDE's news for a TriggerFride run; when the
// agent runs already, the news waits for its next run.
func (a *agents) startWith(br *brainruntime.Brain, s manifest.Schedule, trigger, by string, fride []string) (sched.Run, error) {
	key := sched.Key(br.ID(), s.Name)
	now := time.Now()
	r := sched.Run{ID: sched.NewRunID(now), Brain: br.ID(), Agent: s.Name, Kind: runKind(s), Trigger: trigger, By: by, Started: now, Status: sched.StatusRunning}
	cfg := a.b.cfgs.Get()
	ar := &agentRun{run: r, agent: s, chat: a.resultChat(br, s, cfg), fride: fride}
	a.mu.Lock()
	if a.running[key] != nil {
		if len(fride) > 0 {
			a.frideWaiting[key] = append(fride, a.frideWaiting[key]...)
		}
		a.mu.Unlock()
		return sched.Run{}, errAgentRunning
	}
	a.running[key], a.byID[r.ID] = ar, ar
	a.mu.Unlock()

	lg, err := a.store.OpenLog(br.ID(), s.Name, r.ID, int64(cfg.Live.Schedules.MaxLogKB)<<10)
	if err != nil {
		log.Printf("brain %s: scheduled agent %s: its log: %v", br.ID(), s.Name, err)
	}
	ar.log = lg
	if err := a.store.Begin(r); err != nil {
		log.Printf("brain %s: scheduled agent %s: %v", br.ID(), s.Name, err)
	}
	what := "a brain run"
	if s.Command != nil {
		what = "a program on node " + s.Command.Node
	}
	lg.Line(fmt.Sprintf("Scheduled agent %s of brain %s (%s), %s; started by %s%s", s.Name, br.ID(), s.When, what, trigger, map[bool]string{true: ", " + by, false: ""}[by != ""]))
	log.Printf("brain %s: scheduled agent %s starts (%s, run %s)", br.ID(), s.Name, trigger, r.ID)
	if s.Command != nil {
		go a.runCommand(ar)
		return r, nil
	}
	if err := a.startBrainRun(br, ar); err != nil {
		a.finish(ar, sched.StatusFailed, "", err.Error(), claudecli.Usage{}, a.failureNotice(ar, err.Error()))
	}
	return r, nil
}

// resultChat is where s's results go: its chat, else the brain's reply
// chat; "" for a silent one.
func (a *agents) resultChat(br *brainruntime.Brain, s manifest.Schedule, cfg *config.Settings) string {
	if s.ReportMode() == manifest.ReportSilent {
		return ""
	}
	if s.Chat != "" {
		return s.Chat
	}
	return a.b.replyChatFor(br, cfg)
}

// newsChat is the chat a run's FrIDE thread or workstation session sends
// its questions and news to (docs/BRAINS.md, "Scheduled agents"): the
// chat the run answers, or for a scheduled agent's run the chat its
// report goes to, else the owner's DM. "" when nobody would hear them:
// an observe-only run, a silent agent, a delegated task's chat.
func (b *App) newsChat(c mcpCaller) string {
	if c.run.Policy != nil && c.run.Policy.Silent {
		return ""
	}
	jid := c.run.Chat
	if !chat.IsScheduleChat(jid) {
		if jid == "" || chat.IsInternalChat(jid) {
			return ""
		}
		return jid
	}
	ar := b.agents.lookup(c.run.Message.ID)
	if ar == nil || ar.agent.ReportMode() == manifest.ReportSilent {
		return ""
	}
	if ar.chat != "" {
		return ar.chat
	}
	return ownerChat(b.cfgs.Get())
}

// task is the task of brain run agent s of br: its prompt, its prompt
// file or its template, with its vars filled in.
func (a *agents) task(br *brainruntime.Brain, s manifest.Schedule) (string, error) {
	switch {
	case s.Prompt != "":
		return sched.Render(sched.Template{}, s.Prompt, s.Vars)
	case s.Template != "":
		t, ok := scheduleTemplate(s.Template)
		if !ok {
			return "", fmt.Errorf("there is no built-in template %q", s.Template)
		}
		return sched.Render(t, t.Body, s.Vars)
	}
	file := s.TaskFile()
	// Through the folder, following no symlink, like brain.json: the
	// brain may write the file, but not point it elsewhere.
	data, err := fsx.ReadBrainFile(br.Dir(), file)
	if err != nil {
		return "", fmt.Errorf("its task file %s: %w", file, err)
	}
	t, err := sched.ParseTask(s.Name, string(data))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(t.Body) == "" {
		return "", fmt.Errorf("its task file %s is empty", file)
	}
	return sched.Render(t, t.Body, s.Vars)
}

// startBrainRun hands ar's task to its brain, as a message of its own
// chat, schedule:<agent>: the run goes through the brain's queue, sandbox
// and policy like any other, and its answer comes back to finished.
func (a *agents) startBrainRun(br *brainruntime.Brain, ar *agentRun) error {
	s := ar.agent
	text, err := a.task(br, s)
	if err != nil {
		return err
	}
	ar.risk = skill.Risk(s.Risk)
	if s.TimeoutMinutes > 0 {
		// In run_timeout_minutes' unit, which a test shortens.
		ar.timeout = time.Duration(s.TimeoutMinutes) * a.b.waits.runTimeoutUnit
	}
	report := s.ReportMode()
	if len(ar.fride) > 0 && report != manifest.ReportSilent {
		// FrIDE's news reaches the owner only when it matters.
		report = manifest.ReportWorth
	}
	msg := sched.Message(sched.Task{
		Agent: s.Name, When: s.When, Trigger: ar.run.Trigger, Report: report, Chat: chatLabel(ar.chat),
		Preapproved: s.Preapproved, Text: text, Fride: strings.Join(ar.fride, "\n\n"),
	})
	id := ar.run.ID
	m := brainruntime.Inbound{Inbound: chat.Inbound{
		JID: chat.ScheduleChatPrefix + s.Name, Brain: br.ID(), Sender: intent.SenderSchedule, Text: msg,
		Source: chat.SourceScheduled, At: ar.run.Started, ID: id,
		// After finished has handled the run, this finds it ended; it
		// ends one that never ran: dropped, or its brain removed.
		Done: func(err error) {
			if err == nil {
				err = errors.New("the run ended without an answer")
			}
			a.ended(id, err)
		},
		Log: ar.log.Line,
		// brainRunEnv mints its GitHub token as the run starts
		// (withGitHubToken).
		GitHub: s.GitHub,
		// Its own timeout_minutes, which may also be longer than
		// run_timeout_minutes: the brain's worker sets the run's
		// deadline by it.
		RunTimeout: ar.timeout,
	}}
	if _, _, _, err := a.b.brainMgr.Submit(m); err != nil {
		return fmt.Errorf("the brain did not take the run: %w", err)
	}
	return nil
}

// chatLabel names a chat in a run's message.
func chatLabel(jid string) string {
	if jid == "" {
		return "nobody"
	}
	if chat.IsGroupJID(jid) {
		return "the WhatsApp group " + jid
	}
	return "the owner's chat (" + jid + ")"
}

// lookup returns the run in progress whose id a run's message carries.
func (a *agents) lookup(id string) *agentRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byID[id]
}

// take ends run id in the books, once, and returns it; nil when it has
// ended already.
func (a *agents) take(id string) *agentRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	ar := a.byID[id]
	if ar == nil {
		return nil
	}
	delete(a.byID, id)
	delete(a.running, sched.Key(ar.run.Brain, ar.run.Agent))
	return ar
}

// ended ends a brain run that finished did not: dropped before it ran,
// or its brain gone.
func (a *agents) ended(id string, err error) {
	ar := a.take(id)
	if ar == nil {
		return
	}
	status := sched.StatusFailed
	if errors.Is(err, brainruntime.ErrDropped) || errors.Is(err, brainruntime.ErrStopped) {
		status = sched.StatusDropped
	}
	a.complete(ar, status, "", err.Error(), claudecli.Usage{}, "")
}

// finished takes a scheduled agent's finished brain run, in place of
// delivery to a chat: the result goes where the agent's report setting
// says, and into its history and log.
func (a *agents) finished(br *brainruntime.Brain, m brainruntime.Inbound, res claudecli.Result) {
	ar := a.take(m.ID)
	if ar == nil {
		log.Printf("brain %s: a scheduled run %s finished that the bridge has no record of", br.ID(), m.ID)
		return
	}
	if res.Err == nil && res.Text != "" {
		// In the agent's own chat, so that its next run sees what it
		// answered before.
		br.AppendEntry(history.Entry{From: br.ID(), Chat: m.JID, Text: res.Text, Type: history.Out})
	}
	s := ar.agent
	switch {
	case res.Err != nil:
		status, why := sched.StatusFailed, runFailure(res.Err)
		if refused, ok := errors.AsType[*brainruntime.RunRefused](res.Err); ok {
			status, why = sched.StatusRefused, "the policy did not start it: "+refused.Reason
		} else if errors.Is(res.Err, claudecli.ErrInterrupted) || errors.Is(res.Err, context.Canceled) {
			status, why = sched.StatusStopped, "it was stopped"
		}
		notice := ""
		if status != sched.StatusStopped {
			notice = a.failureNotice(ar, why)
		}
		// The whole error, the CLI's stderr included, goes to the log
		// only, which the dashboard shows the owner.
		ar.log.Line("Error: " + res.Err.Error())
		if res.LastText != "" {
			// What it wrote before the error, its summary often: kept in
			// the log and the run's history, not sent.
			ar.log.Line("Its last text before the error:\n" + res.LastText)
			log.Printf("brain %s: scheduled agent %s: run %s ended in an error after it wrote %d characters, kept in its history", br.ID(), s.Name, ar.run.ID, len(res.LastText))
		}
		a.complete(ar, status, res.LastText, why, res.Usage, notice)
	case s.ReportMode() == manifest.ReportSilent:
		a.complete(ar, sched.StatusOK, res.Text, "", res.Usage, "")
	case (s.ReportMode() == manifest.ReportWorth || len(ar.fride) > 0) && !sched.Worth(res.Text):
		a.complete(ar, sched.StatusNothing, res.Text, "", res.Usage, "")
	case strings.TrimSpace(res.Text) == "":
		// Nothing to send: said in the log, so that a lost report shows.
		ar.log.Line("The run ended without an answer to send")
		log.Printf("brain %s: scheduled agent %s: run %s ended without an answer to send", br.ID(), s.Name, ar.run.ID)
		a.complete(ar, sched.StatusOK, "", "", res.Usage, "")
	default:
		a.deliver(ar, br.ID(), res.Text)
		a.complete(ar, sched.StatusOK, res.Text, "", res.Usage, "")
	}
}

// runFailure says why a run failed, without the CLI's stderr, which can
// hold anything: it goes to a chat.
func runFailure(err error) string {
	var ce *claudecli.Error
	switch {
	case errors.Is(err, claudecli.ErrTimedOut), errors.Is(err, context.DeadlineExceeded):
		return "the run took too long and was stopped"
	case errors.Is(err, brainruntime.ErrSandboxUnavailable):
		return "the sandbox could not be set up"
	case errors.Is(err, claudecli.ErrNoResult):
		return "the agent gave no answer"
	case errors.As(err, &ce):
		return claudecli.AgentName(ce.Provider) + " said: " + textx.Truncate(ce.Message, 200)
	}
	return "the run failed"
}

// failureNotice is what the chat is told of ar's failure, "" when it is
// told nothing.
func (a *agents) failureNotice(ar *agentRun, why string) string {
	if ar.chat == "" {
		return ""
	}
	return outboundmsg.ScheduleFailedNotice(ar.agent.Name, ar.run.Brain, why)
}

// deliver sends text, ar's result, to its chat, in the brain's name: its
// history records it there once it is sent.
func (a *agents) deliver(ar *agentRun, brain, text string) {
	if ar.chat == "" {
		ar.log.Line("Not sent: the agent reports to no chat")
		return
	}
	if err := a.b.pipeline.Reply(context.Background(), brain, ar.chat, text, a.b.cfgs.Get().MaxChunkSize); err != nil {
		log.Printf("brain %s: scheduled agent %s: sending its result: %v", brain, ar.agent.Name, err)
		ar.log.Line("The result could not be sent: " + err.Error())
		return
	}
	ar.run.Sent, ar.run.SentTo = true, ar.chat
	ar.log.Line("Sent to " + ar.chat)
}

// finish is complete for a run that take has not ended yet.
func (a *agents) finish(ar *agentRun, status, result, why string, usage claudecli.Usage, notice string) {
	if a.take(ar.run.ID) == nil {
		return
	}
	a.complete(ar, status, result, why, usage, notice)
}

// complete records how ar ended, tells its chat notice when there is one,
// and keeps the history within its limits.
func (a *agents) complete(ar *agentRun, status, result, why string, usage claudecli.Usage, notice string) {
	r := &ar.run
	r.Ended = time.Now()
	r.DurationMS = r.Ended.Sub(r.Started).Milliseconds()
	r.Status, r.Result, r.Error = status, textx.Truncate(strings.TrimSpace(result), 2000), why
	r.CostUSD, r.InputTokens, r.OutputTokens = usage.CostUSD, usage.InputTokens, usage.OutputTokens
	if notice != "" {
		if err := a.b.pipeline.Notice(context.Background(), ar.chat, notice); err != nil {
			log.Printf("brain %s: scheduled agent %s: telling the chat it failed: %v", r.Brain, r.Agent, err)
		} else {
			r.Sent, r.SentTo = true, ar.chat
		}
	}
	line := fmt.Sprintf("■ %s after %s", status, (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second))
	if why != "" {
		line += ": " + why
	}
	ar.log.Line(line)
	_ = ar.log.Close()
	if err := a.store.Finish(*r); err != nil {
		log.Printf("brain %s: scheduled agent %s: %v", r.Brain, r.Agent, err)
	}
	log.Printf("brain %s: scheduled agent %s: run %s %s", r.Brain, r.Agent, r.ID, status)
	if _, err := a.prune(time.Now()); err != nil {
		log.Printf("scheduled agents: keeping the history within its limits: %v", err)
	}
	a.mu.Lock()
	waiting := len(a.frideWaiting[sched.Key(r.Brain, r.Agent)]) > 0
	a.mu.Unlock()
	if waiting {
		go a.frideRun(r.Brain, r.Agent)
	}
}

// frideNews hands FrIDE's news about work agent of brain handed it to a
// run of that agent: at once, or after the run in progress, together
// with whatever else arrives until then.
func (a *agents) frideNews(brain, agent, news string) {
	key := sched.Key(brain, agent)
	a.mu.Lock()
	a.frideWaiting[key] = append(a.frideWaiting[key], news)
	running := a.running[key] != nil
	a.mu.Unlock()
	if !running {
		a.frideRun(brain, agent)
	}
}

// frideRun starts a run of agent of brain for the FrIDE news waiting for
// it. An agent that is gone, paused, turned off or runs a program drops
// it: the news stays on the dashboard's FrIDE page.
func (a *agents) frideRun(brain, agent string) {
	key := sched.Key(brain, agent)
	a.mu.Lock()
	news := a.frideWaiting[key]
	if len(news) == 0 || a.running[key] != nil {
		a.mu.Unlock()
		return
	}
	delete(a.frideWaiting, key)
	a.mu.Unlock()
	br := a.b.brainMgr.Get(brain)
	var s *manifest.Schedule
	if br != nil {
		for _, sc := range br.Manifest().EffectiveSchedules() {
			if sc.Name == agent {
				s = &sc
				break
			}
		}
	}
	states, _ := a.store.States()
	switch {
	case s == nil:
		log.Printf("brain %s: FrIDE's news for scheduled agent %s, which it no longer has: dropped", brain, agent)
		return
	case s.Command != nil || !s.IsEnabled() || states[key].Paused:
		log.Printf("brain %s: FrIDE's news for scheduled agent %s, which is paused, off or runs a program: dropped", brain, agent)
		return
	}
	if _, err := a.startWith(br, *s, sched.TriggerFride, "", news); err != nil && !errors.Is(err, errAgentRunning) {
		log.Printf("brain %s: scheduled agent %s did not start for FrIDE's news: %v", brain, agent, err)
	}
}

// prune keeps the history and logs within schedules.keep_days, keep_runs
// and max_total_mb (the housekeeping rule), and returns how many runs it
// dropped. The runs in progress are never among them.
func (a *agents) prune(now time.Time) (int, error) {
	cfg := a.b.cfgs.Get().Live.Schedules
	listing, err := a.store.Listing()
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	runs := map[string][]housekeeping.ScheduleRun{}
	for key, files := range listing {
		for _, f := range files {
			if a.byID[f.ID] == nil {
				runs[key] = append(runs[key], housekeeping.ScheduleRun{Agent: key, ID: f.ID, Started: f.Started, Bytes: f.Bytes})
			}
		}
	}
	a.mu.Unlock()
	drop := housekeeping.ScheduleRunsToDrop(runs, housekeeping.ScheduleRetention{
		KeepDays: cfg.KeepDays, KeepRuns: cfg.KeepRuns, MaxTotalBytes: int64(cfg.MaxTotalMB) << 20,
	}, now)
	if len(drop) == 0 {
		return 0, nil
	}
	files := make([]sched.RunFile, 0, len(drop))
	for _, d := range drop {
		brain, agent, _ := strings.Cut(d.Agent, "/")
		files = append(files, sched.RunFile{Brain: brain, Agent: agent, ID: d.ID})
	}
	return len(drop), a.store.Drop(files)
}

// runCommand runs ar's program on its node, through the node's exec: the
// node's allowlist and the core's approval of its commands apply, and a
// lockdown refuses it, as it refuses every exec.
func (a *agents) runCommand(ar *agentRun) {
	c := ar.agent.Command
	if a.b.locked() {
		a.commandEnded(ar, "", sched.StatusRefused, "the bridge is in lockdown, which refuses a node's exec", nil, "")
		return
	}
	node, err := a.nodeID(c.Node)
	if err != nil {
		a.commandEnded(ar, c.Node, sched.StatusFailed, err.Error(), nil, "")
		return
	}
	a.mu.Lock() // list copies the run under it
	ar.run.Node = node
	a.mu.Unlock()
	params := nodeproto.ExecParams{Argv: c.Argv, Dir: c.Dir}
	if t := ar.agent.TimeoutMinutes; t > 0 {
		params.TimeoutSeconds = t * 60
	}
	ar.log.Line(fmt.Sprintf("$ %s (on node %s)", strings.Join(c.Argv, " "), node))
	ctx := context.Background()
	res, err := a.b.nodes.Exec(ctx, node, params)
	if err != nil {
		a.commandEnded(ar, node, sched.StatusFailed, err.Error(), nil, "")
		return
	}
	for _, out := range []struct{ name, text string }{{"stdout", res.Stdout}, {"stderr", res.Stderr}} {
		if out.text != "" {
			ar.log.Line(out.name + ":\n" + out.text)
		}
	}
	if res.Truncated {
		ar.log.Line("(the node cut the output at its max_output_bytes)")
	}
	output := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	exit := res.ExitCode
	switch {
	case res.TimedOut:
		a.commandEnded(ar, node, sched.StatusFailed, "it took too long and the node stopped it", &exit, output)
	case exit != 0:
		a.commandEnded(ar, node, sched.StatusFailed, fmt.Sprintf("it exited with %d", exit), &exit, output)
	default:
		a.commandEnded(ar, node, sched.StatusOK, "", &exit, output)
	}
}

// nodeID is the id of the node ref names, by id or by name.
func (a *agents) nodeID(ref string) (string, error) {
	views, err := a.b.nodes.Views()
	if err != nil {
		return "", err
	}
	for _, v := range views {
		if v.ID == ref || strings.EqualFold(v.Name, ref) {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("there is no node %q (fridolin nodes lists them)", ref)
}

// commandEnded records how ar's program ended, in the audit log too, and
// tells its chat as its report setting says.
func (a *agents) commandEnded(ar *agentRun, node, status, why string, exit *int, output string) {
	if a.take(ar.run.ID) == nil {
		return
	}
	ar.run.ExitCode = exit
	outcome := status
	if why != "" {
		outcome += ": " + why
	}
	a.b.audit.Append(audit.Entry{Kind: audit.ScheduledCommand, Brain: ar.run.Brain, ID: ar.run.ID,
		Detail: fmt.Sprintf("%s on node %s: %s", ar.agent.Name, cmpOr(node, ar.agent.Command.Node), ar.agent.Command.Argv[0]), Outcome: outcome,
		DurationMS: time.Since(ar.run.Started).Milliseconds()})
	notice := ""
	switch {
	case status != sched.StatusOK:
		notice = a.failureNotice(ar, why)
	case ar.agent.ReportMode() == manifest.ReportAlways && ar.chat != "":
		code := 0
		if exit != nil {
			code = *exit
		}
		notice = outboundmsg.ScheduleCommandResult(ar.agent.Name, cmpOr(node, ar.agent.Command.Node), code, output)
	}
	a.complete(ar, status, textx.Tail(output, 2000), why, claudecli.Usage{}, notice)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// admitScheduled is admitRun for a scheduled agent's run: its risk
// budget is the agent's risk, else what its task's skill triggers say,
// and it runs with its own timeout, effort, tools and secrets. In a lockdown, an agent that declares more than reading is
// refused, as a skill that writes is.
func (a *agents) admitScheduled(p *runPolicy, msg chat.Inbound) {
	ar := a.lookup(msg.ID)
	if ar == nil {
		return
	}
	p.Scheduled = ar.agent.Name
	if ar.risk != "" {
		p.Risk = ar.risk
	}
	p.Timeout = ar.timeout
	p.Effort = ar.agent.Effort
	p.DisallowedTools = ar.agent.DisallowedTools
}

// declaresRisk reports whether the scheduled run id's agent sets its
// risk, which a lockdown then holds it to.
func (a *agents) declaresRisk(id string) bool {
	ar := a.lookup(id)
	return ar != nil && ar.agent.Risk != ""
}
