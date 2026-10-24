package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/sched"
)

// scheduleView is one scheduled agent as GET /api/schedules lists it.
type scheduleView struct {
	Brain     string `json:"brain"`
	BrainName string `json:"brain_name"`
	Name      string `json:"name"`
	// Kind is brain or command; Task says what it does, in a few words.
	Kind string `json:"kind"`
	Task string `json:"task"`
	// Report is the report setting with its default, Chat where the
	// results go ("" for nowhere).
	Report string `json:"report"`
	Chat   string `json:"chat"`
	// State is what the Schedules page shows it as: AgentPending,
	// AgentActive, AgentPaused, AgentFailing or AgentDisabled.
	State string `json:"state"`
	// Enabled is the schedule's setting, Paused the dashboard's pause,
	// Running a run in progress.
	Enabled bool `json:"enabled"`
	Paused  bool `json:"paused"`
	Running bool `json:"running"`
	// FromMemory marks the agent memory.consolidate stands for.
	FromMemory bool `json:"from_memory,omitempty"`
	// Next is when it runs next on its schedule; nil when it does not
	// (pending, paused, disabled, schedules.enabled off, or never again).
	Next *time.Time `json:"next"`
	// LastRun is its latest run, a running one included.
	LastRun *sched.Run `json:"last_run"`
	// Problem says why it cannot run as it is (a template that is not
	// there, a task file that is missing, a file that cannot be read).
	Problem string `json:"problem,omitempty"`
	// File is where it is defined, relative to the brain folder: its
	// .brain/schedules/<name>.json, brain.json for one still listed
	// there, "" for the one memory.consolidate stands for.
	File string `json:"file"`
	// Risky says, in plain words, what it may do that deserves a careful
	// look before it is approved: run a program on a node, take
	// irreversible steps, write to GitHub, use every tool.
	Risky []string `json:"risky"`
	// Review is its change on disk that waits for the owner's approval:
	// new (State is AgentPending), changed, removed or invalid. The
	// approved version runs until then.
	Review *manifest.ScheduleReview `json:"review,omitempty"`
	// Schedule is its definition, as the edit form takes it: the approved
	// one, or, for a pending agent, the one on disk.
	Schedule manifest.Schedule `json:"schedule"`
}

// The states of an agent (scheduleView.State).
const (
	// AgentPending waits for the owner's first approval; it never ran.
	AgentPending  = "pending"
	AgentActive   = "active"
	AgentPaused   = "paused"
	AgentFailing  = "failing"
	AgentDisabled = "disabled"
)

// reminderView is a reminder with a time that a brain keeps: a standing
// intent the bridge fires, or one in the brain's own reminders file
// (brainReminderFiles). The Schedules page lists them beside the agents.
type reminderView struct {
	Brain string     `json:"brain"`
	ID    string     `json:"id"`
	Text  string     `json:"text"`
	When  string     `json:"when"`
	Chat  string     `json:"chat"`
	Next  *time.Time `json:"next"`
	// Created is when it was set; zero when the file does not say.
	Created time.Time `json:"created,omitzero"`
	// Source is "bridge" for a standing intent, which the dashboard can
	// remove, else the brain's file that holds it, which the brain keeps.
	Source string `json:"source"`
}

// schedulesView answers GET /api/schedules.
type schedulesView struct {
	// Enabled is schedules.enabled: off, no agent runs on its schedule.
	Enabled bool `json:"enabled"`
	// Pending counts the agents whose definition on disk waits for the
	// owner's approval, new or changed.
	Pending   int              `json:"pending"`
	Agents    []scheduleView   `json:"agents"`
	Reminders []reminderView   `json:"reminders"`
	Templates []sched.Template `json:"templates"`
}

// taskOf says what s does, in a few words.
func taskOf(s manifest.Schedule) string {
	switch {
	case s.Command != nil && len(s.Command.Argv) > 0:
		return "runs " + s.Command.Argv[0] + " on node " + s.Command.Node
	case s.Template != "":
		return "template " + s.Template
	case s.Prompt != "":
		return "its prompt"
	}
	return "the task in " + s.TaskFile()
}

