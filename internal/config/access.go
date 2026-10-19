package config

import (
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// ValidateGroupMembers rejects a group_members that cannot mean what it
// says: an entry for a chat that is no group, or for a group allowed_groups
// does not let in (its members would reach nothing), a number that is none,
// and "*" next to numbers, which would let everyone in while it looks like
// a list. It runs on the Resolve config, where every entry that is a
// number is already digits.
//
// The error names a group by its JID, which check --redact masks, but
// neither a key that is no group JID nor a number that is none: either may
// be a phone number written so that nothing recognises it as one, and the
// error goes into the bug report as it is.
func ValidateGroupMembers(members map[string][]string, allowedGroups []string) error {
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
			if n == configAnyMember {
				if len(list) > 1 {
					return fmt.Errorf("group_members: %s lists %q with numbers: use [%q] alone to let every member in, or only numbers", g, configAnyMember, configAnyMember)
				}
				continue
			}
			if _, err := chat.PhoneNumber(n); err != nil {
				return fmt.Errorf("group_members: %s: entry %d is not a phone number: use digits with the country code, e.g. 15551234567", g, i+1)
			}
		}
	}
	return nil
}

// GroupMembersAsNumbers is group_members with each number in the form the
// bridge compares, digits only, like allowed_numbers (chat.NumbersAsNumbers).
func GroupMembersAsNumbers(members map[string][]string) map[string][]string {
	if members == nil {
		return nil
	}
	out := make(map[string][]string, len(members))
	for g, list := range members {
		if slices.Equal(list, []string{configAnyMember}) {
			out[g] = list
			continue
		}
		out[g] = chat.NumbersAsNumbers(list)
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

// WarnGroupsWithoutMembers logs, once for each, the allowed groups of next
// that admit only allowed_numbers and did not in prev (nil at startup), so
// that an owner whose groups trusted every member before group_members
// existed sees why the others go unheard, and how to change it.
func WarnGroupsWithoutMembers(prev, next *Config) {
	before := onlyAllowedNumbers(prev)
	for _, g := range onlyAllowedNumbers(next) {
		if slices.Contains(before, g) {
			continue
		}
		log.Printf("config: group %s has no group_members entry: only allowed_numbers can start runs there, and what other members write is not read; an entry of [%q] for it lets every member in, as before, or list their numbers", g, configAnyMember)
	}
}

// GroupMemberPairs is group_members as a sorted list of "group:number"
// entries, and "group:" for one that lists nobody, so that
// logSecurityChanges can say who was let in or out of which group.
func GroupMemberPairs(members map[string][]string) []string {
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

// The form allowed_groups holds. Every edit of the allow lists goes
// through chat.PhoneNumber and AllowListGroup, so the dashboard's add boxes and
// its Allow buttons agree on what they accept.
var groupJIDRE = regexp.MustCompile(`^[0-9]+(-[0-9]+)?@g\.us$`)

// AllowListGroup is s as allowed_groups holds it: a group JID.
func AllowListGroup(s string) (string, error) {
	g := strings.TrimSpace(s)
	if !groupJIDRE.MatchString(g) {
		return "", fmt.Errorf("%q is not a group JID: it looks like 120363000000000000@g.us", g)
	}
	return g, nil
}

const configAnyMember = "*"
