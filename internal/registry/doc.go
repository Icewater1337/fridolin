// Package registry resolves configured brains and prepares their folders.
// Resolution receives explicit references and a caller-supplied security
// checker; path checks also receive the home and other configured folders.
// The scaffold transaction retains handles and ownership records so rollback
// removes only unchanged files it created. Configuration registration and
// runtime reconciliation still belong to the caller.
package registry
