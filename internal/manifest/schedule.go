package manifest

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// Schedule is one scheduled agent of a brain (docs/BRAINS.md, "Scheduled
// agents"): a task the brain runs by itself on a schedule, or a program
// the bridge runs on a node. It lives in brain.json, so a schedule a brain
// writes itself, asked in a chat say, waits for the owner's approval like
// any other change to the file; what runs is the approved copy.
type Schedule struct {
	// Name is the agent's slug, unique in the brain: lower-case letters,
	// digits and hyphens.
	Name string `json:"name"`
	// When is "every 6h", "daily 07:00", "weekly mon 09:00" or a cron
	// expression ("cron 0 7 * * 1-5"), in the bridge's time zone.
	When string `json:"when"`
	// Enabled false keeps it from running on its schedule; Run now still
	// runs it. Unset is true.
	Enabled *bool `json:"enabled,omitempty"`
	// Description says what it is for, on the Schedules page.
	Description string `json:"description,omitempty"`

	// What it does, at most one of these; none is the prompt file
	// schedules/<name>.md in the brain's folder.
	//
	// Prompt is the task, inline.
	Prompt string `json:"prompt,omitempty"`
	// PromptFile is a file in the brain's folder that holds the task:
	// schedules/<name>.md, or a skill's file.
	PromptFile string `json:"prompt_file,omitempty"`
	// Template is a built-in task the bridge ships (examples/schedules):
	// memory-consolidation, morning-briefing, code-review-security, …
	Template string `json:"template,omitempty"`
	// Command runs a program on a node instead of a brain run.
	Command *ScheduleCommand `json:"command,omitempty"`
	// Vars fill {{name}} in the template or prompt file.
	Vars map[string]string `json:"vars,omitempty"`

	// Report is where the result goes: ReportAlways, ReportWorth (the
	// default) or ReportSilent.
	Report string `json:"report,omitempty"`
	// Chat is the chat the result goes to; default the brain's reply chat.
	Chat string `json:"chat,omitempty"`
	// Risk is the run's risk budget, read, write or irreversible; default
	// write, as for any run no skill names.
	Risk string `json:"risk,omitempty"`
	// DisallowedTools overrides the brain's list for these runs; nil keeps
	// it, [] allows every tool.
	DisallowedTools *[]string `json:"disallowed_tools,omitempty"`
	// Effort is its runs' reasoning effort in Claude mode, in place of
	// the brain's (brain.json effort, else brain_effort); "" keeps that.
	// Codex mode keeps codex_effort, as it does for the brain's.
	Effort string `json:"effort,omitempty"`
	// TimeoutMinutes stops a run that takes longer, in place of
	// run_timeout_minutes, shorter or longer; 0 is run_timeout_minutes.
	TimeoutMinutes int `json:"timeout_minutes,omitempty"`
	// Missed is what a run that fell due while the bridge was down does:
	// MissedRun (the default) runs once on start, MissedSkip waits for the
	// next time.
	Missed string `json:"missed,omitempty"`
	// JitterMinutes spreads the runs over that many minutes after their
	// time, the same offset each time; unset is schedules.jitter_minutes.
	JitterMinutes *int `json:"jitter_minutes,omitempty"`
	// Preapproved are steps the owner approved with the schedule, which
	// its runs take without asking (filing an issue, say).
	Preapproved []string `json:"preapproved,omitempty"`
	// GitHub is the GitHub access its runs are granted, and no other run:
	// a token for these repositories and permissions, as gh's login, minted
	// as a run starts and ended with it (chat.GitHubGrant).
	GitHub *chat.GitHubGrant `json:"github,omitempty"`

	// FromMemory marks the schedule memory.consolidate stands for
	// (EffectiveSchedules); it is never written.
	FromMemory bool `json:"-"`
}

// ScheduleCommand is a program a scheduled agent runs on a node, through
// its exec capability: the node's allowlist and the core's approval of
// its commands apply, and a lockdown refuses it.
type ScheduleCommand struct {
	// Node is the node's id or name.
	Node string `json:"node"`
	// Argv is the program and its arguments; no shell.
	Argv []string `json:"argv"`
	// Dir is the working directory on the node, "" for the agent's.
	Dir string `json:"dir,omitempty"`
}

