package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// vaultLocalEnv names a folder that stands in for Backblaze in dev mode
// and tests: the cold tier is a folder in it, the restic repositories
// (B2's and Drive's) are folders in it and Litestream replicates into it.
// Never for a core that holds real data.
const vaultLocalEnv = "FRIDOLIN_VAULT_LOCAL"

// driveResticPath is the Drive repository's folder in the Drive remote.
const driveResticPath = "fridolin-restic"

// vaultBaseEnv is what of the bridge's environment env rclone, restic and
// Litestream get: where programs and the home folder are, never the
// bridge's secrets.
func vaultBaseEnv(env Environment) []string {
	var out []string
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if v, ok := envOr(env).LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}
	if !strings.Contains(strings.Join(out, "\n"), "PATH=") {
		out = append(out, "PATH="+helperPath())
	}
	return out
}

// hostSecretStore is the host's secret store of a native install: the
// macOS keychain, the Windows Credential Manager, or 0600 files on Linux.
// Inside the core container there is none: its secrets come only from
// its environment.
func hostSecretStore(container bool) hostsecrets.Store {
	if container {
		return nil
	}
	return hostsecrets.ForHost(runtime.GOOS, os.Getenv, HomeDir())
}

// findVaultTool finds restic or Litestream where the bridge finds its
// helpers, then where Homebrew and the container put them.
func findVaultTool(name string) string {
	if p := lookPathIn(name, helperPath()); p != "" {
		return p
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		if p := lookPathIn(name, dir); p != "" {
			return p
		}
	}
	return ""
}

// b2Region is the region of an endpoint like s3.eu-central-003.backblazeb2.com.
func b2Region(endpoint string) string {
	s := strings.TrimPrefix(endpoint, "s3.")
	r, _, _ := strings.Cut(s, ".")
	return r
}

