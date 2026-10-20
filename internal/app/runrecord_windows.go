package app

import "github.com/Icewater1337/fridolin/internal/platform"

// runIsOurs reports whether rec's run is still going. procStart has no
// answer on Windows, so recordRun saves no run there and this is never
// asked; it says no rather than guess.
func runIsOurs(rec runRecord) bool {
	start, err := procStart(rec.PGID)
	return err == nil && start == rec.Start
}

// stopGroup ends the tree of processes under pgid at once: Windows has no
// SIGTERM to give it time first.
func stopGroup(pgid int) { _ = platform.KillProcessGroup(pgid) }
