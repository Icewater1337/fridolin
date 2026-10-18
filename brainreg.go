package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BrainRef is one brain folder in config.json's brain_dirs. Approved is the
// manifest you accepted in the dashboard. The bridge runs the approved
// version: a brain can edit its own brain.json, and must not be able to grant
// itself your DMs or another brain's groups that way. Differences show up in
// the dashboard as "changed on disk" until you approve them.
type BrainRef struct {
	Path     string         `json:"path"`
	Approved *BrainManifest `json:"approved"`
}

// Dir is the brain's folder with ~ expanded, for code that works on the
// file layer, where Path is kept as typed. The effective configuration has
// Path expanded already.
func (r BrainRef) Dir() string { return expandHome(r.Path, homeDir()) }

// BrainSpec is a brain ready to run.
type BrainSpec struct {
	Manifest BrainManifest
	Dir      string // absolute, symlinks resolved
	// Path is the folder as configured, ~ expanded. The sandbox profiles
	// cover it as well as Dir, which the brain runs in: a symlink on the
	// way to the folder, such as ~/assistant on another volume, is one a
	// brain could swap for its own and so move another brain's folder.
	Path string
	// PromptFile is the prompt file relative to Dir. The bridge reads it
	// through the brain's open folder, which stops a brain from pointing it
	// at ~/.ssh with a symlink; checking the path here would not, as the
	// brain can plant one at any time after.
	PromptFile string
	// entry is the spec's index in brain_dirs, which its issues carry.
	entry int
}

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
func ResolveBrains(cfg *Config) ([]BrainSpec, []BrainIssue) {
	var specs []BrainSpec
	var issues []BrainIssue

	for i, ref := range cfg.BrainDirs {
		dir := canonicalDir(ref.Path)
		if ref.Approved == nil {
			issues = append(issues, BrainIssue{Path: ref.Path, Kind: "unapproved", Detail: "no approved manifest — remove this entry and add the folder again", entry: i})
			continue
		}
		m := ref.Approved.normalized()
		if err := m.Validate(); err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: err.Error(), entry: i})
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "missing", Detail: "folder not found", entry: i})
			continue
		}
		// The dashboard refuses such a folder, but brain_dirs can be edited
		// by hand, and a folder can be moved, or a symlink on its way
		// changed, after it was added.
		if err := checkBrainDir(dir, ref.Path, cfg, homeDir()); err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: "the folder " + err.Error() + ", so it cannot hold a brain; move the brain to a folder of its own"})
			continue
		}
		if disk, err := ReadManifest(dir); err == nil && !disk.Equal(m) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: "brain.json changed on disk — review and approve it in the dashboard", entry: i})
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: err.Error(), entry: i})
		}
		// Validate kept prompt_file inside the folder; the folder's os.Root
		// keeps it there when the bridge reads it.
		specs = append(specs, BrainSpec{Manifest: m, Dir: dir, Path: filepath.Clean(ref.Path), PromptFile: m.PromptFile, entry: i})
	}

	// Drop brains that clash with an earlier one rather than refusing to run.
	var ok []BrainSpec
	for _, s := range specs {
		if err := ValidateBrainSet(append(append([]BrainSpec{}, ok...), s)); err != nil {
			issues = append(issues, BrainIssue{ID: s.Manifest.ID, Path: s.Dir, Kind: "invalid", Detail: err.Error(), entry: s.entry})
			continue
		}
		ok = append(ok, s)
	}
	return ok, issues
}