// defaultVaultBackends builds the backends from the settings and the
// credentials (vaultSecretSource): the cold tier on B2 through rclone
// crypt, restic on the snapshots bucket, Litestream to it, and the Drive
// repository through the Drive connection of the backup.
func defaultVaultBackends(ctx context.Context, s *vaultService, cfg *config.Settings) (vaultBackends, error) {
	env := envOr(s.env)
	if dir := getenv(env, vaultLocalEnv); dir != "" {
		return localVaultBackends(s, cfg, dir)
	}
	var b vaultBackends
	rclone, err := findRclone(cfg.Backup.RclonePath)
	if err != nil {
		return b, fmt.Errorf("the vault reaches B2 through rclone: %w", err)
	}
	base := vaultBaseEnv(env)
	src := vaultSecretSource{getenv: func(name string) string { return getenv(env, name) }, store: hostSecretStore(s.container), random: newVaultPassword, log: s.log}
	c, err := src.read()
	if err != nil {
		return b, err
	}
	if c.VaultKeyID == "" || c.VaultKey == "" {
		return b, errNoVaultKeys
	}
	raw, err := b2RawStore(rclone, base, c, cfg.Vault.Bucket, s.usage)
	if err != nil {
		return b, err
	}
	err = src.ensureCrypt(ctx, &c, func(ctx context.Context) (bool, error) { return storeEmpty(ctx, raw) })
	_ = raw.Close()
	if err != nil {
		return b, err
	}
	if err := src.ensureLayers(&c); err != nil {
		return b, err
	}
	encState, err := readEncryptionState(cfg.Boot.Layout().Core())
	if err != nil {
		return b, err
	}
	cold, closeCold, err := b2ColdStore(rclone, base, c, cfg.Vault.Bucket, s.usage, encState)
	if err != nil {
		return b, err
	}
	b.cold, b.closers, b.b2 = cold, append(b.closers, closeCold), true
	b.bridgeDB = AbsPath(cfg.DBPath)

	restic := findVaultTool("restic")
	switch {
	case c.SnapKeyID == "" || c.SnapKey == "":
		b.snapErr = "no B2 key for the snapshots bucket: B2_SNAPSHOTS_KEY_ID and B2_SNAPSHOTS_KEY, or the keychain items fridolin.b2.snapshots.keyid and .key"
	case restic == "":
		b.snapErr = "restic is not installed"
	default:
		repo := &snapshot.Restic{Binary: restic, Repo: "b2:" + cfg.Vault.SnapshotsBucket + ":restic",
			CacheDir: filepath.Join(cfg.Boot.Layout().Core(), "restic-cache"), Secrets: c.secrets()}
		repoEnv := append(append([]string{}, base...), "B2_ACCOUNT_ID="+c.SnapKeyID, "B2_ACCOUNT_KEY="+c.SnapKey)
		err := src.ensureRestic(ctx, &c, func(ctx context.Context, pw string) (bool, error) {
			probe := *repo
			probe.Env = append(append([]string{}, repoEnv...), "RESTIC_PASSWORD="+pw)
			probe.Secrets = append(append([]string{}, repo.Secrets...), pw)
			return probe.Exists(ctx)
		})
		if err != nil {
			b.snapErr = err.Error()
			break
		}
		repo.Env = append(repoEnv, "RESTIC_PASSWORD="+c.ResticPassword)
		repo.Secrets = c.secrets()
		b.restic = repo
		b.drive = driveRepo(cfg, rclone, restic, base, c.ResticPassword, c.secrets())
		if c.SnapCryptPassword == "" {
			break
		}
		// Two layers: the repository behind rclone crypt, the one of
		// before kept to copy from until it is retired.
		snapEnv, err := cloud.B2Env(snapRemoteName, c.SnapKeyID, c.SnapKey)
		if err != nil {
			b.snapErr = err.Error()
			break
		}
		crypted, err := resticCrypt{restic: restic, rclone: rclone, base: base, backend: snapEnv,
			target: snapRemoteName + ":" + cfg.Vault.SnapshotsBucket + "/" + resticCryptPath, name: snapRemoteName + "crypt",
			password: c.ResticPassword, cryptPassword: c.SnapCryptPassword, cacheDir: repo.CacheDir, secrets: c.secrets()}.repo()
		if err != nil {
			b.snapErr = err.Error()
			break
		}
		if !encState.retired("restic") {
			b.resticOld = repo
		}
		b.restic = crypted
		oldDrive := b.drive
		b.drive = driveCryptRepo(cfg, rclone, restic, base, c)
		if !encState.retired("drive") {
			b.driveOld = oldDrive
		}
	}
	if lite := findVaultTool("litestream"); lite != "" && c.SnapKeyID != "" && c.SnapKey != "" && c.LiteSealKey != "" && c.SnapCryptPassword != "" {
		// Two layers: Litestream replicates into a local folder, which the
		// mirror uploads sealed (its own key) through rclone crypt (the
		// snapshots' crypt password).
		mirror, closeMirror, err := liteMirrorStore(rclone, base, c, cfg.Vault.SnapshotsBucket, s.usage)
		if err != nil {
			return b, err
		}
		b.closers = append(b.closers, closeMirror)
		b.lite, b.liteMirror = mirroredLitestream(lite, cfg, s.log, base, b.bridgeDB, mirror)
	} else if lite != "" && c.SnapKeyID != "" && c.SnapKey != "" {
		replica := func(name string) snapshot.Replica {
			return snapshot.Replica{Type: "s3", Bucket: cfg.Vault.SnapshotsBucket, Path: "litestream/" + name,
				Endpoint: "https://" + cfg.Vault.Endpoint, Region: b2Region(cfg.Vault.Endpoint), ForcePathStyle: true}
		}
		b.lite = &snapshot.Litestream{Binary: lite, TempDir: cfg.Boot.Layout().Core(), Logger: quietLogger(s.log),
			Env: append(append([]string{}, base...), "LITESTREAM_ACCESS_KEY_ID="+c.SnapKeyID, "LITESTREAM_SECRET_ACCESS_KEY="+c.SnapKey),
			DBs: []snapshot.LitestreamDB{
				{Path: b.bridgeDB, Replica: replica("bridge.db")},
				{Path: cfg.Boot.Layout().VaultDB(), Replica: replica("vault.db")},
			}}
	}
	b.oldPurge = b2OldPurge(rclone, base, c, cfg)
	b.encryption = describeLayers(b, c, encState, src.exportKeysHere())
	return b, nil
}

