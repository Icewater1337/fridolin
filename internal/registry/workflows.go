package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/route"
)

// Registry owns brain configuration workflows and their serialization.
// Home and CheckDir are evaluated at the original operation boundaries, so
// moving a workflow does not freeze the command's security policy.
type Registry struct {
	store   *config.Store
	options Options
	addMu   sync.Mutex
}

type Options struct {
	Home func() string
	// ConfigHome is the home folder config.json's ~/ means, when it is
	// not Home (in the core's container, where Home is the folder brain
	// folders go in); nil is Home.
	ConfigHome  func() string
	CheckDir    func(dir, configured string, cfg *config.Settings, home string) error
	Templates   Templates
	HistoryFile string
}

func (g *Registry) configHome() string {
	if g.options.ConfigHome != nil {
		return g.options.ConfigHome()
	}
	return g.options.Home()
}

func New(store *config.Store, options Options) *Registry {
	return &Registry{store: store, options: options}
}

func (g *Registry) resolve(cfg *config.Settings) ([]manifest.Spec, []Issue) {
	return Resolve(cfg.BrainDirs, func(dir, configured string) error {
		return g.options.CheckDir(dir, configured, cfg, g.options.Home())
	})
}

func (g *Registry) checkPath(path string, cfg *config.Settings, others []string) (string, []string, []string) {
	return CheckPath(path, PathContext{Home: g.options.Home(), Others: others,
		CheckDir: func(dir, configured, home string) error {
			return g.options.CheckDir(dir, configured, cfg, home)
		},
	})
}

// updateManifestIn changes brain id's manifest in c: mutate edits a copy of
// the approved one, which becomes the approved copy and is staged in tx as
// brain.json in the brain's folder, so that the two change together and
// the change is not reported as drift. It is the one way the bridge
// changes a manifest; an error from mutate leaves both as they were.
func (g *Registry) updateManifestIn(c *config.File, tx *config.Tx, id string, mutate func(m *manifest.Manifest) error) error {
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
		if err := tx.WriteManifest(ref.Dir(g.options.Home()), &m); err != nil {
			return err
		}
		n := m.Normalized()
		ref.Approved = &n
		return nil
	}
	return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
}

func (g *Registry) UpdateManifest(id string, mutate func(m *manifest.Manifest) error) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error { return g.updateManifestIn(c, tx, id, mutate) })
	return err
}

// assignGroup routes a group to a brain, taking it away from any other brain,
// in their manifests (brain.json, staged in tx, and the approved copy). The
// chosen brain goes first, so that one that is not there is refused before
// the others are looked at.
func (g *Registry) assignGroup(c *config.File, tx *config.Tx, jid, brainID string) error {
	if err := g.updateManifestIn(c, tx, brainID, func(m *manifest.Manifest) error {
		if !slices.Contains(m.Routing.Groups, jid) {
			m.Routing.Groups = append(m.Routing.Groups, jid)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, ref := range c.BrainDirs {
		if a := ref.Approved; a != nil && a.ID != brainID && slices.Contains(a.Routing.Groups, jid) {
			if err := g.updateManifestIn(c, tx, a.ID, func(m *manifest.Manifest) error {
				m.Routing.Groups = slices.DeleteFunc(slices.Clone(m.Routing.Groups), func(g string) bool { return g == jid })
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// updateBrainBackup changes one brain's backup settings in its manifest.
func (g *Registry) updateBrainBackup(c *config.File, tx *config.Tx, id string, enabled *bool, exclude []string) error {
	return g.updateManifestIn(c, tx, id, func(m *manifest.Manifest) error {
		if enabled != nil {
			m.Backup.Enabled = *enabled
		}
		if exclude != nil {
			var clean []string
			for _, x := range exclude {
				if x = strings.TrimSpace(x); x != "" {
					clean = append(clean, x)
				}
			}
			m.Backup.Exclude = clean
		}
		return m.Validate()
	})
}

func (g *Registry) UpdateBackup(id string, enabled *bool, exclude []string) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		return g.updateBrainBackup(c, tx, id, enabled, exclude)
	})
	return err
}

// UpdateBackupTargets sets which backup targets get brain id's folder
// (nil leaves them), and its node_only paths (nil leaves them).
func (g *Registry) UpdateBackupTargets(id string, targets map[string]bool, nodeOnly *[]string) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		return g.updateManifestIn(c, tx, id, func(m *manifest.Manifest) error {
			if targets != nil {
				m.Backup.Targets = map[string]bool{}
				for k, v := range targets {
					// Only what differs from the default is kept.
					if k == manifest.TargetCloud && v || k != manifest.TargetCloud && !v {
						continue
					}
					m.Backup.Targets[k] = v
				}
				if len(m.Backup.Targets) == 0 {
					m.Backup.Targets = nil
				}
			}
			if nodeOnly != nil {
				var clean []string
				for _, x := range *nodeOnly {
					if x = strings.TrimSpace(x); x != "" && !slices.Contains(clean, x) {
						clean = append(clean, x)
					}
				}
				m.Backup.NodeOnly = clean
			}
			return m.Validate()
		})
	})
	return err
}

func (g *Registry) AllowChat(dm bool, entry, brainID string) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		if dm {
			if !slices.Contains(c.AllowedNumbers, entry) {
				c.AllowedNumbers = append(c.AllowedNumbers, entry)
			}
			return nil
		}
		if !slices.Contains(c.AllowedGroups, entry) {
			c.AllowedGroups = append(c.AllowedGroups, entry)
		}
		if brainID == "" {
			return nil
		}
		return g.assignGroup(c, tx, entry, brainID)
	})
	return err
}

