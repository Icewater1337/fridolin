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

// vaultBaseEnv is what of the bridge's environment rclone, restic and
// Litestream get: where programs and the home folder are, never the
// bridge's secrets.
func vaultBaseEnv() []string {
	var env []string
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "PATH=") {
		env = append(env, "PATH="+helperPath())
	}
	return env
}

// hostSecretStore is the host's secret store of a native install: the
// macOS keychain, or 0600 files on Linux. Inside the core container there
// is none: its secrets come only from its environment.
func hostSecretStore(container bool) hostsecrets.Store {
	if container {
		return nil
	}
	if runtime.GOOS == "darwin" {
		return hostsecrets.Keychain{}
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(HomeDir(), ".config")
	}
	return hostsecrets.Files{Dir: filepath.Join(xdg, "fridolin", "secrets")}
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
	if dir := os.Getenv(vaultLocalEnv); dir != "" {
		return localVaultBackends(s, cfg, dir)
	}
	var b vaultBackends
	rclone, err := findRclone(cfg.Backup.RclonePath)
	if err != nil {
		return b, fmt.Errorf("the vault reaches B2 through rclone: %w", err)
	}
	base := vaultBaseEnv()
	src := vaultSecretSource{getenv: os.Getenv, store: hostSecretStore(s.container), random: newVaultPassword, log: s.log}
	c, err := src.read()
	if err != nil {
		return b, err
	}
	if c.VaultKeyID == "" || c.VaultKey == "" {
		return b, errNoVaultKeys
	}
	raw, err := b2RawStore(rclone, base, c, cfg.Vault.Bucket)
	if err != nil {
		return b, err
	}
	if err := src.ensureCrypt(ctx, &c, func(ctx context.Context) (bool, error) { return storeEmpty(ctx, raw) }); err != nil {
		return b, err
	}
	if b.cold, err = b2ColdStore(rclone, base, c, cfg.Vault.Bucket); err != nil {
		return b, err
	}
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
	}
	if lite := findVaultTool("litestream"); lite != "" && c.SnapKeyID != "" && c.SnapKey != "" {
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
	return b, nil
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
	cold, err := cloud.NewDir(filepath.Join(dir, "cold"))
	if err != nil {
		return b, err
	}
	b.cold = cold
	b.bridgeDB = AbsPath(cfg.DBPath)
	pwFile := filepath.Join(dir, "restic-password")
	pw := os.Getenv("RESTIC_PASSWORD")
	if pw == "" {
		data, err := os.ReadFile(pwFile)
		switch {
		case err == nil:
			pw = strings.TrimSpace(string(data))
		case errors.Is(err, os.ErrNotExist):
			if pw, err = newVaultPassword(); err != nil {
				return b, err
			}
			if err := os.WriteFile(pwFile, []byte(pw+"\n"), 0o600); err != nil {
				return b, err
			}
		default:
			return b, err
		}
	}
	base := vaultBaseEnv()
	if restic := findVaultTool("restic"); restic != "" {
		env := append(append([]string{}, base...), "RESTIC_PASSWORD="+pw)
		b.restic = &snapshot.Restic{Binary: restic, Repo: filepath.Join(dir, "restic"), Env: env, Secrets: []string{pw}}
		b.drive = func() (*snapshot.Restic, error) {
			return &snapshot.Restic{Binary: restic, Repo: filepath.Join(dir, "drive-restic"), Env: env, Secrets: []string{pw}}, nil
		}
	} else {
		b.snapErr = "restic is not installed"
	}
	if lite := findVaultTool("litestream"); lite != "" {
		replica := func(name string) snapshot.Replica {
			return snapshot.Replica{Type: "file", Dir: filepath.Join(dir, "litestream", name)}
		}
		b.lite = &snapshot.Litestream{Binary: lite, TempDir: cfg.Boot.Layout().Core(), Logger: quietLogger(s.log), Env: base,
			DBs: []snapshot.LitestreamDB{
				{Path: b.bridgeDB, Replica: replica("bridge.db")},
				{Path: cfg.Boot.Layout().VaultDB(), Replica: replica("vault.db")},
			}}
	}
	return b, nil
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
