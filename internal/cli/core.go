package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/container"
	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/coreexport"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/seal"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/version"
)

const coreUsage = `fridolin core — run, move and inspect the containerized core (docs/CORE.md)

On the host that runs Docker:
  fridolin core start [--compose PATH] [--image NAME] [--tag TAG]
                       start the core container with the secrets from the
                       keychain; makes the age identity when there is none
  fridolin core stop   stop it (the volume stays)
  fridolin core build [--repo DIR]
                       build the image of this version from a checkout (default .)
  fridolin core update pull the image of this version and recreate the container
  fridolin core migrate --from-native [--dry-run] [--local] [--mount PATH]...
                       move this machine's native install into the core's
                       volume, its brain folders bound in at their own paths
                       (--rehearse: on a copy, beside the running native bridge)
  fridolin core migrate --to-native [--dry-run] [--local]
                       bring the volume back into the native data dir
  fridolin core secrets [list]
                       which secrets the host holds for the core (never their values)
  fridolin core secrets set NAME [--replace]
                       store one, typed without echo (or from stdin): age.identity,
                       claude.oauth.token, b2.vault.keyid, … (core secrets lists them)
  fridolin core receive
                       on the machine that takes over: wait, on the tailnet, for
                       the secrets of the current core's host (10 minutes, once)
  fridolin core send --to HOST [--code CODE]
                       on the current core's host: send its secrets to the machine
                       running core receive, after both show the same four words
  fridolin core apparmor
                       Linux with AppArmor, as root, once: install the core's
                       profile, which lets the brains' sandbox make its user
                       namespaces (Ubuntu restricts them); start and run use it
  fridolin core run [--] COMMAND...
                       run a fridolin command in a one-off container on the
                       volume, with the secrets: core import while the core
                       is stopped, say (fridolin core run core import --latest)

In the core (docker exec fridolin-core fridolin core ...):
  fridolin core export [--list-folders | --drift] [--use-approved]
                                 encrypt the data dir to the vault bucket, with the
                                 brain folders it takes along (--list-folders: list
                                 their files and export nothing; --drift: say only
                                 which brain.json on disk differs from the manifest
                                 you approved). It refuses while a brain.json on
                                 disk keeps more home than the approved manifest:
                                 approve it first, or pass --use-approved
  fridolin core import --latest [--force]
                                 fetch, decrypt and check the latest export,
                                 then put it in place of an empty data dir
                                 (--force moves what is there to core/replaced-*)
  fridolin core promote          take the core lease for this data dir
  fridolin core status           who holds the core lease, and this device's id

Flags of export, import, promote and status:
  --config PATH     config file (default: $FRIDOLIN_CONFIG, else ~/.fridolin/config.json)
  --data-dir DIR    the data dir (default: $FRIDOLIN_HOME, else the config's)
  --store LOC       a folder, or an rclone remote:bucket (default:
                    $FRIDOLIN_CORE_STORE, else the B2 bucket with the key in
                    B2_VAULT_KEY_ID and B2_VAULT_KEY)
  --bucket NAME     the vault bucket (default fridolin-vault)

Exports are sealed under FRIDOLIN_EXPORT_KEY, then encrypted with age to the
post-quantum identity in FRIDOLIN_AGE_IDENTITY_PQ; import also reads exports
made for the X25519 identity of before (FRIDOLIN_AGE_IDENTITY). core start
sets them.
`

// coreEnv is what the core commands read of the process: its environment,
// so tests pass their own.
type coreEnv struct {
	environ        []string
	stdin          io.Reader
	stdout, stderr io.Writer
	term           terminalIO
}

func (e coreEnv) getenv(name string) string { return envLookup(e.environ)(name) }

func coreCmd(args []string) int {
	return runCore(args, coreEnv{environ: os.Environ(), stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, term: stdinTerminal()})
}

func runCore(args []string, env coreEnv) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.stdout, coreUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "export", "import", "promote", "status":
		return coreInside(ctx, sub, args, env)
	case "start", "stop", "update", "run", "build":
		return coreHost(ctx, sub, args, env, defaultHostDeps(env))
	case "migrate":
		return coreMigrate(ctx, args, env, defaultHostDeps(env))
	case "secrets":
		return coreSecrets(args, env, defaultHostDeps(env))
	case "send", "receive":
		return coreTransfer(ctx, sub, args, env, defaultHostDeps(env))
	case "apparmor":
		return coreAppArmor(ctx, args, env, defaultHostDeps(env))
	default:
		fmt.Fprintf(env.stderr, "fridolin core: unknown command %q\n\n%s", sub, coreUsage)
		return 2
	}
}

// insideFlags are the flags of the commands that work on a data dir.
type insideFlags struct {
	config, dataDir, store, bucket, key string
	latest, force                       bool
	// export's --list-folders: list what of the brain folders goes along;
	// --drift: say only which brain folders differ from their approved
	// manifests; --use-approved: follow the approved manifests although
	// brain.json on disk keeps more home.
	listFolders, drift, useApproved bool
	// import's moves from a native install (core migrate --from-native).
	fromNative, rehearsal bool
	allowPaths            multiFlag
}

