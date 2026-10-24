package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
	// Enabled is the schedule's setting, Paused the dashboard's pause,
	// Running a run in progress.
	Enabled bool `json:"enabled"`
	Paused  bool `json:"paused"`
	Running bool `json:"running"`
	// FromMemory marks the agent memory.consolidate stands for.
	FromMemory bool `json:"from_memory,omitempty"`
	// Next is when it runs next on its schedule; nil when it does not
	// (paused, disabled, schedules.enabled off, or never again).
	Next *time.Time `json:"next"`
	// LastRun is its latest run, a running one included.
	LastRun *sched.Run `json:"last_run"`
	// Problem says why it cannot run as it is (a template that is not
	// there, a task file that is missing).
	Problem string `json:"problem,omitempty"`
	// Schedule is its entry in brain.json, as the edit form takes it.
	Schedule manifest.Schedule `json:"schedule"`
}

// reminderView is a brain's standing intent with a time: what the
// Schedules page lists beside the agents.
type reminderView struct {
	Brain   string     `json:"brain"`
	ID      string     `json:"id"`
	Text    string     `json:"text"`
	When    string     `json:"when"`
	Chat    string     `json:"chat"`
	Next    *time.Time `json:"next"`
	Created time.Time  `json:"created"`
}

// schedulesView answers GET /api/schedules.
type schedulesView struct {
	// Enabled is schedules.enabled: off, no agent runs on its schedule.
	Enabled   bool             `json:"enabled"`
	Agents    []scheduleView   `json:"agents"`
	Reminders []reminderView   `json:"reminders"`
	Templates []sched.Template `json:"templates"`
}

// taskOf says what s does, in a few words.
func taskOf(s manifest.Schedule) string {
	switch {
	case s.Command != nil:
		return "runs " + s.Command.Argv[0] + " on node " + s.Command.Node
	case s.Template != "":
		return "template " + s.Template
	case s.Prompt != "":
		return "its prompt"
	}
	return "the task in " + s.TaskFile()
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
		for _, s := range m.EffectiveSchedules() {
			key := sched.Key(br.ID(), s.Name)
			st := states[key]
			v := scheduleView{
				Brain: br.ID(), BrainName: m.Name, Name: s.Name, Kind: runKind(s), Task: taskOf(s),
				Report: s.ReportMode(), Chat: a.resultChat(br, s, cfg), Enabled: s.IsEnabled(), Paused: st.Paused,
				FromMemory: s.FromMemory, LastRun: st.LastRun, Problem: a.problemOf(br, s), Schedule: s,
			}
			a.mu.Lock()
			if ar := a.running[key]; ar != nil {
				v.Running = true
				r := ar.run
				v.LastRun = &r
			}
			a.mu.Unlock()
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
		intents, err := a.b.intents.List(br.ID())
		if err != nil {
			continue
		}
		for _, in := range intents {
			if in.Schedule == nil {
				continue
			}
			rv := reminderView{Brain: br.ID(), ID: in.ID, Text: in.Text, When: in.Schedule.String(), Chat: in.Chat, Created: in.Created}
			last := in.LastFired
			if last.IsZero() {
				last = in.Created
			}
			if next, ok := in.Schedule.Next(last); ok {
				rv.Next = &next
			}
			out.Reminders = append(out.Reminders, rv)
		}
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
// new agent, written into brain.json and its approved copy at once, as
// every edit in the dashboard is. The agent memory.consolidate stands for
// moves into the schedules list.
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
	if s.Template != "" {
		if _, ok := scheduleTemplate(s.Template); !ok {
			return nil, apperr.New(apperr.Invalid, fmt.Sprintf("there is no built-in template %q", s.Template))
		}
	}
	err := a.b.registry.UpdateManifest(brain, func(m *manifest.Manifest) error {
		m.SetSchedule(s)
		if err := m.Validate(); err != nil {
			return apperr.Wrap(apperr.Invalid, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return httpapi.OperationResponse{Status: "saved"}, nil
}

// remove answers DELETE /api/schedules/{brain}/{name}. Its history and
// logs stay until they age out.
func (a *agents) remove(_ context.Context, brain, name string) (any, error) {
	err := a.b.registry.UpdateManifest(brain, func(m *manifest.Manifest) error {
		if !m.RemoveSchedule(name) {
			return apperr.New(apperr.NotFound, fmt.Sprintf("brain %s has no scheduled agent %q", brain, name))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return httpapi.OperationResponse{Status: "removed"}, nil
}

func (b *App) newSchedulesAPI() *httpapi.SchedulesAPI {
	a := b.agents
	return &httpapi.SchedulesAPI{
		List: a.list, Run: a.runNow, Pause: a.setPaused, Runs: a.runs, Log: a.runLog,
		Put: a.put, Remove: a.remove,
	}
}
