package connections

import (
	"net/http"
	"slices"
	"strings"
)

// What the dashboard shows of the connections (GET /api/connections).

// Entry is one connection: whether a key is set and its last characters,
// OpenAI's organization and project or ElevenLabs' default voice, and
// which brains may use it.
type Entry struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Configured      bool     `json:"configured"`
	KeyHint         string   `json:"key_hint"`
	Organization    *string  `json:"organization,omitempty"`
	Project         *string  `json:"project,omitempty"`
	Voice           *string  `json:"voice,omitempty"`
	Ops             []string `json:"ops"`
	Brains          []string `json:"brains"`
	BrainMonthlyUSD float64  `json:"brain_monthly_usd"`
}

// Module is a module of the live site with what it declares, by
// connection.
type Module struct {
	ID          string
	Connections map[string]Declaration
}

// ModuleUse is one module's declaration of one connection.
type ModuleUse struct {
	Module     string   `json:"module"`
	Connection string   `json:"connection"`
	Use        []string `json:"use"`
	Models     []string `json:"models"`
	MonthlyUSD float64  `json:"monthly_usd"`
}

// View is the dashboard's page of the connections: this month's usage.
type View struct {
	Month       string      `json:"month"`
	Connections []Entry     `json:"connections"`
	Modules     []ModuleUse `json:"modules"`
	Usage       []UsageRow  `json:"usage"`
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Entry is connection id as the dashboard shows it.
func (s *Service) Entry(id string) (Entry, error) {
	if !Known(id) {
		return Entry{}, fail(http.StatusNotFound, "no connection %q", id)
	}
	k, ok := s.loadKey(id)
	p := s.o.Settings().Of(id)
	e := Entry{ID: id, Title: Title(id), Configured: ok, KeyHint: hint(k.APIKey), Ops: Ops(id),
		Brains: orEmpty(slices.Clone(p.Brains)), BrainMonthlyUSD: p.BrainMonthlyUSD}
	if id == OpenAI {
		e.Organization, e.Project = &k.Organization, &k.Project
	} else {
		e.Voice = &p.Voice
	}
	return e, nil
}

// View is the page: both connections, what the live modules declare
// (modules, nil when the site is off), and this month's usage with each
// caller's limit.
func (s *Service) View(modules []Module) View {
	v := View{Connections: []Entry{}, Modules: []ModuleUse{}}
	for _, id := range IDs() {
		e, _ := s.Entry(id)
		v.Connections = append(v.Connections, e)
	}
	limits := map[string]float64{}
	for _, m := range modules {
		for _, id := range IDs() {
			d, ok := m.Connections[id]
			if !ok {
				continue
			}
			v.Modules = append(v.Modules, ModuleUse{Module: m.ID, Connection: id, Use: orEmpty(slices.Clone(d.Use)),
				Models: orEmpty(slices.Clone(d.Models)), MonthlyUSD: d.MonthlyUSD})
			limits[id+" module:"+m.ID] = d.MonthlyUSD
		}
	}
	slices.SortFunc(v.Modules, func(a, b ModuleUse) int {
		if c := strings.Compare(a.Module, b.Module); c != 0 {
			return c
		}
		return slices.Index(IDs(), a.Connection) - slices.Index(IDs(), b.Connection)
	})
	set := s.o.Settings()
	v.Month, v.Usage = s.MonthUsage()
	for i, row := range v.Usage {
		if strings.HasPrefix(row.Caller, "brain:") {
			v.Usage[i].LimitUSD = set.Of(row.Connection).BrainMonthlyUSD
		} else {
			v.Usage[i].LimitUSD = limits[row.Connection+" "+row.Caller]
		}
	}
	return v
}
