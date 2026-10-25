// Package hostsecrets keeps the secrets the host hands the core container
// at start: the age identity, the Claude token, the Tailscale auth key and
// the Backblaze keys (docs/CORE.md). On macOS they are generic passwords in
// the login keychain, account "fridolin", one service per secret; on Linux
// files of mode 0600 under $XDG_CONFIG_HOME/fridolin/secrets/.
//
// Values never go on a command line: the keychain is read with `security
// find-generic-password -w`, whose output is the value, and written through
// `security -i` with the command on its standard input, where no process
// list shows it.
package hostsecrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Names of the secrets, which are the keychain's service names.
const (
	AgeIdentity        = "fridolin.age.identity"
	ClaudeOAuthToken   = "fridolin.claude.oauth.token"
	TailscaleAuthKey   = "fridolin.tailscale.authkey"
	B2VaultKeyID       = "fridolin.b2.vault.keyid"
	B2VaultKey         = "fridolin.b2.vault.key"
	B2SnapshotsKeyID   = "fridolin.b2.snapshots.keyid"
	B2SnapshotsKey     = "fridolin.b2.snapshots.key"
	VaultCryptPassword = "fridolin.vault.crypt.password"
	VaultCryptSalt     = "fridolin.vault.crypt.password2"
	ResticPassword     = "fridolin.restic.password"

	// The second layers and the post-quantum identity (docs/VAULT.md,
	// "Encryption"): each a key of its own.
	AgeIdentityPQ          = "fridolin.age.identity.pq"
	ExportKey              = "fridolin.export.key"
	VaultSealKey           = "fridolin.vault.seal.key"
	SnapshotsCryptPassword = "fridolin.snapshots.crypt.password"
	LitestreamSealKey      = "fridolin.litestream.seal.key"

	// The GitHub App's credentials on a native install (docs/GITHUB.md),
	// which the dashboard stores when the owner connects GitHub, and the
	// typed token that is the second choice to an app.
	GitHubApp   = "fridolin.github.app"
	GitHubToken = "fridolin.github.token"
)

// EnvSecret is one variable of the core's environment and the host
// secret it comes from.
type EnvSecret struct {
	Env, Name string
	// Optional ones are not named as missing: most cores never have them.
	Optional bool
}

// CoreEnv maps the host secrets onto the core container's environment
// (container/compose.yaml lists the same variables): what `fridolin core
// start` passes from the keychain, and what a box's agent passes from its
// sealed store to the core it runs (docs/NODES.md, "The core in a box").
// The X25519 identity of before only reads the exports and node-target
// passwords made for it.
var CoreEnv = []EnvSecret{
	{"FRIDOLIN_AGE_IDENTITY_PQ", AgeIdentityPQ, false},
	{"FRIDOLIN_AGE_IDENTITY", AgeIdentity, false},
	{"FRIDOLIN_EXPORT_KEY", ExportKey, false},
	{"CLAUDE_CODE_OAUTH_TOKEN", ClaudeOAuthToken, false},
	{"TS_AUTHKEY", TailscaleAuthKey, false},
	{"B2_VAULT_KEY_ID", B2VaultKeyID, false},
	{"B2_VAULT_KEY", B2VaultKey, false},
	{"B2_SNAPSHOTS_KEY_ID", B2SnapshotsKeyID, false},
	{"B2_SNAPSHOTS_KEY", B2SnapshotsKey, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD", VaultCryptPassword, false},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD2", VaultCryptSalt, false},
	{"FRIDOLIN_VAULT_SEAL_KEY", VaultSealKey, false},
	{"RESTIC_PASSWORD", ResticPassword, false},
	{"FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD", SnapshotsCryptPassword, false},
	{"FRIDOLIN_LITESTREAM_SEAL_KEY", LitestreamSealKey, false},
	// A typed GitHub token, the second choice to a connected GitHub App
	// (docs/GITHUB.md).
	{"FRIDOLIN_GITHUB_TOKEN", GitHubToken, true},
}

// Account is the keychain account every secret is stored under.
const Account = "fridolin"

