package google

import (
	"testing"
	"time"
)

// TestGrantHealthStates: a grant that worked is valid, expiring once
// Google ends it within two days, needing a new consent after, and broken
// whatever its expiry once Google refused it.
func TestGrantHealthStates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { e := now.Add(d); return &e }
	ok := grantRecord{LastOK: now.Add(-time.Hour)}
	for _, tc := range []struct {
		name    string
		r       grantRecord
		expires *time.Time
		want    string
	}{
		{"never used", grantRecord{}, nil, GrantUnchecked},
		{"worked", ok, nil, GrantValid},
		{"far from its end", ok, at(6 * 24 * time.Hour), GrantValid},
		{"ends tomorrow", ok, at(20 * time.Hour), GrantExpiring},
		{"ended", ok, at(-time.Minute), GrantReconsent},
		{"revoked", grantRecord{LastOK: now.Add(-time.Hour), Broken: GrantRevoked, Since: now}, at(20 * time.Hour), GrantRevoked},
	} {
		if got := tc.r.health(tc.expires, now); got.State != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got.State, tc.want)
		}
	}
}
