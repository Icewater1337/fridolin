package outbound

import (
	"fmt"
	"time"
)

// FleetNode is one node as !fleet lists it (FleetReply).
type FleetNode struct {
	ID, Name, OS string
	Online       bool
	// LastSeen is when the core last heard from it, zero for never.
	LastSeen time.Time
	// Device and Commands are the two approval stages (nodeproto).
	Device, Commands string
	Tags             []string
	// Can is what it may do now, one plain sentence each
	// (nodeproto.Capabilities.Plain).
	Can []string
	// CoreHostPending is set while its core-host waits for approval.
	CoreHostPending bool
	// Fride is set for a node whose fride is approved, which is arbitrary
	// execution there (nodeproto.FrideApproval); FridePending while its
	// fride waits for approval.
	Fride, FridePending bool
}

func countNodes(n int) string {
	if n == 1 {
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}

func osLabel(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return goos
}

// seen says whether n is online, or how long ago it was last heard from.
func seen(n FleetNode, now time.Time) string {
	if n.Online {
		return "online"
	}
	if n.LastSeen.IsZero() {
		return "never seen"
	}
	switch d := now.Sub(n.LastSeen); {
	case d < time.Minute:
		return "last seen just now"
	case d < time.Hour:
		return fmt.Sprintf("last seen %dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("last seen %dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("last seen %dd ago", int(d/(24*time.Hour)))
	}
}
