package main

import (
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/registry"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// ResolveBrains resolves the current configuration without freezing the
// security policy or ambient home/layout inputs between candidate folders.
func ResolveBrains(cfg *config.Config) ([]manifest.Spec, []registry.Issue) {
	return registry.Resolve(cfg.BrainDirs, func(dir, configured string) error {
		return checkBrainDir(dir, configured, cfg, homeDir())
	})
}

// CheckBrainPath keeps configuration and security-policy projection at the
// command boundary; the registry receives explicit inputs and one checker.
func CheckBrainPath(path string, cfg *config.Config, otherDirs []string) (string, []string, []string) {
	return registry.CheckPath(path, registry.PathContext{
		Home: homeDir(), Others: otherDirs,
		CheckDir: func(dir, configured, home string) error {
			return checkBrainDir(dir, configured, cfg, home)
		},
	})
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
	home := fsx.Canonical(homeDir())
	rel, err := filepath.Rel(home, p)
	if home == "" || err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.Join(home, rel), true
}

// updateManifest changes one brain's manifest, see updateManifestIn.
func updateManifest(cs *config.Store, id string, mutate func(m *manifest.Manifest) error) error {
	_, err := cs.UpdateTx("", func(c *config.Config, tx *config.Tx) error { return updateManifestIn(c, tx, id, mutate) })
	return err
}

// updateManifestIn changes brain id's manifest in c: mutate edits a copy of
// the approved one, which becomes the approved copy and is staged in tx as
// brain.json in the brain's folder, so that the two change together and
// the change is not reported as drift. It is the one way the bridge
// changes a manifest; an error from mutate leaves both as they were.
func updateManifestIn(c *config.Config, tx *config.Tx, id string, mutate func(m *manifest.Manifest) error) error {
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
		if err := tx.WriteManifest(ref.Dir(homeDir()), &m); err != nil {
			return err
		}
		n := m.Normalized()
		ref.Approved = &n
		return nil
	}
	return fmt.Errorf("no brain %q", id)
}
