package main

import "fridolin/internal/textx"

// Aliases for what moved into packages below internal/, until the call
// sites name the packages themselves.
var (
	truncate = textx.Truncate
	tail     = textx.Tail
)
