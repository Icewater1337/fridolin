package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/housekeeping"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/sched"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/version"
)

// systemStatus is what system_status answers: compact, and without
// tokens, secrets or message text. Each part comes from what the
// dashboard's page for it computes.
type systemStatus struct {
	At           time.Time       `json:"at"`
	Core         opsCore         `json:"core"`
	Brains       []opsBrain      `json:"brains"`
	Agents       []opsAgent      `json:"scheduled_agents"`
	Housekeeping opsHousekeeping `json:"housekeeping"`
	Backups      opsBackups      `json:"backups"`
	Nodes        []opsNode       `json:"nodes,omitempty"`
	Fride        *opsFride       `json:"fride,omitempty"`
	Plaud        *opsPlaud       `json:"plaud,omitempty"`
	Problems     []string        `json:"problems,omitempty"`
}

type opsCore struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	UptimeS int64     `json:"uptime_s"`
	// DataDirFreeBytes is the space left on the data dir's volume.
	DataDirFreeBytes uint64 `json:"data_dir_free_bytes,omitempty"`
	Lockdown         bool   `json:"lockdown"`
}

// opsRun is a run in flight in a brain's lane. Chat is the kind of chat
// it answers (chatKind), never its JID; Agent the scheduled agent whose
// chat it is, and From the brain that delegated it.
type opsRun struct {
	Chat     string    `json:"chat"`
	Agent    string    `json:"agent,omitempty"`
	From     string    `json:"from,omitempty"`
	Source   string    `json:"source,omitempty"`
	Started  time.Time `json:"started"`
	AgeS     int64     `json:"age_s"`
	TimeoutS int64     `json:"timeout_s,omitempty"`
}

type opsBrain struct {
	ID         string  `json:"id"`
	ChatRun    *opsRun `json:"chat_run"`
	Background *opsRun `json:"background_run"`
	// ChatWaiting and BackgroundWaiting count what waits in each lane.
	ChatWaiting       int  `json:"chat_waiting"`
	BackgroundWaiting int  `json:"background_waiting"`
	LiveSession       bool `json:"live_session"`
	// Last is how its last run ended, in either lane.
	Last *opsLast `json:"last,omitempty"`
}

type opsLast struct {
	// Chat is the kind of chat it answered (chatKind).
	Chat     string    `json:"chat"`
	Finished time.Time `json:"finished"`
	Seconds  float64   `json:"seconds"`
	Outcome  string    `json:"outcome"`
}

type opsAgent struct {
	Brain        string       `json:"brain"`
	Name         string       `json:"name"`
	State        string       `json:"state"`
	Enabled      bool         `json:"enabled"`
	Paused       bool         `json:"paused"`
	Running      bool         `json:"running"`
	RunningSince *time.Time   `json:"running_since,omitempty"`
	LastRun      *opsAgentRun `json:"last_run,omitempty"`
	Next         *time.Time   `json:"next,omitempty"`
	// ConsecutiveFailures counts its last runs that failed or were
	// refused, newest first, up to opsFailureWindow.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// PendingApproval is its change on disk that waits for the owner:
	// new, changed, removed or invalid.
	PendingApproval string `json:"pending_approval,omitempty"`
	Problem         string `json:"problem,omitempty"`
}

type opsAgentRun struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Trigger   string    `json:"trigger"`
	Started   time.Time `json:"started"`
	DurationS int64     `json:"duration_s,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// opsFailureWindow is how many of an agent's runs consecutive_failures
// looks at.
const opsFailureWindow = 10

type opsHousekeeping struct {
	Enabled bool                `json:"enabled"`
	DryRun  bool                `json:"dry_run"`
	Running bool                `json:"running"`
	NextRun string              `json:"next_run,omitempty"`
	Last    *opsHousekeepingRun `json:"last,omitempty"`
}

type opsHousekeepingRun struct {
	Finished time.Time           `json:"finished"`
	Trigger  string              `json:"trigger"`
	DryRun   bool                `json:"dry_run"`
	Summary  string              `json:"summary"`
	Counts   housekeeping.Counts `json:"counts"`
	Errors   []string            `json:"errors,omitempty"`
}

type opsBackups struct {
	// Drive is the daily backup of the brain folders to Google Drive.
	Drive backup.Summary `json:"drive"`
	Vault opsVault       `json:"vault"`
	// Targets are the node targets.
	Targets []opsTarget `json:"targets,omitempty"`
}

type opsVault struct {
	Enabled   bool      `json:"enabled"`
	State     string    `json:"state,omitempty"`
	Error     string    `json:"error,omitempty"`
	Job       string    `json:"job,omitempty"`
	LastSync  *opsEvent `json:"last_sync,omitempty"`
	Snapshots opsSnaps  `json:"snapshots"`
	// Litestream replicates the databases.
	Litestream *opsEvent `json:"litestream,omitempty"`
}

// opsEvent is when something last happened, and its error.
type opsEvent struct {
	At      time.Time `json:"at,omitzero"`
	Running *bool     `json:"running,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// opsSnaps is a restic repository's snapshots: the last one made, and
