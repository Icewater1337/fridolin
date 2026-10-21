package housekeeping

import (
	"fmt"
	"strings"
	"time"
)

// Report is one run of the job.
type Report struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Trigger is what started it: schedule, dashboard, cli.
	Trigger string `json:"trigger"`
	DryRun  bool   `json:"dry_run"`
	// Note says why a run that was to act only reported (a lockdown).
	Note     string    `json:"note,omitempty"`
	Scanned  int       `json:"scanned"`
	Findings []Finding `json:"findings"`
	Errors   []string  `json:"errors,omitempty"`
}

// Counts sums up a report's findings.
type Counts struct {
	Stopped   int `json:"stopped"`
	WouldStop int `json:"would_stop"`
	Asked     int `json:"asked"`
	Removed   int `json:"removed"`
	Failed    int `json:"failed"`
	Problems  int `json:"problems"`
	Reported  int `json:"reported"`
}

// The outcomes of findings.
const (
	OutcomeStopped   = "stopped"
	OutcomeWouldStop = "would stop"
	OutcomeWouldAsk  = "would ask"
	OutcomeAsked     = "asked"
	OutcomeRemoved   = "removed"
	OutcomeReported  = "reported"
	OutcomeFailed    = "failed"
)

// Counts sums up r.
func (r Report) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch {
		case f.Outcome == OutcomeStopped:
			c.Stopped++
		case f.Outcome == OutcomeRemoved:
			c.Removed++
		case f.Outcome == OutcomeWouldStop:
			c.WouldStop++
		case f.Outcome == OutcomeAsked:
			c.Asked++
		case strings.HasPrefix(f.Outcome, OutcomeFailed):
			c.Failed++
		default:
			c.Reported++
		}
		if f.Problem {
			c.Problems++
		}
	}
	return c
}

// Acted reports whether r changed anything or found what it cannot fix:
// what the owner hears about.
func (r Report) Acted() bool {
	c := r.Counts()
	return c.Stopped > 0 || c.Removed > 0 || c.Failed > 0 || c.Problems > 0
}

// Summary is one line about r.
func (r Report) Summary() string {
	c := r.Counts()
	if r.DryRun {
		return fmt.Sprintf("dry run: %d processes looked at; would stop or remove %d, would ask about %d, %d reported", r.Scanned, c.WouldStop, countOutcome(r, OutcomeWouldAsk), c.Reported-countOutcome(r, OutcomeWouldAsk))
	}
	return fmt.Sprintf("%d processes looked at; stopped %d, removed %d, asked about %d, %d failed, %d reported", r.Scanned, c.Stopped, c.Removed, c.Asked, c.Failed, c.Reported)
}

func countOutcome(r Report, o string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Outcome == o {
			n++
		}
	}
	return n
}

// Describe is one finding in a line: what, which, how much, why.
func (f Finding) Describe() string {
	s := f.Kind
	if f.PID > 0 {
		s += fmt.Sprintf(" PID %d (%s)", f.PID, f.Name)
		if n := len(f.Members); n > 1 {
			s += fmt.Sprintf(" with %d more", n-1)
		}
	} else if f.Target != "" {
		s += " " + f.Target
	}
	if f.CPUPercent > 0 || f.RSS > 0 {
		s += fmt.Sprintf(", %.0f%% CPU, %s", f.CPUPercent, Bytes(f.RSS))
	}
	if f.AgeSeconds > 0 {
		s += ", up " + (time.Duration(f.AgeSeconds) * time.Second).String()
	}
	return s + ": " + f.Reason
}
