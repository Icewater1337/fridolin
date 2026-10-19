// Package registry resolves configured brains and owns their folder and
// configuration workflows. Resolution and edits receive explicit inputs and
// caller-supplied security checks. Manifest/config changes use the Store's
// retained-root transaction; scaffolding retains ownership for rollback.
// Runtime reconciliation and HTTP decoding remain the caller's responsibility.
package registry
