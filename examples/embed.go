// Package examples is the starter content of a new brain folder: its
// prompt template and skills, compiled into the binary.
package examples

import "embed"

// FS is the prompt template and the skills a new brain gets, and the
// built-in tasks of scheduled agents (schedules/, see internal/sched).
//
//go:embed brain-prompt.example.md skills/*.md schedules/*.md
var FS embed.FS
