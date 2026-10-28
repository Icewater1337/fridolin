package app

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Where a FrIDE instance starts, and with how much memory (docs/NODES.md,
// "The core and FrIDE instances"): pure, so that the rules have table
// tests. The runtime (fride_instances.go) reads the nodes and acts on what
// placeFrideInstance decides.

// The sizes a start asks for: small for code and unit tests, medium with
// the project's stack, large for a heavy stack.
const (
	frideSizeSmall  = "small"
	frideSizeMedium = "medium"
	frideSizeLarge  = "large"
)

// frideSizeMB is each size's memory.
var frideSizeMB = map[string]int{frideSizeSmall: 3072, frideSizeMedium: 6144, frideSizeLarge: 12288}

// frideSizes are the sizes, the largest first.
var frideSizes = []string{frideSizeLarge, frideSizeMedium, frideSizeSmall}

// validFrideSize reports whether size is "" (the default) or a size.
func validFrideSize(size string) bool {
	_, ok := frideSizeMB[size]
	return size == "" || ok
}

// frideTries are the memories a start tries, in order: a fixed memory
// from the config alone, else size's (medium when "") and then each
// smaller size down to small.
func frideTries(fixedMB int, size string) []int {
	if fixedMB > 0 {
		return []int{fixedMB}
	}
	if size == "" {
		size = frideSizeMedium
	}
	var out []int
	for _, s := range frideSizes {
		if mb := frideSizeMB[s]; mb <= frideSizeMB[size] {
			out = append(out, mb)
		}
	}
	return out
}

// frideHost is a node that may run the instance being placed: approved,
// online, offering fride_host ready, and one the instance's config allows.
type frideHost struct {
	ID, Name string
	// FreeMB is what the machine can give now less its reserve, with the
	// memory of the instance being placed added back when it runs there
	// already (a start at another size remakes it there).
	FreeMB int
	// Has says the node has a sandbox of the instance being placed: its
	// disk, with its repositories and history, is there.
	Has bool
	// Running are the other instances that run (or start) there.
	Running []frideGuest
}

// frideGuest is an instance running on a host.
type frideGuest struct {
	Name     string
	MemoryMB int
	// Open is how many open threads it has: one with any is never stopped
	// to make room.
	Open int
	// Active is when it last had work or news.
	Active time.Time
}

// frideSpot is where placement starts the instance.
type frideSpot struct {
	// Node is the host's id.
	Node     string
	MemoryMB int
	// Stop are the idle instances of other trust domains to stop on Node
	// first, to make room.
	Stop []string
	// Less says the instance gets less memory than the first try.
	Less bool
}

// placeFrideInstance decides where instance starts, given the memories
// to try (frideTries) and the hosts it may run on. For each memory in
// turn: a host where it fits, the one that has its sandbox first, then
// the one with the most room; else a host where stopping idle instances
// of other trust domains (no open thread; the longest idle first) makes
// room, the one with the most room first. An instance with an open
// thread is never stopped. ok is false when nothing fits at any memory.
func placeFrideInstance(instance string, tries []int, hosts []frideHost) (spot frideSpot, ok bool) {
	byRoom := slices.Clone(hosts)
	slices.SortStableFunc(byRoom, func(a, b frideHost) int {
		return cmp.Or(cmp.Compare(b.FreeMB, a.FreeMB), cmp.Compare(a.ID, b.ID))
	})
	for i, mb := range tries {
		var fits []frideHost
		for _, h := range byRoom {
			if h.FreeMB >= mb {
				fits = append(fits, h)
			}
		}
		if len(fits) > 0 {
			best := fits[0]
			if j := slices.IndexFunc(fits, func(h frideHost) bool { return h.Has }); j >= 0 {
				best = fits[j]
			}
			return frideSpot{Node: best.ID, MemoryMB: mb, Less: i > 0}, true
		}
		for _, h := range byRoom {
			idle := slices.DeleteFunc(slices.Clone(h.Running), func(g frideGuest) bool { return g.Open > 0 || g.Name == instance })
			slices.SortStableFunc(idle, func(a, b frideGuest) int {
				return cmp.Or(a.Active.Compare(b.Active), cmp.Compare(b.MemoryMB, a.MemoryMB), cmp.Compare(a.Name, b.Name))
			})
			free, stop := h.FreeMB, []string(nil)
			for _, g := range idle {
				if free >= mb {
					break
				}
				free += g.MemoryMB
				stop = append(stop, g.Name)
			}
			if free >= mb {
				return frideSpot{Node: h.ID, MemoryMB: mb, Stop: stop, Less: i > 0}, true
			}
		}
	}
	return frideSpot{}, false
}

// frideNoRoom says why no host has room for instance at any of tries:
// what each has free, and what runs there.
func frideNoRoom(instance string, tries []int, hosts []frideHost) string {
	want := fmt.Sprintf("%d MB", tries[0])
	if len(tries) > 1 {
		want += fmt.Sprintf(", or %d MB at the least", tries[len(tries)-1])
	}
	parts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		p := fmt.Sprintf("%s has %d MB free beyond its reserve", h.Name, max(h.FreeMB, 0))
		if len(h.Running) > 0 {
			var runs []string
			for _, g := range h.Running {
				r := fmt.Sprintf("%s (%d MB", g.Name, g.MemoryMB)
				switch g.Open {
				case 0:
					r += ", idle"
				case 1:
					r += ", 1 open thread"
				default:
					r += fmt.Sprintf(", %d open threads", g.Open)
				}
				runs = append(runs, r+")")
			}
			p += " and runs " + strings.Join(runs, ", ")
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("no node has room for the FrIDE instance %s (%s): %s", instance, want, strings.Join(parts, "; "))
}
