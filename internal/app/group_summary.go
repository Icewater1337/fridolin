package app

import (
	"fmt"
	"slices"

	"github.com/Icewater1337/fridolin/internal/config"
)

// groupHeardSummary says for fridolin check whose messages in the allowed
// group g reach its brain, as groupSenderAllowed decides, besides the
// bridge's own account, which always does.
func groupHeardSummary(cfg *config.File, g string) string {
	members, listed := cfg.GroupMembers[g]
	switch {
	case listed && slices.Equal(members, []string{AnyMember}):
		return fmt.Sprintf("every member (group_members is [%q])", AnyMember)
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
