package app

import (
	"fmt"
	"strings"
)

const buildChatSuffix = "@fridolin.dashboard"

func buildChatJID(brainID string, thread int) string {
	return fmt.Sprintf("site-build-%s-%d%s", brainID, thread, buildChatSuffix)
}

// brainOfBuildJID is the brain a build chat JID belongs to, or "".
func brainOfBuildJID(jid string) string {
	rest, ok := strings.CutSuffix(jid, buildChatSuffix)
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(rest, "site-build-")
	if !ok {
		return ""
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}
