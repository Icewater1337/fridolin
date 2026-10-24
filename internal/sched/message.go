package sched

import (
	"fmt"
	"strings"
)

// Task is what a brain run of a scheduled agent is handed.
type Task struct {
	Agent, When string
	// Trigger is what started it (TriggerSchedule, TriggerMissed,
	// TriggerManual).
	Trigger string
	// Report is the agent's report mode (manifest.ReportAlways, …), and
	// Chat where the result goes.
	Report, Chat string
	// Preapproved are the steps approved with the schedule.
	Preapproved []string
	// Text is the task itself.
	Text string
}

// The report modes, as manifest names them (sched does not import it).
const (
	reportAlways = "always"
	reportSilent = "silent"
	// NothingToReport is manifest.NothingToReport.
	NothingToReport = "NOTHING_TO_REPORT"
)

// Message is the message a brain run of t is handed: what it is, where
// its reply goes, the steps it may take without asking, and the task.
func Message(t Task) string {
	var sb strings.Builder
	why := "is due"
	switch t.Trigger {
	case TriggerMissed:
		why = "fell due while the bridge was down, and runs now"
	case TriggerManual:
		why = "was started by hand from the dashboard or the CLI"
	}
	fmt.Fprintf(&sb, "Scheduled agent %q (%s) %s. The owner set it up and approved it in this brain's brain.json; this message comes from the bridge, not from a person, and nobody reads this chat while you work.\n\n", t.Agent, t.When, why)
	switch t.Report {
	case reportAlways:
		fmt.Fprintf(&sb, "Your reply is sent as it is to %s: write it for the owner.", t.Chat)
	case reportSilent:
		sb.WriteString("Your reply is sent to nobody: it is kept in the run's log on the dashboard. Do not message anyone.")
	default:
		fmt.Fprintf(&sb, "Your reply is sent to %s only if it is worth the owner's attention. If there is nothing worth reporting, reply with exactly %s and nothing else.", t.Chat, NothingToReport)
	}
	sb.WriteString(" You cannot ask questions mid-run: decide yourself, or say in your reply what you need.\n\n")
	if len(t.Preapproved) > 0 {
		sb.WriteString("The owner approved these steps together with the schedule: take them without request_approval. Every other irreversible step still needs it.\n")
		for _, p := range t.Preapproved {
			sb.WriteString("- " + p + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("The task:\n\n")
	sb.WriteString(strings.TrimSpace(t.Text))
	return sb.String()
}

// Worth reports whether a reply of a ReportWorth agent is worth sending:
// not empty, and not NothingToReport (alone on its last line counts too,
// as models add a word before it).
func Worth(reply string) bool {
	r := strings.TrimSpace(reply)
	if r == "" || strings.Trim(r, "*`_ .") == NothingToReport {
		return false
	}
	lines := strings.Split(r, "\n")
	return strings.Trim(strings.TrimSpace(lines[len(lines)-1]), "*`_ .") != NothingToReport
}
