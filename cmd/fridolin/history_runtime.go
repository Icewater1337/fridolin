package main

// scanHistory reads through the brain's root; the file reader owns no
// runtime state and never accesses the queue, outbox or history hooks.
func (br *Brain) scanHistory(budget int64, want func([]byte) bool, visit func(historyEntry) bool) {
	scanHistoryFile(br.root, br.id, budget, want, visit)
}

func (br *Brain) recentHistory(n int, budget int64) []historyEntry {
	return recentHistoryFile(br.root, br.id, n, budget)
}
