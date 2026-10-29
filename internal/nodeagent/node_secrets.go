package nodeagent

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// Where FrIDE instances' agents get their Claude login from
// (docs/NODES.md, "The Claude login"): the host's store where the
// agent runs as the owner (the keychain on macOS, the files or Secret
// Service on Linux), else the node's own. A Windows node runs as
// LocalSystem, whose Credential Manager is not the owner's: it keeps the
// token in sealed files in its state dir, under a key derived from its
// device key, as a box keeps host secrets. fridolin node secrets set
// fills either.

// nodeSecretsDir is the node's own store, in its state dir.
const nodeSecretsDir = "node-secrets"

// NodeSecrets are the secrets fridolin node secrets keeps: the Claude
// token FrIDE's agents log in with.
var NodeSecrets = []string{hostsecrets.ClaudeOAuthToken}

// FrideSecretStore is where the agent on goos reads FrIDE's Claude token,
// and how that place is called: the host's store, or on Windows the
// node's own in dir, which needs the node's key.
func FrideSecretStore(goos string, getenv func(string) string, home, dir string) (hostsecrets.Store, string, error) {
	if goos != "windows" {
		return hostsecrets.ForHost(goos, getenv, home), hostsecrets.Place(goos, getenv, home), nil
	}
	key, err := LoadKey(dir)
	if err != nil {
		return nil, "", fmt.Errorf("reading the node's key: %w", err)
	}
	s, err := nodeSecretStore(dir, key)
	if err != nil {
		return nil, "", err
	}
	return s, "the node's state dir (" + filepath.Join(dir, nodeSecretsDir) + ", sealed)", nil
}

// nodeSecretStore is the node's own store in dir, sealed under a key
// derived from key.
func nodeSecretStore(dir string, key ed25519.PrivateKey) (hostsecrets.Store, error) {
	k, err := hkdf.Key(sha256.New, key.Seed(), nil, "fridolin node secrets v1", 32)
	if err != nil {
		return nil, err
	}
	return hostsecrets.Sealed{Dir: filepath.Join(dir, nodeSecretsDir), Key: k}, nil
}

// claudeTokenFix says how the owner stores FrIDE's Claude token on a node
// of goos with state dir dir.
func claudeTokenFix(goos, dir string) string {
	if goos == "windows" {
		return fmt.Sprintf(`in an administrator PowerShell there: fridolin node secrets set claude.oauth.token --state-dir "%s", with the token claude setup-token prints`, dir)
	}
	return "there: fridolin node secrets set claude.oauth.token, with the token claude setup-token prints"
}
