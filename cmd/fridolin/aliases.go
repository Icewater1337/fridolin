package main

import "fridolin/internal/version"

// Aliases for what moved into packages below internal/, until the call
// sites name the packages themselves.
var (
	bridgeVersion = version.String
	versionLine   = version.Line
)
