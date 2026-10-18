package main

import "fridolin/internal/trigger"

// Aliases for what moved into packages below internal/, until the call
// sites name the packages themselves.
type TriggerList = trigger.List

var (
	TriggersFromStrings = trigger.FromStrings
	boundaryAt          = trigger.BoundaryAt
	boundaryBefore      = trigger.BoundaryBefore
)
