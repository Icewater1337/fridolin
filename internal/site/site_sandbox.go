package site

import (
	"encoding/json"
	"errors"
	"fmt"

	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// The live site's sandbox (docs/SITE.md, "What a module can reach").
//
// On macOS the site process runs under sandbox-exec, with a profile built
// from what the live commit's modules declare. A brain's profile allows by
// default and denies a list; this one denies every file by default. The site
// reads the system's files, Node.js, its own code (the shell and the live
// deploy) and the few files Fridolin writes for it; module code adds what its
// module.json declares in capabilities.files, by access mode, and nothing
// else. It writes only what is declared "write" and its own temp folder, and
// runs only node, the programs of its dependencies, a shell and the modules'
// capabilities.commands, all under the same profile, and cannot connect to the
// bridge's own HTTP port. Some paths stay out of reach even when a module
// declares them: Fridolin's data folder (all but the site's own files), the
// bridge's config, database and credentials, and the usual secrets.
//
// All modules share the one process, so the profile allows what any of them
// declares; ctx.file and ctx.secret hand each module only its own.
//
// Linux has no sandbox-exec: there the site runs without a sandbox, with
// every file the bridge's user can reach.

// readModuleCaps reads every modules/<id>/module.json in modulesDir. A
// manifest that does not parse, or whose id is not its folder's name, is
// left out: the site skips that module too.
func readModuleCaps(modulesDir, home string) []sandbox.SiteModule {
	entries, _ := os.ReadDir(modulesDir)
	var out []sandbox.SiteModule
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(modulesDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "module.json"))
		if err != nil {
			continue
		}
		var mf struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			App   *struct {
				Dist string `json:"dist"`
			} `json:"app"`
			Capabilities struct {
				Files map[string]struct {
					Path   string `json:"path"`
					Access string `json:"access"`
				} `json:"files"`
				Commands   []string        `json:"commands"`
				Secrets    []string        `json:"secrets"`
				Ingest     []string        `json:"ingest"`
				Search     []string        `json:"search"`
				OpenAI     json.RawMessage `json:"openai"`
				ElevenLabs json.RawMessage `json:"elevenlabs"`
			} `json:"capabilities"`
		}
		if decodeManifest(data, &mf) != nil || mf.ID != e.Name() {
			continue
		}
		c := sandbox.SiteModule{ID: mf.ID, Title: mf.Title, Dir: dir, Commands: mf.Capabilities.Commands, Secrets: mf.Capabilities.Secrets, Ingest: mf.Capabilities.Ingest, Search: mf.Capabilities.Search}
		for name, raw := range map[string]json.RawMessage{"openai": mf.Capabilities.OpenAI, "elevenlabs": mf.Capabilities.ElevenLabs} {
			if d, ok := connectionCap(raw); ok {
				if c.Connections == nil {
					c.Connections = map[string]sandbox.ConnectionCapability{}
				}
				c.Connections[name] = d
			}
		}
		if mf.App != nil && mf.App.Dist != "" {
			c.Dist = sandbox.ResolveModulePath(mf.App.Dist, dir, home)
		}
		for name, f := range mf.Capabilities.Files {
			if f.Path == "" || (f.Access != "read" && f.Access != "write") {
				continue // the site refuses the module (validateManifest)
			}
			p := sandbox.ResolveModulePath(f.Path, dir, home)
			c.Files = append(c.Files, sandbox.FileCapability{Name: name, Path: p, Access: f.Access, Folder: strings.HasSuffix(f.Path, "/") || isDir(p)})
		}
		sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Name < c.Files[j].Name })
		out = append(out, c)
	}
	return out
}