// ErrNotFound is Get's error for a secret the store does not have.
var ErrNotFound = errors.New("secret not found")

// Store holds the host's secrets by name.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Has(name string) (bool, error)
}

// Deleter is a Store that can forget a secret. Deleting one the store does
// not have is no error.
type Deleter interface {
	Delete(name string) error
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q is not a secret name", name)
	}
	return nil
}

// Keychain is the macOS login keychain, through /usr/bin/security.
type Keychain struct {
	// Security is the security tool's path; default /usr/bin/security.
	Security string
	// File is the keychain file to use; default the user's search list
	// (the login keychain).
	File string
	// Run runs it with stdin and returns its stdout; default exec. Tests
	// replace it.
	Run func(ctx context.Context, path string, args []string, stdin io.Reader) ([]byte, error)
}

func (k Keychain) run(args []string, stdin io.Reader) ([]byte, error) {
	path := k.Security
	if path == "" {
		path = "/usr/bin/security"
	}
	run := k.Run
	if run == nil {
		run = execRun
	}
	return run(context.Background(), path, args, stdin)
}

func execRun(ctx context.Context, path string, args []string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, &ExitError{Code: ee.ExitCode(), Stderr: strings.TrimSpace(stderr.String())}
		}
		return out, err
	}
	return out, nil
}

// ExitError is a security run that failed.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string { return fmt.Sprintf("security exited %d: %s", e.Code, e.Stderr) }

// errSecItemNotFound is security's exit status for a missing item.
const errSecItemNotFound = 44

func (k Keychain) Get(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	args := []string{"find-generic-password", "-a", Account, "-s", name, "-w"}
	if k.File != "" {
		args = append(args, k.File)
	}
	out, err := k.run(args, nil)
	if ee, ok := errors.AsType[*ExitError](err); ok && ee.Code == errSecItemNotFound {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s from the keychain: %w", name, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (k Keychain) Has(name string) (bool, error) {
	_, err := k.Get(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// keychainSafe are the characters a value may have to go through `security
// -i`'s quoting unchanged: tokens, keys and age identities are all of them.
var keychainSafe = regexp.MustCompile(`^[A-Za-z0-9+/=._:@-]+$`)

func (k Keychain) Set(name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !keychainSafe.MatchString(value) {
		return fmt.Errorf("the value for %s has characters the keychain helper does not pass on; store it with Keychain Access", name)
	}
	// -U updates an existing item. The command goes on stdin, not argv.
	cmd := fmt.Sprintf("add-generic-password -U -a %s -s %s -l %s -w \"%s\"", Account, name, name, value)
	if k.File != "" {
		if !keychainSafe.MatchString(k.File) {
			return fmt.Errorf("keychain path %q has characters the keychain helper does not pass on", k.File)
		}
		cmd += " " + k.File
	}
	cmd += "\n"
	if _, err := k.run([]string{"-i"}, strings.NewReader(cmd)); err != nil {
		return fmt.Errorf("writing %s to the keychain: %w", name, err)
	}
	return nil
}

func (k Keychain) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	args := []string{"delete-generic-password", "-a", Account, "-s", name}
	if k.File != "" {
		args = append(args, k.File)
	}
	_, err := k.run(args, nil)
	if ee, ok := errors.AsType[*ExitError](err); ok && ee.Code == errSecItemNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting %s from the keychain: %w", name, err)
	}
	return nil
}

// Files keeps each secret in a file of mode 0600 named after it, in Dir
// (mode 0700): Linux's store, $XDG_CONFIG_HOME/fridolin/secrets.
type Files struct{ Dir string }

func (f Files) path(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	return filepath.Join(f.Dir, name), nil
}

func (f Files) Get(name string) (string, error) {
	p, err := f.path(name)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %v: other users may read it; chmod 600 it first", p, st.Mode().Perm())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func (f Files) Has(name string) (bool, error) {
	_, err := f.Get(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (f Files) Set(name, value string) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(f.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (f Files) Delete(name string) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
