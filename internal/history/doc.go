// Package history reads, appends and rewrites a brain's conversation history
// through its confined folder handle. It does not know about routing, inbound
// queues, the outbox or delivery; callers own serialization and callbacks.
package history
