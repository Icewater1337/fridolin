package main

import (
	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/config"
)

// policyOf is the command's read-only projection from a config snapshot.
// The policy never retains or rereads the config store.
func policyOf(cfg *config.File) access.Policy {
	return access.Policy{AllowedNumbers: cfg.AllowedNumbers, AllowAnyDM: cfg.AllowsAnyDM(), AllowedGroups: cfg.AllowedGroups, GroupMembers: cfg.GroupMembers, Triggers: cfg.Triggers}
}

// numberAllowed compares number and the list as phone numbers, digits
// only, so that "+1 555 123 4567" in a config the bridge did not write
// still lets 15551234567 in. Anything that is no phone number is compared
// as it is.
func numberAllowed(cfg *config.File, number string) bool {
	return access.NumberAllowed(policyOf(cfg), number)
}

// isAllowedGroup reports whether the group JID is in cfg's allowed_groups.
// Unlike DMs, groups are deny-by-default: an empty list means no groups.
func isAllowedGroup(cfg *config.File, jid string) bool {
	return access.GroupAllowed(policyOf(cfg), jid)
}