// the last try's error.
type opsSnaps struct {
	Enabled   bool       `json:"enabled"`
	LastOK    *time.Time `json:"last_ok,omitempty"`
	LastKind  string     `json:"last_kind,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

type opsTarget struct {
	Name    string   `json:"name"`
	Node    string   `json:"node"`
	Online  bool     `json:"online"`
	Ready   bool     `json:"ready"`
	Problem string   `json:"problem,omitempty"`
	Job     string   `json:"job,omitempty"`
	Snaps   opsSnaps `json:"snapshots"`
}

type opsNode struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Device   string     `json:"device"`
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	Version  string     `json:"version"`
	// Offers is what it offers that the owner approved, with problems
	// such as fride-host (not ready); Waiting what it offers that waits
	// for approval.
	Offers   string   `json:"offers"`
	Waiting  string   `json:"waiting_approval,omitempty"`
	Problems []string `json:"problems,omitempty"`
	UpdateTo string   `json:"update_to,omitempty"`
}

type opsFride struct {
	Instances []opsFrideInstance `json:"instances"`
	Threads   []opsFrideThread   `json:"open_threads"`
}

type opsFrideInstance struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Node        string `json:"node,omitempty"`
	OpenThreads int    `json:"open_threads"`
	Problem     string `json:"problem,omitempty"`
}

type opsFrideThread struct {
	Thread      int64     `json:"thread"`
	State       string    `json:"state"`
	FrideStatus string    `json:"fride_status,omitempty"`
	Brain       string    `json:"brain"`
	Agent       string    `json:"agent,omitempty"`
	Node        string    `json:"node,omitempty"`
	Instance    string    `json:"instance,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	AgeS        int64     `json:"age_s"`
	Updated     time.Time `json:"updated"`
}

type opsPlaud struct {
	Phones []opsPhone `json:"phones"`
}

type opsPhone struct {
	ID         string     `json:"id"`
	LastUpload *time.Time `json:"last_upload,omitempty"`
	// LastCheckIn is when its app last asked for an update.
	LastCheckIn *time.Time `json:"last_check_in,omitempty"`
	AppVersion  int        `json:"app_version,omitempty"`
}

func (b *App) callSystemStatus(ctx context.Context, _ mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	return jsonResult(b.systemStatus(ctx)), nil
}

// systemStatus gathers system_status. A part that cannot be read says so
// in Problems, and the rest is still there.
func (b *App) systemStatus(ctx context.Context) systemStatus {
	now := time.Now()
	cfg := b.cfgs.Get()
	st := systemStatus{At: now.UTC(), Brains: []opsBrain{}, Agents: []opsAgent{}}
	st.Core = opsCore{Version: version.String(), Started: b.started.UTC(), UptimeS: int64(now.Sub(b.started).Seconds()), Lockdown: b.locked()}
	if free, err := platform.FreeBytes(cfg.DataDir); err == nil {
		st.Core.DataDirFreeBytes = free
	}
	problem := func(what string, err error) {
		st.Problems = append(st.Problems, what+": "+err.Error())
	}

	brains := b.brainMgr.Brains()
	// Only the core brain delegates.
	core := ""
	for _, br := range brains {
		if br.Manifest().IsCore() {
			core = br.ID()
		}
	}
	for _, br := range brains {
		rt := br.Snapshot()
		ob := opsBrain{ID: rt.ID, ChatRun: opsRunOf(rt.Running, core, now), Background: opsRunOf(rt.Background, core, now),
			ChatWaiting: len(rt.Queue), BackgroundWaiting: len(rt.BackgroundQueue), LiveSession: b.live.up(rt.ID)}
		if l := rt.Last; l != nil {
			ob.Last = &opsLast{Chat: chatKind(l.Chat), Finished: l.Finished.UTC(), Seconds: l.Seconds, Outcome: l.Outcome}
		}
		st.Brains = append(st.Brains, ob)
	}

	if list, err := b.agents.view(ctx); err != nil {
		problem("scheduled agents", err)
	} else {
		for _, v := range list.Agents {
			st.Agents = append(st.Agents, b.agents.opsAgentOf(v))
		}
	}

	st.Housekeeping = b.housekeeping.opsView()

	st.Backups.Drive = b.backup.Summary()
	vs := b.vault.Status(ctx)
	st.Backups.Vault = opsVaultOf(vs)
	for _, t := range vs.Targets {
		st.Backups.Targets = append(st.Backups.Targets, opsTarget{Name: t.Name, Node: orDefault(t.NodeName, t.Node), Online: t.Online, Ready: t.Ready,
			Problem: t.Problem, Job: t.Job, Snaps: opsSnapsOf(true, t.Last, t.LastNightly, t.LastHourly)})
	}

	if b.nodesEnabled {
		views, err := b.nodes.Views()
		if err != nil {
			problem("nodes", err)
		}
		st.Nodes = []opsNode{}
		for _, v := range views {
			st.Nodes = append(st.Nodes, opsNodeOf(v))
		}
		if f, err := b.opsFride(ctx, now); err != nil {
			problem("FrIDE", err)
		} else {
			st.Fride = f
		}
	}

	if app := b.plaud.Status().App; app != nil {
		p := &opsPlaud{Phones: []opsPhone{}}
		for _, d := range app.Devices {
			ph := opsPhone{ID: d.ID, LastUpload: timeOrNil(d.LastUpload)}
			if d.App != nil {
				ph.LastCheckIn, ph.AppVersion = timeOrNil(d.App.Asked), d.App.VersionCode
			}
			p.Phones = append(p.Phones, ph)
		}
		st.Plaud = p
	}
	return st
}