func coreInside(ctx context.Context, sub string, args []string, env coreEnv) int {
	fs := flag.NewFlagSet("core "+sub, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	var f insideFlags
	fs.StringVar(&f.config, "config", "", configFlagHelp)
	fs.StringVar(&f.dataDir, "data-dir", "", "the data dir (default: $FRIDOLIN_HOME, else the config's)")
	fs.StringVar(&f.store, "store", "", "a folder, or an rclone remote:bucket (default: the B2 vault bucket)")
	fs.StringVar(&f.bucket, "bucket", "fridolin-vault", "the vault bucket")
	if sub == "export" {
		fs.BoolVar(&f.listFolders, "list-folders", false, "list the files of the brain folders the export would take along (one per line, <brain id>/<path>), and export nothing")
		fs.BoolVar(&f.drift, "drift", false, "say only which brains' brain.json or scheduled agents on disk differ from the manifests you approved, and export nothing; exits 1 when the export would refuse")
		fs.BoolVar(&f.useApproved, "use-approved", false, "take the brain folders along by the approved manifests although a brain.json on disk keeps more of its folder home")
	}
	if sub == "import" {
		fs.BoolVar(&f.latest, "latest", false, "import the export core/exports/latest names")
		fs.StringVar(&f.key, "key", "", "import this export (core/exports/<time>.tar.gz.age) instead")
		fs.BoolVar(&f.force, "force", false, "replace a data dir that has data in it, moving that to core/replaced-<time>/")
		fs.BoolVar(&f.fromNative, "from-native", false, "the export is a native install's (core migrate --from-native): drop its program paths")
		fs.Var(&f.allowPaths, "allow-path", "add this to sandbox.allow_paths (repeatable; core migrate --from-native)")
		fs.BoolVar(&f.rehearsal, "rehearsal", false, "turn off what would reach past a rehearsal (core migrate --rehearse)")
	}
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	case fs.NArg() > 0:
		fmt.Fprintf(env.stderr, "fridolin core %s takes no arguments, got %q\n", sub, fs.Args())
		return 2
	case f.listFolders && f.drift:
		fmt.Fprintln(env.stderr, "fridolin core export: pass --list-folders or --drift, not both")
		return 2
	case sub == "import" && f.latest == (f.key != ""):
		fmt.Fprintln(env.stderr, "fridolin core import: pass --latest, or --key KEY")
		return 2
	}
	var err error
	switch sub {
	case "export":
		err = coreExport(ctx, f, env)
	case "import":
		err = coreImport(ctx, f, env)
	case "promote":
		err = corePromote(ctx, f, env)
	case "status":
		err = coreStatus(ctx, f, env)
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
		if _, ok := errors.AsType[*lease.HeldError](err); ok {
			return 3
		}
		return 1
	}
	return 0
}

// sqliteDB is the store package's snapshot and check, for coreexport.
type sqliteDB struct{}

func (sqliteDB) VacuumInto(ctx context.Context, src, dst string) error {
	return store.VacuumInto(ctx, src, dst)
}

func (sqliteDB) IntegrityCheck(ctx context.Context, path string) error {
	return store.IntegrityCheck(ctx, path)
}

// exportKeys are the keys of the exports' two layers, from the
// environment core start gives the container.
type exportKeys struct {
	// pq is the post-quantum identity (FRIDOLIN_AGE_IDENTITY_PQ), which
	// new exports are encrypted to; nil when it is not set.
	pq *age.HybridIdentity
	// identities decrypt: the post-quantum one and the X25519 one of
	// before (FRIDOLIN_AGE_IDENTITY), for the exports made for it.
	identities []age.Identity
	// seal is the inner layer's key (FRIDOLIN_EXPORT_KEY); nil when unset.
	seal []byte
}

// exportKeysFrom reads FRIDOLIN_AGE_IDENTITY_PQ, FRIDOLIN_AGE_IDENTITY
// and FRIDOLIN_EXPORT_KEY.
func exportKeysFrom(env coreEnv) (exportKeys, error) {
	var k exportKeys
	if s := strings.TrimSpace(env.getenv("FRIDOLIN_AGE_IDENTITY_PQ")); s != "" {
		id, err := age.ParseHybridIdentity(s)
		if err != nil {
			return k, errors.New("FRIDOLIN_AGE_IDENTITY_PQ is not a post-quantum age identity (AGE-SECRET-KEY-PQ-1…)")
		}
		k.pq = id
		k.identities = append(k.identities, id)
	}
	if s := strings.TrimSpace(env.getenv("FRIDOLIN_AGE_IDENTITY")); s != "" {
		id, err := age.ParseX25519Identity(s)
		if err != nil {
			return k, errors.New("FRIDOLIN_AGE_IDENTITY is not an age identity (AGE-SECRET-KEY-1…)")
		}
		k.identities = append(k.identities, id)
	}
	if s := strings.TrimSpace(env.getenv("FRIDOLIN_EXPORT_KEY")); s != "" {
		key, err := seal.ParseKey(s)
		if err != nil {
			return k, fmt.Errorf("FRIDOLIN_EXPORT_KEY: %w", err)
		}
		k.seal = key
	}
	return k, nil
}

// forExport checks that both layers' keys are there.
func (k exportKeys) forExport() error {
	switch {
	case k.pq == nil:
		return errors.New("FRIDOLIN_AGE_IDENTITY_PQ is not set: fridolin core start passes it into the container from the keychain (and makes it when missing)")
	case k.seal == nil:
		return errors.New("FRIDOLIN_EXPORT_KEY is not set: fridolin core start passes it into the container from the keychain (and makes it when missing)")
	}
	return nil
}

