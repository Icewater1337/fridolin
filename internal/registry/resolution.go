package registry

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/route"
)

// IssueKind describes why a configured brain needs attention.
type IssueKind string

const (
	IssueDrift      IssueKind = "drift"
	IssueMissing    IssueKind = "missing"
	IssueInvalid    IssueKind = "invalid"
	IssueUnapproved IssueKind = "unapproved"
)

// PathContext supplies the home and sibling folders for a path check.
// CheckDir is required and evaluates the caller's security policy at the
// existing check point, after basic home confinement has succeeded.
type PathContext struct {
	Home     string
	Others   []string
	CheckDir func(dir, configured, home string) error
}

// Issue is a problem with one configured brain, shown on the dashboard.
type Issue struct {
	ID     string    `json:"id,omitempty"`
	Path   string    `json:"path"`
	Kind   IssueKind `json:"kind"` // drift, missing, invalid, unapproved
	Detail string    `json:"detail"`
	// AtLoad marks one the brain manager found as it loaded the brains, a
	// folder it could not open, which ResolveBrains does not see.
	AtLoad bool `json:"-"`
	// Entry is the index in brain_dirs of the entry the issue is about.
	// The dashboard shows an issue on that entry, not on every entry with
	// the same folder.
	Entry int `json:"-"`
	// Routing is what the approved manifest routes to the brain, whose
	// chats no other brain takes while it is left out (see LeavesOut).
	Routing manifest.Routing `json:"-"`
}

// LeavesOut reports whether the issue keeps an approved brain from running:
// its folder is missing or cannot hold a brain, or it clashes with another
// brain. Drift does not; the approved manifest still runs.
func (is Issue) LeavesOut() bool {
	return is.ID != "" && (is.Kind == IssueInvalid || is.Kind == IssueMissing)
}

// Resolve turns the brain folders in the configuration into runnable
// brains. Brains with problems are left out and reported, so one broken
// folder never takes the others down.
func Resolve(refs []config.BrainRef, checkDir func(dir, configured string) error) ([]manifest.Spec, []Issue) {
	var specs []manifest.Spec
	var issues []Issue

	for i, ref := range refs {
		dir := fsx.Canonical(ref.Path)
		if ref.Approved == nil {
			issues = append(issues, Issue{Path: ref.Path, Kind: IssueUnapproved, Detail: "no approved manifest — remove this entry and add the folder again", Entry: i})
			continue
		}
		m := ref.Approved.Normalized()
		if err := m.Validate(); err != nil {
			issues = append(issues, Issue{ID: m.ID, Path: ref.Path, Kind: IssueInvalid, Detail: err.Error(), Entry: i, Routing: m.Routing})
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, Issue{ID: m.ID, Path: ref.Path, Kind: IssueMissing, Detail: "folder not found", Entry: i, Routing: m.Routing})
			continue
		}
		// The dashboard refuses such a folder, but brain_dirs can be edited
		// by hand, and a folder can be moved, or a symlink on its way
		// changed, after it was added.
		if err := checkDir(dir, ref.Path); err != nil {
			issues = append(issues, Issue{ID: m.ID, Path: ref.Path, Kind: IssueInvalid, Detail: "the folder " + err.Error() + ", so it cannot hold a brain; move the brain to a folder of its own", Entry: i, Routing: m.Routing})
			continue
		}
		if disk, err := manifest.Read(dir); err == nil && !disk.Equal(m) {
			issues = append(issues, Issue{ID: m.ID, Path: ref.Path, Kind: IssueDrift, Detail: "brain.json changed on disk — review and approve it in the dashboard", Entry: i})
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			issues = append(issues, Issue{ID: m.ID, Path: ref.Path, Kind: IssueDrift, Detail: err.Error(), Entry: i})
		}
		// Validate kept prompt_file inside the folder; the folder's os.Root
		// keeps it there when the bridge reads it.
		specs = append(specs, manifest.Spec{Manifest: m, Dir: dir, Path: filepath.Clean(ref.Path), PromptFile: m.PromptFile, Entry: i})
	}

	// Drop brains that clash with an earlier one rather than refusing to run.
	var ok []manifest.Spec
	for _, s := range specs {
		if err := route.Validate(append(append([]manifest.Spec{}, ok...), s)); err != nil {
			issues = append(issues, Issue{ID: s.Manifest.ID, Path: s.Dir, Kind: IssueInvalid, Detail: err.Error(), Entry: s.Entry, Routing: s.Manifest.Routing})
			continue
		}
		ok = append(ok, s)
	}
	return ok, issues
}

// CheckPath vets a folder before it becomes a brain. Problems block it;
// warnings are shown but allowed.
func CheckPath(path string, ctx PathContext) (abs string, problems, warnings []string) {
	home := ctx.Home
	abs = fsx.Canonical(fsx.ExpandHome(strings.TrimSpace(path), home))
	if !filepath.IsAbs(abs) {
		return abs, []string{"enter an absolute path (or one starting with ~/)"}, nil
	}
	switch {
	case abs == "/" || abs == fsx.Canonical(home):
		problems = append(problems, "a brain cannot be your whole home folder — pick or create a folder for it")
	case !fsx.Within(abs, fsx.Canonical(home)):
		problems = append(problems, "brain folders must be inside your home folder")
	default:
		if err := ctx.CheckDir(abs, abs, home); err != nil {
			problems = append(problems, "this folder "+err.Error()+", so it cannot hold a brain — pick or create another one")
		}
	}
	for _, o := range ctx.Others {
		o = fsx.Canonical(o)
		switch {
		case o == abs:
			problems = append(problems, "this folder is already a brain")
		case fsx.Within(abs, o):
			problems = append(problems, "this folder is inside another brain ("+o+")")
		case fsx.Within(o, abs):
			problems = append(problems, "this folder contains another brain ("+o+")")
		}
	}
	// The sandbox opens a brain's own folder wherever it is, but macOS
	// itself guards these folders (privacy settings), also from the bridge.
	for _, sub := range []string{"Documents", "Desktop", "Downloads"} {
		if fsx.Within(abs, filepath.Join(fsx.Canonical(home), sub)) {
			warnings = append(warnings, "macOS protects ~/"+sub+": the bridge (and so this brain) can work there only while it has your permission under System Settings → Privacy & Security → Files and Folders (or Full Disk Access), which a service started at login may not have")
		}
	}
	return abs, problems, warnings
}