// connectionCap reads capabilities.openai or capabilities.elevenlabs.
// One that does not parse grants nothing: the site refuses the module
// (validateManifest), and the bridge must not read it as no limit.
func connectionCap(raw json.RawMessage) (sandbox.ConnectionCapability, bool) {
	var d struct {
		Use        []string `json:"use"`
		Models     []string `json:"models"`
		MonthlyUSD float64  `json:"monthly_usd"`
	}
	if len(raw) == 0 || decodeManifest(raw, &d) != nil || len(d.Use) == 0 || d.MonthlyUSD < 0 {
		return sandbox.ConnectionCapability{}, false
	}
	return sandbox.ConnectionCapability{Use: d.Use, Models: d.Models, MonthlyUSD: d.MonthlyUSD}, true
}

// errSiteNoSandbox: the system has no sandbox-exec.
var errSiteNoSandbox = errors.New("the site runs without a sandbox on " + runtime.GOOS + ": sandbox-exec is macOS-only, so module code can reach every file this user can")

// siteSandboxAvailable reports why the live site cannot be sandboxed here,
// or nil when it can.
func siteSandboxAvailable(execPath string) error {
	if runtime.GOOS != "darwin" {
		return errSiteNoSandbox
	}
	if _, err := os.Stat(execPath); err != nil {
		return fmt.Errorf("%s not available: %w", execPath, err)
	}
	return nil
}

// SandboxStatus says whether the live site runs sandboxed.
func (m *SiteManager) SandboxStatus() SiteSandboxStatus {
	if err := siteSandboxAvailable(m.sandboxExec); err != nil {
		return SiteSandboxStatus{Note: err.Error()}
	}
	return SiteSandboxStatus{On: true, Note: "module code reaches only the files, programs and secrets its module.json declares"}
}

// siteDeps are the node_modules folders whose programs the live site may
// run: the shell's, root's, and those beside the package.json of each module
// (site_deps.go), all inside the deploy, which the site reads anyway.
func siteDeps(shell, deploy, root string) []string {
	deps := []string{shell, filepath.Join(root, "node_modules")}
	for _, d := range packageDirs(deploy, root) {
		if nm := filepath.Join(d, "node_modules"); !slices.Contains(deps, nm) {
			deps = append(deps, nm)
		}
	}
	return deps
}

// sandboxInput gathers what the live site's profile is built from.
func (m *SiteManager) sandboxInput(node, deploy, root, rtFile string, mods []sandbox.SiteModule) sandbox.SitePolicy {
	in := sandbox.SitePolicy{
		Home:    m.host.Home(),
		DataDir: m.dataDir,
		Shell:   m.dir("shell"),
		Deploy:  deploy,
		Deps:    siteDeps(m.dir("shell", "node_modules"), deploy, root),
		Tmp:     m.dir("tmp"),
		Data:    m.dir("data"),
		Spool:   m.spoolDir(),
		Node:    node,
		Reads: []string{
			rtFile, m.dir("users.json"), m.dir("session.key"), m.dir("previews.json"), m.dir("devices.json"),
			// Both: the site's own client, and Fridolin's shared one.
			m.dir("google_client.json"),
		},
		Modules: mods,
		Path:    siteProcessPath(node),
	}
	if m.googleClient != nil {
		in.Reads = append(in.Reads, m.googleClient())
	}
	if m.hooks.SiteLimits != nil {
		in.SiteLimits = m.hooks.SiteLimits()
	}
	return in
}

// prepareDeclaredFolders creates the folders a module may write that do not
// exist yet: the declared folder, or the folder of a declared file. The
// module itself could not, since the profile lets it write only below them.
// Not where the profile would deny the module anyway (closed): a declared
// ~/.aws/credentials leaves a missing ~/.aws missing.
func prepareDeclaredFolders(mods []sandbox.SiteModule, closed func(string) bool) {
	for _, mod := range mods {
		for _, f := range mod.Files {
			if f.Access != "write" || !filepath.IsAbs(f.Path) {
				continue
			}
			dir := f.Path
			if !f.Folder {
				dir = filepath.Dir(f.Path)
			}
			if closed(dir) || closed(fsx.Resolve(dir)) {
				continue
			}
			if !sitePathExists(dir) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					log.Printf("[site] module %s: could not create %s for capabilities.files.%s: %v", mod.ID, dir, f.Name, err)
				}
			}
		}
	}
}
