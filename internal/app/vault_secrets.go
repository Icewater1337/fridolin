package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// vaultCreds are the secrets the vault and the snapshots need. They come
// from the environment the core container gets (docs/CORE.md, "Secrets"),
// and on a native install from the host's secret store when the
// environment lacks them.
type vaultCreds struct {
	VaultKeyID, VaultKey     string // B2_VAULT_KEY_ID, B2_VAULT_KEY
	CryptPassword, CryptSalt string // FRIDOLIN_VAULT_CRYPT_PASSWORD, ..._PASSWORD2
	SnapKeyID, SnapKey       string // B2_SNAPSHOTS_KEY_ID, B2_SNAPSHOTS_KEY
	ResticPassword           string // RESTIC_PASSWORD
	// The second layers' keys (docs/VAULT.md, "Encryption").
	VaultSealKey      string // FRIDOLIN_VAULT_SEAL_KEY: the cold tier's inner layer
	SnapCryptPassword string // FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD: the restic repositories' outer layer
	LiteSealKey       string // FRIDOLIN_LITESTREAM_SEAL_KEY: the Litestream replicas' inner layer
}

// secrets lists every value, to blank them out of errors.
func (c vaultCreds) secrets() []string {
	return []string{c.VaultKeyID, c.VaultKey, c.CryptPassword, c.CryptSalt, c.SnapKeyID, c.SnapKey, c.ResticPassword,
		c.VaultSealKey, c.SnapCryptPassword, c.LiteSealKey}
}

// vaultSecretNames maps each environment variable to its host secret.
var vaultSecretNames = []struct {
	env, secret string
	field       func(*vaultCreds) *string
}{
	{"B2_VAULT_KEY_ID", hostsecrets.B2VaultKeyID, func(c *vaultCreds) *string { return &c.VaultKeyID }},
	{"B2_VAULT_KEY", hostsecrets.B2VaultKey, func(c *vaultCreds) *string { return &c.VaultKey }},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD", hostsecrets.VaultCryptPassword, func(c *vaultCreds) *string { return &c.CryptPassword }},
	{"FRIDOLIN_VAULT_CRYPT_PASSWORD2", hostsecrets.VaultCryptSalt, func(c *vaultCreds) *string { return &c.CryptSalt }},
	{"B2_SNAPSHOTS_KEY_ID", hostsecrets.B2SnapshotsKeyID, func(c *vaultCreds) *string { return &c.SnapKeyID }},
	{"B2_SNAPSHOTS_KEY", hostsecrets.B2SnapshotsKey, func(c *vaultCreds) *string { return &c.SnapKey }},
	{"RESTIC_PASSWORD", hostsecrets.ResticPassword, func(c *vaultCreds) *string { return &c.ResticPassword }},
	{"FRIDOLIN_VAULT_SEAL_KEY", hostsecrets.VaultSealKey, func(c *vaultCreds) *string { return &c.VaultSealKey }},
	{"FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD", hostsecrets.SnapshotsCryptPassword, func(c *vaultCreds) *string { return &c.SnapCryptPassword }},
	{"FRIDOLIN_LITESTREAM_SEAL_KEY", hostsecrets.LitestreamSealKey, func(c *vaultCreds) *string { return &c.LiteSealKey }},
}

// vaultSecretSource reads the credentials. store is nil inside the core
// container, whose secrets come only from its environment: a password it
// made there would live in a store the next container does not have.
type vaultSecretSource struct {
	getenv func(string) string
	store  hostsecrets.Store
	// random makes a new password.
	random func() (string, error)
	log    *slog.Logger
}

