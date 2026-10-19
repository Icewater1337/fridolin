package main

import "github.com/Icewater1337/fridolin/internal/history"

// scanHistory reads through the brain's root; the file reader owns no
// runtime state and never accesses the queue, outbox or history hooks.
func (br *Brain) scanHistory(budget int64, want func([]byte) bool, visit func(history.Entry) bool) {
	history.Scan(br.root, br.id, budget, want, visit)
}

func (br *Brain) recentHistory(n int, budget int64) []history.Entry {
	return history.Tail(br.root, br.id, n, budget)
}