// risksOf says what s may do that deserves a careful look before the
// owner approves it.
func risksOf(s manifest.Schedule) []string {
	out := []string{}
	if c := s.Command; c != nil {
		out = append(out, fmt.Sprintf("runs a program on node %s: %s", c.Node, strings.Join(c.Argv, " ")))
		return out
	}
	if s.Risk == "irreversible" {
		out = append(out, "may take irreversible steps (risk budget irreversible)")
	}
	if g := s.GitHub; g != nil {
		var writes []string
		for _, p := range slices.Sorted(maps.Keys(g.Permissions)) {
			if g.Permissions[p] == "write" {
				writes = append(writes, p)
			}
		}
		if len(writes) > 0 {
			out = append(out, fmt.Sprintf("can write %s on GitHub in %s", strings.Join(writes, ", "), strings.Join(g.Repos, ", ")))
		}
	}
	if s.DisallowedTools != nil && len(*s.DisallowedTools) == 0 {
		out = append(out, "may use every tool, the brain's blocked tools included")
	}
	return out
}

// problemOf says why s cannot run as it is, "" when nothing stops it.
func (a *agents) problemOf(br *brainruntime.Brain, s manifest.Schedule) string {
	if s.Command != nil {
		return ""
	}
	if _, err := a.task(br, s); err != nil {
		return err.Error()
	}
	return ""
}

// stateOf is v's state, v being an approved agent.
func stateOf(v scheduleView) string {
	switch {
	case !v.Enabled:
		return AgentDisabled
	case v.Paused:
		return AgentPaused
	case v.Problem != "" || v.LastRun != nil && slices.Contains([]string{sched.StatusFailed, sched.StatusRefused}, v.LastRun.Status):
		return AgentFailing
	}
	return AgentActive
}

// list answers GET /api/schedules.
func (a *agents) list(context.Context) (any, error) {
	cfg := a.b.cfgs.Get()
	states, err := a.store.States()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := schedulesView{Enabled: cfg.Live.Schedules.Enabled, Agents: []scheduleView{}, Reminders: []reminderView{}, Templates: scheduleTemplates}
	for _, br := range a.b.brainMgr.Brains() {
		m := br.Manifest()
		reviews := map[string]manifest.ScheduleReview{}
		for _, r := range a.b.registry.ScheduleReviews(cfg, br.ID()) {
			reviews[r.Name] = r
		}
		for _, s := range m.EffectiveSchedules() {
			key := sched.Key(br.ID(), s.Name)
			st := states[key]
			v := scheduleView{
				Brain: br.ID(), BrainName: m.Name, Name: s.Name, Kind: runKind(s), Task: taskOf(s),
				Report: s.ReportMode(), Chat: a.resultChat(br, s, cfg), Enabled: s.IsEnabled(), Paused: st.Paused,
				FromMemory: s.FromMemory, LastRun: st.LastRun, Problem: a.problemOf(br, s), Schedule: s,
				File: manifest.ScheduleFile(s.Name), Risky: risksOf(s),
			}
			if s.FromMemory {
				v.File = ""
			}
			if r, ok := reviews[s.Name]; ok {
				v.Review = &r
				if r.Source != "" && r.Status != manifest.ReviewRemoved {
					v.File = r.Source
				}
				delete(reviews, s.Name)
				if r.Status != manifest.ReviewInvalid {
					out.Pending++
				}
			}
			a.mu.Lock()
			if ar := a.running[key]; ar != nil {
				v.Running = true
				r := ar.run
				v.LastRun = &r
			}
			a.mu.Unlock()
			v.State = stateOf(v)
			if sc, err := s.Parsed(now.Location()); err == nil && v.Enabled && !v.Paused && out.Enabled {
				last := st.Last
				if last.IsZero() {
					last = now
				}
				if next := sched.NextAfter(sc, last, sched.Offset(br.ID(), s.Name, jitterOf(s, cfg.Live.Schedules))); !next.IsZero() {
					v.Next = &next
				}
			}
			out.Agents = append(out.Agents, v)
		}
		// What is on disk and never was approved.
		for _, r := range reviews {
			s := manifest.Schedule{Name: r.Name}
			if r.Disk != nil {
				s = *r.Disk
			}
			v := scheduleView{
				Brain: br.ID(), BrainName: m.Name, Name: r.Name, Kind: runKind(s), Task: taskOf(s),
				Report: s.ReportMode(), Chat: a.resultChat(br, s, cfg), State: AgentPending, Enabled: s.IsEnabled(),
				Problem: r.Detail, File: r.Source, Risky: risksOf(s), Review: &r, Schedule: s,
			}
			if r.Disk != nil && v.Problem == "" {
				v.Problem = a.problemOf(br, s)
			}
			if r.Disk == nil {
				v.Task = "cannot be read"
			}
			out.Agents = append(out.Agents, v)
			if r.Status != manifest.ReviewInvalid {
				out.Pending++
			}
		}
		out.Reminders = append(out.Reminders, a.reminders(br, now)...)
	}
	slices.SortFunc(out.Agents, func(x, y scheduleView) int { return cmp.Or(cmp.Compare(x.Brain, y.Brain), cmp.Compare(x.Name, y.Name)) })
	slices.SortFunc(out.Reminders, func(x, y reminderView) int {
		switch {
		case x.Next == nil && y.Next == nil:
			return cmp.Compare(x.ID, y.ID)
		case x.Next == nil:
			return 1
		case y.Next == nil:
			return -1
		}
		return cmp.Or(x.Next.Compare(*y.Next), cmp.Compare(x.ID, y.ID))
	})
	return out, nil
}