// newVaultPassword is 32 random bytes, base64url: characters the keychain
// helper passes on unchanged (hostsecrets.Keychain.Set).
func newVaultPassword() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// read takes each credential from the environment, else from the store.
func (s vaultSecretSource) read() (vaultCreds, error) {
	var c vaultCreds
	for _, n := range vaultSecretNames {
		v := strings.TrimSpace(s.getenv(n.env))
		if v == "" && s.store != nil {
			got, err := s.store.Get(n.secret)
			switch {
			case errors.Is(err, hostsecrets.ErrNotFound):
			case err != nil:
				return c, fmt.Errorf("reading %s from the host's secrets: %w", n.secret, err)
			default:
				v = got
			}
		}
		*n.field(&c) = v
	}
	return c, nil
}

// errNoVaultKeys is the vault's reason not to run without the B2 key of
// its bucket.
var errNoVaultKeys = errors.New("the vault needs the B2 key of its bucket: B2_VAULT_KEY_ID and B2_VAULT_KEY, or the keychain items fridolin.b2.vault.keyid and fridolin.b2.vault.key (docs/CORE.md, Secrets)")

// ensureCrypt makes the cold tier's crypt passwords when the store has
// none, but only while the cold tier is empty (coldEmpty): a cold tier
// that has files was encrypted with passwords that are lost here, and new
// ones would only hide that. It never logs a value; it reminds the person
// to put the new ones on the recovery sheet.
func (s vaultSecretSource) ensureCrypt(ctx context.Context, c *vaultCreds, coldEmpty func(context.Context) (bool, error)) error {
	if c.CryptPassword != "" {
		return nil
	}
	if s.store == nil {
		return errors.New("the vault needs its encryption passwords: FRIDOLIN_VAULT_CRYPT_PASSWORD and FRIDOLIN_VAULT_CRYPT_PASSWORD2 (fridolin core start passes them from the host's keychain)")
	}
	empty, err := coldEmpty(ctx)
	if err != nil {
		return fmt.Errorf("looking whether the cold tier is empty before making its passwords: %w", err)
	}
	if !empty {
		return fmt.Errorf("the cold tier in the bucket has files, but the keychain has no %s: restore it and %s from the recovery sheet; new passwords could not read what is there", hostsecrets.VaultCryptPassword, hostsecrets.VaultCryptSalt)
	}
	pw, err := s.random()
	if err != nil {
		return err
	}
	salt, err := s.random()
	if err != nil {
		return err
	}
	if err := s.store.Set(hostsecrets.VaultCryptPassword, pw); err != nil {
		return err
	}
	if err := s.store.Set(hostsecrets.VaultCryptSalt, salt); err != nil {
		return err
	}
	c.CryptPassword, c.CryptSalt = pw, salt
	s.log.Warn("vault: made new encryption passwords for the cold tier and stored them in the host's secrets as " +
		hostsecrets.VaultCryptPassword + " and " + hostsecrets.VaultCryptSalt +
		". Write them on the recovery sheet now (security find-generic-password -a fridolin -s <name> -w): without them nobody can read the cold tier.")
	return nil
}

// ensureRestic makes restic's password when the store has none, but only
// when the repository does not exist yet (exists answers for the new
// password: false means there is no repository at all).
func (s vaultSecretSource) ensureRestic(ctx context.Context, c *vaultCreds, exists func(ctx context.Context, password string) (bool, error)) error {
	if c.ResticPassword != "" {
		return nil
	}
	if s.store == nil {
		return errors.New("the snapshots need RESTIC_PASSWORD (fridolin core start passes it from the host's keychain)")
	}
	pw, err := s.random()
	if err != nil {
		return err
	}
	found, err := exists(ctx, pw)
	if errors.Is(err, snapshot.ErrWrongPassword) {
		return fmt.Errorf("the restic repository exists, but the keychain has no %s: restore it from the recovery sheet", hostsecrets.ResticPassword)
	}
	if err != nil {
		return fmt.Errorf("looking for the restic repository before making its password: %w", err)
	}
	if found {
		return fmt.Errorf("the restic repository exists, but the keychain has no %s: restore it from the recovery sheet", hostsecrets.ResticPassword)
	}
	if err := s.store.Set(hostsecrets.ResticPassword, pw); err != nil {
		return err
	}
	c.ResticPassword = pw
	s.log.Warn("vault: made a new restic password and stored it in the host's secrets as " + hostsecrets.ResticPassword +
		". Write it on the recovery sheet now: without it nobody can restore a snapshot.")
	return nil
}