// liteReplicaDir is the local folder Litestream replicates into when the
// mirror carries the replicas to B2.
func liteReplicaDir(cfg *config.Settings) string {
	return filepath.Join(cfg.Boot.Layout().Core(), "litestream-replica")
}

// mirroredLitestream replicates bridge.db and vault.db into
// core/litestream-replica, which the mirror uploads to store.
func mirroredLitestream(bin string, cfg *config.Settings, log *slog.Logger, base []string, bridgeDB string, store cloud.ObjectStore) (*snapshot.Litestream, *cloud.Mirror) {
	dir := liteReplicaDir(cfg)
	replica := func(name string) snapshot.Replica {
		return snapshot.Replica{Type: "file", Dir: filepath.Join(dir, name)}
	}
	lite := &snapshot.Litestream{Binary: bin, TempDir: cfg.Boot.Layout().Core(), Logger: quietLogger(log), Env: base,
		DBs: []snapshot.LitestreamDB{
			{Path: bridgeDB, Replica: replica("bridge.db")},
			{Path: cfg.Boot.Layout().VaultDB(), Replica: replica("vault.db")},
		}}
	return lite, &cloud.Mirror{Dir: dir, Store: store, DeleteEvery: liteDeleteEvery}
}

// liteDeleteEvery spaces the mirror's batches of deletes, each a listing
// (one Class C on B2): Litestream drops level-0 files hourly.
const liteDeleteEvery = 6 * time.Hour

// liteMirrorStore is <snapshots bucket>/litestream-sealed through rclone
// crypt, sealed inside, on a daemon of its own.
func liteMirrorStore(rclone string, base []string, c vaultCreds, bucket string, usage *cloud.Usage) (*cloud.Sealed, func() error, error) {
	env, err := cloud.B2Env(snapRemoteName, c.SnapKeyID, c.SnapKey)
	if err != nil {
		return nil, nil, err
	}
	crypt, err := cloud.CryptEnv("fridolinlitecrypt", snapRemoteName+":"+bucket+"/"+liteSealedPath, c.SnapCryptPassword, "")
	if err != nil {
		return nil, nil, err
	}
	d := &cloud.RcloneDaemon{Binary: rclone, Env: append(append(append([]string{}, base...), env...), crypt...), Secrets: c.secrets(),
		Usage: usage, Op: "litestream", FastList: true}
	sealed, err := cloud.NewSealed(d.Store("fridolinlitecrypt:"), c.LiteSealKey)
	if err != nil {
		_ = d.Close()
		return nil, nil, fmt.Errorf("%s: %w", hostsecrets.LitestreamSealKey, err)
	}
	return sealed, d.Close, nil
}

// b2OldPurge empty the single-layer folders of before on B2 and Drive
// with rclone purge (on B2 a delete hides the files; the bucket's
// lifecycle rules drop them); rcloneRetire says what counts as done.
func b2OldPurge(rclone string, base []string, c vaultCreds, cfg *config.Settings) map[string]oldPurger {
	snap := func(path string) oldPurger {
		return func(ctx context.Context) (string, error) {
			env, err := cloud.B2Env(snapRemoteName, c.SnapKeyID, c.SnapKey)
			if err != nil {
				return "", err
			}
			run := execRclone(rclone, append(append(append([]string{}, base...), env...), "RCLONE_CONFIG="+os.DevNull), c.secrets())
			return rcloneRetire(ctx, run, snapRemoteName+":"+cfg.Vault.SnapshotsBucket+"/"+path, "")
		}
	}
	return map[string]oldPurger{
		"restic":     snap("restic"),
		"litestream": snap("litestream"),
		"drive": func(ctx context.Context) (string, error) {
			conf := cfg.Boot.Layout().RcloneConf()
			remote := cfg.Backup.Remote
			if remote == "" {
				remote = "brain-gdrive"
			}
			if !rcloneHasRemote(conf, remote) {
				return "", errDriveNotConnected
			}
			run := execRclone(rclone, append(append([]string{}, base...), "RCLONE_CONFIG="+conf), c.secrets())
			return rcloneRetire(ctx, run, remote+":"+driveResticPath, driveResticPath)
		},
	}
}

