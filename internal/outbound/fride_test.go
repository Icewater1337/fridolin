package outbound

import (
	"strings"
	"testing"
)

func TestFrideShort(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"one\ntwo", "one\ntwo"},
		{"one\n\n\ntwo\nthree", "one\ntwo\nthree"},
		{"1\n2\n3\n4", "1\n2\n3\n" + FrideMore},
		{strings.Repeat("x", 301), strings.Repeat("x", 300) + "\n" + FrideMore},
	} {
		if got := frideShort(tc.in); got != tc.want {
			t.Errorf("frideShort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFrideBookkeepingAndAsks(t *testing.T) {
	for in, want := range map[string]bool{
		"Created task fix-1 on `api`, with new branch fix/1":       true,
		"FrIDE inbox #3 · update\n• → agent: Fix GitHub issue #12": true,
		"Created task fix-1\nThe tests fail on main.":              false,
		"PR merged": false,
	} {
		if got := FrideBookkeeping(in); got != want {
			t.Errorf("FrideBookkeeping(%q) = %v", in, got)
		}
	}
	for in, want := range map[string]bool{
		"Which empty state?": true,
		"FrIDE inbox #2 · needs your reply\nThe agent is still working; I'd resend it": false,
		"Please confirm the plan.": true,
		"Should I force-push":      true,
	} {
		if got := FrideAsks(in); got != want {
			t.Errorf("FrideAsks(%q) = %v", in, got)
		}
	}
}

func TestFrideFanoutMessage(t *testing.T) {
	got := FrideFanoutMessage("mini", "update", []int64{9, 10, 15}, "FrIDE inbox #9 · update\nAll merged.")
	if want := "🛠️ *FrIDE on mini* (#9, #10, #15):\nAll merged."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
