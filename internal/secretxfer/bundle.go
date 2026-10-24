package secretxfer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// MoveNames are the host secrets a move carries to the next machine: all
// the core's but the Tailscale auth key, since the next machine logs in
// to the tailnet itself and the core's own node key moves in the export.
// `fridolin core send` sends them (with --with-tailscale-key that key
// too); over the node channel (#187) they are all that goes, and all a
// target takes.
var MoveNames = []string{
	hostsecrets.AgeIdentityPQ, hostsecrets.AgeIdentity, hostsecrets.ExportKey, hostsecrets.ClaudeOAuthToken,
	hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey, hostsecrets.B2SnapshotsKeyID, hostsecrets.B2SnapshotsKey,
	hostsecrets.VaultCryptPassword, hostsecrets.VaultCryptSalt, hostsecrets.VaultSealKey,
	hostsecrets.ResticPassword, hostsecrets.SnapshotsCryptPassword, hostsecrets.LitestreamSealKey,
	hostsecrets.GitHubToken,
}

// SealBundle encrypts secrets, from the host named from, to recipient: a
// post-quantum age recipient (ML-KEM-768 + X25519), the bundle core send
// sends. Nothing of it stays in clear.
func SealBundle(from string, secrets map[string]string, recipient string) ([]byte, error) {
	if len(secrets) == 0 {
		return nil, errors.New("there are no secrets to send")
	}
	for name, v := range secrets {
		if !secretName.MatchString(name) || !validValue(v) {
			return nil, fmt.Errorf("%s cannot be sent: its value is empty, too long or not printable", name)
		}
	}
	r, err := age.ParseHybridRecipient(recipient)
	if err != nil {
		return nil, errors.New("the receiver's key is not a post-quantum age key")
	}
	plain, err := encodeBundle(from, secrets)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return ct.Bytes(), nil
}

// OpenBundle decrypts a bundle with id, in memory, and reads it.
func OpenBundle(ct []byte, id age.Identity) (map[string]string, string, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), id)
	if err != nil {
		return nil, "", fmt.Errorf("decrypting the bundle: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, maxMessage))
	if err != nil {
		return nil, "", fmt.Errorf("decrypting the bundle: %w", err)
	}
	defer clear(plain)
	return DecodeBundle(plain)
}

// StoreBundle writes received secrets into st as core receive does: a
// name outside allowed refuses the whole bundle, a value that differs
// from one already stored refuses it unless replace, and when a write
// fails the earlier ones are put back. Stored were written, Unchanged
// were there with the same value.
func StoreBundle(st Store, secrets map[string]string, allowed []string, replace bool) (stored, unchanged []string, err error) {
	for name := range secrets {
		if !slices.Contains(allowed, name) {
			return nil, nil, fmt.Errorf("the bundle holds %s, which this machine does not take", name)
		}
	}
	return storeAll(st, secrets, replace)
}