// ValidateBrainSet checks brains against each other: unique ids, every group
// owned by one brain, at most one DM brain and one default, no two brains in
// one folder and no brain folder inside another (sandbox profiles deny other
// brains' folders, so a nested brain could not read itself).
func ValidateBrainSet(specs []BrainSpec) error {
	ids := map[string]string{}
	groups := map[string]string{}
	dm, def := "", ""
	for _, s := range specs {
		m := s.Manifest
		if other, ok := ids[m.ID]; ok {
			return fmt.Errorf("two brains use the id %q (%s and %s)", m.ID, other, s.Dir)
		}
		ids[m.ID] = s.Dir
		for _, g := range m.Routing.Groups {
			if owner, ok := groups[g]; ok {
				return fmt.Errorf("group %s is claimed by both %q and %q", g, owner, m.ID)
			}
			groups[g] = m.ID
		}
		if m.Routing.HandlesDMs {
			if dm != "" {
				return fmt.Errorf("brains %q and %q both handle DMs", dm, m.ID)
			}
			dm = m.ID
		}
		if m.Routing.Default {
			if def != "" {
				return fmt.Errorf("brains %q and %q are both marked default", def, m.ID)
			}
			def = m.ID
		}
	}
	for i, a := range specs {
		for j, b := range specs {
			switch {
			case i == j:
			case a.Dir == b.Dir:
				return fmt.Errorf("brains %q and %q use the same folder %s", a.Manifest.ID, b.Manifest.ID, a.Dir)
			case isWithin(b.Dir, a.Dir):
				return fmt.Errorf("brain folder %s is inside brain folder %s", b.Dir, a.Dir)
			}
		}
	}
	return nil
}

