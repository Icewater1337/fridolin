package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// Each off-machine store is encrypted twice, with independent keys
// (docs/VAULT.md, "Encryption"). The double-encrypted copies live beside
// the single-encrypted ones of before, under new names, until fridolin
// vault migrate-encryption has copied everything over and, with
// --delete-old, removed the old ones. encryptionState remembers which old
// stores are retired, so that the vault stops reading them.

// encryptionStateName is the state's file in core/.
const encryptionStateName = "encryption.json"

// Where the double-encrypted stores live, beside the old ones.
const (
	// vaultSealedPath is the cold tier's new folder in the vault bucket:
	// rclone crypt outside, seal inside.
	vaultSealedPath = "vault-sealed"
	// resticCryptPath is the restic repository's new folder in the
	// snapshots bucket and on Drive, behind an rclone crypt remote.
	resticCryptPath = "restic-crypt"
	// liteSealedPath is the Litestream replicas' new folder in the
	// snapshots bucket: seal inside, rclone crypt outside.
	liteSealedPath = "litestream-sealed"
)

// encryptionState is core/encryption.json.
type encryptionState struct {
	// The old stores fridolin vault migrate-encryption --delete-old
	// removed, by store id ("cold", "restic", "drive", "litestream").
	Retired map[string]time.Time `json:"retired,omitempty"`
	// Migrated is when each store's copy was last verified.
	Migrated map[string]time.Time `json:"migrated,omitempty"`
}

func encryptionStatePath(coreDir string) string {
	return filepath.Join(coreDir, encryptionStateName)
}

// readEncryptionState reads the state; a missing file is the empty state.
func readEncryptionState(coreDir string) (encryptionState, error) {
	var st encryptionState
	data, err := os.ReadFile(encryptionStatePath(coreDir))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", encryptionStatePath(coreDir), err)
	}
	return st, nil
}

func (st encryptionState) retired(store string) bool {
	_, ok := st.Retired[store]
	return ok
}

func writeEncryptionState(coreDir string, st encryptionState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(coreDir, 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(encryptionStatePath(coreDir), append(data, '\n'), 0o600, false)
}

// layerKeys are the second layers' keys, each with its host secret.
var layerKeys = []struct {
	secret string
	field  func(*vaultCreds) *string
}{
	{hostsecrets.VaultSealKey, func(c *vaultCreds) *string { return &c.VaultSealKey }},
	{hostsecrets.SnapshotsCryptPassword, func(c *vaultCreds) *string { return &c.SnapCryptPassword }},
	{hostsecrets.LitestreamSealKey, func(c *vaultCreds) *string { return &c.LiteSealKey }},
}

// ensureLayers makes the second layers' keys a native install lacks and
// stores them in the host's secrets. Each guards a store under a new name
// that the key is made for, so a new key never hides data under a lost
// one: that data stays where it was, single-encrypted, and the doctor
// says so. Inside the container (no store) a missing key leaves its store
// with one layer; core start makes them on the host.
func (s vaultSecretSource) ensureLayers(c *vaultCreds) error {
	if s.store == nil {
		return nil
	}
	for _, k := range layerKeys {
		if *k.field(c) != "" {
			continue
		}
		v, err := s.random()
		if err != nil {
			return err
		}
		if err := s.store.Set(k.secret, v); err != nil {
			return err
		}
		*k.field(c) = v
		s.log.Warn("vault: made a new key for a second layer of encryption and stored it in the host's secrets as " + k.secret +
			". Write it on the recovery sheet now (docs/VAULT.md, \"Recovery sheet\"): without it nobody can read what it encrypts.")
	}
	return nil
}

// liteMirrorEvery is how often the mirror uploads what Litestream wrote:
// with Litestream's own sync each minute, the most of the databases a
// disaster loses.
var liteMirrorEvery = time.Minute

// runLiteMirror uploads the local replicas until ctx ends, and once more
// as it ends so that a clean stop leaves the cloud current.
func (s *vaultService) runLiteMirror(ctx context.Context, m *cloud.Mirror) {
	t := time.NewTicker(liteMirrorEvery)
	defer t.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			_, _ = m.Sync(final, time.Now())
			cancel()
			return
		case <-t.C:
		}
		_, err := m.Sync(ctx, time.Now())
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			s.log.Warn("vault: uploading Litestream's replicas failed; retrying each minute", "err", err)
			failing = true
		case err == nil && failing:
			s.log.Info("vault: Litestream's replicas upload again")
			failing = false
		}
	}
}
