package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// BrainSpec is a brain ready to run, from either source.
type BrainSpec struct {
	Manifest   BrainManifest
	Dir        string // absolute, symlinks resolved
	PromptPath string
	Source     string // "inline" or "folder"
}

// BrainIssue is a problem with one configured brain, shown on the dashboard.
type BrainIssue struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path"`
	Kind   string `json:"kind"` // drift, missing, invalid, unapproved
	Detail string `json:"detail"`
}

// ResolveBrains turns the configuration into runnable brains: inline brains
// first, then brain folders. A folder that is also configured inline is used
// once (the folder entry wins). Brains with problems are left out and
// reported, so one broken folder never takes the others down.
func ResolveBrains(cfg *Config) ([]BrainSpec, []BrainIssue) {
	var specs []BrainSpec
	var issues []BrainIssue
	seenDir := map[string]int{}

	add := func(s BrainSpec) {
		if i, ok := seenDir[s.Dir]; ok {
			specs[i] = s
			return
		}
		seenDir[s.Dir] = len(specs)
		specs = append(specs, s)
	}

	for _, b := range cfg.Brains {
		dir := canonicalDir(b.WorkDir)
		m := ManifestFromInline(b)
		add(BrainSpec{Manifest: m, Dir: dir, PromptPath: b.PromptFile, Source: "inline"})
	}

	for _, ref := range cfg.BrainDirs {
		dir := canonicalDir(ref.Path)
		if ref.Approved == nil {
			issues = append(issues, BrainIssue{Path: ref.Path, Kind: "unapproved", Detail: "no approved manifest — re-add this brain"})
			continue
		}
		m := ref.Approved.normalized()
		if err := m.Validate(); err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: err.Error()})
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "missing", Detail: "folder not found"})
			continue
		}
		if disk, err := ReadManifest(dir); err == nil && !disk.Equal(m) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: "brain.json changed on disk — review and approve it in the dashboard"})
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "drift", Detail: err.Error()})
		}
		prompt, err := confinedPath(dir, m.PromptFile)
		if err != nil {
			issues = append(issues, BrainIssue{ID: m.ID, Path: ref.Path, Kind: "invalid", Detail: err.Error()})
			continue
		}
		add(BrainSpec{Manifest: m, Dir: dir, PromptPath: prompt, Source: "folder"})
	}

	// Drop brains that clash with an earlier one rather than refusing to run.
	var ok []BrainSpec
	for _, s := range specs {
		if err := ValidateBrainSet(append(append([]BrainSpec{}, ok...), s)); err != nil {
			issues = append(issues, BrainIssue{ID: s.Manifest.ID, Path: s.Dir, Kind: "invalid", Detail: err.Error()})
			continue
		}
		ok = append(ok, s)
	}
	return ok, issues
}

// ValidateBrainSet checks brains against each other: unique ids, every group
// owned by one brain, at most one DM brain and one default, no brain folder
// inside another (sandbox profiles deny other brains' folders, so a nested
// brain could not read itself).
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
			if i != j && isWithin(b.Dir, a.Dir) {
				return fmt.Errorf("brain folder %s is inside brain folder %s", b.Dir, a.Dir)
			}
		}
	}
	return nil
}

