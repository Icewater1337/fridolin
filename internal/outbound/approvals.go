package outbound

import (
	"fmt"
	"time"
)

// The owner's approvals in their direct chat: everything that waits for
// them (a node, its commands or core-host, a scheduled agent, a changed
// brain.json, a skill, a run's request) is told the same way, in plain
// words, and answered with !approve or !deny alone. The id comes last,
// for reference: nobody has to type it.

// OwnerApproval is one thing that waits for the owner's approval, as an
// alert, a poll option and a reply name it.
type OwnerApproval struct {
	// Subject says what wants what, in one line: "*laptop* (Windows)
	// wants to join as a node".
	Subject string
	// Label names it in a reply: "*laptop* as a node".
	Label string
	// When it started; zero when the bridge cannot tell.
	When time.Time
	// Facts are where it came from and how it was started, each "Label:
	// what".
	Facts []string
	// Allows says what approving lets it do, one sentence each.
	Allows []string
	// Note is a paragraph after Allows: what approving means, when it
	// means much (core-host).
	Note string
	// Unexpected says why the owner may not have started it, "" when
	// they did (a join code from !join or the dashboard).
	Unexpected string
	// Ref is its id, for `!approve <ref>`.
	Ref string
	// Option is its line in a poll: short, plain, no formatting.
	Option string
}

// ApprovalExpiredWhy says that something waited longer than it should
// have, since when.
func ApprovalExpiredWhy(since, now time.Time) string {
	return "It has waited since " + when(since, now) + ", longer than approvals are good for."
}

// when says when t was, for an approval: "Fri 23 Oct 14:03 (5 minutes
// ago)".
func when(t, now time.Time) string {
	s := t.In(now.Location()).Format("Mon 2 Jan 15:04")
	switch d := now.Sub(t); {
	case d < 0:
	case d < time.Minute:
		s += " (just now)"
	case d < 2*time.Minute:
		s += " (a minute ago)"
	case d < time.Hour:
		s += fmt.Sprintf(" (%d minutes ago)", int(d/time.Minute))
	case d < 2*time.Hour:
		s += " (an hour ago)"
	case d < 48*time.Hour:
		s += fmt.Sprintf(" (%d hours ago)", int(d/time.Hour))
	default:
		s += fmt.Sprintf(" (%d days ago)", int(d/(24*time.Hour)))
	}
	return s
}
