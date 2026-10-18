package main

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

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// siteModuleCaps is what one module's module.json declares, as the sandbox
// and the secrets need it. Paths are resolved as the site resolves them
// (modules.ts resolveModulePath): ~/… in HOME, relative in the module folder.
type siteModuleCaps struct {
	ID, Title string
	Dir       string
	Files     []siteFileCap
	Commands  []string // as declared
	Secrets   []string
	Ingest    []string // brains it may hand files to (site_ingest.go)
	Search    []string // brains whose folder it may search (site_ingest.go)
	// Dist is the resolved app.dist, the files the site serves; "" without an app.
	Dist string
}

// siteFileCap is one capabilities.files entry.
type siteFileCap struct {
	Name, Path, Access string
	// Folder: the path is a folder (it is one now, or is written with a
	// trailing /), so everything below it is covered.
	Folder bool
}

// readModuleCaps reads every modules/<id>/module.json in modulesDir. A
// manifest that does not parse, or whose id is not its folder's name, is
// left out: the site skips that module too.
func readModuleCaps(modulesDir, home string) []siteModuleCaps {
	entries, _ := os.ReadDir(modulesDir)
	var out []siteModuleCaps
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
				Commands []string `json:"commands"`
				Secrets  []string `json:"secrets"`
				Ingest   []string `json:"ingest"`
				Search   []string `json:"search"`
			} `json:"capabilities"`
		}
		if json.Unmarshal(data, &mf) != nil || mf.ID != e.Name() {
			continue
		}
		c := siteModuleCaps{ID: mf.ID, Title: mf.Title, Dir: dir, Commands: mf.Capabilities.Commands, Secrets: mf.Capabilities.Secrets, Ingest: mf.Capabilities.Ingest, Search: mf.Capabilities.Search}
		if mf.App != nil && mf.App.Dist != "" {
			c.Dist = resolveModulePath(mf.App.Dist, dir, home)
		}
		for name, f := range mf.Capabilities.Files {
			if f.Path == "" || (f.Access != "read" && f.Access != "write") {
				continue // the site refuses the module (validateManifest)
			}
			p := resolveModulePath(f.Path, dir, home)
			c.Files = append(c.Files, siteFileCap{Name: name, Path: p, Access: f.Access, Folder: strings.HasSuffix(f.Path, "/") || isDir(p)})
		}
		sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Name < c.Files[j].Name })
		out = append(out, c)
	}
	return out
}

// resolveModulePath is modules.ts's resolveModulePath: ~/… is in HOME, a
// relative path is in the module folder.
func resolveModulePath(p, moduleDir, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return expandHome(p, home)
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(moduleDir, p)
}

