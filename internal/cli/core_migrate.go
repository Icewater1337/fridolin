package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/coreexport"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/seal"
	"github.com/Icewater1337/fridolin/internal/version"
)

// Moving a native install into the container and back (docs/CORE.md,
// "Moving a native install into the container"). The brain folders, the
// calendar credentials and whatever else of the Mac's home folder the
// config names stay where they are and are bound into the container at
// their own paths, with HOME the Mac's home folder, so that every path in
// config.json, in brain prompts and in brains' scripts means the same
// file there. The compose override that does it, compose.host.yaml beside
// compose.yaml, is the host's: the container cannot add mounts to itself.

// hostOverrideName is the compose override beside compose.yaml that binds
// the Mac's folders into the core; core start, stop, update and run add it
// while it exists.
const hostOverrideName = "compose.host.yaml"

// containerUID is the image's user (container/Dockerfile).
const containerUID = 10001

// migrateMountPoint is where a one-off container sees the local folder an
// export goes through with --local.
const migrateMountPoint = "/migrate"

// rehearsal names: a project, container and volume of their own, never
// the core's.
const (
	rehearsalProject   = "fridolin-rehearsal"
	rehearsalContainer = "fridolin-rehearsal"
	rehearsalVolume    = "fridolin-rehearsal"
	coreVolume         = "fridolin-data"
	// coreContainer is the core's container (container/compose.yaml).
	coreContainer = "fridolin-core"
)

// hostMount is a folder or file of the host bound into the container at
// its own path.
type hostMount struct {
	Path     string
	ReadOnly bool
	// Why says what needs it, for the override's comments and the plan.
	Why string
	// BrainsRead: brains read it (sandbox.allow_paths in the container).
	BrainsRead bool
}

// mountInputs are what nativeMounts reads besides the config.
type mountInputs struct {
	home  string
	extra []string // --mount: read-only, read by brains
	kind  func(string) string
}