// errDriveNotConnected: the weekly copy has no Drive to go to.
var errDriveNotConnected = errors.New("the weekly copy needs Google Drive, which is not connected (Backup → Connect Google Drive)")

// driveRepo is the restic repository on Google Drive, through the rclone
// remote the Drive backup connected (backup.remote in the bridge's own
// rclone.conf); it fails while Drive is not connected.
func driveRepo(cfg *config.Settings, rclone, restic string, base []string, password string, secrets []string) func() (*snapshot.Restic, error) {
	return func() (*snapshot.Restic, error) {
		conf := cfg.Boot.Layout().RcloneConf()
		remote := cfg.Backup.Remote
		if remote == "" {
			remote = "brain-gdrive"
		}
		if !rcloneHasRemote(conf, remote) {
			return nil, errDriveNotConnected
		}
		env := append(append([]string{}, base...), "RCLONE_CONFIG="+conf, "RESTIC_PASSWORD="+password)
		return &snapshot.Restic{Binary: restic, Repo: "rclone:" + remote + ":" + driveResticPath, Env: env,
			Options: []string{"rclone.program=" + rclone}, CacheDir: filepath.Join(cfg.Boot.Layout().Core(), "restic-cache"), Secrets: secrets}, nil
	}
}

// snapRemoteName is the rclone remote of the snapshots bucket, under
// the crypt remote of the restic repository.
const snapRemoteName = "fridolinsnap"

// driveCryptRepo is the Drive repository behind rclone crypt, in
// fridolin-restic-crypt beside the single-layer fridolin-restic.
func driveCryptRepo(cfg *config.Settings, rclone, restic string, base []string, c vaultCreds) func() (*snapshot.Restic, error) {
	return func() (*snapshot.Restic, error) {
		conf := cfg.Boot.Layout().RcloneConf()
		remote := cfg.Backup.Remote
		if remote == "" {
			remote = "brain-gdrive"
		}
		if !rcloneHasRemote(conf, remote) {
			return nil, errDriveNotConnected
		}
		return resticCrypt{restic: restic, rclone: rclone, base: base, backend: []string{"RCLONE_CONFIG=" + conf},
			target: remote + ":" + driveResticPath + "-crypt", name: "fridolindrivecrypt",
			password: c.ResticPassword, cryptPassword: c.SnapCryptPassword,
			cacheDir: filepath.Join(cfg.Boot.Layout().Core(), "restic-cache"), secrets: c.secrets()}.repo()
	}
}

// rcloneHasRemote reports whether the rclone config at conf defines remote.
func rcloneHasRemote(conf, remote string) bool {
	f, err := os.Open(conf)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "["+remote+"]" {
			return true
		}
	}
	return false
}

