package main

import "fridolin/internal/fsx"

// Aliases for what moved into packages below internal/, until the call
// sites name the packages themselves.
var (
	writeFileAtomic = fsx.WriteFileAtomic
	expandHome      = fsx.ExpandHome
	canonicalDir    = fsx.Canonical
	resolvePath     = fsx.Resolve
	isWithin        = fsx.Within
	tildePath       = fsx.Tilde
	fileExists      = fsx.Exists
)
