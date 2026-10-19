// Package whatsapp owns the WhatsApp transport, per-chat intake workers,
// authorization, attachment handling, pairing, connection retries and caches.
// Command composition supplies settings, persistence and runtime callbacks.
// HandleEvent preserves production event ordering; synchronous ProcessMessage
// and ProcessVote callers are responsible for their own ordering.
package whatsapp
