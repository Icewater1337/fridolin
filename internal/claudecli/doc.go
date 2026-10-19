// Package claudecli owns the Claude and Codex command contracts, JSONL
// parsing, bounded process output, version probing and run supervision.
// Callers provide resolved commands, environment and callbacks for runtime
// effects. Process-group cleanup has one owner, with an acknowledgement for
// forced exit that does not wait for output hooks or reply delivery.
package claudecli
