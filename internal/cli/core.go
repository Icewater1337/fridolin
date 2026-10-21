package cli

import (
	"context"
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
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/version"
)

const coreUsage = `fridolin core — run, move and inspect the containerized core (docs/CORE.md)

On the host that runs Docker:
  fridolin core start [--compose PATH] [--image NAME] [--tag TAG]
                       start the core container with the secrets from the
                       keychain; makes the age identity when there is none
  fridolin core stop   stop it (the volume stays)
  fridolin core update pull the image of this version and recreate the container
  fridolin core migrate --from-native [--dry-run] [--local] [--mount PATH]...
                       move this machine's native install into the core's
                       volume, its brain folders bound in at their own paths
                       (--rehearse: on a copy, beside the running native bridge)
  fridolin core migrate --to-native [--dry-run] [--local]
                       bring the volume back into the native data dir
  fridolin core run [--] COMMAND...
                       run a fridolin command in a one-off container on the
                       volume, with the secrets: core import while the core
                       is stopped, say (fridolin core run core import --latest)

In the core (docker exec fridolin-core fridolin core ...):
  fridolin core export           encrypt the data dir to the vault bucket
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

The age identity comes from FRIDOLIN_AGE_IDENTITY, which core start sets.
`

// coreEnv is what the core commands read of the process: its environment,
// so tests pass their own.
type coreEnv struct {
	environ        []string
	stdout, stderr io.Writer
}

func (e coreEnv) getenv(name string) string { return envLookup(e.environ)(name) }

func coreCmd(args []string) int {
	return runCore(args, coreEnv{environ: os.Environ(), stdout: os.Stdout, stderr: os.Stderr})
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
	case "start", "stop", "update", "run":
		return coreHost(ctx, sub, args, env, defaultHostDeps(env))
	case "migrate":
		return coreMigrate(ctx, args, env, defaultHostDeps(env))
	default:
		fmt.Fprintf(env.stderr, "fridolin core: unknown command %q\n\n%s", sub, coreUsage)
		return 2
	}
}

// insideFlags are the flags of the commands that work on a data dir.
type insideFlags struct {
	config, dataDir, store, bucket, key string
	latest, force                       bool
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

// identityFrom parses FRIDOLIN_AGE_IDENTITY.
func identityFrom(env coreEnv) (*age.X25519Identity, error) {
	s := strings.TrimSpace(env.getenv("FRIDOLIN_AGE_IDENTITY"))
	if s == "" {
		return nil, errors.New("FRIDOLIN_AGE_IDENTITY is not set: fridolin core start passes it into the container from the keychain")
	}
	id, err := age.ParseX25519Identity(s)
	if err != nil {
		return nil, errors.New("FRIDOLIN_AGE_IDENTITY is not an age identity (AGE-SECRET-KEY-1…)")
	}
	return id, nil
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
	id, err := identityFrom(env)
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
	var brains []string
	for _, b := range cfg.BrainDirs {
		brains = append(brains, app.AbsPath(b.Dir(app.HomeDir())))
	}
	host, _ := os.Hostname()
	res, err := coreexport.Export(ctx, dataDir, []age.Recipient{id.Recipient()}, st, coreexport.ExportOptions{
		DB: sqliteDB{}, Version: version.String(), Hostname: host, BrainFolders: brains,
		Logf: func(format string, a ...any) { fmt.Fprintf(env.stderr, format+"\n", a...) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "Exported %s to %s (%s): %d files, %d databases.\n", dataDir, res.Key, where, res.Manifest.Files, len(res.Manifest.Databases))
	return nil
}

func coreImport(ctx context.Context, f insideFlags, env coreEnv) error {
	id, err := identityFrom(env)
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
	res, err := coreexport.Import(ctx, st, []age.Identity{id}, staging, coreexport.ImportOptions{Key: f.key, DB: sqliteDB{}, Logf: importLog(f, logf)})
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
	// For core migrate: the host's home folder, user id and system, the
	// native service's unit file, a cache folder for temporary exports,
	// and the clock.
	home, goos, unitPath, cacheDir string
	uid                            int
	now                            func() time.Time
	sleep                          func(time.Duration)
}

func defaultHostDeps(env coreEnv) hostDeps {
	d := hostDeps{run: runProgram, version: version.String(), newIdentity: func() (string, error) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			return "", err
		}
		return id.String(), nil
	}}
	d.docker, _ = exec.LookPath("docker")
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		cfgDir = filepath.Join(app.HomeDir(), ".config")
	}
	d.composePath = filepath.Join(cfgDir, "fridolin", "compose.yaml")
	d.home, d.goos, d.uid, d.now, d.sleep = app.HomeDir(), runtime.GOOS, os.Getuid(), time.Now, time.Sleep
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
	if runtime.GOOS == "darwin" {
		d.secrets = hostsecrets.Keychain{}
	} else {
		xdg := env.getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(app.HomeDir(), ".config")
		}
		d.secrets = hostsecrets.Files{Dir: filepath.Join(xdg, "fridolin", "secrets")}
	}
	for _, kv := range env.environ {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "PATH", name == "HOME", name == "USER", name == "TMPDIR", name == "XDG_RUNTIME_DIR",
			strings.HasPrefix(name, "DOCKER_"), strings.HasPrefix(name, "COMPOSE_"):
			d.baseEnv = append(d.baseEnv, kv)
		}
	}
	return d
}