// opsRunOf is a lane's run as system_status shows it, without its text
// or its chat's JID; core is the core brain, which delegates.
func opsRunOf(v *brain.RunView, core string, now time.Time) *opsRun {
	if v == nil {
		return nil
	}
	r := &opsRun{Chat: chatKind(v.Chat), Source: v.Source, Started: v.Started.UTC(), AgeS: int64(now.Sub(v.Started).Seconds()), TimeoutS: v.TimeoutSeconds}
	switch {
	case chat.IsScheduleChat(v.Chat):
		r.Agent = strings.TrimPrefix(v.Chat, chat.ScheduleChatPrefix)
	case chat.IsDelegateChat(v.Chat):
		r.From = core
	}
	return r
}

// chatKind names the chat jid without its JID, for what a run is told:
// dm, group, schedule:<name> for a scheduled agent's own chat, delegate
// for a delegated task's, and internal for any other of the bridge's own
// (a site build's, say).
func chatKind(jid string) string {
	switch {
	case chat.IsScheduleChat(jid):
		return jid
	case chat.IsDelegateChat(jid):
		return "delegate"
	case chat.IsGroupJID(jid):
		return "group"
	case chat.IsDirectJID(jid):
		return "dm"
	}
	return "internal"
}

// opsAgentOf is a scheduled agent as the Schedules page lists it (v),
// with how many of its last runs failed in a row.
func (a *agents) opsAgentOf(v scheduleView) opsAgent {
	out := opsAgent{Brain: v.Brain, Name: v.Name, State: v.State, Enabled: v.Enabled, Paused: v.Paused, Running: v.Running, Problem: v.Problem}
	if v.Next != nil {
		out.Next = timeOrNil(*v.Next)
	}
	if r := v.LastRun; r != nil {
		out.LastRun = &opsAgentRun{ID: r.ID, Status: r.Status, Trigger: r.Trigger, Started: r.Started.UTC(), DurationS: r.DurationMS / 1000, Error: r.Error}
		if v.Running {
			since := r.Started.UTC()
			out.RunningSince = &since
		}
	}
	if v.Review != nil {
		out.PendingApproval = v.Review.Status
	}
	if runs, err := a.store.Runs(v.Brain, v.Name, opsFailureWindow); err == nil {
		for _, r := range runs {
			if r.Status != sched.StatusFailed && r.Status != sched.StatusRefused {
				break
			}
			out.ConsecutiveFailures++
		}
	}
	return out
}

// opsView is the housekeeping card (GET /api/housekeeping) as
// system_status shows it: its last report summed up.
func (h *housekeepingService) opsView() opsHousekeeping {
	v := h.view()
	var out opsHousekeeping
	out.NextRun, _ = v["next_run"].(string)
	out.Enabled, _ = v["enabled"].(bool)
	out.DryRun, _ = v["dry_run"].(bool)
	out.Running, _ = v["running"].(bool)
	if last, ok := v["last"].(*housekeeping.Report); ok && last != nil {
		out.Last = &opsHousekeepingRun{Finished: last.Finished, Trigger: last.Trigger, DryRun: last.DryRun, Summary: last.Summary(), Counts: last.Counts(), Errors: last.Errors}
	}
	return out
}