// reminders are br's reminders with a time: its standing intents and
// those in its own reminders files.
func (a *agents) reminders(br *brainruntime.Brain, now time.Time) []reminderView {
	var out []reminderView
	if intents, err := a.b.intents.List(br.ID()); err == nil {
		for _, in := range intents {
			if in.Schedule == nil {
				continue
			}
			rv := reminderView{Brain: br.ID(), ID: in.ID, Text: in.Text, When: in.Schedule.String(), Chat: in.Chat, Created: in.Created, Source: reminderSourceBridge}
			last := in.LastFired
			if last.IsZero() {
				last = in.Created
			}
			if next, ok := in.Schedule.Next(last); ok {
				rv.Next = &next
			}
			out = append(out, rv)
		}
	}
	return append(out, brainFileReminders(br.ID(), br.Dir(), now.Location())...)
}

// agent finds brain's agent name, among those that run.
func (a *agents) agent(brain, name string) (*brainruntime.Brain, manifest.Schedule, error) {
	br := a.b.brainMgr.Get(brain)
	if br == nil {
		return nil, manifest.Schedule{}, apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q runs", brain))
	}
	s, ok := br.Manifest().ScheduleNamed(name)
	if !ok {
		return nil, manifest.Schedule{}, apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no scheduled agent %q", brain, name))
	}
	return br, s, nil
}

// runNow answers POST /api/schedules/{brain}/{name}/run.
func (a *agents) runNow(_ context.Context, brain, name, by string) (any, error) {
	br, s, err := a.agent(brain, name)
	if err != nil {
		return nil, err
	}
	return a.start(br, s, sched.TriggerManual, by)
}

// setPaused answers POST /api/schedules/{brain}/{name}/pause and /resume.
func (a *agents) setPaused(_ context.Context, brain, name string, paused bool) (any, error) {
	if _, _, err := a.agent(brain, name); err != nil {
		return nil, err
	}
	changed := false
	err := a.store.Update(brain, name, func(st *sched.AgentState) bool {
		if st.Paused == paused {
			return false
		}
		st.Paused, changed = paused, true
		st.PausedAt = time.Time{}
		if paused {
			st.PausedAt = time.Now()
		}
		return true
	})
	return map[string]any{"paused": paused, "changed": changed}, err
}

// runs answers GET /api/schedules/{brain}/{name}/runs: its runs, newest
// first, the one in progress included.
func (a *agents) runs(_ context.Context, brain, name string, limit int) (any, error) {
	if !manifest.ValidID(brain) || !manifest.ValidScheduleName(name) {
		return nil, apperr.New(apperr.NotFound, "no such scheduled agent")
	}
	runs, err := a.store.Runs(brain, name, limit)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if ar := a.running[sched.Key(brain, name)]; ar != nil {
		runs = append([]sched.Run{ar.run}, runs...)
	}
	a.mu.Unlock()
	return runs, nil
}

// maxLogRead is the most of a run's log the dashboard is sent.
const maxLogRead = 4 << 20

