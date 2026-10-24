package app

import (
	"strings"
	"testing"
)

func TestApplicationFallbackRingRecordsShutdown(t *testing.T) {
	t.Parallel()
	b := newTestBridge(t, "")
	b.Stop()
	lines, _ := b.logs.Since(0, 0)
	for _, line := range lines {
		if strings.HasSuffix(line.Text, "Bridge stopped.") {
			return
		}
	}
	t.Fatalf("fallback dashboard log did not record shutdown: %v", lines)
}
