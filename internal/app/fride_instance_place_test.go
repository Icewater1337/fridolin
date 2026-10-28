package app

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFrideTries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		fixed int
		size  string
		want  []int
	}{
		{0, "", []int{6144, 3072}},
		{0, frideSizeMedium, []int{6144, 3072}},
		{0, frideSizeLarge, []int{12288, 6144, 3072}},
		{0, frideSizeSmall, []int{3072}},
		// A fixed memory overrides the size, and never shrinks.
		{8192, frideSizeSmall, []int{8192}},
		{4096, "", []int{4096}},
	} {
		if got := frideTries(c.fixed, c.size); !slices.Equal(got, c.want) {
			t.Errorf("frideTries(%d, %q) = %v, want %v", c.fixed, c.size, got, c.want)
		}
	}
	for _, s := range []string{"", "small", "medium", "large"} {
		if !validFrideSize(s) {
			t.Errorf("%q is no size", s)
		}
	}
	if validFrideSize("huge") {
		t.Error("huge is a size")
	}
}

// Placement picks the host by room, prefers the one with the instance's
// sandbox, makes room by stopping idle instances of other trust domains,
// never one with an open thread, then shrinks, then gives up.
func TestPlaceFrideInstance(t *testing.T) {
	t.Parallel()
	old, older := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		tries []int
		hosts []frideHost
		want  frideSpot
		ok    bool
	}{
		{name: "no hosts", tries: []int{6144, 3072}},
		{
			name: "the most room", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 7000}, {ID: "b", FreeMB: 9000}, {ID: "c", FreeMB: 2000}},
			want:  frideSpot{Node: "b", MemoryMB: 6144}, ok: true,
		},
		{
			name: "a tie goes by id", tries: []int{6144},
			hosts: []frideHost{{ID: "b", FreeMB: 7000}, {ID: "a", FreeMB: 7000}},
			want:  frideSpot{Node: "a", MemoryMB: 6144}, ok: true,
		},
		{
			name: "the host with its sandbox, when it fits", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 7000, Has: true}, {ID: "b", FreeMB: 20000}},
			want:  frideSpot{Node: "a", MemoryMB: 6144}, ok: true,
		},
		{
			name: "its sandbox's host does not fit: the most room", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 5000, Has: true}, {ID: "b", FreeMB: 7000}},
			want:  frideSpot{Node: "b", MemoryMB: 6144}, ok: true,
		},
		{
			name: "stop the idle other, the longest idle first, just enough", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 1000, Running: []frideGuest{
				{Name: "fridolin", MemoryMB: 6144, Active: old},
				{Name: "site", MemoryMB: 6144, Active: older},
			}}},
			want: frideSpot{Node: "a", MemoryMB: 6144, Stop: []string{"site"}}, ok: true,
		},
		{
			name: "never one with an open thread: a smaller size instead", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 4000, Running: []frideGuest{{Name: "fridolin", MemoryMB: 6144, Open: 1}}}},
			want:  frideSpot{Node: "a", MemoryMB: 3072, Less: true}, ok: true,
		},
		{
			name: "making room comes before a smaller size", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 4000, Running: []frideGuest{{Name: "fridolin", MemoryMB: 3072}}}},
			want:  frideSpot{Node: "a", MemoryMB: 6144, Stop: []string{"fridolin"}}, ok: true,
		},
		{
			name: "room is made on the host with the most room that gets enough", tries: []int{12288},
			hosts: []frideHost{
				{ID: "a", FreeMB: 9000, Running: []frideGuest{{Name: "x", MemoryMB: 3072, Open: 2}}},
				{ID: "b", FreeMB: 4000, Running: []frideGuest{{Name: "y", MemoryMB: 6144, Active: old}, {Name: "z", MemoryMB: 3072, Active: older}}},
			},
			want: frideSpot{Node: "b", MemoryMB: 12288, Stop: []string{"z", "y"}}, ok: true,
		},
		{
			name: "down to small", tries: []int{12288, 6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: 3500}},
			want:  frideSpot{Node: "a", MemoryMB: 3072, Less: true}, ok: true,
		},
		{
			name: "a fixed memory does not shrink", tries: []int{8192},
			hosts: []frideHost{{ID: "a", FreeMB: 7000}},
		},
		{
			name: "nothing fits", tries: []int{6144, 3072},
			hosts: []frideHost{{ID: "a", FreeMB: -500, Running: []frideGuest{{Name: "fridolin", MemoryMB: 6144, Open: 1}}}},
		},
		{
			name: "the instance itself is never in the way", tries: []int{6144},
			hosts: []frideHost{{ID: "a", FreeMB: 1000, Running: []frideGuest{{Name: "work", MemoryMB: 6144}}}},
		},
	} {
		got, ok := placeFrideInstance("work", c.tries, c.hosts)
		if ok != c.ok || got.Node != c.want.Node || got.MemoryMB != c.want.MemoryMB || got.Less != c.want.Less || !slices.Equal(got.Stop, c.want.Stop) {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestFrideNoRoom(t *testing.T) {
	t.Parallel()
	got := frideNoRoom("work", []int{6144, 3072}, []frideHost{
		{Name: "mini", FreeMB: 1000, Running: []frideGuest{{Name: "fridolin", MemoryMB: 6144, Open: 2}, {Name: "site", MemoryMB: 3072, Open: 1}}},
		{Name: "laptop", FreeMB: -200},
	})
	want := "no node has room for the FrIDE instance work (6144 MB, or 3072 MB at the least): mini has 1000 MB free beyond its reserve and runs fridolin (6144 MB, 2 open threads), site (3072 MB, 1 open thread); laptop has 0 MB free beyond its reserve"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := frideNoRoom("work", []int{8192}, nil); !strings.HasSuffix(got, "(8192 MB): ") {
		t.Errorf("a fixed memory: %s", got)
	}
}