// runLog answers GET /api/schedules/{brain}/{name}/runs/{run}/log.
func (a *agents) runLog(_ context.Context, brain, name, id string) (any, error) {
	if !manifest.ValidID(brain) || !manifest.ValidScheduleName(name) || !sched.ValidRunID(id) {
		return nil, apperr.New(apperr.NotFound, "no such run")
	}
	text, cut, err := a.store.ReadLog(brain, name, id, maxLogRead)
	if errors.Is(err, sched.ErrNotFound) {
		return nil, apperr.New(apperr.NotFound, "no log of run "+id+": it aged out, or never started")
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": id, "log": text, "cut": cut, "running": false}
	a.mu.Lock()
	if ar := a.byID[id]; ar != nil {
		out["running"] = true
	}
	a.mu.Unlock()
	return out, nil
}

// put answers PUT /api/schedules/{brain}/{name}: the owner's edit, or a
// new agent, written into its file and the approved copy at once, so it
// needs no approval after. The agent memory.consolidate stands for moves
// into a file of its own.
func (a *agents) put(_ context.Context, brain, name string, s manifest.Schedule) (any, error) {
	if s.Name == "" {
		s.Name = name
	}
	if s.Name != name {
		return nil, apperr.New(apperr.Invalid, fmt.Sprintf("the agent's name is %q in the address and %q in the body: a name cannot change; add it anew and remove the old one", name, s.Name))
	}
	s.When = strings.TrimSpace(s.When)
	if err := s.Validate(); err != nil {
		return nil, apperr.Wrap(apperr.Invalid, err)
	}
	if err := checkTemplate(s); err != nil {
		return nil, err
	}
	if err := a.b.registry.PutSchedule(brain, s); err != nil {
		return nil, err
	}
	return httpapi.OperationResponse{Status: "saved"}, nil
}

// checkTemplate refuses an agent of a template the bridge does not ship.
func checkTemplate(s manifest.Schedule) error {
	if s.Template != "" {
		if _, ok := scheduleTemplate(s.Template); !ok {
			return apperr.New(apperr.Invalid, fmt.Sprintf("there is no built-in template %q", s.Template))
		}
	}
	return nil
}

// approve answers POST /api/schedules/{brain}/{name}/approve: the owner
// approves the agent's file as they reviewed it, reviewed nil being its
// removal.
func (a *agents) approve(_ context.Context, brain, name string, reviewed *manifest.Schedule) (any, error) {
	if reviewed != nil {
		if err := checkTemplate(*reviewed); err != nil {
			return nil, err
		}
	}
	if err := a.b.registry.ApproveSchedule(brain, name, reviewed); err != nil {
		return nil, err
	}
	return httpapi.OperationResponse{Status: "approved"}, nil
}

// remove answers DELETE /api/schedules/{brain}/{name}: its file and its
// approved copy go. Its history and logs stay until they age out.
func (a *agents) remove(_ context.Context, brain, name string) (any, error) {
	if err := a.b.registry.RemoveSchedule(brain, name); err != nil {
		return nil, err
	}
	return httpapi.OperationResponse{Status: "removed"}, nil
}

// schedulePreview answers GET /api/schedules/preview: when the schedule
// when would run next, in the bridge's time zone, without jitter.
type schedulePreview struct {
	When  string      `json:"when"`
	Next  []time.Time `json:"next"`
	Error string      `json:"error,omitempty"`
}

func (a *agents) preview(_ context.Context, when string) (any, error) {
	out := schedulePreview{When: strings.TrimSpace(when), Next: []time.Time{}}
	now := time.Now()
	sc, err := (manifest.Schedule{When: out.When}).Parsed(now.Location())
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	t := now
	for range 3 {
		n, ok := sc.Next(t)
		if !ok {
			break
		}
		out.Next = append(out.Next, n)
		t = n
	}
	return out, nil
}

func (b *App) newSchedulesAPI() *httpapi.SchedulesAPI {
	a := b.agents
	return &httpapi.SchedulesAPI{
		List: a.list, Run: a.runNow, Pause: a.setPaused, Runs: a.runs, Log: a.runLog,
		Put: a.put, Remove: a.remove, Approve: a.approve, Preview: a.preview,
	}
}