// Where a scheduled agent's result goes (Schedule.Report).
const (
	// ReportAlways sends every result, and a failure, to the chat.
	ReportAlways = "always"
	// ReportWorth sends a result only when the run says there is
	// something to report (it answers NothingToReport otherwise), and a
	// failure; a program's run, only its failure.
	ReportWorth = "worth"
	// ReportSilent sends nothing: the result is in its history and log.
	ReportSilent = "silent"
)

// NothingToReport is what a run of a ReportWorth agent answers when it
// has nothing worth a message.
const NothingToReport = "NOTHING_TO_REPORT"

// What a run that fell due while the bridge was down does
// (Schedule.Missed).
const (
	MissedRun  = "run"
	MissedSkip = "skip"
)

// ConsolidationSchedule is the name of the schedule memory.consolidate
// stands for, and of its template.
const ConsolidationSchedule = "memory-consolidation"

// Bounds of a brain's schedules.
const (
	MaxSchedules        = 50
	MaxSchedulePrompt   = 20000
	MaxScheduleTimeout  = 24 * 60
	MaxScheduleJitter   = 24 * 60
	maxScheduleArgv     = 64
	maxSchedulePreapprv = 10
)

var (
	scheduleNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	scheduleVarRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	validReports   = map[string]bool{"": true, ReportAlways: true, ReportWorth: true, ReportSilent: true}
	validMissed    = map[string]bool{"": true, MissedRun: true, MissedSkip: true}
	validRisks     = map[string]bool{"": true, "read": true, "write": true, "irreversible": true}
	githubRepoRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	githubPermRe   = regexp.MustCompile(`^[a-z][a-z_]{1,39}$`)
	validGitHubAcc = map[string]bool{"read": true, "write": true}
)

// ValidScheduleName reports whether name may name a scheduled agent or a
// template.
func ValidScheduleName(name string) bool { return scheduleNameRe.MatchString(name) }

// ScheduleReports, ScheduleMissed and ScheduleRisks are the values the
// fields take, the unset one included (TestSchemaEnumsAreTheBridgesValues).
func ScheduleReports() []string { return slices.Sorted(maps.Keys(validReports)) }
func ScheduleMissed() []string  { return slices.Sorted(maps.Keys(validMissed)) }
func ScheduleRisks() []string   { return slices.Sorted(maps.Keys(validRisks)) }

// ValidateGitHubGrant checks a GitHub grant: at least one repository, each
// "owner/name", each once; at least one permission, each read or write.
func ValidateGitHubGrant(g chat.GitHubGrant) error {
	if len(g.Repos) == 0 || len(g.Repos) > 20 {
		return fmt.Errorf("github: give 1 to 20 repos, as owner/name")
	}
	seen := map[string]bool{}
	for _, r := range g.Repos {
		if !githubRepoRe.MatchString(r) {
			return fmt.Errorf("github: repo %q must be owner/name", r)
		}
		if seen[strings.ToLower(r)] {
			return fmt.Errorf("github: repo %q is listed twice", r)
		}
		seen[strings.ToLower(r)] = true
	}
	if len(g.Permissions) == 0 {
		return fmt.Errorf("github: give its permissions, such as {\"issues\": \"write\", \"contents\": \"read\"}")
	}
	for name, access := range g.Permissions {
		if !githubPermRe.MatchString(name) || !validGitHubAcc[access] {
			return fmt.Errorf("github: permission %q: %q must be read or write", name, access)
		}
	}
	return nil
}

// IsEnabled reports whether s runs on its schedule.
func (s Schedule) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// ReportMode is s.Report with its default.
func (s Schedule) ReportMode() string {
	if s.Report == "" {
		return ReportWorth
	}
	return s.Report
}

// MissedMode is s.Missed with its default.
func (s Schedule) MissedMode() string {
	if s.Missed == "" {
		return MissedRun
	}
	return s.Missed
}

// Parsed is s.When as a trigger.Schedule, in loc.
func (s Schedule) Parsed(loc *time.Location) (trigger.Schedule, error) {
	return trigger.ParseRecurring(s.When, loc)
}

