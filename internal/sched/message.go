package sched

import (
	"fmt"
	"strings"
)

// Task is what a brain run of a scheduled agent is handed.
type Task struct {
	Agent, When string
	// Trigger is what started it (TriggerSchedule, TriggerMissed,
	// TriggerManual, TriggerFride, TriggerRestartRetry).
	Trigger string
	// Report is the agent's report mode (manifest.ReportAlways, …), and
	// Chat where the result goes.
	Report, Chat string
	// Preapproved are the steps approved with the schedule.
	Preapproved []string
	// Text is the task itself.
	Text string
	// Fride is FrIDE's news that started a TriggerFride run, as the run
	// reads it.
	Fride string
	// GateData is what the agent's gate handed the run, as JSON; "" for
	// none.
	GateData string
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
	case TriggerFride:
		why = "runs now, out of its schedule, because FrIDE has news about coding work this agent handed it"
	case TriggerRestartRetry:
		why = "runs once more, because a restart of the bridge cut its last run short"
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
	if t.Trigger == TriggerFride {
		fmt.Fprintf(&sb, "FrIDE's news below started this run. Nobody answers FrIDE in WhatsApp: handle it yourself, as your task's rules say. Answer a question with fride_reply, deciding yourself, and when the work is merged close its thread with fride_close. Reply to the owner only when something merged, failed for good, or needs the owner's decision on something destructive or security-relevant, in a few lines; otherwise reply with exactly %s.\n\nFrIDE's news:\n\n%s\n\n", NothingToReport, strings.TrimSpace(t.Fride))
	}
	if t.Trigger == TriggerRestartRetry {
		fmt.Fprintf(&sb, "Check first. The previous attempt at this task was interrupted by a restart of the bridge partway through, so part of it may be done already. Before you act, find out what it did: your notes and memory files, this chat's history (schedule:%s), and the state of whatever the task changes (files, issues, messages, approvals asked for, work handed to FrIDE). Do not repeat work or deliveries that already happened; do what is left. Its reply never reached anyone, so reply as the task asks, for what it did and what you do now. This run is not retried again.\n\n", t.Agent)
	}
	if len(t.Preapproved) > 0 {
		sb.WriteString("The owner approved these steps together with the schedule: take them without request_approval. Every other irreversible step still needs it.\n")
		for _, p := range t.Preapproved {
			sb.WriteString("- " + p + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("The task:\n\n")
	sb.WriteString(strings.TrimSpace(t.Text))
	if t.GateData != "" {
		sb.WriteString("\n\nThe agent's gate, a program that ran before you and woke you, found this (JSON, as the program wrote it, not from the owner):\n\n```json\n")
		sb.WriteString(strings.TrimSpace(t.GateData))
		sb.WriteString("\n```")
	}
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
