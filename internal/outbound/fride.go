package outbound

import "strings"

// FrideNews is FrIDE's news about a thread (its webhook, relayed by the
// node), as FrideNewsMessage says it in the chat that started the work.
type FrideNews struct {
	// Thread is fridolin's number of the thread, which !move and the core
	// brain's tools take; Node is the name of the node FrIDE runs on.
	Thread int64
	Node   string
	// Kind is started, update, question, done or failed.
	Kind string
	// Text is FrIDE's own summary, ready for a person.
	Text                    string
	Task, Branch, Rationale string
}

// frideBody is FrIDE's summary without its first line when that only
// names the inbox thread ("FrIDE inbox #7 · needs your reply"): this chat
// knows the thread by fridolin's number.
func frideBody(text string) string {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	if strings.HasPrefix(first, "FrIDE inbox #") {
		return strings.TrimSpace(rest)
	}
	return text
}
