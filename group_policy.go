package main

import (
	"fmt"
	"log"
	"slices"
	"sort"
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
func groupSenderAllowed(cfg *Config, group string, s groupSender) bool {
	if s.FromMe {
		return true
	}
	members, listed := cfg.GroupMembers[group]
	switch {
	case listed && slices.Equal(members, []string{anyMember}):
		return true
	case !listed && cfg.AnyoneCanDM():
		return true
	case !s.Resolved:
		return false
	case listed:
		number := s.Number
		if n, err := phoneNumber(number); err == nil {
			number = n
		}
		return slices.Contains(allowedNumbersAsNumbers(members), number)
	default:
		return numberAllowed(cfg, s.Number)
	}
}

// masked is the sender for the log: a number down to its country code and
// last two digits, anything else (a LID, mostly) down to its last two and
// the server that names it.
func (s groupSender) masked() string {
	switch {
	case s.Resolved:
		return maskPhone(s.Number)
	case s.Server != "":
		return maskDigits(s.Number, 0) + "@" + s.Server
	}
	return maskDigits(s.Number, 0)
}

// validateGroupMembers rejects a group_members that cannot mean what it
// says: an entry for a chat that is no group, or for a group allowed_groups
// does not let in (its members would reach nothing), a number that is none,
// and "*" next to numbers, which would let everyone in while it looks like
// a list. It runs on the effective config, where every entry that is a
// number is already digits.
//
// The error names a group by its JID, which check --redact masks, but
// neither a key that is no group JID nor a number that is none: either may
// be a phone number written so that nothing recognises it as one, and the
// error goes into the bug report as it is.
func validateGroupMembers(members map[string][]string, allowedGroups []string) error {
	groups := make([]string, 0, len(members))
	for g := range members {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		if !groupJIDRE.MatchString(g) {
			return fmt.Errorf("group_members: a key is not a group JID: each key is a group from allowed_groups, written like 120363000000000000@g.us")
		}
		if !slices.Contains(allowedGroups, g) {
			return fmt.Errorf("group_members: %s is not in allowed_groups: allow the group too, or remove its entry", g)
		}
		list := members[g]
		for i, n := range list {
			if n == anyMember {
				if len(list) > 1 {
					return fmt.Errorf("group_members: %s lists %q with numbers: use [%q] alone to let every member in, or only numbers", g, anyMember, anyMember)
				}
				continue
			}
			if _, err := phoneNumber(n); err != nil {
				return fmt.Errorf("group_members: %s: entry %d is not a phone number: use digits with the country code, e.g. 15551234567", g, i+1)
			}
		}
	}
	return nil
}

// groupMembersAsNumbers is group_members with each number in the form the
// bridge compares, digits only, like allowed_numbers (allowedNumbersAsNumbers).
func groupMembersAsNumbers(members map[string][]string) map[string][]string {
	if members == nil {
		return nil
	}
	out := make(map[string][]string, len(members))
	for g, list := range members {
		if slices.Equal(list, []string{anyMember}) {
			out[g] = list
			continue
		}
		out[g] = allowedNumbersAsNumbers(list)
	}
	return out
}

// onlyAllowedNumbers lists the groups of c in which only allowed_numbers
// reach the brain: allowed and without a group_members entry, unless an
// empty allowed_numbers with allow_any_dm lets everyone in anyway.
func onlyAllowedNumbers(c *Config) []string {
	if c == nil || c.AnyoneCanDM() {
		return nil
	}
	var out []string
	for _, g := range c.AllowedGroups {
		if _, listed := c.GroupMembers[g]; !listed {
			out = append(out, g)
		}
	}
	return out
}

// warnGroupsWithoutMembers logs, once for each, the allowed groups of next
// that admit only allowed_numbers and did not in prev (nil at startup), so
// that an owner whose groups trusted every member before group_members
// existed sees why the others go unheard, and how to change it.
func warnGroupsWithoutMembers(prev, next *Config) {
	before := onlyAllowedNumbers(prev)
	for _, g := range onlyAllowedNumbers(next) {
		if slices.Contains(before, g) {
			continue
		}
		log.Printf("config: group %s has no group_members entry: only allowed_numbers can start runs there, and what other members write is not read; an entry of [%q] for it lets every member in, as before, or list their numbers", g, anyMember)
	}
}

// groupMemberPairs is group_members as a sorted list of "group:number"
// entries, and "group:" for one that lists nobody, so that
// logSecurityChanges can say who was let in or out of which group.
func groupMemberPairs(members map[string][]string) []string {
	var out []string
	for g, list := range members {
		if len(list) == 0 {
			out = append(out, g+":")
		}
		for _, n := range list {
			out = append(out, g+":"+n)
		}
	}
	sort.Strings(out)
	return out
}