func (g *Registry) Update(id string, input manifest.Manifest) error {
	m := input.Normalized()
	if m.ID != id {
		return apperr.New(apperr.Invalid, "a brain's id cannot change — remove it and add the folder again under a new id")
	}
	if err := m.Validate(); err != nil {
		return apperr.New(apperr.Invalid, err.Error())
	}
	// The brain.json the update stages has it check the brain set with
	// the edit before anything is written (see checkSet).
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		return g.updateManifestIn(c, tx, id, func(cur *manifest.Manifest) error {
			*cur = m
			return nil
		})
	})
	return err
}

func (g *Registry) Approve(id string, reviewed manifest.Manifest) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		for i := range c.BrainDirs {
			ref := &c.BrainDirs[i]
			if ref.Approved == nil || ref.Approved.ID != id {
				continue
			}
			disk, err := manifest.Read(ref.Dir(g.options.Home()))
			if err != nil {
				return err
			}
			if !disk.Equal(reviewed) {
				return ErrReviewedManifestChanged
			}
			if disk.ID != id {
				return fmt.Errorf("the brain.json on disk changed the id to %q — ids cannot change", disk.ID)
			}
			if err := disk.Validate(); err != nil {
				return err
			}
			ref.Approved = disk
			tx.CheckBrains()
			return nil
		}
		return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
	})
	return err
}

func (g *Registry) Remove(id string) error {
	_, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved != nil && ref.Approved.ID == id {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return g.dropGrantsOn(c, tx, id)
			}
		}
		return apperr.New(apperr.NotFound, fmt.Sprintf("no brain %q", id))
	})
	return err
}