// nativeMounts are the host paths a native install's config needs in the
// container: each brain folder outside the data dir (read-write), the
// calendar credentials (sandbox.credentials_dir, read-write as natively:
// the bridge saves a refreshed or new sign-in's token there, by renaming
// a new file over the old one; brains never see the folder), vault.secret_dirs
// (read-only: the vault backs them up and never writes there), sandbox.allow_paths and
// startup_checks.required_files (read-only), plaud.phone_dir, the Plaud
// CLI's login in ~/.plaud with plaud.brain_access (read-write: the login
// is refreshed there), ~/.gitconfig unless sandbox.git_config is "deny",
// and the --mount extras (read-only, read by brains). Paths that do not
// exist are left out with a note; a missing brain folder is an error.
func nativeMounts(cfg *config.Settings, in mountInputs) ([]hostMount, []string, error) {
	home := in.home
	dataDir := app.AbsPath(cfg.DataDir)
	var out []hostMount
	var notes []string
	add := func(p string, ro bool, why string, required, brainsRead bool) error {
		if p == "" {
			return nil
		}
		p = filepath.Clean(fsx.ExpandHome(p, home))
		if in.kind(p) == "" {
			if required {
				return fmt.Errorf("%s %s does not exist", why, p)
			}
			notes = append(notes, fmt.Sprintf("%s %s does not exist: not bound", why, p))
			return nil
		}
		if err := checkHostMount(p, home, dataDir); err != nil {
			return fmt.Errorf("%s %s: %w", why, p, err)
		}
		for i, m := range out {
			if m.Path == p {
				// Read-write wins: a brain folder also listed read-only.
				out[i].ReadOnly = m.ReadOnly && ro
				out[i].BrainsRead = m.BrainsRead || brainsRead
				return nil
			}
		}
		out = append(out, hostMount{Path: p, ReadOnly: ro, Why: why, BrainsRead: brainsRead})
		return nil
	}
	for _, b := range cfg.BrainDirs {
		d := app.AbsPath(b.Dir(home))
		if fsx.Within(d, dataDir) {
			continue
		}
		why := "brain folder"
		if b.Approved != nil {
			why = "brain " + b.Approved.ID + "'s folder"
		}
		if err := add(d, false, why, true, false); err != nil {
			return nil, nil, err
		}
	}
	if err := add(cfg.Sandbox.CredentialsDir, false, "sandbox.credentials_dir", false, false); err != nil {
		return nil, nil, err
	}
	for _, p := range cfg.Vault.SecretDirs {
		// The vault only reads them, to back them up.
		if err := add(p, true, "vault.secret_dirs entry", false, false); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range cfg.Sandbox.AllowPaths {
		if err := add(p, true, "sandbox.allow_paths entry", false, true); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range cfg.StartupChecks.RequiredFiles {
		if covered(out, filepath.Clean(fsx.ExpandHome(p, home))) {
			continue
		}
		if err := add(p, true, "startup_checks.required_files entry", false, false); err != nil {
			return nil, nil, err
		}
	}
	if cfg.Plaud.Enabled {
		if err := add(cfg.Plaud.PhoneDir, false, "plaud.phone_dir", false, false); err != nil {
			return nil, nil, err
		}
		if cfg.Plaud.BrainAccess {
			if err := add(filepath.Join(home, ".plaud"), false, "the Plaud CLI's login (plaud.brain_access)", false, false); err != nil {
				return nil, nil, err
			}
		}
	}
	if cfg.Sandbox.GitConfig != "deny" {
		if err := add(filepath.Join(home, ".gitconfig"), true, "your git identity (sandbox.git_config)", false, false); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range in.extra {
		if !filepath.IsAbs(fsx.ExpandHome(p, home)) {
			return nil, nil, fmt.Errorf("--mount %s: an absolute path or one starting with ~/", p)
		}
		if err := add(p, true, "--mount", true, true); err != nil {
			return nil, nil, err
		}
	}
	return out, notes, nil
}

// covered reports whether p lies in one of mounts, or is one.
func covered(mounts []hostMount, p string) bool {
	return slices.ContainsFunc(mounts, func(m hostMount) bool { return fsx.Within(p, m.Path) })
}

// checkHostMount refuses a host path the container must never see: the
// home folder itself, the native data dir (its bridge.db holds the
// WhatsApp session a second bridge must not run on) or a folder holding
// it, and the usual secrets. Docker Desktop shares /Users with containers
// by default; a path outside the home folder may need its file sharing.
func checkHostMount(p, home, dataDir string) error {
	if p == "/" || p == filepath.Clean(home) {
		return errors.New("is the home folder, which the container never gets whole")
	}
	if fsx.Within(p, dataDir) || fsx.Within(dataDir, p) {
		return fmt.Errorf("is or holds the native data dir %s, whose WhatsApp session only one bridge may use", dataDir)
	}
	for _, s := range []string{".ssh", ".gnupg", ".aws", ".config/gh", ".git-credentials", "Library/Keychains", ".claude", ".claude.json"} {
		if fsx.Within(p, filepath.Join(home, s)) {
			return fmt.Errorf("is inside ~/%s", s)
		}
		if fsx.Within(filepath.Join(home, s), p) {
			return fmt.Errorf("holds ~/%s", s)
		}
	}
	if !fsx.Within(p, home) {
		return fmt.Errorf("is outside the home folder %s; Docker Desktop shares only /Users by default", home)
	}
	return nil
}

// composeOverride is compose.host.yaml for mounts: HOME and
// FRIDOLIN_HOST_HOME set to home, home itself an empty tmpfs of the
// container's user (as the image's own HOME is not kept either), and each
// mount bound at its own path on top of it. env are more NAME=value for
// the container (FRIDOLIN_BRAIN_PATH).
func composeOverride(home, source string, mounts []hostMount, env []string) []byte {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by `fridolin core migrate --from-native` from %s.\n", source)
	b.WriteString(`#
# The host's folders the core needs, bound into the container at their own
# paths, with HOME the host's home folder, so that ~ means the same in
# config.json, in the bridge and in a brain's run (docs/CORE.md, "Moving a
# native install into the container"). fridolin core start, stop, update
# and run add this file to compose.yaml while it exists.
services:
  core:
    environment:
`)
	for _, kv := range append([]string{"HOME=" + home, app.HostHomeVar + "=" + home}, env...) {
		fmt.Fprintf(&b, "      - %s\n", q(kv))
	}
	b.WriteString("    tmpfs:\n")
	fmt.Fprintf(&b, "      - %s\n", q(fmt.Sprintf("%s:uid=%d,gid=%d,mode=0700", home, containerUID, containerUID)))
	if len(mounts) > 0 {
		b.WriteString("    volumes:\n")
	}
	for _, m := range mounts {
		fmt.Fprintf(&b, "      # %s\n", m.Why)
		fmt.Fprintf(&b, "      - type: bind\n        source: %s\n        target: %s\n        read_only: %t\n        bind:\n          create_host_path: false\n", q(m.Path), q(m.Path), m.ReadOnly)
	}
	return []byte(b.String())
}

// rehearsalOverride runs the rehearsal's container beside the native
// bridge: a name of its own, no published port (the native bridge has
// 127.0.0.1:8765), no restarts, and the bridge in dev mode, without
// WhatsApp.
func rehearsalOverride() []byte {
	return []byte(`# fridolin core migrate --from-native --rehearse: never the core.
services:
  core:
    container_name: ` + rehearsalContainer + `
    hostname: ` + rehearsalContainer + `
    restart: "no"
    ports: !reset []
    command: ["run", "--container", "--no-whatsapp"]
`)
}

// brainPathFromUnit is the native service's FRIDOLIN_BRAIN_PATH, the
// folders in brain folders whose programs only that brain's runs get,
// kept to those inside mounts.
func brainPathFromUnit(unit installedUnit, mounts []hostMount) string {
	var keep []string
	for _, d := range filepath.SplitList(unit.Env[app.BrainPATHVar]) {
		if d != "" && filepath.IsAbs(d) && covered(mounts, filepath.Clean(d)) {
			keep = append(keep, filepath.Clean(d))
		}
	}
	return strings.Join(keep, string(filepath.ListSeparator))
}

// migrateFlags are core migrate's flags.
type migrateFlags struct {
	fromNative, toNative, dryRun, local, rehearse, keep, force bool
	config, localDir, image, tag, bucket                       string
	mounts                                                     multiFlag
}

func coreMigrate(ctx context.Context, args []string, env coreEnv, d hostDeps) int {
	fs := flag.NewFlagSet("core migrate", flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	var f migrateFlags
	fs.BoolVar(&f.fromNative, "from-native", false, "move this machine's native install into the core's volume")
	fs.BoolVar(&f.toNative, "to-native", false, "bring the core's volume back into the native data dir (a rollback)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "check and print the plan; change nothing")
	fs.BoolVar(&f.local, "local", false, "carry the export through a local folder instead of the vault bucket")
	fs.StringVar(&f.localDir, "local-dir", "", "the folder for --local (default: a new one in the user cache folder)")
	fs.BoolVar(&f.rehearse, "rehearse", false, "with --from-native: a rehearsal on a copy, in a volume and container of its own, beside the running native bridge")
	fs.BoolVar(&f.keep, "keep", false, "with --rehearse: leave the rehearsal running")
	fs.BoolVar(&f.force, "force", false, "with --from-native: replace a volume with data in it (moved to core/replaced-<time>/ there)")
	fs.Var(&f.mounts, "mount", "with --from-native: also bind this host folder, read-only, and let brains read it (repeatable)")
	fs.StringVar(&f.config, "config", "", "the native config file (default: "+configPath("")+")")
	fs.StringVar(&f.image, "image", "fridolin-core", "the image's name")
	fs.StringVar(&f.tag, "tag", imageTag(d.version), "the image's tag (default: this fridolin's version)")
	fs.StringVar(&f.bucket, "bucket", "fridolin-vault", "the vault bucket, without --local")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	case fs.NArg() > 0:
		fmt.Fprintf(env.stderr, "fridolin core migrate takes no arguments, got %q\n", fs.Args())
		return 2
	case f.fromNative == f.toNative:
		fmt.Fprintln(env.stderr, "fridolin core migrate: pass --from-native or --to-native")
		return 2
	case f.toNative && (f.rehearse || len(f.mounts) > 0):
		fmt.Fprintln(env.stderr, "fridolin core migrate: --rehearse and --mount go with --from-native")
		return 2
	}
	if d.goos == "windows" {
		fmt.Fprintln(env.stderr, "fridolin core migrate: Windows has no native bridge to move; bring the core here with fridolin core run core import --latest (docs/CORE.md, Moving the core to another machine)")
		return 2
	}
	if f.rehearse {
		f.local = true
	}
	if d.docker == "" {
		fmt.Fprintln(env.stderr, dockerMissing)
		return 1
	}
	m := &migration{f: f, env: env, d: d, ctx: ctx, out: env.stdout}
	var err error
	if f.fromNative {
		err = m.fromNative()
	} else {
		err = m.toNative()
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core migrate: %v\n", err)
		return 1
	}
	return 0
}

// migration is one run of core migrate.
type migration struct {
	f   migrateFlags
	env coreEnv
	d   hostDeps
	ctx context.Context
	out io.Writer
}

func (m *migration) say(format string, a ...any) { fmt.Fprintf(m.out, format+"\n", a...) }

// docker runs docker with args and the environment env, its output to the
// migration's.
func (m *migration) docker(env []string, args ...string) error {
	return m.d.run(m.ctx, m.d.docker, args, env, m.out, m.env.stderr)
}

// dockerOut runs docker and returns its output.
func (m *migration) dockerOut(env []string, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	err := m.d.run(m.ctx, m.d.docker, args, env, &out, &errOut)
	if err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// native loads the native install's config.
func (m *migration) native() (*config.Settings, string, error) {
	path := configPath(m.f.config)
	cfg, err := app.LoadExistingConfig(path)
	if err != nil {
		return nil, "", fmt.Errorf("the native config: %w", err)
	}
	return cfg, path, nil
}

// unit reads the native service's launchd agent (or systemd unit); a
// missing one is an empty unit.
func (m *migration) unit() installedUnit {
	p := m.d.unitPath
	if p == "" {
		return installedUnit{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return installedUnit{}
	}
	u, _ := readUnit(m.d.goos, data)
	return u
}

// cutover are the commands that stop and start the native service on this
// host, for the plan.
func (m *migration) serviceCommands() (stop, start []string) {
	switch m.d.goos {
	case "darwin":
		target := fmt.Sprintf("gui/%d/%s", m.d.uid, serviceLabel)
		// disable first: a booted-out agent comes back at the next login.
		stop := []string{"launchctl disable " + target, "launchctl bootout " + target}
		start := []string{"launchctl enable " + target, "launchctl bootstrap gui/" + fmt.Sprint(m.d.uid) + " " + m.d.unitPath}
		return stop, start
	case "linux":
		return []string{"systemctl --user disable --now " + systemdUnitName},
			[]string{"systemctl --user enable --now " + systemdUnitName}
	}
	return []string{"stop the native bridge"}, []string{"start the native bridge"}
}

// composeArgs are compose's -f arguments for the core, with override when
// it is set, and more.
func composeArgs(base string, overrides ...string) []string {
	args := []string{"compose", "-f", base}
	for _, o := range overrides {
		if o != "" {
			args = append(args, "-f", o)
		}
	}
	return args
}

// identity is the post-quantum age identity exports are encrypted to,
// and exportKey the key of their inner layer (docs/VAULT.md,
// "Encryption"): the keychain's, made and stored when there is none,
// except for a rehearsal, which makes them for itself and stores nothing.
func (m *migration) identity() (*age.HybridIdentity, string, error) {
	v, where, err := m.layerSecret(hostsecrets.AgeIdentityPQ, madeIdentity, "post-quantum age identity")
	if err != nil {
		return nil, "", err
	}
	id, err := age.ParseHybridIdentity(strings.TrimSpace(v))
	if err != nil {
		return nil, "", fmt.Errorf("%s is not a post-quantum age identity (AGE-SECRET-KEY-PQ-1…)", hostsecrets.AgeIdentityPQ)
	}
	return id, where, nil
}

func (m *migration) exportKey() ([]byte, string, error) {
	v, where, err := m.layerSecret(hostsecrets.ExportKey, madeKey, "export key")
	if err != nil {
		return nil, "", err
	}
	k, err := seal.ParseKey(v)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", hostsecrets.ExportKey, err)
	}
	return k, where, nil
}

// layerSecret reads the keychain's secret name, making one of kind made
// when there is none (only for this run in a rehearsal or a dry run).
func (m *migration) layerSecret(name string, made madeSecret, what string) (string, string, error) {
	v, err := m.d.secrets.Get(name)
	switch {
	case errors.Is(err, hostsecrets.ErrNotFound) && m.f.rehearse:
		v, err = m.d.makeSecret(made)
		return v, "a new one for this rehearsal only", err
	case errors.Is(err, hostsecrets.ErrNotFound) && m.f.dryRun:
		v, err = m.d.makeSecret(made)
		return v, "none in the keychain: the migration makes one", err
	case errors.Is(err, hostsecrets.ErrNotFound):
		if v, err = m.d.makeSecret(made); err != nil {
			return "", "", err
		}
		if err := m.d.secrets.Set(name, v); err != nil {
			return "", "", err
		}
		m.say("Made a new %s and stored it as %s. Copy it to a password manager: exports made with it cannot be read without it.", what, name)
	case err != nil:
		return "", "", err
	}
	return v, "the keychain's " + name, nil
}

// ensureClaudeToken stores the native service's CLAUDE_CODE_OAUTH_TOKEN
// (the token brains log in with under the macOS sandbox, in the launchd
// agent's environment) as the keychain's fridolin.claude.oauth.token when
// that is missing, which core start passes into the container. The value
// is never printed; it goes to the keychain on security's stdin.
func ensureClaudeToken(secrets hostsecrets.Store, unit installedUnit, dryRun bool) (string, error) {
	have, err := secrets.Has(hostsecrets.ClaudeOAuthToken)
	if err != nil {
		return "", err
	}
	if have {
		return hostsecrets.ClaudeOAuthToken + ": in the keychain", nil
	}
	tok := strings.TrimSpace(unit.Env["CLAUDE_CODE_OAUTH_TOKEN"])
	if tok == "" {
		return hostsecrets.ClaudeOAuthToken + ": missing, and the native service has no CLAUDE_CODE_OAUTH_TOKEN to copy: run claude setup-token and store it (docs/CORE.md, Secrets)", nil
	}
	if dryRun {
		return hostsecrets.ClaudeOAuthToken + ": missing; the migration copies the native service's CLAUDE_CODE_OAUTH_TOKEN into it", nil
	}
	if err := secrets.Set(hostsecrets.ClaudeOAuthToken, tok); err != nil {
		return "", err
	}
	return hostsecrets.ClaudeOAuthToken + ": copied from the native service's environment into the keychain", nil
}

func (m *migration) fromNative() error {
	cfg, cfgPath, err := m.native()
	if err != nil {
		return err
	}
	home := m.d.home
	dataDir := app.AbsPath(cfg.DataDir)
	mode := "Moving"
	switch {
	case m.f.rehearse:
		mode = "Rehearsing the move of"
	case m.f.dryRun:
		mode = "Plan for moving"
	}
	m.say("%s the native install (config %s, data dir %s) into the core's container.", mode, cfgPath, dataDir)

	// 1. What the container needs of this machine.
	mounts, notes, err := nativeMounts(cfg, mountInputs{home: home, extra: m.f.mounts, kind: pathKindOf})
	if err != nil {
		return err
	}
	if m.f.rehearse {
		// Nothing of the Mac's is written by a rehearsal: everything read-only,
		// and no Plaud login, which a second Plaud CLI could refresh away.
		mounts = slices.DeleteFunc(mounts, func(h hostMount) bool { return h.Path == filepath.Join(home, ".plaud") })
		for i := range mounts {
			mounts[i].ReadOnly = true
		}
	}
	unit := m.unit()
	var extraEnv []string
	if bp := brainPathFromUnit(unit, mounts); bp != "" {
		extraEnv = append(extraEnv, app.BrainPATHVar+"="+bp)
	}
	m.say("\nBound into the container at the same paths (HOME=%s):", home)
	for _, h := range mounts {
		rw := "read-write"
		if h.ReadOnly {
			rw = "read-only"
		}
		m.say("  %-10s %s  (%s)", rw, h.Path, h.Why)
	}
	for _, n := range notes {
		m.say("  note: %s", n)
	}
	var allow []string
	for _, h := range mounts {
		if h.BrainsRead {
			allow = append(allow, fsx.Tilde(h.Path, home))
		}
	}

	// 2. The override.
	overrideDir := filepath.Dir(m.d.composePath)
	if m.f.rehearse {
		if overrideDir, err = os.MkdirTemp(m.d.cacheDir, "fridolin-rehearsal-"); err != nil {
			return err
		}
		defer os.RemoveAll(overrideDir)
	}
	override := filepath.Join(overrideDir, hostOverrideName)
	overrideData := composeOverride(home, cfgPath, mounts, extraEnv)
	base := filepath.Join(overrideDir, "compose.yaml")

	// 3. Secrets.
	id, idWhere, err := m.identity()
	if err != nil {
		return err
	}
	exportKey, keyWhere, err := m.exportKey()
	if err != nil {
		return err
	}
	m.say("\nSecrets:\n  %s: %s\n  %s: %s", hostsecrets.AgeIdentityPQ, idWhere, hostsecrets.ExportKey, keyWhere)
	if !m.f.rehearse {
		line, err := ensureClaudeToken(m.d.secrets, unit, m.f.dryRun)
		if err != nil {
			return err
		}
		m.say("  %s", line)
	}

	stop, start := m.serviceCommands()
	if m.f.dryRun {
		m.say("\nThe compose override (%s):\n%s", override, overrideData)
		m.say("Not done (dry run). The move, with the native bridge stopped:")
		m.printCutover(stop, start)
		return nil
	}

	// 4. The native bridge must not run while it is copied, except for a
	// rehearsal, which only reads (SQLite's VACUUM INTO from a read-only
	// connection) and leaves the data dir as it is.
	if !m.f.rehearse {
		lock, err := platform.AcquireLock(config.Layout{Root: dataDir}.Lock())
		if err != nil {
			return fmt.Errorf("%w\nStop the native bridge first: %s", err, strings.Join(stop, "; "))
		}
		defer lock.Release()
	}
	if err := writeCompose(base); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(override, overrideData, 0o600, false); err != nil {
		return err
	}
	m.say("\nWrote %s.", override)

	// 5. Export the native data dir.
	var st cloud.ObjectStore
	var where, localDir string
	if m.f.local {
		localDir = m.f.localDir
		if localDir == "" {
			if localDir, err = os.MkdirTemp(m.d.cacheDir, "fridolin-migrate-"); err != nil {
				return err
			}
		}
		if localDir, err = filepath.Abs(localDir); err != nil {
			return err
		}
		if err := os.MkdirAll(localDir, 0o700); err != nil {
			return err
		}
		if m.f.rehearse {
			defer os.RemoveAll(localDir)
		}
		st, where = &cloud.Dir{Root: localDir}, "folder "+localDir
	} else {
		secrets, err := secretEnv(m.d, io.Discard)
		if err != nil {
			return err
		}
		if st, where, err = coreStoreFrom("", m.f.bucket, append(slices.Clone(m.d.baseEnv), secrets...)); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp(m.d.cacheDir, "fridolin-export-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var brainDirs []string
	for _, b := range cfg.BrainDirs {
		brainDirs = append(brainDirs, app.AbsPath(b.Dir(home)))
	}
	host, _ := os.Hostname()
	logf := func(format string, a ...any) { fmt.Fprintf(m.env.stderr, format+"\n", a...) }
	res, err := coreexport.Export(m.ctx, dataDir, []age.Recipient{id.Recipient()}, st, coreexport.ExportOptions{
		DB: sqliteDB{}, Version: version.String(), Hostname: host, BrainFolders: brainDirs,
		TempDir: tmp, IncludeMedia: true, SealKey: exportKey,
		Logf: func(format string, a ...any) {
			// The brain folders stay on the host: no warning for those.
			if !strings.Contains(format, "outside the data dir") {
				logf(format, a...)
			}
		},
	})
	if err != nil {
		return err
	}
	m.say("Exported %s to %s (%s): %d files, %d bytes, %d databases.", dataDir, res.Key, where, res.Manifest.Files, res.Manifest.Bytes, len(res.Manifest.Databases))

	// 6. Import it into the volume, in a one-off container with the mounts.
	childEnv := append(append(slices.Clone(m.d.baseEnv), "FRIDOLIN_IMAGE="+m.f.image, "FRIDOLIN_VERSION="+m.f.tag), m.d.zoneEnv()...)
	compose := composeArgs(base, override)
	volume := coreVolume
	if m.f.rehearse {
		volume = rehearsalVolume
		rehearsal := filepath.Join(overrideDir, "compose.rehearsal.yaml")
		if err := fsx.WriteFileAtomic(rehearsal, rehearsalOverride(), 0o600, false); err != nil {
			return err
		}
		compose = append([]string{"compose", "-p", rehearsalProject}, composeArgs(base, override, rehearsal)[1:]...)
		childEnv = append(childEnv, "FRIDOLIN_VOLUME="+rehearsalVolume, "FRIDOLIN_AGE_IDENTITY="+id.String())
		if out, _ := m.dockerOut(childEnv, "volume", "ls", "-q", "--filter", "name=^"+rehearsalVolume+"$"); strings.TrimSpace(out) != "" {
			return fmt.Errorf("the volume %s exists from an earlier rehearsal: remove it first (docker volume rm %s)", rehearsalVolume, rehearsalVolume)
		}
	} else {
		secrets, err := secretEnv(m.d, m.env.stderr)
		if err != nil {
			return err
		}
		childEnv = append(childEnv, secrets...)
	}
	if _, err := m.dockerOut(childEnv, "image", "inspect", "--format", "{{.Id}}", m.f.image+":"+m.f.tag); err != nil {
		return fmt.Errorf("the image %s:%s is not here: build it (docs/CORE.md, The image): %w", m.f.image, m.f.tag, err)
	}
	run := append(slices.Clone(compose), "run", "--rm", "--no-deps")
	importArgs := []string{"core", "import", "--key", res.Key, "--from-native"}
	if m.f.local {
		run = append(run, "-v", localDir+":"+migrateMountPoint+":ro")
		importArgs = append(importArgs, "--store", migrateMountPoint)
	}
	for _, a := range allow {
		importArgs = append(importArgs, "--allow-path", a)
	}
	if m.f.rehearse {
		importArgs = append(importArgs, "--rehearsal")
	}
	if m.f.force {
		importArgs = append(importArgs, "--force")
	}
	m.say("\nImporting into the volume %s:", volume)
	if err := m.docker(childEnv, append(append(run, "core"), importArgs...)...); err != nil {
		return fmt.Errorf("the import into %s: %w", volume, err)
	}

	if m.f.rehearse {
		return m.rehearse(childEnv, compose, cfg)
	}

	// 7. The lease, when the config wants one.
	if cfg.Core.Lease.Enabled {
		m.say("\ncore.lease is on: taking the lease for the volume.")
		if err := m.docker(childEnv, append(append(slices.Clone(compose), "run", "--rm", "--no-deps"), "core", "core", "promote")...); err != nil {
			return fmt.Errorf("promote: %w", err)
		}
	} else {
		m.say("\ncore.lease is off in the config: no lease to take.")
	}
	if localDir != "" {
		m.say("\nThe export stays in %s (encrypted to the age identity): it is the native install as it was moved.", localDir)
	}
	m.say("\nThe volume %s holds the native install now. Next:", coreVolume)
	m.printCutover(stop, start)
	return nil
}

// printCutover prints the move and the way back.
func (m *migration) printCutover(stop, start []string) {
	self := "fridolin"
	local := ""
	if m.f.local {
		local = " --local"
	}
	mounts := ""
	for _, p := range m.f.mounts {
		mounts += " --mount " + p
	}
	m.say(`
Cutover:
  %[1]s core migrate --from-native --dry-run%[2]s%[3]s
  %[4]s
  %[1]s core migrate --from-native%[2]s%[3]s
  %[1]s core start
  curl -s http://127.0.0.1:8765/health      # ok
  curl -s http://127.0.0.1:8765/ready       # "whatsapp":"connected"
  docker exec fridolin-core fridolin check
  docker exec fridolin-core fridolin doctor
  then send the bridge a WhatsApp message and wait for the answer.

Rollback:
  %[1]s core stop
  %[1]s core migrate --to-native%[2]s
  %[5]s

Never start the native bridge while the core runs, or the core while the
native bridge runs: two bridges on one WhatsApp session fight over it.
Once the core has been connected, the native bridge.db's WhatsApp session
is stale: core migrate --to-native brings the core's back into the native
data dir (the native data dir's files move to its migration/<time>/ first;
nothing is deleted). Starting the native bridge on its old bridge.db
instead may make WhatsApp ask to pair again.`,
		self, local, mounts, strings.Join(stop, "\n  "), strings.Join(start, "\n  "))
}

// rehearse starts the rehearsal's container on its volume, checks it,
// and removes both unless --keep.
func (m *migration) rehearse(env, compose []string, cfg *config.Settings) (err error) {
	cleanup := func() {
		if m.f.keep {
			m.say("\n--keep: the rehearsal runs on as %s (volume %s). Remove it with:\n  docker rm -f %s && docker volume rm %s",
				rehearsalContainer, rehearsalVolume, rehearsalContainer, rehearsalVolume)
			return
		}
		m.say("\nRemoving the rehearsal:")
		_ = m.docker(env, append(slices.Clone(compose), "down")...)
		// By its own name: never the core's volume.
		_ = m.docker(env, "volume", "rm", rehearsalVolume)
	}
	defer cleanup()
	m.say("\nStarting the rehearsal (%s, dev mode: no WhatsApp):", rehearsalContainer)
	if err := m.docker(env, append(slices.Clone(compose), "up", "-d")...); err != nil {
		return err
	}
	exec := func(args ...string) (string, error) {
		return m.dockerOut(env, append([]string{"exec", rehearsalContainer}, args...)...)
	}
	deadline := m.d.now().Add(2 * time.Minute)
	for {
		if _, err := exec("curl", "-fsS", "http://127.0.0.1:8765/health"); err == nil {
			break
		}
		if m.d.now().After(deadline) {
			logs, _ := m.dockerOut(env, "logs", "--tail", "50", rehearsalContainer)
			return fmt.Errorf("the rehearsal did not answer /health within 2 minutes; its log:\n%s", logs)
		}
		m.d.sleep(2 * time.Second)
	}
	var failed []string
	step := func(name string, args ...string) {
		out, err := exec(args...)
		m.say("\n$ %s\n%s", name, strings.TrimRight(out, "\n"))
		if err != nil {
			m.say("  -> %v", err)
			failed = append(failed, name)
		}
	}
	step("curl /health", "curl", "-sS", "http://127.0.0.1:8765/health")
	step("curl /ready", "curl", "-sS", "http://127.0.0.1:8765/ready")
	step("fridolin check", "fridolin", "check")
	step("fridolin doctor", "fridolin", "doctor")
	if cfg.Sandbox.CredentialsDir != "" {
		step("the calendar credentials, as the bridge", "ls", "-l", fsx.ExpandHome(cfg.Sandbox.CredentialsDir, m.d.home))
	}
	step("the image's tools", "sh", "-c", rehearsalToolsScript)
	first := ""
	for _, b := range cfg.BrainDirs {
		if b.Approved != nil {
			step("a sandboxed run of brain "+b.Approved.ID, "fridolin", "check", "--try-sandbox", b.Approved.ID)
			if first == "" {
				first = b.Approved.ID
			}
		}
	}
	if first != "" {
		step("a transcription in brain "+first+"'s sandbox", "fridolin", "check", "--try-sandbox", first, "--", "sh", "-c", rehearsalTranscribeScript)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the rehearsal failed: %s", strings.Join(failed, ", "))
	}
	m.say("\nThe rehearsal passed.")
	return nil
}

// rehearsalToolsScript prints the versions of what brains' skills run
// besides the agents, and imports the Python packages of the image's venv.
const rehearsalToolsScript = `set -e
gh --version | head -n 1
cloudflared --version
ffmpeg -version | head -n 1
command -v python3
python3 -c 'import torch, transformers, groq, googleapiclient, google_auth_oauthlib, google.oauth2.credentials, yt_dlp; print("python: torch", torch.__version__, "transformers", transformers.__version__)'
ls -l "$HOME/.local/bin" 2>/dev/null || true`

// rehearsalTranscribeScript runs tools/transcribe.py on three seconds of
// a tone with Whisper's smallest model, in a brain's sandbox with its
// caches and TMPDIR, as a brain transcribes a voice note: ffmpeg, the
// venv's torch and transformers, the model's download into the brain's
// cache and the inference. A tone may come out as no text at all, which
// the tool reports as an error after the model ran; that passes too.
const rehearsalTranscribeScript = `set -e
cd "$TMPDIR"
ffmpeg -loglevel error -y -f lavfi -i sine=frequency=440:duration=3 -ar 16000 -ac 1 rehearsal.wav
set +e
out=$(python3 "$FRIDOLIN_TOOLS_DIR/transcribe.py" rehearsal.wav --engine local --model openai/whisper-tiny \
	--output-dir "$TMPDIR/rehearsal-transcripts" --no-keep-original 2>"$TMPDIR/rehearsal-transcribe.log")
status=$?
rm -rf rehearsal.wav "$TMPDIR/rehearsal-transcripts"
echo "$out"
case "$out" in
*'"transcript"'* | *'produced no text'*) exit 0 ;;
esac
tail -n 20 "$TMPDIR/rehearsal-transcribe.log"
exit "$status"`

func (m *migration) toNative() error {
	cfg, cfgPath, err := m.native()
	if err != nil {
		return err
	}
	dataDir := app.AbsPath(cfg.DataDir)
	nativeConfig, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	now := m.d.now()
	ts := now.UTC().Format("20060102T150405Z")
	runDir := filepath.Join(dataDir, "migration", ts)
	m.say("Bringing the core's volume %s back into the native data dir %s.", coreVolume, dataDir)
	m.say("Its files now move to %s first; nothing is deleted.", filepath.Join(runDir, "replaced"))
	_, start := m.serviceCommands()
	if m.f.dryRun {
		m.say("\nNot done (dry run). With the core stopped (fridolin core stop):\n  fridolin core migrate --to-native%s\n  %s", map[bool]string{true: " --local"}[m.f.local], strings.Join(start, "\n  "))
		return nil
	}
	// The core must be stopped; the native bridge too (Activate's lock).
	if out, _ := m.dockerOut(m.d.baseEnv, "ps", "-q", "--filter", "name=^fridolin-core$"); strings.TrimSpace(out) != "" {
		return errors.New("the core runs: stop it first (fridolin core stop)")
	}
	lock, err := platform.AcquireLock(config.Layout{Root: dataDir}.Lock())
	if err != nil {
		return fmt.Errorf("%w (stop the native bridge first)", err)
	}
	lock.Release()

	id, _, err := m.identity()
	if err != nil {
		return err
	}
	exportKey, _, err := m.exportKey()
	if err != nil {
		return err
	}
	secrets, err := secretEnv(m.d, m.env.stderr)
	if err != nil {
		return err
	}
	base := m.d.composePath
	if err := writeCompose(base); err != nil {
		return err
	}
	childEnv := append(append(append(slices.Clone(m.d.baseEnv), "FRIDOLIN_IMAGE="+m.f.image, "FRIDOLIN_VERSION="+m.f.tag), m.d.zoneEnv()...), secrets...)
	run := append(composeArgs(base, m.overridePath()), "run", "--rm", "--no-deps")
	exportArgs := []string{"core", "export"}
	var st cloud.ObjectStore
	var where string
	if m.f.local {
		localDir := m.f.localDir
		if localDir == "" {
			if localDir, err = os.MkdirTemp(m.d.cacheDir, "fridolin-migrate-"); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(localDir, 0o700); err != nil {
			return err
		}
		run = append(run, "-v", localDir+":"+migrateMountPoint)
		exportArgs = append(exportArgs, "--store", migrateMountPoint)
		st, where = &cloud.Dir{Root: localDir}, "folder "+localDir
	} else if st, where, err = coreStoreFrom("", m.f.bucket, childEnv); err != nil {
		return err
	}
	m.say("\nExporting the volume:")
	if err := m.docker(childEnv, append(append(run, "core"), exportArgs...)...); err != nil {
		return fmt.Errorf("the export of the volume: %w", err)
	}
	staging := filepath.Join(runDir, "staged")
	logf := func(format string, a ...any) { fmt.Fprintf(m.env.stderr, format+"\n", a...) }
	res, err := coreexport.Import(m.ctx, st, []age.Identity{id}, staging, coreexport.ImportOptions{SealKey: exportKey, DB: sqliteDB{}, Logf: logf})
	if err != nil {
		return err
	}
	lockFn := func(dir string) (func(), error) {
		l, err := platform.AcquireLock(config.Layout{Root: dir}.Lock())
		if err != nil {
			return nil, err
		}
		return l.Release, nil
	}
	if err := coreexport.ActivateWith(staging, dataDir, coreexport.ActivateOptions{
		Force: true, Now: now, Lock: lockFn, Aside: filepath.Join(runDir, "replaced"), Keep: []string{"migration"},
	}); err != nil {
		return fmt.Errorf("%w (the checked copy stays in %s)", err, staging)
	}
	notes, err := coreexport.RestoreHostKeys(dataDir, nativeConfig)
	if err != nil {
		return err
	}
	for _, n := range notes {
		m.say("  config: %s", n)
	}
	m.say("\nImported %s from %s into %s; what was there is in %s.", res.Key, where, dataDir, filepath.Join(runDir, "replaced"))
	m.say("Start the native bridge (and never the core with it):\n  %s", strings.Join(start, "\n  "))
	return nil
}

// overridePath is compose.host.yaml beside the compose file when it exists.
func (m *migration) overridePath() string {
	p := filepath.Join(filepath.Dir(m.d.composePath), hostOverrideName)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// pathKindOf is what p is: "dir", "file" or "" when it is not there.
func pathKindOf(p string) string {
	st, err := os.Stat(p)
	switch {
	case err != nil:
		return ""
	case st.IsDir():
		return "dir"
	}
	return "file"
}
