package cli

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// vaultRemote is the rclone remote the core defines for the vault bucket,
// through the environment of rclone alone (cloud.B2Env).
const vaultRemote = "fridolinvault"

// coreStoreFrom picks the vault bucket of the core commands and of a bridge
// with core.lease on: --store (a folder, or an rclone "remote:bucket" whose
// remote rclone's own environment defines), else $FRIDOLIN_CORE_STORE the
// same way, else the B2 vault bucket with the key in B2_VAULT_KEY_ID and
// B2_VAULT_KEY, which `fridolin core start` hands the container. A folder
// is a cloud.Dir, anything else a cloud.Rclone that sees only PATH, HOME
// and the RCLONE_ variables: never the rest of this process's environment.
func coreStoreFrom(flagValue, bucket string, environ []string) (cloud.ObjectStore, string, error) {
	getenv := envLookup(environ)
	loc := flagValue
	if loc == "" {
		loc = getenv("FRIDOLIN_CORE_STORE")
	}
	rclone := func() (string, error) {
		path, err := exec.LookPath("rclone")
		if err != nil {
			return "", errors.New("rclone is not installed: the core reaches the vault bucket through it")
		}
		return path, nil
	}
	baseEnv := []string{"PATH=" + getenv("PATH"), "HOME=" + getenv("HOME")}
	switch {
	case loc != "" && (strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, ".")):
		return &cloud.Dir{Root: loc}, "folder " + loc, nil
	case loc != "":
		path, err := rclone()
		if err != nil {
			return nil, "", err
		}
		env := baseEnv
		for _, kv := range environ {
			if strings.HasPrefix(kv, "RCLONE_") {
				env = append(env, kv)
			}
		}
		return &cloud.Rclone{Binary: path, Remote: strings.TrimSuffix(loc, "/"), Env: env}, loc, nil
	}
	id, key := getenv("B2_VAULT_KEY_ID"), getenv("B2_VAULT_KEY")
	if id == "" || key == "" {
		return nil, "", errors.New("no vault bucket: B2_VAULT_KEY_ID and B2_VAULT_KEY are not set (fridolin core start passes them into the container from the keychain); or pass --store")
	}
	path, err := rclone()
	if err != nil {
		return nil, "", err
	}
	remote, err := cloud.B2Env(vaultRemote, id, key)
	if err != nil {
		return nil, "", err
	}
	return &cloud.Rclone{Binary: path, Remote: vaultRemote + ":" + bucket, Env: append(baseEnv, remote...)}, "b2 bucket " + bucket, nil
}

// envLookup returns a getenv over environ, a list of NAME=value.
func envLookup(environ []string) func(string) string {
	return func(name string) string {
		for i := len(environ) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(environ[i], name+"="); ok {
				return v
			}
		}
		return ""
	}
}