// localVaultBackends stand in for Backblaze with folders in dir
// (FRIDOLIN_VAULT_LOCAL): dev mode and tests.
func localVaultBackends(s *vaultService, cfg *config.Settings, dir string) (vaultBackends, error) {
	var b vaultBackends
	dir = AbsPath(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return b, err
	}
	encState, err := readEncryptionState(cfg.Boot.Layout().Core())
	if err != nil {
		return b, err
	}
	oldCold, err := cloud.NewDir(filepath.Join(dir, "cold"))
	if err != nil {
		return b, err
	}
	sealKey, err := localSecret(s.env, dir, "vault-seal-key", "FRIDOLIN_VAULT_SEAL_KEY")
	if err != nil {
		return b, err
	}
	newCold, err := cloud.NewDir(filepath.Join(dir, "cold-sealed"))
	if err != nil {
		return b, err
	}
	sealed, err := cloud.NewSealed(newCold, sealKey)
	if err != nil {
		return b, err
	}
	b.cold = layeredCold(sealed, oldCold, encState)
	b.bridgeDB = AbsPath(cfg.DBPath)
	pw, err := localSecret(s.env, dir, "restic-password", "RESTIC_PASSWORD")
	if err != nil {
		return b, err
	}
	base := vaultBaseEnv(s.env)
	if restic := findVaultTool("restic"); restic != "" {
		env := append(append([]string{}, base...), "RESTIC_PASSWORD="+pw)
		b.restic = &snapshot.Restic{Binary: restic, Repo: filepath.Join(dir, "restic"), Env: env, Secrets: []string{pw}}
		b.drive = func() (*snapshot.Restic, error) {
			return &snapshot.Restic{Binary: restic, Repo: filepath.Join(dir, "drive-restic"), Env: env, Secrets: []string{pw}}, nil
		}
		// With rclone, the repositories get their outer layer as on B2:
		// rclone crypt over a folder.
		if rclone, err := findRclone(cfg.Backup.RclonePath); err == nil {
			cryptPW, err := localSecret(s.env, dir, "snapshots-crypt-password", "FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD")
			if err != nil {
				return b, err
			}
			crypted := func(folder, name string) (*snapshot.Restic, error) {
				return resticCrypt{restic: restic, rclone: rclone, base: base, target: localCryptTarget(filepath.Join(dir, folder)),
					name: name, password: pw, cryptPassword: cryptPW}.repo()
			}
			repo, err := crypted("restic-crypt", "fridolinsnapcrypt")
			if err != nil {
				return b, err
			}
			if !encState.retired("restic") {
				b.resticOld = b.restic
			}
			if !encState.retired("drive") {
				b.driveOld = b.drive
			}
			b.restic = repo
			b.drive = func() (*snapshot.Restic, error) { return crypted("drive-restic-crypt", "fridolindrivecrypt") }
		}
	} else {
		b.snapErr = "restic is not installed"
	}
	if lite := findVaultTool("litestream"); lite != "" {
		// As on B2: replicas in core/, mirrored sealed into the stand-in.
		key, err := localSecret(s.env, dir, "litestream-seal-key", "FRIDOLIN_LITESTREAM_SEAL_KEY")
		if err != nil {
			return b, err
		}
		folder, err := cloud.NewDir(filepath.Join(dir, "litestream-sealed"))
		if err != nil {
			return b, err
		}
		sealed, err := cloud.NewSealed(folder, key)
		if err != nil {
			return b, err
		}
		b.lite, b.liteMirror = mirroredLitestream(lite, cfg, s.log, base, b.bridgeDB, sealed)
	}
	b.oldPurge = map[string]oldPurger{}
	for id, folder := range map[string]string{"restic": "restic", "drive": "drive-restic", "litestream": "litestream"} {
		// The dev stand-in's own folder under FRIDOLIN_VAULT_LOCAL.
		b.oldPurge[id] = func(context.Context) (string, error) { return "", os.RemoveAll(filepath.Join(dir, folder)) }
	}
	lc := vaultCreds{VaultSealKey: sealKey}
	if b.resticOld != nil {
		lc.SnapCryptPassword = "set"
	}
	b.encryption = describeLayers(b, lc, encState, true)
	return b, nil
}

// localSecret is the dev stand-ins' secret: from the variable env of the
// bridge's environment from, else from the file name in dir, made there
// when missing.
func localSecret(from Environment, dir, name, env string) (string, error) {
	if v := getenv(envOr(from), env); v != "" {
		return v, nil
	}
	file := filepath.Join(dir, name)
	data, err := os.ReadFile(file)
	switch {
	case err == nil:
		return strings.TrimSpace(string(data)), nil
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	v, err := newVaultPassword()
	if err != nil {
		return "", err
	}
	return v, os.WriteFile(file, []byte(v+"\n"), 0o600)
}

// quietLogger drops what l would log below info: Litestream's own chatter
// (its compaction monitors and the like) stays out of the bridge's log,
// its warnings and errors go in.
func quietLogger(l *slog.Logger) *slog.Logger {
	return slog.New(minLevel{Handler: l.Handler(), min: slog.LevelInfo})
}

type minLevel struct {
	slog.Handler
	min slog.Level
}

func (h minLevel) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.Handler.Enabled(ctx, level)
}
