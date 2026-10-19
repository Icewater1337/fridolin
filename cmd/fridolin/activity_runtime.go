package main

import (
	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/history"
)

// seedActivity supplies the bounded history read while the feed keeps its
// original seed-once lock around loading and sorting the feed.
func seedActivity(a *activity.Feed, br *Brain) {
	a.Seed(br.ID(), func() []history.Entry {
		return br.recentHistory(50, 128<<10)
	})
}