func runProgram(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, stdout, stderr
	return cmd.Run()
}

// containerSecrets map the keychain's secrets onto the container's
// environment (compose.yaml lists the same names).
var containerSecrets = []struct {
	env, secret string
	required    bool
}{
	{"FRIDOLIN_AGE_IDENTITY", hostsecrets.AgeIdentity, true},
	{"CLAUDE_CODE_OAUTH_TOKEN", hostsecrets.ClaudeOAuthToken, false},
	{"TS_AUTHKEY", hostsecrets.TailscaleAuthKey, false},
	{"B2_VAULT_KEY_ID", hostsecrets.B2VaultKeyID, false},
	{"B2_VAULT_KEY", hostsecrets.B2VaultKey, false},
	{"B2_SNAPSHOTS_KEY_ID", hostsecrets.B2SnapshotsKeyID, false},
	{"B2_SNAPSHOTS_KEY", hostsecrets.B2SnapshotsKey, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD", hostsecrets.VaultCryptPassword, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD2", hostsecrets.VaultCryptSalt, false},
	{"RESTIC_PASSWORD", hostsecrets.ResticPassword, false},
}

// imageTag is the image tag of a fridolin version: Docker tags take
// letters, digits, '_', '.' and '-'.
func imageTag(v string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(v, "-")
}

// secretEnv reads the container's secrets, making the age identity when
// the keychain has none, and returns them as NAME=value.
func secretEnv(d hostDeps, stderr io.Writer) ([]string, error) {
	var env, missing []string
	for _, s := range containerSecrets {
		v, err := d.secrets.Get(s.secret)
		if errors.Is(err, hostsecrets.ErrNotFound) && s.secret == hostsecrets.AgeIdentity {
			if v, err = d.newIdentity(); err == nil {
				if err = d.secrets.Set(s.secret, v); err == nil {
					fmt.Fprintf(stderr, "Made a new age identity and stored it as %s. Exports are encrypted to it: copy it to every machine that may run the core.\n", s.secret)
				}
			}
		}
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
			missing = append(missing, s.secret)
			continue
		case err != nil:
			return nil, err
		}
		env = append(env, s.env+"="+v)
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "Not in the keychain, so not passed to the core: %s\n", strings.Join(missing, ", "))
	}
	return env, nil
}

func coreHost(ctx context.Context, sub string, args []string, env coreEnv, d hostDeps) int {
	fs := flag.NewFlagSet("core "+sub, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	compose := fs.String("compose", "", "the compose file (default: the one built into fridolin, written to "+d.composePath+")")
	image := fs.String("image", "fridolin-core", "the image's name")
	tag := fs.String("tag", imageTag(d.version), "the image's tag (default: this fridolin's version)")
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
		fmt.Fprintln(env.stderr, "fridolin core: docker is not installed or not on PATH (Docker Desktop or OrbStack on macOS)")
		return 1
	}
	path := *compose
	if path == "" {
		path = d.composePath
		if err := writeCompose(path); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core %s: %v\n", sub, err)
			return 1
		}
	}
	childEnv := append(append([]string{}, d.baseEnv...), "FRIDOLIN_IMAGE="+*image, "FRIDOLIN_VERSION="+*tag)
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
	compose1 := func(args ...string) error {
		return d.run(ctx, d.docker, append(composeArgs(path, override), args...), childEnv, env.stdout, env.stderr)
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