// CheckBrainPath vets a folder before it becomes a brain. Problems block it;
// warnings are shown but allowed.
func CheckBrainPath(path string, cfg *Config, otherDirs []string) (abs string, problems, warnings []string) {
	home, _ := os.UserHomeDir()
	abs = canonicalDir(expandHome(strings.TrimSpace(path), home))
	if !filepath.IsAbs(abs) {
		return abs, []string{"enter an absolute path (or one starting with ~/)"}, nil
	}
	switch {
	case abs == "/" || abs == canonicalDir(home):
		problems = append(problems, "a brain cannot be your whole home folder — pick or create a folder for it")
	case !isWithin(abs, canonicalDir(home)):
		problems = append(problems, "brain folders must be inside your home folder")
	}
	data := canonicalDir(cfg.DataDir)
	if isWithin(abs, data) || isWithin(data, abs) {
		problems = append(problems, "the bridge's data folder cannot be (or contain) a brain")
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
	if exe, err := os.Executable(); err == nil && isWithin(abs, canonicalDir(filepath.Dir(exe))) {
		warnings = append(warnings, "this folder is inside the bridge's source checkout — keep brain data out of the git repo")
	}
	for _, sub := range []string{"Documents", "Desktop", "Downloads"} {
		if isWithin(abs, filepath.Join(canonicalDir(home), sub)) {
			warnings = append(warnings, "macOS protects ~/"+sub+": the sandbox denies it to brains by default, so this brain may not be able to read its own files")
		}
	}
	return abs, problems, warnings
}

// confinedPath resolves rel inside dir and rejects anything that escapes it,
// including through symlinks. The bridge reads the prompt file outside the
// sandbox, so this is what stops brain.json from pointing it at ~/.ssh.
func confinedPath(dir, rel string) (string, error) {
	if rel == "" {
		rel = "brain-prompt.md"
	}
	p := filepath.Join(dir, rel)
	if !isWithin(p, dir) {
		return "", fmt.Errorf("prompt_file %q leaves the brain folder", rel)
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		if !isWithin(resolved, canonicalDir(dir)) {
			return "", fmt.Errorf("prompt_file %q points outside the brain folder", rel)
		}
		return resolved, nil
	}
	return p, nil
}

// canonicalDir cleans p and resolves symlinks when it exists (on macOS /tmp is
// a symlink to /private/tmp, and the sandbox compares real paths).
func canonicalDir(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
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

// UpdateManifest changes one brain's manifest: brain.json in its folder and
// the approved copy in the config, together, so the change is not reported
// as drift. A brain still configured inline is moved to its folder first.
func (cs *ConfigStore) UpdateManifest(id string, mutate func(m *BrainManifest) error) error {
	_, err := cs.Update("", func(c *Config) error { return updateManifestIn(c, id, mutate) })
	return err
}

func updateManifestIn(c *Config, id string, mutate func(m *BrainManifest) error) error {
	for i, bc := range c.Brains {
		if bc.Name == id {
			home, _ := os.UserHomeDir()
			eff := bc
			eff.WorkDir = expandHome(bc.WorkDir, home)
			if eff.PromptFile == "" {
				eff.PromptFile = filepath.Join(eff.WorkDir, "brain-prompt.md")
			}
			m := ManifestFromInline(eff)
			c.Brains = append(c.Brains[:i], c.Brains[i+1:]...)
			c.BrainDirs = append(c.BrainDirs, BrainRef{Path: bc.WorkDir, Approved: &m})
			break
		}
	}
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
		home, _ := os.UserHomeDir()
		if err := WriteManifest(expandHome(ref.Path, home), &m, true); err != nil {
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

	prompt := starterPrompt(m)
	files := []struct{ rel, content string }{
		{m.normalized().PromptFile, prompt},
		{"memories/people/whatsapp-identity-map.md", identityMapTemplate},
		{"memories/README.md", "# Memories\n\nOne markdown file per topic. The brain reads and updates these between conversations.\n"},
		{"skills/README.md", "# Skills\n\nReusable how-tos and scripts this brain has written for itself.\n"},
	}
	for _, sk := range exampleSkills() {
		files = append(files, struct{ rel, content string }{"skills/" + sk.name, sk.content})
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
instead of raw numbers in the chat history it gives the brain.

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
`

// starterPrompt fills the example brain prompt for a new brain.
func starterPrompt(m *BrainManifest) string {
	tpl := examplePromptTemplate()
	repo := bridgeRepoDir()
	r := strings.NewReplacer(
		"{{BRAIN_NAME}}", m.Name,
		"<bridge-repo>", repo,
		"{{CREATED}}", time.Now().Format("2006-01-02"),
	)
	return r.Replace(tpl)
}

// bridgeRepoDir guesses where the bridge's tools/ live: next to the binary
// when it was built in the checkout.
func bridgeRepoDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "<bridge-repo>"
	}
	if dir := checkoutOf(exe); dir != "" {
		return dir
	}
	return "<bridge-repo>"
}

// checkoutOf is the source checkout the binary exe was built in, or "".
func checkoutOf(exe string) string {
	dir := filepath.Dir(exe)
	if st, err := os.Stat(filepath.Join(dir, "tools")); err == nil && st.IsDir() {
		return dir
	}
	return ""
}
