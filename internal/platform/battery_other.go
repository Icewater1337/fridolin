//go:build !linux

package platform

// Battery reports no battery: reading one is not cheap here (macOS needs
// IOKit or pmset, Windows a WMI query), so a node leaves it out.
func Battery() (int, bool) { return 0, false }
