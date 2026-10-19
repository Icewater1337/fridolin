package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/route"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// BrainIssue is a problem with one configured brain, shown on the dashboard.
type BrainIssue struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path"`
	Kind   string `json:"kind"` // drift, missing, invalid, unapproved
	Detail string `json:"detail"`
	// atLoad marks one the brain manager found as it loaded the brains, a
	// folder it could not open, which ResolveBrains does not see.
	atLoad bool
	// entry is the index in brain_dirs of the entry the issue is about.
	// The dashboard shows an issue on that entry, not on every entry with
	// the same folder.
	entry int
	// routing is what the approved manifest routes to the brain, whose
	// chats no other brain takes while it is left out (see leavesOut).
	routing manifest.Routing
}

// leavesOut reports whether the issue keeps an approved brain from running:
// its folder is missing or cannot hold a brain, or it clashes with another
// brain. Drift does not; the approved manifest still runs.
func (is BrainIssue) leavesOut() bool {
	return is.ID != "" && (is.Kind == "invalid" || is.Kind == "missing")
}

// ResolveBrains turns the brain folders in the configuration into runnable
// brains. Brains with problems are left out and reported, so one broken
// folder never takes the others down.
func ResolveBrains(cfg *config.Config) ([]manifest.Spec, []BrainIssue) {
	var specs []manifest.Spec
	var issues []BrainIssue

	for i, ref := range cfg.BrainDirs {
		dir := fsx.Canonical(ref.Path)
		if ref.Approved == nil {
			issues = append(issues, BrainIssue{Path: ref.Path, Kind: "unapproved", Detail: "no approved manifest — remove this entry and add the folder again", entry: i})
			continue
		}
		m := ref.Approved.Normalized()
		if err := m.Validate(); err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: err.Error(), entry: i, routing: m.Routing})
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "missing", Detail: "folder not found", entry: i, routing: m.Routing})
			continue
		}
		// The dashboard refuses such a folder, but brain_dirs can be edited
		// by hand, and a folder can be moved, or a symlink on its way
		// changed, after it was added.
		if err := checkBrainDir(dir, ref.Path, cfg, config.HomeDir()); err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: "the folder " + err.Error() + ", so it cannot hold a brain; move the brain to a folder of its own", entry: i, routing: m.Routing})
			continue
		}
		if disk, err := manifest.Read(dir); err == nil && !disk.Equal(m) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: "brain.json changed on disk — review and approve it in the dashboard", entry: i})
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: err.Error(), entry: i})
		}
		// Validate kept prompt_file inside the folder; the folder's os.Root
		// keeps it there when the bridge reads it.
		specs = append(specs, manifest.Spec{Manifest: m, Dir: dir, Path: filepath.Clean(ref.Path), PromptFile: m.PromptFile, Entry: i})
	}

	// Drop brains that clash with an earlier one rather than refusing to run.
	var ok []manifest.Spec
	for _, s := range specs {
		if err := route.Validate(append(append([]manifest.Spec{}, ok...), s)); err != nil {
			issues = append(issues, BrainIssue{ID: s.Manifest.ID, Path: s.Dir, Kind: "invalid", Detail: err.Error(), entry: s.Entry, routing: s.Manifest.Routing})
			continue
		}
		ok = append(ok, s)
	}
	return ok, issues
}

// CheckBrainPath vets a folder before it becomes a brain. Problems block it;
// warnings are shown but allowed.
func CheckBrainPath(path string, cfg *config.Config, otherDirs []string) (abs string, problems, warnings []string) {
	home := config.HomeDir()
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
		if err := checkBrainDir(abs, abs, cfg, home); err != nil {
			problems = append(problems, "this folder "+err.Error()+", so it cannot hold a brain — pick or create another one")
		}
	}
	for _, o := range otherDirs {
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

// checkBrainDir reports why dir, a brain's folder by its real path, cannot
// be one ("is …", to follow "the folder"), with configured the folder as
// configured; nil when it can. The
// sandbox lets a brain read and write its own folder, so the folder must not
// be your home folder or hold it, nor be, hold or lie inside what the
// sandbox keeps from brains (see sandboxPolicy.protected): a brain in
// ~/.claude/skills would write what your own sessions load, one holding the
// data folder would read the WhatsApp session. The dashboard asks before it
// adds a brain (CheckBrainPath), ResolveBrains for every configured brain,
// a hand-edited brain_dirs included, and each sandbox profile again.
func checkBrainDir(dir, configured string, cfg *config.Config, home string) error {
	// A folder of the bridge's PATH inside this one is no reason: the
	// bridge takes it off its PATH once the folder is a brain's (see
	// setBridgePATH), as it does for every configured one.
	p := sandboxPolicyOf(cfg)
	for _, d := range []string{dir, configured} {
		if d != "" {
			p.brainDirs = append(p.brainDirs, d, fsx.Canonical(d))
		}
	}
	protected := p.protected(home, ownLayout())
	for _, d := range []string{dir, configured} {
		if d == "" {
			continue
		}
		if fsx.Within(home, d) || fsx.Within(fsx.Canonical(home), fsx.Canonical(d)) {
			return errors.New("is your home folder or holds it")
		}
		if what, ok := protectedIn(d, protected); ok {
			return fmt.Errorf("%s, which the sandbox keeps from brains", what)
		}
	}
	return nil
}

// inHome builds p, an absolute path a request named, again from your home
// folder and the part of p below it. CheckBrainPath decides whether such a
// folder may be used, but it only reports: the folder it hands back is still
// the one the request named. What a handler then makes, lists or removes is
// built this way, inside the home folder by construction. ok is false for
// the home folder itself and for anything outside it.
func inHome(p string) (string, bool) {
	home := fsx.Canonical(config.HomeDir())
	rel, err := filepath.Rel(home, p)
	if home == "" || err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.Join(home, rel), true
}

// UpdateManifest changes one brain's manifest, see updateManifestIn.
func (cs *ConfigStore) UpdateManifest(id string, mutate func(m *manifest.Manifest) error) error {
	_, err := cs.UpdateTx("", func(c *config.Config, tx *Tx) error { return updateManifestIn(c, tx, id, mutate) })
	return err
}

// updateManifestIn changes brain id's manifest in c: mutate edits a copy of
// the approved one, which becomes the approved copy and is staged in tx as
// brain.json in the brain's folder, so that the two change together and
// the change is not reported as drift. It is the one way the bridge
// changes a manifest; an error from mutate leaves both as they were.
func updateManifestIn(c *config.Config, tx *Tx, id string, mutate func(m *manifest.Manifest) error) error {
	for i := range c.BrainDirs {
		ref := &c.BrainDirs[i]
		if ref.Approved == nil || ref.Approved.ID != id {
			continue
		}
		m := *ref.Approved
		if err := mutate(&m); err != nil {
			return err
		}
		if m.ID != id {
			return fmt.Errorf("a brain's id cannot change")
		}
		if err := tx.WriteManifest(ref.Dir(), &m); err != nil {
			return err
		}
		n := m.Normalized()
		ref.Approved = &n
		return nil
	}
	return fmt.Errorf("no brain %q", id)
}
