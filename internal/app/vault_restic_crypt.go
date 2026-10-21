package app

import (
	"path/filepath"

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// resticCrypt describes a restic repository behind an rclone crypt
// remote: restic encrypts inside (AES-256 with Poly1305, its password),
// rclone crypt outside (XSalsa20-Poly1305, names too, the snapshots'
// crypt password), two independent keys (docs/VAULT.md, "Encryption").
// restic reaches it through its rclone backend, which runs rclone serve
// restic over the crypt remote; the remotes are defined in rclone's
// environment only, so no file holds the passwords.
type resticCrypt struct {
	restic, rclone string
	// base is the environment of every vault helper; backend defines the
	// remote the crypt remote sits on (B2's keys), and target is that
	// remote with the repository's folder ("fridolinsnap:bucket/restic-crypt").
	base, backend []string
	target        string
	// name is the crypt remote's name.
	name                    string
	password, cryptPassword string
	cacheDir                string
	secrets                 []string
}

// repo returns the repository.
func (c resticCrypt) repo() (*snapshot.Restic, error) {
	crypt, err := cloud.CryptEnv(c.name, c.target, c.cryptPassword, "")
	if err != nil {
		return nil, err
	}
	env := append(append(append([]string{}, c.base...), c.backend...), crypt...)
	env = append(env, "RESTIC_PASSWORD="+c.password)
	return &snapshot.Restic{Binary: c.restic, Repo: "rclone:" + c.name + ":", Env: env,
		Options: []string{"rclone.program=" + c.rclone}, CacheDir: c.cacheDir,
		Secrets: append(append([]string{}, c.secrets...), c.password, c.cryptPassword)}, nil
}

// localCryptTarget is rclone's name for a local folder, for the dev
// stand-ins: the crypt remote sits on it as it does on B2.
func localCryptTarget(dir string) string {
	return ":local:" + filepath.ToSlash(dir)
}
