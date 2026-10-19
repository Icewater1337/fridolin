package main

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

// A brain's own PATH folders. Your shell's PATH may name a folder inside a
// brain's folder, such as a bin/ with the scripts that brain's prompt runs
// by name, and the service gets that PATH (see servicePath). The bridge
// runs programs by name as well (git, gh, node, rclone), outside any
// sandbox, while a brain may write anything in its own folder: a program
// it put in such a folder would run unsandboxed. The sandbox therefore
// used to refuse the brain itself. Instead the bridge now runs nothing
// from such a folder: it takes it out of its own PATH (setBridgePATH) and
// the service file's, and hands it back, first on PATH, to the runs of the
// brain whose folder holds it, which run nothing there the brain could
// not run itself.

// brainPATHVar is the service file's variable for the PATH folders that lie
// in brain folders, which servicePath keeps out of the service's PATH. The
// bridge never runs programs from them; it gives each to the runs of the
// brain whose folder holds it.
const brainPATHVar = "FRIDOLIN_BRAIN_PATH"

// pathOwner is one brain_dirs entry: its folder as configured and by its
// real path, and its brain's id, "" for an entry without an approved
// manifest, whose folder may still become a brain's.
type pathOwner struct {
	id   string
	dirs []string
}

// pathOwners lists cfg's brain folders.
func pathOwners(cfg *config.File) []pathOwner {
	var out []pathOwner
	for _, ref := range cfg.BrainDirs {
		dir := ref.Dir(homeDir())
		if !filepath.IsAbs(dir) {
			continue
		}
		o := pathOwner{dirs: slices.Compact([]string{filepath.Clean(dir), fsx.Canonical(dir)})}
		if ref.Approved != nil {
			o.id = ref.Approved.ID
		}
		out = append(out, o)
	}
	return out
}

// pathOwnerOf returns the entry whose folder is or holds the PATH folder
// dir, by either path of each.
func pathOwnerOf(dir string, owners []pathOwner) (pathOwner, bool) {
	if !filepath.IsAbs(dir) {
		// exec.LookPath skips relative entries.
		return pathOwner{}, false
	}
	spellings := slices.Compact([]string{filepath.Clean(dir), fsx.Resolve(filepath.Clean(dir))})
	for _, o := range owners {
		for _, s := range spellings {
			if withinAny(s, o.dirs) {
				return o, true
			}
		}
	}
	return pathOwner{}, false
}

// inBrainFolders splits the PATH folders entries into those inside none of
// owners' folders, in their order, and those inside one, by brain id, in
// their order, each once. A folder inside an entry without an id goes to
// neither.
func inBrainFolders(entries []string, owners []pathOwner) (outside []string, own map[string][]string) {
	own = map[string][]string{}
	for _, e := range entries {
		o, in := pathOwnerOf(e, owners)
		switch {
		case !in:
			outside = append(outside, e)
		case o.id != "" && !slices.Contains(own[o.id], filepath.Clean(e)):
			own[o.id] = append(own[o.id], filepath.Clean(e))
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
func setBridgePATH(cfg *config.File) map[string][]string {
	st := &bridgePATH
	st.mu.Lock()
	defer st.mu.Unlock()
	cur := os.Getenv("PATH")
	if !st.applied || cur != st.set {
		st.base = filepath.SplitList(cur)
		st.brain = filepath.SplitList(os.Getenv(brainPATHVar))
		st.out = map[string]bool{}
		st.logged = map[string]string{}
		st.handed = map[string]string{}
	}
	owners := pathOwners(cfg)
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
		o, _ := pathOwnerOf(e, owners)
		if who, ok := st.logged[e]; ok && who == o.id {
			continue
		}
		st.logged[e] = o.id
		e := filepath.Clean(e)
		switch {
		case o.id != "":
			log.Printf("PATH: %s is in brain %s's folder, which the brain may write, so the bridge runs no program from it; only that brain's runs get it, first on their PATH", e, o.id)
		case len(o.dirs) > 0:
			log.Printf("PATH: %s is in the brain folder %s, so the bridge runs no program from it", e, o.dirs[0])
		default:
			log.Printf("PATH: %s was in a brain's folder, so the bridge still runs no program from it", e)
		}
	}
	for _, e := range st.brain {
		if !filepath.IsAbs(e) {
			continue
		}
		o, _ := pathOwnerOf(e, owners)
		e := filepath.Clean(e)
		if who, ok := st.handed[e]; ok && who == o.id {
			continue
		}
		st.handed[e] = o.id
		if o.id != "" {
			log.Printf("PATH: %s, from %s, goes to brain %s's runs only, first on their PATH; the bridge runs no program from it", e, brainPATHVar, o.id)
		} else {
			log.Printf("PATH: %s, from %s, is in no approved brain's folder, so no run gets it", e, brainPATHVar)
		}
	}
	path := strings.Join(keep, string(filepath.ListSeparator))
	if path != cur {
		os.Setenv("PATH", path)
	}
	st.applied, st.set = true, path
	return own
}