// resolveSiteCommand finds a capabilities.commands entry: a name on the
// site's PATH, ~/… or an absolute path, or a path inside the module folder.
// It returns "" for one that is not there.
func resolveSiteCommand(c, moduleDir, home, path string) string {
	var p string
	switch {
	case c == "":
		return ""
	case c == "~" || strings.HasPrefix(c, "~/") || filepath.IsAbs(c):
		p = resolveModulePath(c, moduleDir, home)
	case strings.Contains(c, "/"):
		p = filepath.Join(moduleDir, c)
	default:
		return lookPathIn(c, path)
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// siteSystemReads are the folders of the system every program reads:
// libraries, programs, time zones, /etc and the choice of /bin/sh. Writes
// stay denied.
var siteSystemReads = []string{
	"/System",
	"/usr/lib",
	"/usr/libexec",
	"/usr/share",
	"/usr/bin",
	"/usr/sbin",
	"/bin",
	"/sbin",
	"/dev",
	"/private/etc",
	"/private/var/db/timezone",
	"/private/var/select",
}

// siteSoftwareReads are the parts of the Homebrew prefixes that hold
// installed software (Node.js from Homebrew, the libraries it and other
// programs load, the programs a module runs). Their var/ and etc/, which
// hold databases and settings, are not among them.
var siteSoftwareReads = func() []string {
	var out []string
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		for _, sub := range []string{"Cellar", "opt", "bin", "sbin", "lib", "libexec", "share", "Frameworks", "include"} {
			out = append(out, filepath.Join(prefix, sub))
		}
	}
	return out
}()

// siteShells may run: what child_process.exec starts commands with, and
// #!/usr/bin/env. They run only the programs the profile allows.
var siteShells = []string{"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash", "/usr/bin/env"}

// siteOpenSubpaths are the entries of defaultDenySubpaths a module may be
// given by declaring a path in them: the folders where your own documents
// live. The rest of that list (credentials, mail, messages, browser data,
// the bridge's logs) stays out of reach whatever a module declares.
var siteOpenSubpaths = []string{"Documents", "Downloads", "Desktop", "Movies", "Music", "Pictures"}

// siteDenyExtra are home-relative paths, beyond the brains' lists, that no
// module may be given: Claude Code's state and your instructions, and the
// keychains.
var siteDenyExtra = []string{".claude", "CLAUDE.md", "CLAUDE.local.md", ".mcp.json", "Library/Keychains"}

// siteProfileInput is everything the profile is built from.
type siteProfileInput struct {
	home    string
	dataDir string // Fridolin's data dir, which holds <data_dir>/site
	shell   string // the site process: <data_dir>/site/shell
	deploy  string // the live deploy folder
	// deps are node_modules folders whose programs may run (esbuild, which
	// tsx starts, and the like): the shell's and the deploy's.
	deps []string
	tmp  string // the site's TMPDIR, readable and writable
	// data holds the modules' own folders (ctx.dataDir) and spool the
	// handoff to brains (ctx.ingest): readable and writable like tmp.
	data, spool string
	node        string // the node binary, as found
	// reads are the files Fridolin writes for the site: runtime.json,
	// users.json, session.key, previews.json, the Google client.
	reads   []string
	modules []siteModuleCaps
	siteBridgeLimits
	// path is the PATH commands are looked up on.
	path string
}

// siteBridgeLimits is what the bridge keeps from the site beyond the
// profile's own list (set through builderHooks.siteLimits).
type siteBridgeLimits struct {
	// deny and noWrite are more paths no module may be given, or write: see
	// SandboxProfiler.sitePaths.
	deny, noWrite []string
	// bridgePort is the port of the bridge's own HTTP server: the dashboard,
	// and /send and /ingest, which take no token from programs on this Mac.
	bridgePort string
}

// siteSandboxProfile builds the live site's profile. Rules are written in
// order and the last one that matches wins: what is denied to every module
// comes after what the modules declare, and the site's own files come after
// that, so no declaration can open the rest of Fridolin's data folder and
// none can take the site's own files away.
func siteSandboxProfile(in siteProfileInput) (string, error) {
	if !filepath.IsAbs(in.home) {
		return "", fmt.Errorf("HOME %q is not an absolute path", in.home)
	}
	var b profileBuilder
	// The folders an allow may be spelled through: HOME and the data dir,
	// which no module can replace, and the system's own /tmp, /var and /etc.
	for _, root := range []string{in.home, in.dataDir, "/tmp", "/var", "/etc"} {
		if root != "" {
			b.trustRoot(root)
		}
	}
	// reach is every spelling allowed below, whose folders' metadata Node
	// needs to resolve real paths.
	var reach []string
	allow := func(op, filter, p string) { reach = append(reach, b.allow(op, filter, p)...) }

	b.WriteString("(version 1)\n(allow default)\n\n")
	b.WriteString(";; Files: none but those allowed below\n(deny file-read* file-write*)\n\n")

	// ── the system and Node.js ───────────────────────────────────────────
	b.WriteString(";; The system's libraries, programs and settings, and installed software: readable\n")
	b.allow("file-read*", "literal", "/")
	for _, p := range siteSystemReads {
		allow("file-read*", "subpath", p)
	}
	for _, p := range siteSoftwareReads {
		if pathExists(p) {
			allow("file-read*", "subpath", p)
		}
	}
	// Reading a symlink is part of following it.
	for _, p := range []string{"/tmp", "/var", "/etc"} {
		b.allow("file-read*", "literal", p)
	}
	b.allow("file-write*", "literal", "/dev/null")
	if in.node != "" {
		// Its own installation (nvm's and Homebrew's are folders of their
		// own), or only the binary where it shares a prefix.
		real := resolvePath(in.node)
		allow("file-read*", "literal", in.node)
		allow("file-read*", "literal", real)
		if prefix := filepath.Dir(filepath.Dir(real)); filepath.Base(filepath.Dir(real)) == "bin" && !sharedSoftwarePrefix(prefix, in.home) {
			allow("file-read*", "subpath", prefix)
		}
	}
	b.WriteString("\n")

	// ── programs ─────────────────────────────────────────────────────────
	b.WriteString(";; Programs: node, its dependencies' own, a shell, and the modules' capabilities.commands\n")
	b.WriteString("(deny process-exec*)\n")
	execs := []string{}
	if in.node != "" {
		execs = append(execs, in.node)
	}
	execs = append(execs, siteShells...)
	for _, p := range execs {
		b.allowResolved("process-exec*", "literal", p)
	}
	for _, dir := range in.deps {
		b.allowResolved("process-exec*", "subpath", dir)
	}
	b.WriteString("\n")

	// ── what the modules declare ─────────────────────────────────────────
	for _, mod := range in.modules {
		if len(mod.Files) == 0 && len(mod.Commands) == 0 && mod.Dist == "" {
			continue
		}
		b.WriteString(";; Module " + sbplComment(mod.ID) + "\n")
		if mod.Dist != "" && usablePath(mod.ID, "app.dist", mod.Dist) {
			allow("file-read*", "subpath", mod.Dist)
		}
		for _, f := range mod.Files {
			if !usablePath(mod.ID, "capabilities.files."+f.Name, f.Path) {
				continue
			}
			ops := "file-read*"
			if f.Access == "write" {
				ops = "file-read* file-write*"
			}
			switch {
			case f.Folder:
				allow(ops, "subpath", f.Path)
			case f.Access == "write":
				// A file to write: with the -journal, -wal and -shm files
				// SQLite keeps next to a database.
				for _, s := range b.allowSpellings(f.Path) {
					b.emit(ruleAllow, "allow "+ops, "regex", s, "^"+regexp.QuoteMeta(s)+`(-journal|-wal|-shm)?$`)
					reach = append(reach, s)
				}
			default:
				allow(ops, "literal", f.Path)
			}
		}
		for _, c := range mod.Commands {
			p := resolveSiteCommand(c, mod.Dir, in.home, in.path)
			if p == "" {
				log.Printf("[site] module %s: the command %q is not installed (looked on %s)", mod.ID, c, in.path)
				continue
			}
			if !usablePath(mod.ID, "capabilities.commands", p) {
				continue
			}
			b.allowResolved("process-exec*", "literal", p)
			// A script is read by its interpreter.
			allow("file-read*", "literal", p)
		}
		b.WriteString("\n")
	}

	// ── the bridge ───────────────────────────────────────────────────────
	if in.bridgePort != "" {
		if !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(in.bridgePort) {
			return "", fmt.Errorf("the bridge's port %q is not a number", in.bridgePort)
		}
		b.WriteString(";; Fridolin's own HTTP server (dashboard, /send, /ingest): not from here, on any address\n")
		b.WriteString(`(deny network-outbound (remote ip "*:` + in.bridgePort + `"))` + "\n\n")
	}

	// ── what no module may be given ──────────────────────────────────────
	b.WriteString(";; Fridolin's data, the bridge's config, database and credentials, and the usual secrets: never, whatever a module declares\n")
	for _, p := range in.neverPaths() {
		b.deny("file-read*", "subpath", p)
		b.deny("file-write*", "subpath", p)
	}
	// ~/.something-token, ~/.something-api-key.
	b.denyBelow("file-read*", in.home, `/\.[^/]*-(token|api-key)$`)
	b.denyBelow("file-write*", in.home, `/\.[^/]*-(token|api-key)$`)
	b.WriteString("\n")

	b.WriteString(";; Loaded or run outside this sandbox: never writable\n")
	for _, p := range in.neverWritable() {
		b.deny("file-write*", "subpath", p)
	}
	b.WriteString("\n")

	// ── the site's own files ─────────────────────────────────────────────
	b.WriteString(";; The site's own code and files (after the data folder's deny)\n")
	allow("file-read*", "subpath", in.shell)
	allow("file-read*", "subpath", in.deploy)
	for _, p := range in.reads {
		if p != "" {
			allow("file-read*", "literal", p)
		}
	}
	for _, p := range []string{in.tmp, in.data, in.spool} {
		if p != "" {
			allow("file-read* file-write*", "subpath", p)
		}
	}
	b.WriteString("\n")

	// ── folders that stay put ────────────────────────────────────────────
	// The sandbox matches paths: a module that could rename a folder above a
	// denied path, or swap a folder it may write for a symlink, could read
	// what the rule protects under another name, or make the next start
	// allow what the symlink points to.
	b.WriteString(";; Folders above the denied paths and the declared ones: not renamable or removable\n")
	b.pinParents(in.home, resolvePath(in.home))
	for _, mod := range in.modules {
		for _, f := range mod.Files {
			if f.Access != "write" || !usablePath("", "", f.Path) {
				continue
			}
			if f.Folder {
				b.pin(f.Path)
			} else {
				for _, s := range b.spellings(f.Path, true) {
					b.emitNoSymlink(rulePin, "literal", s, s)
				}
			}
		}
	}
	b.WriteString("\n")

	// ── metadata on the way ──────────────────────────────────────────────
	// Node resolves the real path of everything it loads, which stats every
	// folder above it. Only their metadata: no listing, no contents.
	b.WriteString(";; The folders above what is allowed: their metadata only\n")
	seen := map[string]bool{}
	var above []string
	for _, p := range reach {
		for d := filepath.Dir(p); !seen[d]; d = filepath.Dir(d) {
			seen[d] = true
			above = append(above, d)
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	sort.Strings(above)
	for _, d := range above {
		b.emit(ruleAllow, "allow file-read-metadata", "literal", d, d)
	}

	if b.err != nil {
		return "", b.err
	}
	return b.String(), nil
}

// neverPaths are what no module may be given, to read or to write:
// Fridolin's data folder under both its names and the configured one, the
// bridge's own files, and the usual secrets.
func (in siteProfileInput) neverPaths() []string {
	out := []string{filepath.Join(in.home, dataDirName), filepath.Join(in.home, legacyDataDirName)}
	if in.dataDir != "" && !slices.Contains(out, filepath.Clean(in.dataDir)) {
		out = append(out, in.dataDir)
	}
	out = append(out, in.deny...)
	for _, sub := range defaultDenySubpaths {
		if !slices.Contains(siteOpenSubpaths, sub) {
			out = append(out, filepath.Join(in.home, sub))
		}
	}
	for _, p := range slices.Concat(defaultDenyLiterals, siteDenyExtra) {
		out = append(out, filepath.Join(in.home, p))
	}
	return out
}

// neverWritable are what no module may write: what runs outside the
// sandbox (see defaultWriteDenySubpaths) and the bridge's own program.
func (in siteProfileInput) neverWritable() []string {
	var out []string
	for _, p := range slices.Concat(defaultWriteDenySubpaths, defaultWriteDenyLiterals) {
		out = append(out, filepath.Join(in.home, p))
	}
	return append(out, in.noWrite...)
}

// allowResolved emits an allow for path as configured and for its real path,
// following every symlink: for programs, which the sandbox checks by the real
// path of what runs, and which a module cannot replace (it writes neither
// them nor the folders they sit in, unless it declares them).
func (b *profileBuilder) allowResolved(op, filter, path string) {
	for _, s := range b.spellings(path, false) {
		b.emit(ruleAllow, "allow "+op, filter, s, s)
	}
}

// usablePath reports whether p can go into a profile, and logs why not.
// One module's odd path must not keep the whole site from starting: the
// module just cannot reach it.
func usablePath(module, field, p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if _, err := sbplString(p); err != nil {
		if module != "" {
			log.Printf("[site] module %s: %s is left out of the sandbox: %v", module, field, err)
		}
		return false
	}
	return true
}

// sharedSoftwarePrefix reports whether prefix holds more than one program's
// installation, so that a node in prefix/bin opens only itself.
func sharedSoftwarePrefix(prefix, home string) bool {
	switch filepath.Clean(prefix) {
	case "/", "/usr", "/usr/local", "/opt/homebrew", "/opt/local", home, filepath.Join(home, ".local"):
		return true
	}
	return false
}

// sbplComment keeps a module id on its comment line.
func sbplComment(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
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

// SiteSandboxStatus is what the dashboard shows about the sandbox.
type SiteSandboxStatus struct {
	On   bool   `json:"on"`
	Note string `json:"note"`
}

// SandboxStatus says whether the live site runs sandboxed.
func (m *SiteManager) SandboxStatus() SiteSandboxStatus {
	if err := siteSandboxAvailable(m.sandboxExec); err != nil {
		return SiteSandboxStatus{Note: err.Error()}
	}
	return SiteSandboxStatus{On: true, Note: "module code reaches only the files, programs and secrets its module.json declares"}
}

// sandboxInput gathers what the live site's profile is built from.
func (m *SiteManager) sandboxInput(node, deploy, root, rtFile string, mods []siteModuleCaps) siteProfileInput {
	in := siteProfileInput{
		home:    homeDir(),
		dataDir: m.dataDir,
		shell:   m.dir("shell"),
		deploy:  deploy,
		deps:    []string{m.dir("shell", "node_modules"), filepath.Join(root, "node_modules")},
		tmp:     m.dir("tmp"),
		data:    m.dir("data"),
		spool:   m.spoolDir(),
		node:    node,
		reads: []string{
			rtFile, m.dir("users.json"), m.dir("session.key"), m.dir("previews.json"),
			// Both: the site's own client, and Fridolin's shared one.
			m.dir("google_client.json"),
		},
		modules: mods,
		path:    siteEnvPath(node),
	}
	if m.googleClient != nil {
		in.reads = append(in.reads, m.googleClient())
	}
	if m.hooks.siteLimits != nil {
		in.siteBridgeLimits = m.hooks.siteLimits()
	}
	return in
}

// prepareDeclaredFolders creates the folders a module may write that do not
// exist yet: the declared folder, or the folder of a declared file. The
// module itself could not, since the profile lets it write only below them.
// Not where the profile would deny the module anyway (closed): a declared
// ~/.aws/credentials leaves a missing ~/.aws missing.
func prepareDeclaredFolders(mods []siteModuleCaps, closed []string) {
	for _, mod := range mods {
		for _, f := range mod.Files {
			if f.Access != "write" || !filepath.IsAbs(f.Path) {
				continue
			}
			dir := f.Path
			if !f.Folder {
				dir = filepath.Dir(f.Path)
			}
			if withinAny(dir, closed) || withinAny(resolvePath(dir), closed) {
				continue
			}
			if !pathExists(dir) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					log.Printf("[site] module %s: could not create %s for capabilities.files.%s: %v", mod.ID, dir, f.Name, err)
				}
			}
		}
	}
}
