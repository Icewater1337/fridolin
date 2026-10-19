package app

import (
	"errors"
	"fmt"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/registry"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// ResolveBrains resolves the current configuration without freezing the
// security policy or ambient home/layout inputs between candidate folders.
func ResolveBrains(cfg *config.Settings) ([]manifest.Spec, []registry.Issue) {
	return registry.Resolve(cfg.BrainDirs, func(dir, configured string) error {
		return checkBrainDir(dir, configured, cfg, HomeDir())
	})
}

// CheckBrainPath keeps configuration and security-policy projection at the
// command boundary; the registry receives explicit inputs and one checker.
func CheckBrainPath(path string, cfg *config.Settings, otherDirs []string) (string, []string, []string) {
	return registry.CheckPath(path, registry.PathContext{
		Home: HomeDir(), Others: otherDirs,
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
func checkBrainDir(dir, configured string, cfg *config.Settings, home string) error {
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

// updateManifest changes one brain's manifest, see updateManifestIn.
func updateManifest(cs *config.Store, id string, mutate func(m *manifest.Manifest) error) error {
	return registryFor(cs).UpdateManifest(id, mutate)
}

// registryFor validates requested settings first, preserving their errors, then
// also protects the active process's paths until a restart applies the saved
// paths. The nil-store checker is used only for candidate config validation.
func registryFor(cs *config.Store) *registry.Registry {
	options := registry.Options{Home: HomeDir, CheckDir: brainDirCheck(cs), Templates: brainTemplates, HistoryFile: history.FileName}
	if cs != nil {
		options.Home = func() string { return cs.Boot().Home }
	}
	return registry.New(cs, options)
}

// brainDirCheck keeps requested validation first and additionally protects the
// running process when a store is available, retaining its live policy rules.
func brainDirCheck(cs *config.Store) func(string, string, *config.Settings, string) error {
	return func(dir, configured string, cfg *config.Settings, home string) error {
		if err := checkBrainDir(dir, configured, cfg, home); err != nil {
			return err
		}
		if cs != nil {
			return checkBrainDir(dir, configured, RuntimeConfig(cs), cs.Boot().Home)
		}
		return nil
	}
}

func CheckSet(before, after *config.Settings) error { return registryFor(nil).CheckSet(before, after) }