// forImport checks that some identity is there.
func (k exportKeys) forImport() error {
	if len(k.identities) == 0 {
		return errors.New("neither FRIDOLIN_AGE_IDENTITY_PQ nor FRIDOLIN_AGE_IDENTITY is set: fridolin core start passes them into the container from the keychain")
	}
	return nil
}

// insideDataDir is the data dir of export, import, promote and status:
// --data-dir, else $FRIDOLIN_HOME (the container's volume), else the one
// the config names.
func insideDataDir(f insideFlags, env coreEnv) (string, error) {
	if f.dataDir != "" {
		return filepath.Abs(f.dataDir)
	}
	if d := env.getenv("FRIDOLIN_HOME"); d != "" {
		return filepath.Abs(d)
	}
	p, err := resolvePaths(f.config)
	if err != nil {
		return "", err
	}
	return p.DataDir, nil
}

func coreExport(ctx context.Context, f insideFlags, env coreEnv) error {
	if f.listFolders {
		return coreListFolders(f, env)
	}
	if f.drift {
		return coreDrift(f, env)
	}
	keys, err := exportKeysFrom(env)
	if err == nil {
		err = keys.forExport()
	}
	if err != nil {
		return err
	}
	st, where, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	cfg, err := app.LoadExistingConfig(configPath(f.config))
	if err != nil {
		return err
	}
	dataDir := cfg.DataDir
	if f.dataDir != "" || env.getenv("FRIDOLIN_HOME") != "" {
		if dataDir, err = insideDataDir(f, env); err != nil {
			return err
		}
	}
	brains, folders, drifts := exportFolders(cfg, configPath(f.config), f.useApproved)
	if err := reportDrift(env.stderr, drifts, f.useApproved); err != nil {
		return err
	}
	host, _ := os.Hostname()
	res, err := coreexport.Export(ctx, dataDir, []age.Recipient{keys.pq.Recipient()}, st, coreexport.ExportOptions{
		DB: sqliteDB{}, SealKey: keys.seal, Version: version.String(), Hostname: host, BrainFolders: brains, Folders: folders,
		Logf: func(format string, a ...any) { fmt.Fprintf(env.stderr, format+"\n", a...) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "Exported %s to %s (%s): %d files, %d databases.\n", dataDir, res.Key, where, res.Manifest.Files, len(res.Manifest.Databases))
	return nil
}

// exportFolders are the config's brain folders, absolute, and those an
// export takes along: as far as each brain's backup settings let it reach
// the cloud, since the export goes through the vault bucket. A folder's
// Configured path is the one config.json at cfgPath has, as typed
// (~/Personal), which the move keeps in moved_from and brings back.
// drifts are the brains whose folder differs from the approved manifest
// (#189); useApproved follows the approved manifests alone.
func exportFolders(cfg *config.Settings, cfgPath string, useApproved bool) (brains []string, folders []coreexport.Folder, drifts []brainDrift) {
	written := writtenBrainPaths(cfgPath)
	for _, b := range cfg.BrainDirs {
		dir := app.AbsPath(b.Dir(app.HomeDir()))
		brains = append(brains, dir)
		if b.Approved == nil || b.Approved.ID == "" {
			continue
		}
		if d := manifest.DriftOf(dir, *b.Approved); d.Any() {
			drifts = append(drifts, brainDrift{ID: b.Approved.ID, Dir: dir, Drift: d})
		}
		skip, excluded := app.BrainFolderFilter(b.Approved.ID, dir, b.Approved, cfg.Backup.IncludeSecretFiles, useApproved)
		configured := b.Path
		for _, w := range written {
			if w != b.Path && fsx.ExpandHome(w, app.HomeDir()) == b.Path {
				configured = w
				break
			}
		}
		folders = append(folders, coreexport.Folder{ID: b.Approved.ID, Configured: configured, Dir: dir, Skip: skip, Excluded: excluded, Visiting: b.MovedFrom != ""})
	}
	return brains, folders, drifts
}

// brainDrift is a brain whose folder on disk differs from the manifest
// the owner approved.
type brainDrift struct {
	ID, Dir string
	manifest.Drift
}

// reportDrift writes a warning per brain whose folder differs from its
// approved manifest, and refuses, unless useApproved, when a brain.json
// on disk would keep more of its folder home than the approved one: the
// owner then probably means that to stay on this machine, and only they
// can say whether it should (#189).
func reportDrift(w io.Writer, drifts []brainDrift, useApproved bool) error {
	var refuse []string
	for _, d := range drifts {
		var then string
		switch {
		case d.Unreadable != "":
			then = "The export leaves the folder home until it can be read again (fridolin check says what is wrong)."
		case d.KeepsMoreHome() && useApproved:
			then = "With --use-approved the export follows the approved manifest and takes that along."
		case d.KeepsMoreHome():
			then = "The export goes by the approved manifest, which takes that along, so it refuses until you approve the change in the dashboard (Brains → " + d.ID + "), which keeps that home, or pass --use-approved, which takes it along."
			refuse = append(refuse, d.ID)
		case d.Backup():
			then = "The export follows the approved manifest; approve the change in the dashboard (Brains → " + d.ID + ") for it to count."
		default:
			then = "What the export takes is the same either way; review it in the dashboard (Brains → " + d.ID + ")."
		}
		fmt.Fprintf(w, "warning: brain %s (%s): %s. %s\n", d.ID, d.Dir, d.Describe(), then)
	}
	if len(refuse) == 0 {
		return nil
	}
	return fmt.Errorf("brain.json on disk of %s keeps more of its folder home than the manifest you approved (see the warning above), so nothing was exported: approve it in the dashboard first to keep that home, or run again with --use-approved to take it along by the approved manifest", strings.Join(refuse, ", "))
}

// coreDrift is core export --drift: the warnings alone.
func coreDrift(f insideFlags, env coreEnv) error {
	cfg, err := app.LoadExistingConfig(configPath(f.config))
	if err != nil {
		return err
	}
	_, _, drifts := exportFolders(cfg, configPath(f.config), f.useApproved)
	if err := reportDrift(env.stderr, drifts, f.useApproved); err != nil {
		return errors.New("the export would refuse: " + err.Error())
	}
	if len(drifts) == 0 {
		fmt.Fprintln(env.stdout, "Every brain folder is as you approved it.")
	}
	return nil
}

// writtenBrainPaths are the paths of brain_dirs as config.json has them,
// before ~ is expanded; none when it cannot be read.
func writtenBrainPaths(cfgPath string) []string {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var raw struct {
		BrainDirs []struct {
			Path string `json:"path"`
		} `json:"brain_dirs"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	out := make([]string, 0, len(raw.BrainDirs))
	for _, b := range raw.BrainDirs {
		out = append(out, b.Path)
	}
	return out
}

// coreListFolders is core export --list-folders.
func coreListFolders(f insideFlags, env coreEnv) error {
	cfg, err := app.LoadExistingConfig(configPath(f.config))
	if err != nil {
		return err
	}
	dataDir := cfg.DataDir
	if f.dataDir != "" || env.getenv("FRIDOLIN_HOME") != "" {
		if dataDir, err = insideDataDir(f, env); err != nil {
			return err
		}
	}
	_, folders, drifts := exportFolders(cfg, configPath(f.config), f.useApproved)
	// The list is what the export takes once it goes ahead: a refusal is
	// a warning here.
	_ = reportDrift(env.stderr, drifts, f.useApproved)
	infos, err := coreexport.ListFolders(app.AbsPath(dataDir), folders, func(p string) { fmt.Fprintln(env.stdout, p) },
		func(format string, a ...any) { fmt.Fprintf(env.stderr, format+"\n", a...) })
	for _, i := range infos {
		fmt.Fprintf(env.stderr, "%s (%s): %d files, %d bytes would go along; %d left home\n", i.ID, i.Dir, i.Files, i.Bytes, i.Left)
	}
	return err
}

func coreImport(ctx context.Context, f insideFlags, env coreEnv) error {
	keys, err := exportKeysFrom(env)
	if err == nil {
		err = keys.forImport()
	}
	if err != nil {
		return err
	}
	st, where, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	dataDir, err := insideDataDir(f, env)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	now := time.Now()
	staging := filepath.Join(dataDir, "core", "import", now.UTC().Format("20060102T150405Z"))
	logf := func(format string, a ...any) { fmt.Fprintf(env.stderr, format+"\n", a...) }
	res, err := coreexport.Import(ctx, st, keys.identities, staging, coreexport.ImportOptions{Key: f.key, SealKey: keys.seal, DB: sqliteDB{}, Logf: importLog(f, logf)})
	if err != nil {
		return err
	}
	lock := func(dir string) (func(), error) {
		l, err := platform.AcquireLock(config.Layout{Root: dir}.Lock())
		if err != nil {
			return nil, err
		}
		return l.Release, nil
	}
	if err := coreexport.Activate(staging, dataDir, f.force, now, lock); err != nil {
		return fmt.Errorf("%w (the checked copy stays in %s)", err, staging)
	}
	placed, err := coreexport.PlaceFolders(dataDir, staging, app.HomeDir(), res.Manifest, now)
	for _, p := range placed {
		if p.Home {
			msg := fmt.Sprintf("Brought brain %s's folder home to %s: %d files added, %d updated, %d the same", p.ID, p.Dir, p.Added, p.Updated, p.Same)
			if p.Kept > 0 {
				msg += fmt.Sprintf(", %d changed at home only and kept", p.Kept)
			}
			if p.Aside != "" {
				msg += "; the files it replaced are in " + p.Aside
			}
			logf("%s", msg)
			for _, c := range p.Conflicts {
				logf("conflict: %s/%s changed at home and on the other machine: the home version stays, the other one is in %s", p.Dir, c, p.Aside)
			}
		} else {
			logf("Put brain %s's folder into %s; the move back brings it home", p.ID, p.Dir)
		}
	}
	if err != nil {
		return fmt.Errorf("%w (the imported data dir is in place; the brain folders are in %s)", err, staging)
	}
	_ = os.RemoveAll(staging)
	warnings, err := coreexport.FixDataDir(dataDir, app.HomeDir())
	if err != nil {
		return err
	}
	if f.fromNative {
		more, err := coreexport.ResetProgramPaths(dataDir)
		if err != nil {
			return err
		}
		warnings = append(warnings, more...)
	}
	if len(f.allowPaths) > 0 {
		more, err := coreexport.AddAllowPaths(dataDir, f.allowPaths)
		if err != nil {
			return err
		}
		warnings = append(warnings, more...)
	}
	if f.rehearsal {
		more, err := coreexport.ForRehearsal(dataDir)
		if err != nil {
			return err
		}
		warnings = append(warnings, more...)
	}
	for _, w := range warnings {
		logf("note: %s", w)
	}
	fmt.Fprintf(env.stdout, "Imported %s from %s into %s. Next: fridolin core promote, then start the core.\n", res.Key, where, dataDir)
	return nil
}

// importLog is import's log: from a native install whose brain folders
// are bound into the container at their own paths, the warnings about
// folders outside the export are left out.
func importLog(f insideFlags, logf func(string, ...any)) func(string, ...any) {
	if !f.fromNative {
		return logf
	}
	return func(format string, a ...any) {
		if !strings.Contains(format, "outside the exported data dir") {
			logf(format, a...)
		}
	}
}

// leaseOptions are the lease's settings for this data dir.
func leaseOptions(st cloud.ObjectStore, dataDir string, f insideFlags, env coreEnv) (lease.Options, error) {
	id, err := lease.DeviceID(dataDir)
	if err != nil {
		return lease.Options{}, err
	}
	host, _ := os.Hostname()
	renew, stale := leaseTimings(f)
	return lease.Options{Store: st, DeviceID: id, Hostname: host, PID: os.Getpid(), Version: version.String(),
		Renew: renew, Stale: stale,
		Logf: func(format string, a ...any) { fmt.Fprintf(env.stderr, format+"\n", a...) }}, nil
}

// leaseTimings are core.lease.renew_seconds and stale_seconds of the
// config, so that promote and status judge a lease as the cores do; the
// defaults when there is no config yet (a fresh volume before import).
func leaseTimings(f insideFlags) (renew, stale time.Duration) {
	path := configPath(f.config)
	if _, err := os.Stat(path); err != nil {
		return lease.DefaultRenew, lease.DefaultStale
	}
	cfg, err := app.LoadExistingConfig(path)
	if err != nil {
		return lease.DefaultRenew, lease.DefaultStale
	}
	return cfg.Core.Lease.Renew(), cfg.Core.Lease.Stale()
}

func corePromote(ctx context.Context, f insideFlags, env coreEnv) error {
	st, where, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	dataDir, err := insideDataDir(f, env)
	if err != nil {
		return err
	}
	opts, err := leaseOptions(st, dataDir, f, env)
	if err != nil {
		return err
	}
	l, err := lease.Acquire(ctx, opts)
	if err != nil {
		return err
	}
	// A copy that was taken over may run as the core again.
	if err := os.Remove(filepath.Join(config.Layout{Root: dataDir}.Core(), app.SupersededFile)); err == nil {
		fmt.Fprintln(env.stderr, "This data dir had been taken over by another device; it may run as the core again.")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	r := l.Record()
	fmt.Fprintf(env.stdout, "This device (%s, %s) holds the core lease in %s. A core on this data dir with core.lease on renews it; unrenewed it goes stale after %s.\n",
		r.DeviceID, r.Hostname, where, opts.Stale)
	return nil
}

func coreStatus(ctx context.Context, f insideFlags, env coreEnv) error {
	dataDir, err := insideDataDir(f, env)
	if err != nil {
		return err
	}
	if id, err := lease.DeviceID(dataDir); err == nil {
		fmt.Fprintf(env.stdout, "this device: %s (data dir %s)\n", id, dataDir)
	} else {
		return err
	}
	if by, err := os.ReadFile(filepath.Join(config.Layout{Root: dataDir}.Core(), app.SupersededFile)); err == nil {
		fmt.Fprintf(env.stdout, "taken over by device %s: this copy does not start as the core until fridolin core promote\n", strings.TrimSpace(string(by)))
	}
	st, where, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	r, ok, err := lease.Read(ctx, st)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(env.stdout, "lease: free (no %s in %s)\n", lease.Key, where)
		return nil
	}
	now := time.Now()
	_, stale := leaseTimings(f)
	state := "live"
	switch {
	case r.Released:
		state = "released"
	case !r.Live(now, stale):
		state = "stale"
	}
	fmt.Fprintf(env.stdout, "lease: %s — device %s (%s, pid %d, version %s), started %s, renewed %s ago\n",
		state, r.DeviceID, r.Hostname, r.PID, r.Version, r.StartedAt.Format(time.RFC3339), r.Age(now).Round(time.Second))
	if latest, err := coreexport.Latest(ctx, st); err == nil {
		fmt.Fprintf(env.stdout, "latest export: %s\n", latest)
	}
	return nil
}

// ── the host side: start, stop, update ────────────────────────────────────

// hostDeps are what the host commands use of the machine, so that tests
// see what would run without Docker or a keychain.
type hostDeps struct {
	secrets hostsecrets.Store
	// secretsPlace names where secrets keeps them, for messages.
	secretsPlace string
	// tailnet is this machine's Tailscale, for core send and receive;
	// nil is its LocalAPI.
	tailnet secretxfer.Tailnet
	// run runs a program with exactly env as its environment.
	run func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error
	// docker is the docker CLI's path.
	docker      string
	composePath string
	// baseEnv is the part of the host's environment docker compose needs:
	// PATH, HOME and Docker's own settings, not the rest of the shell's.
	baseEnv     []string
	version     string
	newIdentity func() (string, error)
	// newKey makes the key of a second layer; nil is seal.NewKey.
	newKey func() (string, error)
	// For core migrate: the host's home folder, user id and system, the
	// native service's unit file, a cache folder for temporary exports,
	// and the clock.
	home, goos, unitPath, cacheDir string
	uid                            int
	now                            func() time.Time
	sleep                          func(time.Duration)
	// apparmor reads the host's AppArmor state (hostAppArmor), and
	// apparmorDir is where core apparmor writes the profile.
	apparmor    func() apparmorState
	apparmorDir string
	// git runs git in dir and returns its output, for core build's
	// box source; nil leaves that out.
	git func(dir string, args ...string) (string, error)
	// zone is the host's IANA time zone (FRIDOLIN_TZ, else
	// platform.HostZone), which the container gets as TZ; "" when this
	// machine does not say, and the container stays in UTC.
	zone string
}

// zoneEnv is the host's time zone for compose, which passes it to the
// container as TZ (compose.yaml): the bridge reads schedules in it when
// config's timezone is unset, and brains see it.
func (d hostDeps) zoneEnv() []string {
	if d.zone == "" {
		return nil
	}
	return []string{"FRIDOLIN_TZ=" + d.zone}
}

// hostZone is FRIDOLIN_TZ when set, else this machine's time zone.
func hostZone(getenv func(string) string) string {
	if tz := strings.TrimSpace(getenv("FRIDOLIN_TZ")); tz != "" {
		return tz
	}
	zone, _ := platform.HostZone()
	return zone
}

func defaultHostDeps(env coreEnv) hostDeps {
	d := hostDeps{run: runProgram, version: version.String(), newIdentity: func() (string, error) {
		id, err := age.GenerateHybridIdentity()
		if err != nil {
			return "", err
		}
		return id.String(), nil
	}}
	d.docker, _ = exec.LookPath("docker")
	d.composePath = composePathFor(runtime.GOOS)
	d.home, d.goos, d.uid, d.now, d.sleep = app.HomeDir(), runtime.GOOS, os.Getuid(), time.Now, time.Sleep
	d.apparmor, d.apparmorDir = hostAppArmor, "/etc/apparmor.d"
	d.git = func(dir string, args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	if c, err := os.UserCacheDir(); err == nil {
		d.cacheDir = filepath.Join(c, "fridolin")
		_ = os.MkdirAll(d.cacheDir, 0o700)
	}
	switch d.goos {
	case "darwin":
		d.unitPath = filepath.Join(d.home, "Library", "LaunchAgents", serviceLabel+".plist")
	case "linux":
		d.unitPath = filepath.Join(d.home, ".config", "systemd", "user", systemdUnitName)
	}
	d.secrets = hostsecrets.ForHost(d.goos, env.getenv, d.home)
	d.secretsPlace = hostsecrets.Place(d.goos, env.getenv, d.home)
	d.baseEnv = composeBaseEnv(d.goos, env.environ)
	d.zone = hostZone(env.getenv)
	return d
}

// composePathFor is where the host commands write the compose file:
// <user config dir>/fridolin/compose.yaml (on macOS ~/Library/Application
// Support/fridolin), and on Windows %LOCALAPPDATA%\fridolin, which stays
// on this machine (the config dir, %APPDATA%, roams with the profile).
func composePathFor(goos string) string {
	dir, err := os.UserConfigDir()
	if goos == "windows" {
		dir, err = os.UserCacheDir() // %LOCALAPPDATA%
	}
	if err != nil {
		dir = filepath.Join(app.HomeDir(), ".config")
	}
	return filepath.Join(dir, "fridolin", "compose.yaml")
}

// windowsComposeEnv are the variables the docker CLI needs on Windows to
// find its config (USERPROFILE), Docker Desktop's pipe and its helpers.
var windowsComposeEnv = []string{
	"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "SYSTEMDRIVE", "COMSPEC", "USERPROFILE", "USERNAME", "USERDOMAIN",
	"HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"PROGRAMW6432", "COMMONPROGRAMFILES", "TEMP", "TMP", "COMPUTERNAME", "OS", "PROCESSOR_ARCHITECTURE",
	"NUMBER_OF_PROCESSORS",
}

// composeBaseEnv is the part of the host's environment docker compose
// gets: PATH, HOME and Docker's own settings, not the rest of the shell's
// (on Windows, where names are case-insensitive, what Windows programs
// need to run at all).
func composeBaseEnv(goos string, environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if goos == "windows" {
			name = strings.ToUpper(name)
			if slices.Contains(windowsComposeEnv, name) || strings.HasPrefix(name, "DOCKER_") || strings.HasPrefix(name, "COMPOSE_") {
				out = append(out, kv)
			}
			continue
		}
		switch {
		case name == "PATH", name == "HOME", name == "USER", name == "TMPDIR", name == "XDG_RUNTIME_DIR",
			strings.HasPrefix(name, "DOCKER_"), strings.HasPrefix(name, "COMPOSE_"):
			out = append(out, kv)
		}
	}
	return out
}

func runProgram(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, stdout, stderr
	return cmd.Run()
}

// containerSecrets map the keychain's secrets onto the container's
// environment (compose.yaml lists the same names). made marks those core
// start makes when the keychain has none: the post-quantum age identity
// and the keys of the second layers (docs/VAULT.md, "Encryption"). The
// X25519 identity of before is passed on while the keychain has it, only
// to read the exports and node-target passwords made for it. optional
// ones are not named as missing: most cores never have them.
var containerSecrets = []struct {
	env, secret string
	made        madeSecret
	optional    bool
}{
	{"FRIDOLIN_AGE_IDENTITY_PQ", hostsecrets.AgeIdentityPQ, madeIdentity, false},
	{"FRIDOLIN_AGE_IDENTITY", hostsecrets.AgeIdentity, notMade, false},
	{"FRIDOLIN_EXPORT_KEY", hostsecrets.ExportKey, madeKey, false},
	{"CLAUDE_CODE_OAUTH_TOKEN", hostsecrets.ClaudeOAuthToken, notMade, false},
	{"TS_AUTHKEY", hostsecrets.TailscaleAuthKey, notMade, false},
	{"B2_VAULT_KEY_ID", hostsecrets.B2VaultKeyID, notMade, false},
	{"B2_VAULT_KEY", hostsecrets.B2VaultKey, notMade, false},
	{"B2_SNAPSHOTS_KEY_ID", hostsecrets.B2SnapshotsKeyID, notMade, false},
	{"B2_SNAPSHOTS_KEY", hostsecrets.B2SnapshotsKey, notMade, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD", hostsecrets.VaultCryptPassword, notMade, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD2", hostsecrets.VaultCryptSalt, notMade, false},
	{"FRIDOLIN_VAULT_SEAL_KEY", hostsecrets.VaultSealKey, madeKey, false},
	{"RESTIC_PASSWORD", hostsecrets.ResticPassword, notMade, false},
	{"FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD", hostsecrets.SnapshotsCryptPassword, madeKey, false},
	{"FRIDOLIN_LITESTREAM_SEAL_KEY", hostsecrets.LitestreamSealKey, madeKey, false},
	// A typed GitHub token, the second choice to a connected GitHub App
	// (docs/GITHUB.md).
	{"FRIDOLIN_GITHUB_TOKEN", hostsecrets.GitHubToken, notMade, true},
}

// madeSecret says whether and how core start makes a missing secret.
type madeSecret int

const (
	notMade      madeSecret = iota
	madeIdentity            // an ML-KEM-768 + X25519 age identity
	madeKey                 // 32 random bytes, base64url (seal.NewKey)
)

// place names where the host secrets are.
func (d hostDeps) place() string {
	if d.secretsPlace == "" {
		return "the host's secrets"
	}
	return d.secretsPlace
}

// imageTag is the image tag of a fridolin version: Docker tags take
// letters, digits, '_', '.' and '-'.
func imageTag(v string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(v, "-")
}

// secretEnv reads the container's secrets, making the post-quantum age
// identity and the second layers' keys when the keychain has none, and
// returns them as NAME=value. It never prints a value; it names what it
// made, for the recovery sheet.
func secretEnv(d hostDeps, stderr io.Writer) ([]string, error) {
	var env, missing, made []string
	for _, s := range containerSecrets {
		v, err := d.secrets.Get(s.secret)
		if errors.Is(err, hostsecrets.ErrNotFound) && s.made != notMade {
			if v, err = d.makeSecret(s.made); err == nil {
				if err = d.secrets.Set(s.secret, v); err == nil {
					made = append(made, s.secret)
				}
			}
		}
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
			if !s.optional {
				missing = append(missing, s.secret)
			}
			continue
		case err != nil:
			return nil, err
		}
		env = append(env, s.env+"="+v)
	}
	if len(made) > 0 {
		fmt.Fprintf(stderr, "Made %s and stored them in %s. Put each on the recovery sheet now (docs/VAULT.md, \"Recovery sheet\") and copy them to every machine that may run the core: without them nobody can read what they encrypt.\n", strings.Join(made, ", "), d.place())
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "Not in %s, so not passed to the core: %s (store one with: fridolin core secrets set NAME)\n", d.place(), strings.Join(missing, ", "))
	}
	return env, nil
}

// makeSecret makes a new secret of kind m.
func (d hostDeps) makeSecret(m madeSecret) (string, error) {
	if m == madeIdentity {
		return d.newIdentity()
	}
	if d.newKey != nil {
		return d.newKey()
	}
	return seal.NewKey()
}

func coreHost(ctx context.Context, sub string, args []string, env coreEnv, d hostDeps) int {
	fs := flag.NewFlagSet("core "+sub, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	compose := fs.String("compose", "", "the compose file (default: the one built into fridolin, written to "+d.composePath+")")
	image := fs.String("image", "fridolin-core", "the image's name")
	tag := fs.String("tag", imageTag(d.version), "the image's tag (default: this fridolin's version)")
	repo := "."
	if sub == "build" {
		fs.StringVar(&repo, "repo", ".", "the fridolin checkout to build the image from")
	}
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	case sub == "run" && fs.NArg() == 0:
		fmt.Fprintln(env.stderr, "fridolin core run: name the fridolin command to run, such as: core import --latest")
		return 2
	case sub != "run" && fs.NArg() > 0:
		fmt.Fprintf(env.stderr, "fridolin core %s takes no arguments, got %q\n", sub, fs.Args())
		return 2
	}
	if d.docker == "" {
		fmt.Fprintln(env.stderr, dockerMissing)
		return 1
	}
	if sub == "build" {
		return coreBuild(ctx, repo, *image+":"+*tag, env, d)
	}
	path := *compose
	if path == "" {
		path = d.composePath
		if err := writeCompose(path); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
			return 1
		}
	}
	childEnv := append(append(append([]string{}, d.baseEnv...), "FRIDOLIN_IMAGE="+*image, "FRIDOLIN_VERSION="+*tag), d.zoneEnv()...)
	if sub != "stop" && d.zone == "" {
		fmt.Fprintln(env.stderr, "Could not tell this machine's time zone, so the core runs in UTC: set FRIDOLIN_TZ (such as Europe/Zurich) for core start, or \"timezone\" in the core's config.")
	}
	if sub != "stop" {
		secrets, err := secretEnv(d, env.stderr)
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
			return 1
		}
		childEnv = append(childEnv, secrets...)
	}
	// The host's folders a moved native install needs (core migrate).
	override := ""
	if *compose == "" {
		if p := filepath.Join(filepath.Dir(path), hostOverrideName); pathKindOf(p) == "file" {
			override = p
		}
	}
	// On a host with AppArmor, the core's own profile (core apparmor).
	armor := ""
	if *compose == "" && d.apparmor != nil {
		var err error
		armor, err = apparmorOverride(d.goos, path, d.apparmor(), func(msg string) { fmt.Fprintf(env.stderr, "warning: %s\n", msg) })
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
			return 1
		}
	}
	compose1 := func(args ...string) error {
		return d.run(ctx, d.docker, append(composeArgs(path, override, armor), args...), childEnv, env.stdout, env.stderr)
	}
	var err error
	switch sub {
	case "start":
		err = compose1("up", "-d")
	case "stop":
		// Never -v: the volume is the core.
		err = compose1("down")
	case "update":
		if err = compose1("pull"); err == nil {
			err = compose1("up", "-d", "--force-recreate")
		}
	case "run":
		// No published ports: the one-off container is no second dashboard.
		err = compose1(append([]string{"run", "--rm", "--no-deps", "core"}, fs.Args()...)...)
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
		return 1
	}
	if sub == "start" || sub == "update" {
		fmt.Fprintf(env.stdout, "The core runs %s:%s. Dashboard: http://127.0.0.1:8765/ (sign in with: docker exec fridolin-core fridolin open --print)\n", *image, *tag)
	}
	return 0
}

// coreBuild builds the core's image from a checkout, tagged as core start
// expects it: the image of this fridolin's version, with no secrets in
// the environment.
func coreBuild(ctx context.Context, repo, ref string, env coreEnv, d hostDeps) int {
	dockerfile := filepath.Join(repo, "container", "Dockerfile")
	if pathKindOf(dockerfile) != "file" {
		fmt.Fprintf(env.stderr, "fridolin core build: %s is not a fridolin checkout (no container/Dockerfile); pass --repo DIR\n", repo)
		return 2
	}
	args := []string{"build", "-f", dockerfile, "--build-arg", "VERSION=" + d.version}
	if src, reg := boxBuildSource(repo, d); src != "" {
		args = append(args, "--build-arg", "FRIDOLIN_BOX_SOURCE="+src, "--build-arg", "FRIDOLIN_BOX_REGISTRY="+reg)
	}
	args = append(args, "-t", ref, repo)
	if err := d.run(ctx, d.docker, args, d.baseEnv, env.stdout, env.stderr); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core build: %v\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Built %s; fridolin core start runs it.\n", ref)
	return 0
}

// githubRemoteRE is a GitHub remote, https or ssh: its owner and name.
var githubRemoteRE = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9-]+)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// boxBuildSource is where the box script of the checkout's commit is
// published, and the registry of its images, for an image core build
// makes: the GitHub repository of the checkout's origin at its HEAD, so
// that the core's !join reply can offer a node in a box. Empty when the
// origin is not on GitHub or the checkout has local changes, whose
// commit no box can fetch.
func boxBuildSource(repo string, d hostDeps) (source, registry string) {
	if d.git == nil {
		return "", ""
	}
	remote, err := d.git(repo, "remote", "get-url", "origin")
	if err != nil {
		return "", ""
	}
	m := githubRemoteRE.FindStringSubmatch(remote)
	if m == nil {
		return "", ""
	}
	if dirty, err := d.git(repo, "status", "--porcelain", "--untracked-files=no"); err != nil || dirty != "" {
		return "", ""
	}
	head, err := d.git(repo, "rev-parse", "HEAD")
	if err != nil || len(head) != 40 {
		return "", ""
	}
	return "https://raw.githubusercontent.com/" + m[1] + "/" + m[2] + "/" + head, "ghcr.io/" + strings.ToLower(m[1])
}

// dockerMissing is what the host commands say without a docker CLI.
const dockerMissing = "fridolin core: docker is not installed or not on PATH (Docker Desktop or OrbStack on macOS, Docker Desktop with WSL2 on Windows, Docker Engine on Linux)"

// writeCompose writes the compose file built into this binary to path, and
// the seccomp profile it names beside it, so the container is always the
// one this version expects.
func writeCompose(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	for dst, data := range map[string][]byte{path: container.Compose, filepath.Join(filepath.Dir(path), "seccomp.json"): container.Seccomp} {
		if err := fsx.WriteFileAtomic(dst, data, 0o600, false); err != nil {
			return err
		}
	}
	return nil
}
