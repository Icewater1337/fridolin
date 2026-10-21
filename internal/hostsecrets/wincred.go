package hostsecrets

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// WinCred is the Windows Credential Manager: one generic credential per
// secret, whose target name is the secret's name (fridolin.age.identity
// and so on), user name Account, kept for this user on this machine
// (CRED_PERSIST_LOCAL_MACHINE: it does not roam with a domain profile).
// Control Panel → Credential Manager → Windows Credentials lists them.
//
// Values go through advapi32's CredWriteW and CredReadW, never a command
// line. `cmdkey /generic:fridolin.age.identity /user:fridolin /pass`,
// which prompts for the value, stores one too: Get reads its UTF-16 as
// well as the UTF-8 that Set writes.
type WinCred struct {
	// API is the Credential Manager; nil is the system's, which exists
	// only on Windows. Tests replace it.
	API CredAPI
}

// CredAPI is the part of the Credential Manager WinCred uses: generic
// credentials by target name.
type CredAPI interface {
	// Read returns the credential's blob, or an error wrapping
	// ErrNotFound when there is none.
	Read(target string) ([]byte, error)
	Write(target, user string, blob []byte) error
	// Delete removes the credential; one that is not there wraps
	// ErrNotFound.
	Delete(target string) error
}

// maxCredBlob is CRED_MAX_CREDENTIAL_BLOB_SIZE: 5 × 512 bytes.
const maxCredBlob = 5 * 512

func (w WinCred) api() CredAPI {
	if w.API != nil {
		return w.API
	}
	return systemCredAPI{}
}

func (w WinCred) Get(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	blob, err := w.api().Read(name)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s from the Credential Manager: %w", name, err)
	}
	return strings.TrimRight(decodeCredBlob(blob), "\r\n"), nil
}

func (w WinCred) Has(name string) (bool, error) {
	_, err := w.Get(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (w WinCred) Set(name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	switch {
	case value == "":
		return fmt.Errorf("the value for %s is empty", name)
	case len(value) > maxCredBlob:
		return fmt.Errorf("the value for %s is %d bytes; the Credential Manager keeps at most %d", name, len(value), maxCredBlob)
	}
	if err := w.api().Write(name, Account, []byte(value)); err != nil {
		return fmt.Errorf("writing %s to the Credential Manager: %w", name, err)
	}
	return nil
}

// Delete removes the secret; one that is not there is no error.
func (w WinCred) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := w.api().Delete(name); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("removing %s from the Credential Manager: %w", name, err)
	}
	return nil
}

// decodeCredBlob reads a blob as Set writes it (UTF-8), or as cmdkey and
// the Control Panel write it (UTF-16LE). The secrets are ASCII, which in
// UTF-8 has no zero byte, while in UTF-16LE every second byte is zero.
func decodeCredBlob(b []byte) string {
	if len(b) >= 2 && len(b)%2 == 0 {
		utf16le := true
		for i := 1; i < len(b); i += 2 {
			if b[i] != 0 || b[i-1] == 0 {
				utf16le = false
				break
			}
		}
		if utf16le {
			u := make([]uint16, len(b)/2)
			for i := range u {
				u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
			}
			return string(utf16.Decode(u))
		}
	}
	return string(b)
}

// ForHost is the host's secret store on goos: the login keychain on
// macOS, the Credential Manager on Windows, and 0600 files under
// $XDG_CONFIG_HOME/fridolin/secrets (default ~/.config/...) elsewhere.
func ForHost(goos string, getenv func(string) string, home string) Store {
	switch goos {
	case "darwin":
		return Keychain{}
	case "windows":
		return WinCred{}
	}
	return Files{Dir: FilesDir(getenv, home)}
}

// FilesDir is where Files keeps the secrets off macOS and Windows.
func FilesDir(getenv func(string) string, home string) string {
	xdg := getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "fridolin", "secrets")
}

// Place names where ForHost(goos, …) keeps the secrets, for messages.
func Place(goos string, getenv func(string) string, home string) string {
	switch goos {
	case "darwin":
		return "the login keychain"
	case "windows":
		return "the Windows Credential Manager"
	}
	return FilesDir(getenv, home)
}
