//go:build race

package main

// This test binary runs with the race detector, so TestSmoke builds the
// bridge with it too.
func init() { raceDetector = true }
