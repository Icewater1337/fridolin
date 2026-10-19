package app

import "time"

// KillRuns interrupts active provider processes when a second termination signal
// asks for immediate shutdown. Ordinary cancellation retains the drain grace.
func (b *App) KillRuns() { b.brainMgr.KillRuns() }

// NewLoginCode creates a short-lived, single-use dashboard login code. The CLI
// may show it only on an interactive terminal or pass it directly to a browser.
func (b *App) NewLoginCode() (string, time.Time) { return b.sessions.NewCode() }
