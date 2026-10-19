// Package archtest holds the tests that keep the module's layout to the
// rules in docs/ARCHITECTURE.md: which package may import which, that the
// packages below internal/ do not read the process's environment (but for
// those ambientAllowed names), and that the repository's root has no Go
// files. It has no code of its own.
package archtest
