package app

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// brainPATHVar is the service file's variable for the PATH folders that lie
// in brain folders, which servicePath keeps out of the service's PATH. The
// bridge never runs programs from them; it gives each to the runs of the
// brain whose folder holds it.
const BrainPATHVar = "FRIDOLIN_BRAIN_PATH"

// pathOwner is one brain_dirs entry: its folder as configured and by its
// real path, and its brain's id, "" for an entry without an approved
// manifest, whose folder may still become a brain's.
type PathOwner struct {
	ID   string
	Dirs []string
}

// pathOwners lists cfg's brain folders.
func PathOwners(cfg *config.File) []PathOwner {
	var out []PathOwner
	for _, ref := range cfg.BrainDirs {
		dir := ref.Dir(HomeDir())
		if !filepath.IsAbs(dir) {
			continue
		}
		o := PathOwner{Dirs: slices.Compact([]string{filepath.Clean(dir), fsx.Canonical(dir)})}
		if ref.Approved != nil {
			o.ID = ref.Approved.ID
		}
		out = append(out, o)
	}
	return out
}

// pathOwnerOf returns the entry whose folder is or holds the PATH folder
// dir, by either path of each.
func PathOwnerOf(dir string, owners []PathOwner) (PathOwner, bool) {
	if !filepath.IsAbs(dir) {
		// exec.LookPath skips relative entries.
		return PathOwner{}, false
	}
	spellings := slices.Compact([]string{filepath.Clean(dir), fsx.Resolve(filepath.Clean(dir))})
	for _, o := range owners {
		for _, s := range spellings {
			if withinAny(s, o.Dirs) {
				return o, true
			}
		}
	}
	return PathOwner{}, false
}

// inBrainFolders splits the PATH folders entries into those inside none of
// owners' folders, in their order, and those inside one, by brain id, in
// their order, each once. A folder inside an entry without an id goes to
// neither.
func inBrainFolders(entries []string, owners []PathOwner) (outside []string, own map[string][]string) {
	own = map[string][]string{}
	for _, e := range entries {
		o, in := PathOwnerOf(e, owners)
		switch {
		case !in:
			outside = append(outside, e)
		case o.ID != "" && !slices.Contains(own[o.ID], filepath.Clean(e)):
			own[o.ID] = append(own[o.ID], filepath.Clean(e))
		}
	}
	return outside, own
}

// bridgePATH is what setBridgePATH keeps of the PATH it was given.
var bridgePATH struct {
	mu      sync.Mutex
	applied bool
	// base is PATH as the bridge was started with it, and brain the
	// entries of brainPATHVar.
	base, brain []string
	// set is what setBridgePATH last set PATH to: a PATH changed since,
	// by a test say, is a new base.
	set string
	// out holds every folder taken out so far. It stays out after its
	// brain is removed: what the brain left there is still the brain's.
	out map[string]bool
	// logged maps each folder logged as taken out to its brain, and
	// handed each folder of brainPATHVar logged to the brain it went to,
	// "" for none.
	logged, handed map[string]string
}

// setBridgePATH takes the folders that lie in cfg's brain folders out of
// the bridge's own PATH, so that nothing the bridge runs by name comes from
// one, and returns them by brain id, in PATH's order, for that brain's runs
// (see brainSettings.ownPATH); so does each folder of brainPATHVar whose
// brain folder holds it. It logs each folder it takes out, and where each
// folder of brainPATHVar goes: under the service, which keeps them out of
// PATH, those are the lines that say which brain gets which. The bridge calls
// it before it loads the brains, at its start and on every change of the
// config, and check calls it too, so that it sees the brains and profiles
// the bridge would.
func SetBridgePATH(cfg *config.File) map[string][]string {
	st := &bridgePATH
	st.mu.Lock()
	defer st.mu.Unlock()
	cur := os.Getenv("PATH")
	if !st.applied || cur != st.set {
		st.base = filepath.SplitList(cur)
		st.brain = filepath.SplitList(os.Getenv(BrainPATHVar))
		st.out = map[string]bool{}
		st.logged = map[string]string{}
		st.handed = map[string]string{}
	}
	owners := PathOwners(cfg)
	_, fromVar := inBrainFolders(st.brain, owners)
	outside, own := inBrainFolders(st.base, owners)
	for id, dirs := range fromVar {
		for _, d := range dirs {
			if !slices.Contains(own[id], d) {
				own[id] = append(own[id], d)
			}
		}
	}
	for _, e := range st.base {
		if !slices.Contains(outside, e) {
			st.out[e] = true
		}
	}
	keep := slices.DeleteFunc(slices.Clone(outside), func(e string) bool { return st.out[e] })
	for _, e := range st.base {
		if !st.out[e] {
			continue
		}
		o, _ := PathOwnerOf(e, owners)
		if who, ok := st.logged[e]; ok && who == o.ID {
			continue
		}
		st.logged[e] = o.ID
		e := filepath.Clean(e)
		switch {
		case o.ID != "":
			log.Printf("PATH: %s is in brain %s's folder, which the brain may write, so the bridge runs no program from it; only that brain's runs get it, first on their PATH", e, o.ID)
		case len(o.Dirs) > 0:
			log.Printf("PATH: %s is in the brain folder %s, so the bridge runs no program from it", e, o.Dirs[0])
		default:
			log.Printf("PATH: %s was in a brain's folder, so the bridge still runs no program from it", e)
		}
	}
	for _, e := range st.brain {
		if !filepath.IsAbs(e) {
			continue
		}
		o, _ := PathOwnerOf(e, owners)
		e := filepath.Clean(e)
		if who, ok := st.handed[e]; ok && who == o.ID {
			continue
		}
		st.handed[e] = o.ID
		if o.ID != "" {
			log.Printf("PATH: %s, from %s, goes to brain %s's runs only, first on their PATH; the bridge runs no program from it", e, BrainPATHVar, o.ID)
		} else {
			log.Printf("PATH: %s, from %s, is in no approved brain's folder, so no run gets it", e, BrainPATHVar)
		}
	}
	path := strings.Join(keep, string(filepath.ListSeparator))
	if path != cur {
		os.Setenv("PATH", path)
	}
	st.applied, st.set = true, path
	return own
}
