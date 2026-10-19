package app

import "errors"

// ErrRestart is the cancellation cause for an application-requested restart.
// The CLI replaces the process only after the application's teardown completes.
var ErrRestart = errors.New("restart requested")