// TaskFile is the prompt file of a schedule that names no task: its own
// file in the brain's schedules/ folder.
func (s Schedule) TaskFile() string {
	if s.PromptFile != "" {
		return s.PromptFile
	}
	if s.Prompt == "" && s.Template == "" && s.Command == nil {
		return filepath.Join("schedules", s.Name+".md")
	}
	return ""
}

// Validate checks one schedule on its own.
func (s Schedule) Validate() error {
	if !ValidScheduleName(s.Name) {
		return fmt.Errorf("schedule name %q must be 1–40 lowercase letters, digits or -", s.Name)
	}
	at := fmt.Sprintf("schedule %q", s.Name)
	if _, err := s.Parsed(time.UTC); err != nil {
		return fmt.Errorf("%s: %v", at, err)
	}
	tasks := 0
	for _, set := range []bool{s.Prompt != "", s.PromptFile != "", s.Template != "", s.Command != nil} {
		if set {
			tasks++
		}
	}
	if tasks > 1 {
		return fmt.Errorf("%s: give one of prompt, prompt_file, template and command (none is the file schedules/%s.md)", at, s.Name)
	}
	if n := utf8.RuneCountInString(s.Prompt); n > MaxSchedulePrompt {
		return fmt.Errorf("%s: prompt is at most %d characters, not %d", at, MaxSchedulePrompt, n)
	}
	if utf8.RuneCountInString(s.Description) > 500 || strings.ContainsAny(s.Description, "\r\n") {
		return fmt.Errorf("%s: description must be one line of at most 500 characters", at)
	}
	if p := s.PromptFile; p != "" {
		if filepath.IsAbs(p) || strings.HasPrefix(filepath.Clean(p), "..") || strings.ContainsRune(p, 0) {
			return fmt.Errorf("%s: prompt_file must be a path inside the brain folder", at)
		}
	}
	if s.Template != "" && !ValidScheduleName(s.Template) {
		return fmt.Errorf("%s: template %q is no template name", at, s.Template)
	}
	for k, v := range s.Vars {
		if !scheduleVarRe.MatchString(k) {
			return fmt.Errorf("%s: var %q must be a lower-case name", at, k)
		}
		if utf8.RuneCountInString(v) > 2000 {
			return fmt.Errorf("%s: var %q is longer than 2000 characters", at, k)
		}
	}
	if !validReports[s.Report] {
		return fmt.Errorf("%s: report %q must be %q, %q or %q", at, s.Report, ReportAlways, ReportWorth, ReportSilent)
	}
	if s.Chat != "" && (!strings.Contains(s.Chat, "@") || strings.ContainsAny(s.Chat, " \r\n")) {
		return fmt.Errorf("%s: chat %q must be a chat's JID (…@s.whatsapp.net or …@g.us)", at, s.Chat)
	}
	if !validMissed[s.Missed] {
		return fmt.Errorf("%s: missed %q must be %q or %q", at, s.Missed, MissedRun, MissedSkip)
	}
	if !ValidEffort(s.Effort) {
		return fmt.Errorf("%s: effort %q must be one of low, medium, high, xhigh, max", at, s.Effort)
	}
	if s.TimeoutMinutes < 0 || s.TimeoutMinutes > MaxScheduleTimeout {
		return fmt.Errorf("%s: timeout_minutes must be between 1 and %d, or 0 for run_timeout_minutes", at, MaxScheduleTimeout)
	}
	if j := s.JitterMinutes; j != nil && (*j < 0 || *j > MaxScheduleJitter) {
		return fmt.Errorf("%s: jitter_minutes must be between 0 and %d", at, MaxScheduleJitter)
	}
	if len(s.Preapproved) > maxSchedulePreapprv {
		return fmt.Errorf("%s: at most %d preapproved steps", at, maxSchedulePreapprv)
	}
	for _, p := range s.Preapproved {
		if strings.TrimSpace(p) == "" || utf8.RuneCountInString(p) > 300 || strings.ContainsAny(p, "\r\n") {
			return fmt.Errorf("%s: a preapproved step is one line of at most 300 characters", at)
		}
	}
	if s.Command != nil {
		c := s.Command
		if strings.TrimSpace(c.Node) == "" || len(c.Argv) == 0 || strings.TrimSpace(c.Argv[0]) == "" {
			return fmt.Errorf("%s: command needs its node and argv", at)
		}
		if len(c.Argv) > maxScheduleArgv {
			return fmt.Errorf("%s: command has more than %d arguments", at, maxScheduleArgv)
		}
		for _, a := range append(slices.Clone(c.Argv), c.Dir, c.Node) {
			if strings.ContainsAny(a, "\x00\r\n") {
				return fmt.Errorf("%s: command arguments must be single lines", at)
			}
		}
		if s.Risk != "" || s.Effort != "" || s.DisallowedTools != nil || s.GitHub != nil || len(s.Preapproved) > 0 || len(s.Vars) > 0 {
			return fmt.Errorf("%s: risk, effort, disallowed_tools, github, preapproved and vars are for brain runs, not a command", at)
		}
		return nil
	}
	if !validRisks[s.Risk] {
		return fmt.Errorf("%s: risk %q must be read, write or irreversible", at, s.Risk)
	}
	if s.GitHub != nil {
		if err := ValidateGitHubGrant(*s.GitHub); err != nil {
			return fmt.Errorf("%s: %v", at, err)
		}
	}
	return nil
}

