package cli

import (
	"log"
	"testing"

	"github.com/Icewater1337/fridolin/internal/logx"
)

// captureLog collects what the bridge logs until the test ends.
func captureLog(t *testing.T) func() []string {
	t.Helper()
	ring := logx.NewRing(50)
	prev := log.Writer()
	log.SetOutput(ring)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		lines, _ := ring.Since(0, 50)
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return out
	}
}
