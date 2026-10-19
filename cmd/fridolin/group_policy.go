package main

import (
	"fmt"
	"github.com/Icewater1337/fridolin/internal/logx"
	"slices"

	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/config"
)

// Who in an allowed group may reach its brain. allowed_groups lets a group
// in; group_members says which of its members count. A message that reaches
// the brain can start a run with every permission the brain has (one that
// addresses the bridge always, untagged chatter when the brain observes with
// runs), and anyone with invite rights can add a stranger to a group, so
// being in an allowed group is not enough.

// anyMember is the group_members entry that lets every member of the group
// in, as allowed_groups alone did before group_members existed.
const anyMember = "*"

// groupSender is who wrote a group message, as the policy sees them.
type groupSender struct {
	// Number is the sender's phone number, digits only, or their LID's
	// digits (another JID's user part, rarely) when nothing maps the LID
	// to a number.
	Number string
	// Resolved means Number is a phone number. A LID's digits are
	// WhatsApp's internal id, which no number in a list may match.
	Resolved bool
	// Server is the server of the JID Number comes from (lid for a LID),
	// which the log names along with a sender that is not Resolved.
	Server string
	// FromMe means the bridge's own account wrote it, from any of its
	// devices: the owner's phone, when the bridge runs on their number.
	FromMe bool
}

// groupSenderAllowed reports whether s may reach the brain of group, a group
// allowed_groups lets in. The bridge's own account always may. A group with
// a group_members entry admits the numbers it lists, or everyone for ["*"];
// one without admits whom allowed_numbers admits (numberAllowed): its
// numbers, or anyone while it is empty and allow_any_dm is true. A sender
// known only by a LID is admitted only where everyone is.
func groupSenderAllowed(cfg *config.File, group string, s groupSender) bool {
	return access.GroupSenderAllowed(policyOf(cfg), group, s.Number, s.Resolved, s.FromMe)
}

// masked is the sender for the log: a number down to its country code and
// last two digits, anything else (a LID, mostly) down to its last two and
// the server that names it.
func (s groupSender) masked() string {
	switch {
	case s.Resolved:
		return logx.MaskPhone(s.Number)
	case s.Server != "":
		return logx.MaskDigits(s.Number, 0) + "@" + s.Server
	}
	return logx.MaskDigits(s.Number, 0)
}

// groupHeardSummary says for fridolin check whose messages in the allowed
// group g reach its brain, as groupSenderAllowed decides, besides the
// bridge's own account, which always does.
func groupHeardSummary(cfg *config.File, g string) string {
	members, listed := cfg.GroupMembers[g]
	switch {
	case listed && slices.Equal(members, []string{anyMember}):
		return fmt.Sprintf("every member (group_members is [%q])", anyMember)
	case listed && len(members) == 0:
		return "nobody (its group_members entry lists no one)"
	case listed:
		return fmt.Sprintf("%v (group_members)", members)
	case cfg.AnyoneCanDM():
		return "every member (no group_members entry, allow_any_dm is true and allowed_numbers is empty)"
	case len(cfg.AllowedNumbers) == 0:
		return "nobody (no group_members entry, and allowed_numbers is empty)"
	}
	return fmt.Sprintf("%v (no group_members entry: allowed_numbers)", cfg.AllowedNumbers)
}