// validateSchedules checks a manifest's schedules: each on its own, and
// their names unique.
func (m *Manifest) validateSchedules() error {
	if len(m.Schedules) > MaxSchedules {
		return fmt.Errorf("brain %q: at most %d schedules", m.ID, MaxSchedules)
	}
	seen := map[string]bool{}
	for _, s := range m.Schedules {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("brain %q: %v", m.ID, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("brain %q: two schedules are called %q", m.ID, s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

// EffectiveSchedules are the brain's scheduled agents: its schedules,
// and the one memory.consolidate stands for, unless a schedule of that
// name replaces it. memory.consolidate means what it always did: the
// brain follows skills/memory-consolidation.md on that schedule, and its
// one-line answer goes to its reply chat.
func (m Manifest) EffectiveSchedules() []Schedule {
	out := slices.Clone(m.Schedules)
	c := MemoryOf(m).Consolidate
	if c == "" || slices.ContainsFunc(out, func(s Schedule) bool { return s.Name == ConsolidationSchedule }) {
		return out
	}
	return append(out, Schedule{
		Name: ConsolidationSchedule, When: c, Template: ConsolidationSchedule, Report: ReportAlways,
		Description: "memory.consolidate: promote what lasts from the daily notes into memories/core.md",
		Missed:      MissedRun, FromMemory: true,
	})
}

// ScheduleNamed returns the brain's effective schedule name.
func (m Manifest) ScheduleNamed(name string) (Schedule, bool) {
	for _, s := range m.EffectiveSchedules() {
		if s.Name == name {
			return s, true
		}
	}
	return Schedule{}, false
}

// SetSchedule puts s among m's schedules, in place of the one of its name.
// The schedule memory.consolidate stood for moves into the list, which
// then holds it: the setting is cleared, so the two cannot disagree.
func (m *Manifest) SetSchedule(s Schedule) {
	s.FromMemory = false
	if s.Name == ConsolidationSchedule && m.Memory != nil && m.Memory.Consolidate != "" {
		mem := *m.Memory
		mem.Consolidate = ""
		m.Memory = &mem
	}
	m.Schedules = slices.Clone(m.Schedules)
	if i := slices.IndexFunc(m.Schedules, func(o Schedule) bool { return o.Name == s.Name }); i >= 0 {
		m.Schedules[i] = s
		return
	}
	m.Schedules = append(m.Schedules, s)
}

// RemoveSchedule takes schedule name out of m, the one memory.consolidate
// stands for too, and reports whether there was one.
func (m *Manifest) RemoveSchedule(name string) bool {
	found := false
	if name == ConsolidationSchedule && m.Memory != nil && m.Memory.Consolidate != "" {
		mem := *m.Memory
		mem.Consolidate = ""
		m.Memory = &mem
		found = true
	}
	if i := slices.IndexFunc(m.Schedules, func(o Schedule) bool { return o.Name == name }); i >= 0 {
		m.Schedules = slices.Delete(slices.Clone(m.Schedules), i, i+1)
		found = true
	}
	return found
}