// dropGrantsOn takes brain id, which is being removed, out of the core
// brain's grants, in its brain.json and the approved copy: a grant on a
// brain that is not configured would leave the core brain unable to run.
func (g *Registry) dropGrantsOn(c *config.File, tx *config.Tx, id string) error {
	for _, ref := range c.BrainDirs {
		if a := ref.Approved; a != nil && len(a.Grants[id]) > 0 {
			if err := g.updateManifestIn(c, tx, a.ID, func(m *manifest.Manifest) error {
				grants := maps.Clone(m.Grants)
				delete(grants, id)
				m.Grants = grants
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Registry) RemoveDir(path string) error {
	want := fsx.Canonical(fsx.ExpandHome(path, g.options.Home()))
	_, err := g.store.Update("", func(c *config.File) error {
		for i, ref := range c.BrainDirs {
			if ref.Approved == nil && fsx.Canonical(ref.Dir(g.options.Home())) == want {
				c.BrainDirs = slices.Delete(c.BrainDirs, i, i+1)
				return nil
			}
		}
		return apperr.New(apperr.NotFound, fmt.Sprintf("no unapproved brain folder %s (remove an approved brain by its id)", path))
	})
	return err
}

// checkSet refuses an edit that makes brains clash, or leaves one that
// cannot run: before and after are the effective configurations without
// and with the edit, and an "invalid" issue of after's brains that
// before's do not have refuses it. One that was there already, a brain a
// hand edit broke say, is not the edit's doing, and does not keep the
// other brains from being edited or approved. Issues are told apart by
// brain, kind and detail.
func (g *Registry) CheckSet(before, after *config.Settings) error {
	_, was := g.resolve(before)
	seen := map[string]bool{}
	for _, is := range was {
		seen[issueKey(is)] = true
	}
	_, issues := g.resolve(after)
	for _, is := range issues {
		if is.Kind == "invalid" && !seen[issueKey(is)] {
			return errors.New(is.Detail)
		}
	}
	return nil
}

// issueKey tells brain issues apart for checkSet.
func issueKey(is Issue) string {
	return is.ID + "\x00" + string(is.Kind) + "\x00" + is.Detail
}

// ErrReviewedManifestChanged refuses an approval of a brain.json that is no
// longer the one on disk.
var ErrReviewedManifestChanged = apperr.New(apperr.Conflict, "brain.json changed again since you reviewed it — look at the new changes and approve those")

type AddInput struct {
	Path        string
	Manifest    manifest.Manifest
	BackupGiven bool
	CreateDir   bool
	UseExisting bool
}
type AddResult struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Created []string `json:"created"`
}

func (g *Registry) Add(req AddInput) (AddResult, error) {
	m, backupGiven := req.Manifest, req.BackupGiven
	// One add at a time, from the checks to the registration, so that an
	// add's checks see every brain the adds before it registered. Two adds
	// of the same new folder would otherwise each scaffold a part of it, and
	// the one that then lost the check in cfgs.Update would undo its part,
	// which can be the brain.json or prompt the other one was added with.
	g.addMu.Lock()
	defer g.addMu.Unlock()
	cfg := g.store.Get()
	abs, problems, _ := g.checkPath(req.Path, cfg, g.OtherDirs(cfg))
	if len(problems) > 0 {
		return AddResult{}, apperr.New(apperr.Invalid, strings.Join(problems, "; "))
	}
	abs, ok := g.inHome(abs)
	if !ok {
		return AddResult{}, apperr.New(apperr.Invalid, "brain folders must be inside your home folder")
	}
	if st, err := os.Stat(abs); err != nil {
		if !req.CreateDir {
			return AddResult{}, apperr.New(apperr.Invalid, "folder does not exist")
		}
	} else if !st.IsDir() {
		return AddResult{}, apperr.New(apperr.Invalid, "not a folder")
	}

	if req.UseExisting {
		disk, err := manifest.Read(abs)
		if err != nil {
			return AddResult{}, apperr.New(apperr.Invalid, "no readable brain.json in that folder: "+err.Error())
		}
		m = *disk
	} else if _, err := manifest.Read(abs); err == nil {
		return AddResult{}, apperr.New(apperr.Conflict, "this folder already has a brain.json — review it and add it with “use existing”")
	}
	switch {
	case req.UseExisting:
	case !backupGiven:
		// No backup settings given: new brains are backed up, with the
		// default exclusions.
		m.Backup = manifest.Backup{Enabled: true, Exclude: append([]string{}, manifest.DefaultBackupExcludes()...)}
	case m.Backup.Exclude == nil:
		m.Backup.Exclude = append([]string{}, manifest.DefaultBackupExcludes()...)
	}
	m = m.Normalized()
	if err := m.Validate(); err != nil {
		return AddResult{}, apperr.Wrap(apperr.Invalid, err)
	}

	// Check the new brain against the others before touching the disk.
	if err := g.checkNewBrain(cfg, m, abs); err != nil {
		return AddResult{}, apperr.Wrap(apperr.Invalid, err)
	}

	// Everything but brain.json, which goes with the update that registers
	// the brain: written only once the checks there pass, and taken away
	// again if config.json then cannot be written.
	scaffold, err := ScaffoldFolder(abs, &m, g.options.Templates)
	if err != nil {
		defer scaffold.Close()
		scaffold.Rollback()
		return AddResult{}, apperr.Wrap(apperr.Unknown, err)
	}
	defer scaffold.Close()
	created := scaffold.Created()
	writesManifest := false
	if _, err := g.store.UpdateTx("", func(c *config.File, tx *config.Tx) error {
		// Again, against the config this update changes. addMu keeps adds
		// apart, but not the other config writers: a brain saved or
		// approved since the check above may claim the same group or the
		// DMs, and the two must not both be registered.
		eff, err := g.store.Resolve(c)
		if err != nil {
			return err
		}
		if err := g.checkNewBrain(eff, m, abs); err != nil {
			return err
		}
		// A brain.json the folder has, one that does not load say, stays
		// as ScaffoldBrain would leave it.
		if _, err := fsx.ReadBrainFile(abs, manifest.File); !req.UseExisting && errors.Is(err, fs.ErrNotExist) {
			if err := tx.CreateManifest(abs, &m); err != nil {
				return err
			}
			writesManifest = true
		}
		approved := m
		c.BrainDirs = append(c.BrainDirs, config.BrainRef{Path: fsx.Tilde(abs, g.configHome()), Approved: &approved})
		return nil
	}); err != nil {
		scaffold.Rollback()
		return AddResult{}, apperr.Wrap(apperr.Invalid, err)
	}
	if writesManifest {
		created = append(created, manifest.File)
		sort.Strings(created)
	}
	log.Printf("brain %s: added (%s), created %v", m.ID, abs, created)
	return AddResult{ID: m.ID, Path: abs, Created: nonNil(created)}, nil
}

// checkNewBrain checks brain m, to go in folder abs, against the brains cfg
// (an effective configuration) has: the folder must be none of theirs, in
// or around one, and m must not clash with them.
func (g *Registry) checkNewBrain(cfg *config.Settings, m manifest.Manifest, abs string) error {
	if _, problems, _ := g.checkPath(abs, cfg, g.OtherDirs(cfg)); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	specs, _ := g.resolve(cfg)
	return route.Validate(append(specs, manifest.Spec{Manifest: m, Dir: abs}))
}

// otherDirs lists every folder in c's brain_dirs, ~ expanded, approved or
// not: a new brain, or a restore, must not land in, around or on any of
// them, an entry still waiting for approval included.
func (g *Registry) OtherDirs(c *config.Settings) []string {
	var dirs []string
	for _, ref := range c.BrainDirs {
		dirs = append(dirs, ref.Dir(g.options.Home()))
	}
	return dirs
}

func prettyName(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	if s == "" {
		return "Brain"
	}
	// The first rune, not byte: a folder named "éclair" must not lose its é.
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

type Inspection struct {
	AbsPath    string             `json:"abs_path"`
	Entries    *int               `json:"entries,omitempty"`
	Exists     *bool              `json:"exists,omitempty"`
	HasHistory *bool              `json:"has_history,omitempty"`
	HasPrompt  *bool              `json:"has_prompt,omitempty"`
	IsDir      *bool              `json:"is_dir,omitempty"`
	IsGit      *bool              `json:"is_git,omitempty"`
	Manifest   *manifest.Manifest `json:"manifest,omitempty"`
	Problems   []string           `json:"problems"`
	Suggested  *SuggestedBrain    `json:"suggested,omitempty"`
	Warnings   []string           `json:"warnings"`
}
type SuggestedBrain struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (g *Registry) Inspect(path string) (Inspection, error) {
	cfg := g.store.Get()
	abs, problems, warnings := g.checkPath(path, cfg, g.OtherDirs(cfg))
	resp := Inspection{AbsPath: abs, Problems: nonNil(problems), Warnings: nonNil(warnings)}
	if len(problems) > 0 {
		// A folder that cannot be a brain is not looked into: outside the
		// home folder that would be a way to probe any path on disk.
		return resp, nil
	}
	root, err := os.OpenRoot(fsx.Canonical(g.options.Home()))
	if err != nil {
		return Inspection{}, err
	}
	defer root.Close()
	g.inspectFolderMetadata(root, abs, &resp)
	id := manifest.Slugify(filepath.Base(abs))
	taken := map[string]bool{}
	for _, ref := range cfg.BrainDirs {
		if ref.Approved != nil {
			taken[ref.Approved.ID] = true
		}
	}
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", manifest.Slugify(filepath.Base(abs)), i)
	}
	resp.Suggested = &SuggestedBrain{ID: id, Name: prettyName(filepath.Base(abs))}
	return resp, nil
}

// inspectFolderMetadata only receives a canonical path accepted by Registry.checkPath.
// Keep the listing and manifest on the same opened directory.
func (g *Registry) inspectFolderMetadata(home *os.Root, abs string, resp *Inspection) {
	dir, st, err := openInspectFolder(home, abs)
	exists, isDir := err == nil, err == nil && st.IsDir()
	resp.Exists, resp.IsDir = &exists, &isDir
	if err != nil || dir == nil {
		return
	}
	defer dir.Close()
	entries := 0
	if f, err := dir.Open("."); err == nil {
		list, _ := f.ReadDir(-1)
		entries = len(list)
		f.Close()
	}
	resp.Entries = &entries
	is_git := fileExistsInRoot(home, filepath.Join(abs, ".git"))
	resp.IsGit = &is_git
	has_prompt := fileExistsInRoot(home, filepath.Join(abs, manifest.DefaultPromptFile))
	resp.HasPrompt = &has_prompt
	has_history := fileExistsInRoot(home, filepath.Join(abs, g.options.HistoryFile))
	resp.HasHistory = &has_history
	if m, err := manifest.ReadRoot(dir); err == nil {
		resp.Manifest = m
	}
}

// openInspectFolder pins each component of an already canonical, accepted path.
// Reject new symlinks even within home: a replacement could point into a
// protected sibling. Initially legitimate aliases were resolved by Registry.checkPath.
func openInspectFolder(home *os.Root, abs string) (*os.Root, os.FileInfo, error) {
	rel, err := filepath.Rel(home.Name(), abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, nil, errors.New("inspection path is not inside home")
	}
	current, err := home.OpenRoot(".")
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if current != nil {
			current.Close()
		}
	}()
	parts := strings.Split(rel, string(filepath.Separator))
	var st os.FileInfo
	for i, part := range parts {
		st, err = current.Lstat(part)
		if err != nil {
			return nil, nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fsx.ErrSymlink
		}
		if !st.IsDir() {
			if i == len(parts)-1 {
				return nil, st, nil
			}
			return nil, nil, errors.New("inspection parent is not a directory")
		}
		next, err := openInspectChild(current, part, st)
		if err != nil {
			return nil, nil, err
		}
		current.Close()
		current = next
	}
	dir := current
	current = nil // the caller owns the pinned directory
	return dir, st, nil
}

// The identity check closes the Lstat/OpenRoot interval without reading any
// entries from a replacement directory, including a relative symlink in home.
func openInspectChild(parent *os.Root, name string, observed os.FileInfo) (*os.Root, error) {
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	st, err := dir.Stat(".")
	if err != nil || !os.SameFile(observed, st) {
		dir.Close()
		if err == nil {
			err = errors.New("inspection folder changed while opening it")
		}
		return nil, err
	}
	return dir, nil
}

func fileExistsInRoot(root *os.Root, p string) bool {
	rel, err := filepath.Rel(root.Name(), fsx.Canonical(p))
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return false
	}
	_, err = root.Stat(rel)
	return err == nil
}

// inHome builds p, an absolute path a request named, again from your home
// folder and the part of p below it. CheckBrainPath decides whether such a
// folder may be used, but it only reports: the folder it hands back is still
// the one the request named. What a handler then makes, lists or removes is
// built this way, inside the home folder by construction. ok is false for
// the home folder itself and for anything outside it.
func (g *Registry) inHome(p string) (string, bool) {
	home := fsx.Canonical(g.options.Home())
	rel, err := filepath.Rel(home, p)
	if home == "" || err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.Join(home, rel), true
}

type View struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Path     string             `json:"path"`
	Status   string             `json:"status"`
	Manifest *manifest.Manifest `json:"manifest"`
	Disk     *manifest.Manifest `json:"disk_manifest,omitempty"`
	Issues   []Issue            `json:"issues"`
}