// CheckBrainPath vets a folder before it becomes a brain. Problems block it;
// warnings are shown but allowed.
func CheckBrainPath(path string, cfg *Config, otherDirs []string) (abs string, problems, warnings []string) {
	home := homeDir()
	abs = canonicalDir(expandHome(strings.TrimSpace(path), home))
	if !filepath.IsAbs(abs) {
		return abs, []string{"enter an absolute path (or one starting with ~/)"}, nil
	}
	switch {
	case abs == "/" || abs == canonicalDir(home):
		problems = append(problems, "a brain cannot be your whole home folder — pick or create a folder for it")
	case !isWithin(abs, canonicalDir(home)):
		problems = append(problems, "brain folders must be inside your home folder")
	default:
		if err := checkBrainDir(abs, abs, cfg, home); err != nil {
			problems = append(problems, "this folder "+err.Error()+", so it cannot hold a brain — pick or create another one")
		}
	}
	for _, o := range otherDirs {
		o = canonicalDir(o)
		switch {
		case o == abs:
			problems = append(problems, "this folder is already a brain")
		case isWithin(abs, o):
			problems = append(problems, "this folder is inside another brain ("+o+")")
		case isWithin(o, abs):
			problems = append(problems, "this folder contains another brain ("+o+")")
		}
	}
	// The sandbox opens a brain's own folder wherever it is, but macOS
	// itself guards these folders (privacy settings), also from the bridge.
	for _, sub := range []string{"Documents", "Desktop", "Downloads"} {
		if isWithin(abs, filepath.Join(canonicalDir(home), sub)) {
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
func checkBrainDir(dir, configured string, cfg *Config, home string) error {
	// A folder of the bridge's PATH inside this one is no reason: the
	// bridge takes it off its PATH once the folder is a brain's (see
	// setBridgePATH), as it does for every configured one.
	p := sandboxPolicyOf(cfg)
	for _, d := range []string{dir, configured} {
		if d != "" {
			p.brainDirs = append(p.brainDirs, d, canonicalDir(d))
		}
	}
	protected := p.protected(home, ownLayout())
	for _, d := range []string{dir, configured} {
		if d == "" {
			continue
		}
		if isWithin(home, d) || isWithin(canonicalDir(home), canonicalDir(d)) {
			return errors.New("is your home folder or holds it")
		}
		if what, ok := protectedIn(d, protected); ok {
			return fmt.Errorf("%s, which the sandbox keeps from brains", what)
		}
	}
	return nil
}

// canonicalDir cleans p and resolves its symlinks (on macOS /tmp is a
// symlink to /private/tmp, and the sandbox compares real paths). For an
// absolute path that does not exist yet, such as a folder the dashboard is
// about to create, it resolves the part that does: ~/link/new, with ~/link
// a symlink out of HOME, lies where the link points, and that is where
// MkdirAll would create it.
func canonicalDir(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if filepath.IsAbs(p) {
		return resolvePath(p)
	}
	return p
}

// isWithin reports whether p is dir or inside it.
func isWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// inHome builds p, an absolute path a request named, again from your home
// folder and the part of p below it. CheckBrainPath decides whether such a
// folder may be used, but it only reports: the folder it hands back is still
// the one the request named. What a handler then makes, lists or removes is
// built this way, inside the home folder by construction. ok is false for
// the home folder itself and for anything outside it.
func inHome(p string) (string, bool) {
	home := canonicalDir(homeDir())
	rel, err := filepath.Rel(home, p)
	if home == "" || err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.Join(home, rel), true
}

// UpdateManifest changes one brain's manifest, see updateManifestIn.
func (cs *ConfigStore) UpdateManifest(id string, mutate func(m *BrainManifest) error) error {
	_, err := cs.Update("", func(c *Config) error { return updateManifestIn(c, id, mutate) })
	return err
}

// updateManifestIn changes brain id's manifest in c: mutate edits a copy of
// the approved one, which goes to brain.json in the brain's folder and
// becomes the approved copy, together, so the change is not reported as
// drift. It is the one way the bridge changes a manifest; an error from
// mutate leaves both as they were.
func updateManifestIn(c *Config, id string, mutate func(m *BrainManifest) error) error {
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
		if err := WriteManifest(ref.Dir(), &m, true); err != nil {
			return err
		}
		n := m.normalized()
		ref.Approved = &n
		return nil
	}
	return fmt.Errorf("no brain %q", id)
}

// ScaffoldBrain prepares a folder for a new brain: the folder itself, a
// starter prompt file, the memory layout and brain.json. It never replaces
// a file that already exists, so adopting a folder with content is safe. It
// returns the files it created.
func ScaffoldBrain(dir string, m *BrainManifest) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var created []string
	write := func(rel, content string) error {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return err
		}
		created = append(created, rel)
		return nil
	}

	prompt := renderTemplate(examplePromptTemplate(), m)
	files := []struct{ rel, content string }{
		{m.normalized().PromptFile, prompt},
		{identityMapFile, identityMapTemplate},
		{"memories/README.md", "# Memories\n\nOne markdown file per topic. The brain reads and updates these between conversations.\n"},
		{"skills/README.md", "# Skills\n\nReusable how-tos and scripts this brain has written for itself.\n"},
	}
	for _, sk := range exampleSkills() {
		files = append(files, struct{ rel, content string }{"skills/" + sk.name, renderTemplate(sk.content, m)})
	}
	for _, f := range files {
		if err := write(f.rel, f.content); err != nil {
			return created, err
		}
	}
	if err := WriteManifest(dir, m, false); err == nil {
		created = append(created, ManifestFile)
	} else if !errors.Is(err, errManifestExists) {
		return created, err
	}
	sort.Strings(created)
	return created, nil
}

const identityMapTemplate = `# WhatsApp identity map

Who is who in this brain's chats. The bridge reads this table to show names
instead of raw numbers in the chat history it gives the brain. Write numbers
with the country code, any way you like: +1 555 123-4567 works.

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
`

// renderTemplate fills a brain template, the starter prompt or an example
// skill, for the new brain m. The templates name the bridge's commands by
// $FRIDOLIN_BIN and its helpers by $FRIDOLIN_TOOLS_DIR, which every run
// sets (see brainRunEnv), and never by a path: a brain folder keeps
// working when it or the bridge moves.
func renderTemplate(tpl string, m *BrainManifest) string {
	return strings.NewReplacer("{{BRAIN_NAME}}", m.Name).Replace(tpl)
}
