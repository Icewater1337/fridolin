// Package httpapi owns HTTP request guards, tokens, JSON encoding and static file responses.
// It receives authentication policy and session checks from its caller; it
// does not read configuration or manage brains, transports or their lifecycle.
// Resource handlers and the server move here in the later HTTP extraction.
package httpapi