// vaultRemoteName is the rclone remote of the cold tier; its crypt remote
// is vaultRemoteName+"crypt" (cloud.VaultRemote).
const vaultRemoteName = "fridolinvault"

// vaultColdPath is the cold tier's folder in the vault bucket.
const vaultColdPath = "vault"

// b2RawStore is the vault bucket's cold folder without encryption, which
// only says whether anything is there, on its own rclone daemon (the
// crypt passwords may not exist yet).
func b2RawStore(rclone string, base []string, c vaultCreds, bucket string, usage *cloud.Usage) (*cloud.RcloneRC, error) {
	env, err := cloud.B2Env(vaultRemoteName, c.VaultKeyID, c.VaultKey)
	if err != nil {
		return nil, err
	}
	d := &cloud.RcloneDaemon{Binary: rclone, Env: append(append([]string{}, base...), env...), Secrets: c.secrets(), Usage: usage, Op: "vault"}
	return d.Store(vaultRemoteName + ":" + bucket + "/" + vaultColdPath), nil
}

// b2ColdStore is the cold tier on B2: crypt over <bucket>/vault, on one
// long-lived rclone daemon (cloud.RcloneDaemon), which logs in to B2 once
// a day instead of once per operation. With the seal key it is two
// layers: seal inside, crypt over <bucket>/vault-sealed outside, reading
// what is not copied over yet from the single-layer folder until that is
// retired (docs/VAULT.md, "Encryption"). The vault closes it when it
// stops.
func b2ColdStore(rclone string, base []string, c vaultCreds, bucket string, usage *cloud.Usage, st encryptionState) (cloud.ObjectStore, func() error, error) {
	env, remote, err := cloud.VaultRemote{Name: vaultRemoteName, KeyID: c.VaultKeyID, Key: c.VaultKey, Bucket: bucket, Path: vaultColdPath,
		Password: c.CryptPassword, Password2: c.CryptSalt}.Env()
	if err != nil {
		return nil, nil, err
	}
	var sealedRemote string
	if c.VaultSealKey != "" {
		sealedRemote = vaultRemoteName + "sealedcrypt"
		more, err := cloud.CryptEnv(sealedRemote, vaultRemoteName+":"+bucket+"/"+vaultSealedPath, c.CryptPassword, c.CryptSalt)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, more...)
	}
	d := &cloud.RcloneDaemon{Binary: rclone, Env: append(append([]string{}, base...), env...), Secrets: c.secrets(), Usage: usage, Op: "vault", FastList: true}
	old := d.Store(remote)
	if sealedRemote == "" {
		return old, d.Close, nil
	}
	sealed, err := cloud.NewSealed(d.Store(sealedRemote+":"), c.VaultSealKey)
	if err != nil {
		_ = d.Close()
		return nil, nil, fmt.Errorf("%s: %w", hostsecrets.VaultSealKey, err)
	}
	return layeredCold(sealed, old, st), d.Close, nil
}

// layeredCold is the two-layer cold tier, with the old one behind it
// until it is retired.
func layeredCold(sealed *cloud.Sealed, old cloud.ObjectStore, st encryptionState) cloud.ObjectStore {
	if st.retired("cold") {
		return sealed
	}
	return &cloud.Migrating{New: sealed, Old: old}
}

// storeEmpty reports whether st holds no object at all.
func storeEmpty(ctx context.Context, st cloud.ObjectStore) (bool, error) {
	objs, err := st.List(ctx, "")
	if err != nil {
		return false, err
	}
	return len(objs) == 0, nil
}
