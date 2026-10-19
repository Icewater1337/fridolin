package app

import (
	"github.com/Icewater1337/fridolin/internal/activity"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/history"
)

// seedActivity supplies the bounded history read while the feed keeps its
// original seed-once lock around loading and sorting the feed.
func seedActivity(a *activity.Feed, br *brain.Brain) {
	a.Seed(br.ID(), func() []history.Entry {
		return br.Tail(50, 128<<10)
	})
}