// opsVaultOf is the vault's status (GET /api/vault/status) summed up.
func opsVaultOf(vs vaultStatus) opsVault {
	out := opsVault{Enabled: vs.Enabled, State: vs.State, Error: vs.Error, Job: vs.Job}
	if s := vs.LastSync; s != nil {
		out.LastSync = &opsEvent{At: s.At.UTC(), Error: s.Error}
	}
	sn := vs.Snapshots
	out.Snapshots = opsSnapsOf(sn.Enabled, sn.Last, sn.LastNightly, sn.LastHourly)
	if sn.Error != "" && out.Snapshots.LastError == "" {
		out.Snapshots.LastError = sn.Error
	}
	if l := vs.Litestream; l.Enabled {
		running := l.Running
		out.Litestream = &opsEvent{At: l.At.UTC(), Running: &running, Error: orDefault(l.LastError, l.LastProblem)}
	}
	return out
}

// opsSnapsOf sums up a repository's snapshots: the newest one made of
// those taken, and the error of the last try.
func opsSnapsOf(enabled bool, last *snapshotRecord, taken ...*snapshotRecord) opsSnaps {
	out := opsSnaps{Enabled: enabled}
	if ok := newestSnapshot(taken...); ok != nil {
		at := ok.At.UTC()
		out.LastOK, out.LastKind = &at, ok.Kind
	}
	if last != nil {
		out.LastError = last.Error
	}
	return out
}

// opsNodeOf is a node as the Nodes page lists it (v), summed up.
func opsNodeOf(v nodes.View) opsNode {
	out := opsNode{ID: v.ID, Name: v.Name, Device: v.Device, Online: v.Online, LastSeen: v.LastSeen, Version: v.Version,
		Offers: v.Effective.Summary(), UpdateTo: v.UpdateTo}
	if declared := v.Declared.Summary(); declared != out.Offers {
		out.Waiting = declared
	}
	if fh := v.Effective.FrideHost; fh != nil {
		if !fh.Ready && fh.Problem != "" {
			out.Problems = append(out.Problems, "fride-host not ready: "+fh.Problem)
		}
		if fh.NoLogin != "" {
			out.Problems = append(out.Problems, "fride-host: "+fh.NoLogin)
		}
	}
	if f := v.Effective.Fride; f != nil && f.NotLoggedIn {
		out.Problems = append(out.Problems, "fride: Claude is not logged in")
	}
	if p := v.Programs; p != nil && p.Error != "" {
		out.Problems = append(out.Problems, "programs: "+p.Error)
	}
	if v.UpdateNote != "" {
		out.Problems = append(out.Problems, "update: "+v.UpdateNote)
	}
	return out
}

// opsFride is FrIDE's page (GET /api/fride) summed up: the instances, and
// the threads that are not closed, without their messages.
func (b *App) opsFride(ctx context.Context, now time.Time) (*opsFride, error) {
	all, err := b.store.FrideThreads(ctx, "")
	if err != nil {
		return nil, err
	}
	open, err := b.fride.inst.openThreads(ctx)
	if err != nil {
		return nil, err
	}
	out := &opsFride{Instances: []opsFrideInstance{}, Threads: []opsFrideThread{}}
	for _, v := range b.fride.inst.instanceViews(b.fride.inst.views(), open) {
		out.Instances = append(out.Instances, opsFrideInstance{Name: v.Name, State: v.State, Node: v.Node, OpenThreads: v.OpenThreads, Problem: v.Problem})
	}
	for _, t := range all {
		if t.State == store.FrideClosed {
			continue
		}
		ot := opsFrideThread{Thread: t.ID, State: t.State, FrideStatus: t.FrideStatus, Brain: t.Brain, Instance: t.Instance, Branch: t.Branch,
			AgeS: int64(now.Sub(t.CreatedAt).Seconds()), Updated: t.UpdatedAt.UTC()}
		if t.Node != "" {
			ot.Node = b.fride.nodeName(t.Node)
		}
		if chat.IsScheduleChat(t.Chat) {
			ot.Agent = strings.TrimPrefix(t.Chat, chat.ScheduleChatPrefix)
		}
		out.Threads = append(out.Threads, ot)
	}
	slices.SortFunc(out.Threads, func(x, y opsFrideThread) int { return int(x.Thread - y.Thread) })
	return out, nil
}

// timeOrNil is t in UTC, nil for the zero time.
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
