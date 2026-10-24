package manifest

import (
	"encoding/json"

	"github.com/Icewater1337/fridolin/internal/chat"
	"strings"
	"testing"
)

func TestScheduleValidate(t *testing.T) {
	ok := []Schedule{
		{Name: "daily-check", When: "every 6h"},
		{Name: "brief", When: "daily 06:00", Template: "morning-briefing", Report: ReportWorth, Chat: "15551234567@s.whatsapp.net"},
		{Name: "review", When: "cron 0 3 * * *", Template: "code-review-bugs", Vars: map[string]string{"repos": "a/b"}, GitHub: &chat.GitHubGrant{Repos: []string{"a/b"}, Permissions: map[string]string{"issues": "write", "contents": "read"}}, Risk: "irreversible", Preapproved: []string{"file issues on a/b"}, TimeoutMinutes: 60},
		{Name: "scrape", When: "daily 21:00", Command: &ScheduleCommand{Node: "mac-mini", Argv: []string{"/Users/you/run.sh"}}, Report: ReportSilent, Missed: MissedSkip},
		{Name: "inline", When: "weekly mon 09:00", Prompt: "Check the inbox", JitterMinutes: new(0)},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
	bad := map[string]Schedule{
		"name":        {Name: "Bad Name", When: "every 6h"},
		"when":        {Name: "a", When: "sometimes"},
		"once":        {Name: "a", When: "2026-10-23T09:00"},
		"two tasks":   {Name: "a", When: "every 6h", Prompt: "x", Template: "y"},
		"report":      {Name: "a", When: "every 6h", Report: "loud"},
		"chat":        {Name: "a", When: "every 6h", Chat: "my chat"},
		"missed":      {Name: "a", When: "every 6h", Missed: "later"},
		"risk":        {Name: "a", When: "every 6h", Risk: "huge"},
		"github repo": {Name: "a", When: "every 6h", GitHub: &chat.GitHubGrant{Repos: []string{"not a repo"}, Permissions: map[string]string{"issues": "write"}}},
		"github dup":  {Name: "a", When: "every 6h", GitHub: &chat.GitHubGrant{Repos: []string{"a/b", "A/B"}, Permissions: map[string]string{"issues": "write"}}},
		"github perm": {Name: "a", When: "every 6h", GitHub: &chat.GitHubGrant{Repos: []string{"a/b"}, Permissions: map[string]string{"issues": "admin"}}},
		"github none": {Name: "a", When: "every 6h", GitHub: &chat.GitHubGrant{Repos: []string{"a/b"}}},
		"timeout":     {Name: "a", When: "every 6h", TimeoutMinutes: -1},
		"jitter":      {Name: "a", When: "every 6h", JitterMinutes: new(99999)},
		"prompt file": {Name: "a", When: "every 6h", PromptFile: "../x.md"},
		"command":     {Name: "a", When: "every 6h", Command: &ScheduleCommand{Node: "n"}},
		"command env": {Name: "a", When: "every 6h", Command: &ScheduleCommand{Node: "n", Argv: []string{"x"}}, GitHub: &chat.GitHubGrant{Repos: []string{"a/b"}, Permissions: map[string]string{"issues": "write"}}},
		"var":         {Name: "a", When: "every 6h", Vars: map[string]string{"Bad-Name": "x"}},
		"template":    {Name: "a", When: "every 6h", Template: "No"},
		"preapproved": {Name: "a", When: "every 6h", Preapproved: []string{"two\nlines"}},
	}
	for what, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
	m := Manifest{ID: "a", Name: "A", Schedules: []Schedule{{Name: "x", When: "every 6h"}, {Name: "x", When: "every 7h"}}}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "two schedules") {
		t.Errorf("duplicate names: %v", err)
	}
}

func TestEffectiveSchedulesKeepMemoryConsolidate(t *testing.T) {
	m := Manifest{ID: "a", Name: "A", Memory: &Memory{Consolidate: "weekly sun 18:00"}}
	got := m.EffectiveSchedules()
	if len(got) != 1 || got[0].Name != ConsolidationSchedule || got[0].When != "weekly sun 18:00" || !got[0].FromMemory || got[0].ReportMode() != ReportAlways || got[0].Template != ConsolidationSchedule {
		t.Fatalf("memory.consolidate becomes %+v", got)
	}
	// A schedule of that name replaces it.
	m.Schedules = []Schedule{{Name: ConsolidationSchedule, When: "daily 07:00", Template: ConsolidationSchedule, Enabled: new(false)}}
	if got := m.EffectiveSchedules(); len(got) != 1 || got[0].When != "daily 07:00" || got[0].FromMemory {
		t.Fatalf("the explicit schedule does not win: %+v", got)
	}
	// Editing it moves it into the list and clears the setting.
	m = Manifest{ID: "a", Name: "A", Memory: &Memory{Inject: true, Consolidate: "weekly sun 18:00"}}
	s, _ := m.ScheduleNamed(ConsolidationSchedule)
	s.Enabled = new(false)
	m.SetSchedule(s)
	if m.Memory.Consolidate != "" || !m.Memory.Inject || len(m.Schedules) != 1 || m.Schedules[0].IsEnabled() {
		t.Fatalf("after the edit: memory %+v, schedules %+v", m.Memory, m.Schedules)
	}
	data, _ := json.Marshal(m.Schedules[0])
	if strings.Contains(string(data), "FromMemory") || strings.Contains(string(data), "from_memory") {
		t.Errorf("FromMemory is written: %s", data)
	}
	if !m.RemoveSchedule(ConsolidationSchedule) || len(m.Schedules) != 0 || m.RemoveSchedule("nope") {
		t.Error("RemoveSchedule")
	}
}

func TestSchedulesRoundTrip(t *testing.T) {
	doc := `{"id":"a","name":"A","schedules":[{"name":"b","when":"every 6h","template":"morning-briefing","vars":{"repos":"x"}}]}`
	m, err := Decode([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(m.Schedules) != 1 || m.Schedules[0].Vars["repos"] != "x" {
		t.Fatalf("%+v", m.Schedules)
	}
	if (Schedule{Name: "x", When: "every 1h"}).TaskFile() != "schedules/x.md" || (Schedule{Name: "x", Template: "t"}).TaskFile() != "" {
		t.Error("TaskFile")
	}
	empty := Manifest{ID: "a", Name: "A", Schedules: []Schedule{}}
	if !empty.Equal(Manifest{ID: "a", Name: "A"}) {
		t.Error("no schedules and an empty list differ")
	}
}
