// Package memindex is a brain's memory index: a SQLite FTS5 index over
// the brain's markdown memories (memories/**/*.md), its conversation
// history (conversation-history.jsonl) and its transcripts
// (transcripts/**), derived from those files and rebuildable from them at
// any time. The files stay the truth.
//
// Every chunk carries a provenance class the indexer sets from where the
// text came, never from what it says: owner (typed by an allowed number
// in a direct chat, on WhatsApp), agent (written by the brain itself in
// memories/), untrusted (other people, groups, files handed in,
// transcripts, web pages) and system (what went out to a chat, the
// brain's own replies included, and the bridge's schedules).
// The index lives in the bridge's data dir, which the sandbox keeps from
// brains, so a brain can change what its notes say but not the class the
// index gives them. Only owner and agent chunks are ever put into a
// prompt without being asked for (see Recall); a search the brain makes
// itself returns the others too, labelled.
package memindex
