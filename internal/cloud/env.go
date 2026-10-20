package cloud

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// remoteName is what a remote may be called here: lower-case letters and
// digits, so that its RCLONE_CONFIG_<NAME>_* variables are unambiguous.
var remoteName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// optionName is an rclone option name as RemoteEnv takes it.
var optionName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// RemoteEnv returns the environment variables that define the rclone
// remote name with the given options (rclone's option names, such as
// "type", "account", "remote"): RCLONE_CONFIG_<NAME>_<OPTION>=value. rclone
// reads a remote from these when no config file defines it, so no file
// holding secrets is ever written.
func RemoteEnv(name string, options map[string]string) ([]string, error) {
	if !remoteName.MatchString(name) {
		return nil, fmt.Errorf("cloud: remote name %q: use lower-case letters and digits", name)
	}
	if options["type"] == "" {
		return nil, fmt.Errorf("cloud: remote %q has no type", name)
	}
	keys := make([]string, 0, len(options))
	for k := range options {
		if !optionName.MatchString(k) {
			return nil, fmt.Errorf("cloud: remote %q: bad option name %q", name, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, envName(name, k)+"="+options[k])
	}
	return env, nil
}

func envName(remote, option string) string {
	return "RCLONE_CONFIG_" + strings.ToUpper(remote) + "_" + strings.ToUpper(option)
}

// B2Env defines a Backblaze B2 remote from an application key ID and key.
// hard_delete stays off, so a delete only hides the object and the
// bucket's lifecycle rules keep its earlier versions.
func B2Env(name, keyID, key string) ([]string, error) {
	if keyID == "" || key == "" {
		return nil, fmt.Errorf("cloud: B2 remote %q needs a key ID and a key", name)
	}
	return RemoteEnv(name, map[string]string{
		"type":        "b2",
		"account":     keyID,
		"key":         key,
		"hard_delete": "false",
	})
}

// CryptEnv defines a crypt remote over target (for example
// "vault:fridolin-vault/vault") that encrypts contents, file names and
// directory names. password and password2 (the salt, may be empty) are
// given in the clear and obscured here, as rclone expects.
func CryptEnv(name, target, password, password2 string) ([]string, error) {
	if target == "" || !strings.Contains(target, ":") {
		return nil, fmt.Errorf("cloud: crypt remote %q needs a target remote like name:path", name)
	}
	if password == "" {
		return nil, fmt.Errorf("cloud: crypt remote %q needs a password", name)
	}
	opts := map[string]string{
		"type":                      "crypt",
		"remote":                    target,
		"filename_encryption":       "standard",
		"directory_name_encryption": "true",
	}
	var err error
	if opts["password"], err = Obscure(password); err != nil {
		return nil, err
	}
	if password2 != "" {
		if opts["password2"], err = Obscure(password2); err != nil {
			return nil, err
		}
	}
	return RemoteEnv(name, opts)
}

// VaultRemote is the vault's cold tier: a B2 bucket with a crypt remote
// over Path inside it. The crypt remote is called Name+"crypt", as the
// contract between the vault and its configuration fixes.
type VaultRemote struct {
	Name      string // the B2 remote, for example "vault"
	KeyID     string // B2 application key ID
	Key       string // B2 application key
	Bucket    string // for example "fridolin-vault"
	Path      string // folder in the bucket, for example "vault"
	Password  string // crypt password, in the clear
	Password2 string // crypt salt, in the clear; may be empty
}

// Env returns the variables that define both remotes, and the remote
// (with trailing colon) to hand to Rclone.Remote.
func (v VaultRemote) Env() (env []string, remote string, err error) {
	if v.Bucket == "" {
		return nil, "", fmt.Errorf("cloud: vault remote %q has no bucket", v.Name)
	}
	b2, err := B2Env(v.Name, v.KeyID, v.Key)
	if err != nil {
		return nil, "", err
	}
	target := v.Name + ":" + v.Bucket
	if p := strings.Trim(v.Path, "/"); p != "" {
		target += "/" + p
	}
	crypt, err := CryptEnv(v.Name+"crypt", target, v.Password, v.Password2)
	if err != nil {
		return nil, "", err
	}
	return append(b2, crypt...), v.Name + "crypt:", nil
}
