package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// siteModuleCaps is what one module's module.json declares, as the sandbox
// and the secrets need it. Paths are resolved as the site resolves them
// (modules.ts resolveModulePath): ~/… in HOME, relative in the module folder.
type SiteModule struct {
	ID, Title string
	Dir       string
	Files     []FileCapability
	Commands  []string // as declared
	Secrets   []string
	Ingest    []string // brains it may hand files to (site_ingest.go)
	Search    []string // brains whose folder it may search (site_ingest.go)
	// Dist is the resolved app.dist, the files the site serves; "" without an app.
	Dist string
}

// siteFileCap is one capabilities.files entry.
type FileCapability struct {
	Name, Path, Access string
	// Folder: the path is a folder (it is one now, or is written with a
	// trailing /), so everything below it is covered.
	Folder bool
}

// resolveModulePath is modules.ts's resolveModulePath: ~/… is in HOME, a
// relative path is in the module folder.
func ResolveModulePath(p, moduleDir, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return fsx.ExpandHome(p, home)
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(moduleDir, p)
}

// resolveSiteCommand finds a capabilities.commands entry: a name on the
// site's PATH, ~/… or an absolute path, or a path inside the module folder.
// It returns "" for one that is not there.
func ResolveSiteCommand(c, moduleDir, home, path string) string {
	var p string
	switch {
	case c == "":
		return ""
	case c == "~" || strings.HasPrefix(c, "~/") || filepath.IsAbs(c):
		p = ResolveModulePath(c, moduleDir, home)
	case strings.Contains(c, "/"):
		p = filepath.Join(moduleDir, c)
	default:
		return sandboxLookPathIn(c, path)
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
type SitePolicy struct {
	WorkingDir    string
	WorkingDirErr error
	Logf          func(string, ...any)
	Home          string
	DataDir       string // Fridolin's data dir, which holds <data_dir>/site
	Shell         string // the site process: <data_dir>/site/shell
	Deploy        string // the live deploy folder
	// deps are node_modules folders whose programs may run (esbuild, which
	// tsx starts, and the like): the shell's and the deploy's.
	Deps []string
	Tmp  string // the site's TMPDIR, readable and writable
	// data holds the modules' own folders (ctx.dataDir) and spool the
	// handoff to brains (ctx.ingest): readable and writable like tmp.
	Data, Spool string
	Node        string // the node binary, as found
	// reads are the files Fridolin writes for the site: runtime.json,
	// users.json, session.key, previews.json, the Google client.
	Reads   []string
	Modules []SiteModule
	SiteLimits
	// path is the PATH commands are looked up on.
	Path string
}

// siteBridgeLimits is what the bridge keeps from the site beyond the
// profile's own list (set through builderHooks.siteLimits).
type SiteLimits struct {
	// deny and noWrite are more paths no module may be given, or write: see
	// SandboxProfiler.sitePaths.
	Deny, NoWrite []string
	// bridgePort is the port of the bridge's own HTTP server: the dashboard,
	// /send, /ingest and /brain/. A module has no token for them, but /send
	// still takes token-free sends to the allowed chats while
	// script_api.tokenless_send is on.
	BridgePort string
}

// siteSandboxProfile builds the live site's profile. Rules are written in
// order and the last one that matches wins: what is denied to every module
// comes after what the modules declare, and the site's own files come after
// that, so no declaration can open the rest of Fridolin's data folder and
// none can take the site's own files away.
func SiteProfile(in SitePolicy) (string, error) {
	if !filepath.IsAbs(in.Home) {
		return "", fmt.Errorf("HOME %q is not an absolute path", in.Home)
	}
	b := profileBuilder{cwd: in.WorkingDir, cwdErr: in.WorkingDirErr}
	// The folders an allow may be spelled through: HOME and the data dir,
	// which no module can replace, and the system's own /tmp, /var and /etc.
	for _, root := range []string{in.Home, in.DataDir, "/tmp", "/var", "/etc"} {
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
		if sandboxPathExists(p) {
			allow("file-read*", "subpath", p)
		}
	}
	// Reading a symlink is part of following it.
	for _, p := range []string{"/tmp", "/var", "/etc"} {
		b.allow("file-read*", "literal", p)
	}
	b.allow("file-write*", "literal", "/dev/null")
	if in.Node != "" {
		// Its own installation (nvm's and Homebrew's are folders of their
		// own), or only the binary where it shares a prefix.
		real := fsx.Resolve(in.Node)
		allow("file-read*", "literal", in.Node)
		allow("file-read*", "literal", real)
		if prefix := filepath.Dir(filepath.Dir(real)); filepath.Base(filepath.Dir(real)) == "bin" && !sharedSoftwarePrefix(prefix, in.Home) {
			allow("file-read*", "subpath", prefix)
		}
	}
	b.WriteString("\n")

	// ── programs ─────────────────────────────────────────────────────────
	b.WriteString(";; Programs: node, its dependencies' own, a shell, and the modules' capabilities.commands\n")
	b.WriteString("(deny process-exec*)\n")
	execs := []string{}
	if in.Node != "" {
		execs = append(execs, in.Node)
	}
	execs = append(execs, siteShells...)
	for _, p := range execs {
		b.allowResolved("process-exec*", "literal", p)
	}
	for _, dir := range in.Deps {
		b.allowResolved("process-exec*", "subpath", dir)
	}
	b.WriteString("\n")

	// ── what the modules declare ─────────────────────────────────────────
	for _, mod := range in.Modules {
		if len(mod.Files) == 0 && len(mod.Commands) == 0 && mod.Dist == "" {
			continue
		}
		b.WriteString(";; Module " + sbplComment(mod.ID) + "\n")
		if mod.Dist != "" && in.usablePath(mod.ID, "app.dist", mod.Dist) {
			allow("file-read*", "subpath", mod.Dist)
		}
		for _, f := range mod.Files {
			if !in.usablePath(mod.ID, "capabilities.files."+f.Name, f.Path) {
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
					b.emit("allow "+ops, "regex", "^"+regexp.QuoteMeta(s)+`(-journal|-wal|-shm)?$`)
					reach = append(reach, s)
				}
			default:
				allow(ops, "literal", f.Path)
			}
		}
		for _, c := range mod.Commands {
			p := ResolveSiteCommand(c, mod.Dir, in.Home, in.Path)
			if p == "" {
				in.Logf("[site] module %s: the command %q is not installed (looked on %s)", mod.ID, c, in.Path)
				continue
			}
			if !in.usablePath(mod.ID, "capabilities.commands", p) {
				continue
			}
			b.allowResolved("process-exec*", "literal", p)
			// A script is read by its interpreter.
			allow("file-read*", "literal", p)
		}
		b.WriteString("\n")
	}

	// ── the bridge ───────────────────────────────────────────────────────
	if in.BridgePort != "" {
		if !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(in.BridgePort) {
			return "", fmt.Errorf("the bridge's port %q is not a number", in.BridgePort)
		}
		b.WriteString(";; Fridolin's own HTTP server (dashboard, /send, /ingest): not from here, on any address\n")
		b.WriteString(`(deny network-outbound (remote ip "*:` + in.BridgePort + `"))` + "\n\n")
	}

	// ── what no module may be given ──────────────────────────────────────
	b.WriteString(";; Fridolin's data, the bridge's config, database and credentials, and the usual secrets: never, whatever a module declares\n")
	for _, p := range in.neverPaths() {
		b.deny("file-read*", "subpath", p)
		b.deny("file-write*", "subpath", p)
	}
	// ~/.something-token, ~/.something-api-key.
	b.denyBelow("file-read*", in.Home, `/\.[^/]*-(token|api-key)$`)
	b.denyBelow("file-write*", in.Home, `/\.[^/]*-(token|api-key)$`)
	// The browsers whose every release channel has a folder of its own, by
	// the start of its name; the others' profiles are in neverPaths.
	appSupport := filepath.Join(in.Home, "Library", "Application Support")
	b.denyBelow("file-read*", appSupport, BrowserProfilePattern())
	b.denyBelow("file-write*", appSupport, BrowserProfilePattern())
	b.WriteString("\n")

	b.WriteString(";; Loaded or run outside this sandbox: never writable\n")
	for _, p := range in.neverWritable() {
		b.deny("file-write*", "subpath", p)
	}
	b.WriteString("\n")

	// ── the site's own files ─────────────────────────────────────────────
	b.WriteString(";; The site's own code and files (after the data folder's deny)\n")
	allow("file-read*", "subpath", in.Shell)
	allow("file-read*", "subpath", in.Deploy)
	for _, p := range in.Reads {
		if p != "" {
			allow("file-read*", "literal", p)
		}
	}
	for _, p := range []string{in.Tmp, in.Data, in.Spool} {
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
	b.pinParents(in.Home, fsx.Resolve(in.Home))
	for _, mod := range in.Modules {
		for _, f := range mod.Files {
			if f.Access != "write" || !in.usablePath("", "", f.Path) {
				continue
			}
			if f.Folder {
				b.pin(f.Path)
			} else {
				for _, s := range b.spellings(f.Path, true) {
					b.emitNoSymlink("literal", s)
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
		b.emit("allow file-read-metadata", "literal", d)
	}

	if b.err != nil {
		return "", b.err
	}
	return b.String(), nil
}

// neverPaths are what no module may be given, to read or to write:
// Fridolin's data folder under both its names and the configured one, the
// bridge's own files, and the usual secrets.
func (in SitePolicy) neverPaths() []string {
	out := []string{filepath.Join(in.Home, ".fridolin"), filepath.Join(in.Home, ".whatsapp-claude-bridge")}
	if in.DataDir != "" && !slices.Contains(out, filepath.Clean(in.DataDir)) {
		out = append(out, in.DataDir)
	}
	out = append(out, in.Deny...)
	for _, sub := range defaultDenySubpaths {
		if !slices.Contains(siteOpenSubpaths, sub) {
			out = append(out, filepath.Join(in.Home, sub))
		}
	}
	for _, p := range slices.Concat(defaultDenyLiterals, siteDenyExtra) {
		out = append(out, filepath.Join(in.Home, p))
	}
	return out
}

// unwritable reports whether the profile keeps every module from writing
// p, whatever it declares: never given, never writable, or a browser's
// profile.
func (in SitePolicy) Unwritable(p string) bool {
	return sandboxWithinAny(p, in.neverPaths()) || sandboxWithinAny(p, in.neverWritable()) || inBrowserProfile(in.Home, p)
}

// neverWritable are what no module may write: what runs outside the
// sandbox (see defaultWriteDenySubpaths) and the bridge's own program.
func (in SitePolicy) neverWritable() []string {
	var out []string
	for _, p := range slices.Concat(defaultWriteDenySubpaths, defaultWriteDenyLiterals) {
		out = append(out, filepath.Join(in.Home, p))
	}
	return append(out, in.NoWrite...)
}

// allowResolved emits an allow for path as configured and for its real path,
// following every symlink: for programs, which the sandbox checks by the real
// path of what runs, and which a module cannot replace (it writes neither
// them nor the folders they sit in, unless it declares them).
func (b *profileBuilder) allowResolved(op, filter, path string) {
	for _, s := range b.spellings(path, false) {
		b.emit("allow "+op, filter, s)
	}
}

// usablePath reports whether p can go into a profile, and logs why not.
// One module's odd path must not keep the whole site from starting: the
// module just cannot reach it.
func (in SitePolicy) usablePath(module, field, p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if _, err := textx.SBPLString(p); err != nil {
		if module != "" && in.Logf != nil {
			in.Logf("[site] module %s: %s is left out of the sandbox: %v", module, field, err)
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

func sandboxPathExists(p string) bool { _, err := os.Stat(p); return err == nil }
func sandboxLookPathIn(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
