package connections

import (
	"encoding/json"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// The usage log, usage.json in Dir: per month (UTC), connection and
// caller, what was asked and what it cost, estimated. It keeps the last
// keepMonths months. A caller whose estimate this month reached its limit
// is refused until the next month.

// keepMonths is how many months the log keeps.
const keepMonths = 13

// Usage is a caller's use of a connection in a month.
type Usage struct {
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	AudioSeconds float64 `json:"audio_seconds"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Characters   int64   `json:"characters"`
	// USD is the estimated cost (prices.go).
	USD float64 `json:"usd"`
}

func (u *Usage) add(d Usage) {
	u.Requests += d.Requests
	u.Errors += d.Errors
	u.AudioSeconds += d.AudioSeconds
	u.InputTokens += d.InputTokens
	u.OutputTokens += d.OutputTokens
	u.Characters += d.Characters
	u.USD += d.USD
}

// usageLog is usage.json: month → connection → caller.
type usageLog struct {
	Months map[string]map[string]map[string]*Usage `json:"months"`
}

func (s *Service) usagePath() string { return filepath.Join(s.o.Dir, "usage.json") }

// loadUsage reads the log once; s.mu is held.
func (s *Service) loadUsage() *usageLog {
	if s.usage != nil {
		return s.usage
	}
	l := &usageLog{}
	if data, err := os.ReadFile(s.usagePath()); err == nil {
		if err := json.Unmarshal(data, l); err != nil {
			log.Printf("[connections] %s does not parse, starting a new one: %v", s.usagePath(), err)
			l = &usageLog{}
		}
	}
	if l.Months == nil {
		l.Months = map[string]map[string]map[string]*Usage{}
	}
	s.usage = l
	return l
}

// spent is what caller spent on conn this month.
func (s *Service) spent(conn, caller string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.loadUsage().Months[s.month()][conn][caller]; u != nil {
		return u.USD
	}
	return 0
}

// overBudget refuses c when its estimate this month reached its limit.
func (s *Service) overBudget(conn string, c Caller) error {
	if c.LimitUSD <= 0 {
		return nil
	}
	if s.spent(conn, c.ID) >= c.LimitUSD {
		return fail(http.StatusTooManyRequests, "%s is over its monthly budget of $%s for %s", c.ID, usd(c.LimitUSD), Title(conn))
	}
	return nil
}

// record adds d to caller's use of conn this month and writes the log.
func (s *Service) record(conn, caller string, d Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.loadUsage()
	month := s.month()
	byConn := l.Months[month]
	if byConn == nil {
		byConn = map[string]map[string]*Usage{}
		l.Months[month] = byConn
	}
	byCaller := byConn[conn]
	if byCaller == nil {
		byCaller = map[string]*Usage{}
		byConn[conn] = byCaller
	}
	u := byCaller[caller]
	if u == nil {
		u = &Usage{}
		byCaller[caller] = u
	}
	u.add(d)
	months := slices.Sorted(maps.Keys(l.Months))
	for len(months) > keepMonths {
		delete(l.Months, months[0])
		months = months[1:]
	}
	data, _ := json.MarshalIndent(l, "", "  ")
	if err := writeAtomic(s.usagePath(), append(data, '\n')); err != nil {
		log.Printf("[connections] could not write the usage log: %v", err)
	}
}

// UsageRow is one caller's use of one connection this month.
type UsageRow struct {
	Connection string `json:"connection"`
	Caller     string `json:"caller"`
	Usage
	LimitUSD float64 `json:"limit_usd"`
}

// MonthUsage is this month's usage, by connection and caller in order.
func (s *Service) MonthUsage() (month string, rows []UsageRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	month = s.month()
	rows = []UsageRow{}
	for conn, byCaller := range s.loadUsage().Months[month] {
		for caller, u := range byCaller {
			rows = append(rows, UsageRow{Connection: conn, Caller: caller, Usage: *u})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Connection != rows[j].Connection {
			return slices.Index(IDs(), rows[i].Connection) < slices.Index(IDs(), rows[j].Connection)
		}
		return rows[i].Caller < rows[j].Caller
	})
	return month, rows
}
