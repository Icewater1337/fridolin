// Package mcp is a small Model Context Protocol server: JSON-RPC 2.0 over
// stdio, one message per line as Claude Code's and Codex's stdio
// transports send them, with initialize (and protocol version
// negotiation), ping, tools/list, tools/call and the notifications a
// client sends. It knows nothing of brains: a Handler supplies the tools,
// and Registry builds one from tools that each decide, per caller, whether
// that caller sees them.
package mcp
