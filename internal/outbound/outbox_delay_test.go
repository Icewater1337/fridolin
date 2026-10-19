package outbound

import (
	"testing"
	"time"
)

func TestOutboxDelay(t *testing.T) {
	cases := map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 5: 80 * time.Second, 6: outboxMaxRetry, 40: outboxMaxRetry}
	for attempts, want := range cases {
		if got := outboxDelay(attempts); got != want {
			t.Errorf("outboxDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}
