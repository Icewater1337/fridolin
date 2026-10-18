//go:build !unix

package platform

// Fridolin runs on macOS and Linux only: it needs flock, process groups
// and a POSIX shell. This name is defined nowhere, so on any other system
// the build stops here with an error that says why.
var _ = Fridolin_runs_on_macOS_and_Linux_only