func Views(cfg *config.Settings, issues []Issue) []View {
	byEntry := map[int][]Issue{}
	for _, is := range issues {
		// One found at load is about the entry it had then; skip it if the
		// entries changed since.
		if is.AtLoad && (is.Entry >= len(cfg.BrainDirs) || fsx.Canonical(cfg.BrainDirs[is.Entry].Path) != fsx.Canonical(is.Path)) {
			continue
		}
		byEntry[is.Entry] = append(byEntry[is.Entry], is)
	}
	var out []View
	for _, ref := range cfg.BrainDirs {
		v := View{Path: ref.Path, Status: "ok", Manifest: ref.Approved}
		if ref.Approved != nil {
			v.ID, v.Name = ref.Approved.ID, ref.Approved.Name
		}
		if disk, err := manifest.Read(fsx.Canonical(ref.Path)); err == nil && (ref.Approved == nil || !disk.Equal(*ref.Approved)) {
			v.Disk = disk
		}
		out = append(out, v)
	}
	for i := range out {
		out[i].Issues = nonNil(byEntry[i])
		for _, is := range out[i].Issues {
			if out[i].Status == "ok" || is.Kind != "drift" {
				out[i].Status = string(is.Kind)
			}
		}
	}
	return nonNil(out)
}

// nonNil retains the dashboard's empty-array representation.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
