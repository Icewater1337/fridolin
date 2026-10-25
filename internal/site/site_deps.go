package site

// A checkout's npm dependencies. A site repository may keep its package.json
// beside modules/ (Repo.Subdir), at its root, or in a module's own folder:
// Node looks for a package in node_modules beside the importing file and in
// every folder above it, so each module's dependencies are installed into
// the folder of the package.json nearest above its manifest.
//
// Installs with a lock file are kept in <data_dir>/site/npm-cache, by the
// lock file's hash, so a deploy whose dependencies did not change copies
// them instead of running npm ci again.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// siteKeepNpmCaches is how many cached installs are kept.
const siteKeepNpmCaches = 6

// packageDirs lists the folders of a checkout (top) whose dependencies are
// installed: root's own when it has a package.json, and for each module in
// root/modules the folder of the package.json nearest above its manifest,
// up to top. A module with none above it adds nothing.
func packageDirs(top, root string) []string {
	var dirs []string
	add := func(d string) {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if fsx.Exists(filepath.Join(root, "package.json")) {
		add(root)
	}
	modulesDir := filepath.Join(root, "modules")
	entries, _ := os.ReadDir(modulesDir)
	for _, e := range entries {
		dir := filepath.Join(modulesDir, e.Name())
		if !e.IsDir() || !fsx.Exists(filepath.Join(dir, "module.json")) {
			continue
		}
		if d := nearestPackageDir(top, dir); d != "" {
			add(d)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// nearestPackageDir is the first of dir and the folders above it, up to and
// with top, that holds a package.json, or "".
func nearestPackageDir(top, dir string) string {
	top = filepath.Clean(top)
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if fsx.Exists(filepath.Join(d, "package.json")) {
			return d
		}
		if d == top || filepath.Dir(d) == d || !strings.HasPrefix(d, top+string(filepath.Separator)) {
			return ""
		}
	}
}

// missingDeps are those of dirs without a node_modules folder.
func missingDeps(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if !fsx.Exists(filepath.Join(d, "node_modules")) {
			out = append(out, d)
		}
	}
	return out
}

// relDir names dir as from top, for a job's log ("." for top itself).
func relDir(top, dir string) string {
	if rel, err := filepath.Rel(top, dir); err == nil {
		return filepath.ToSlash(rel)
	}
	return dir
}

// installDeploy installs a deploy's dependencies into dir, from the cache
// when its lock file was installed before.
func (m *SiteManager) installDeploy(ctx context.Context, dir string) error {
	nodeModules := filepath.Join(dir, "node_modules")
	key := m.npmCacheKey(dir)
	if key != "" {
		cached := m.dir("npm-cache", key, "node_modules")
		if fsx.Exists(cached) {
			if err := copyTree(ctx, cached, nodeModules); err == nil {
				now := time.Now()
				_ = os.Chtimes(filepath.Dir(cached), now, now) // the newest are kept
				return nil
			}
			_ = os.RemoveAll(nodeModules)
		}
	}
	if err := m.npmInstall(ctx, dir, false); err != nil {
		return err
	}
	// npm makes none for a package.json without dependencies; the folder
	// says the install is done.
	if err := os.MkdirAll(nodeModules, 0o700); err != nil {
		return err
	}
	if key != "" {
		m.saveNpmCache(ctx, key, nodeModules)
	}
	return nil
}

// npmCacheKey identifies an install by what decides its result: the lock
// file and package.json, the node it runs with and the platform. "" when
// dir has no lock file, whose installs are not cached.
func (m *SiteManager) npmCacheKey(dir string) string {
	lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return ""
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	node, _ := m.host.Node()
	h := sha256.New()
	fmt.Fprintf(h, "%s/%s\x00%s\x00", runtime.GOOS, runtime.GOARCH, node)
	h.Write(pkg)
	h.Write([]byte{0})
	h.Write(lock)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// saveNpmCache keeps a copy of an install under key and drops the oldest
// beyond siteKeepNpmCaches. A copy that fails only costs the next deploy
// an install.
func (m *SiteManager) saveNpmCache(ctx context.Context, key, nodeModules string) {
	base := m.dir("npm-cache")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return
	}
	tmp, err := os.MkdirTemp(base, ".new-")
	if err != nil {
		return
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(ctx, nodeModules, filepath.Join(tmp, "node_modules")); err != nil {
		log.Printf("[site] keeping the npm install of %s: %v", key, err)
		return
	}
	dst := filepath.Join(base, key)
	_ = os.RemoveAll(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return
	}
	entries, _ := os.ReadDir(base)
	type cache struct {
		name string
		mod  int64
	}
	var caches []cache
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			caches = append(caches, cache{e.Name(), info.ModTime().UnixNano()})
		}
	}
	slices.SortFunc(caches, func(a, b cache) int { return cmp.Compare(b.mod, a.mod) })
	for _, c := range caches[min(len(caches), siteKeepNpmCaches):] {
		_ = os.RemoveAll(filepath.Join(base, c.name))
	}
}

// copyTree copies a folder; cp keeps node_modules' symlinks (its .bin) as they are.
func copyTree(ctx context.Context, src, dst string) error {
	if out, err := exec.CommandContext(ctx, "cp", "-R", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp: %v %s", err, out)
	}
	return nil
}

// ensureLiveDeps installs what the live deploy is missing before the site
// starts: a data dir moved to another machine (docs/CORE.md) arrives
// without node_modules, and the site would start without its packages.
func (m *SiteManager) ensureLiveDeps(ctx context.Context) {
	s := m.Settings()
	if s.Approved == "" {
		return
	}
	top := m.deployDir(s.Approved)
	if !fsx.Exists(filepath.Join(top, ".fridolin-ready")) {
		return
	}
	for _, dir := range missingDeps(packageDirs(top, filepath.Join(top, filepath.FromSlash(s.Repo.Subdir)))) {
		log.Printf("[site] installing the dependencies of %s in %s", s.Approved[:12], relDir(top, dir))
		if err := m.installDeploy(ctx, dir); err != nil {
			log.Printf("[site] installing the dependencies of %s: %v", s.Approved[:12], err)
		}
	}
}
