// Package throwaway227 holds a fuzz target that fails on purpose, to try
// the CI report of #227 on a throwaway branch. Never merged.
package throwaway227

import "testing"

func FuzzThrowawayFor227(f *testing.F) {
	f.Add("ab")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) < 0 {
			t.Fatalf("an input longer than 4 bytes: %d", len(s))
		}
	})
}
