//go:build unix && !darwin && !linux

package main

import "errors"

// errNoProcInfo: on this system the bridge cannot tell when a process
// started, so it records no runs and stops none after a crash.
var errNoProcInfo = errors.New("process start times are not available on this system")

func procStart(int) (string, error) { return "", errNoProcInfo }

func bootID() (string, error) { return "", errNoProcInfo }
