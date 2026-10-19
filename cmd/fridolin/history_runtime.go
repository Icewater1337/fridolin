package main

import "github.com/Icewater1337/fridolin/internal/history"

func (br *Brain) Tail(n int, budget int64) []history.Entry {
	return history.Tail(br.root, br.id, n, budget)
}
