package outbound

import (
	"fmt"
	"strings"
	"time"
)

// What !undo says (messages.go): the runs that changed files, what
// putting one back would do, and what it did.

// UndoRun is a run !undo lists.
type UndoRun struct {
	Run, Brain string
	// Summary is the record in a line (runsnap.Record.Summary).
	Summary string
	Ended   time.Time
	Undone  bool
}

// UndoStep is one file of an undo: its path, what the run did to it
// (created, changed, deleted) and what the undo does (restore, trash,
// none, conflict), with why for none and conflict; Done and Error once it
// was taken.
type UndoStep struct {
	Path, Op, Action, Why string
	Done                  bool
	Error                 string
}

// maxUndoLines bounds the files one !undo answer lists.
const maxUndoLines = 12

// ago says how long ago d was: "just now", "12 min ago", "3 h ago", "2 days
// ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	}
}

// undoLine is what undoing s does, as a line of the plan.
func undoLine(s UndoStep) string {
	switch s.Action {
	case "restore":
		if s.Op == "deleted" {
			return "bring back " + s.Path + " (the run deleted it)"
		}
		return "write back " + s.Path + " as it was"
	case "trash":
		return "move " + s.Path + " to the folder's trash (the run created it)"
	}
	return "leave " + s.Path + ": " + s.Why
}

// undoList writes the lines of steps, at most maxUndoLines.
func undoList(b *strings.Builder, steps []UndoStep, line func(UndoStep) string) {
	for i, s := range steps {
		if i == maxUndoLines {
			fmt.Fprintf(b, "\n• … and %d more (the dashboard's Audit page lists them all)", len(steps)-i)
			break
		}
		b.WriteString("\n• " + line(s))
	}
}

// undoCounts counts steps by action.
func undoCounts(steps []UndoStep) map[string]int {
	n := map[string]int{}
	for _, s := range steps {
		n[s.Action]++
	}
	return n
}

// files says n files.
func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}
