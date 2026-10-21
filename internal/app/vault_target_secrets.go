package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// The password of a node target's restic repository (docs/VAULT.md, "Node
// targets", and docs/DECISIONS.md): its own secret per target, made on the
// core when the repository is made, kept where the core keeps its
// secrets, and never sent to the node. restic encrypts on the core; the
// node stores ciphertext only.
//
// It is read, in order, from the environment (FRIDOLIN_RESTIC_NODE_<NAME>,
// the name upper-cased with - as _), from the host's secret store
// (fridolin.restic.node.<name>: the keychain, the Windows Credential
// Manager, 0600 files on Linux), and inside the core container, which has
// no such store, from secrets/restic-node-<name>.age in the data dir,
// encrypted to the core's post-quantum age identity
// (FRIDOLIN_AGE_IDENTITY_PQ, which every core has and which is on the
// recovery sheet already); files made for the X25519 identity of before
// (FRIDOLIN_AGE_IDENTITY) still open.

// targetSecretName is the host secret of target name's password.
func targetSecretName(name string) string { return "fridolin.restic.node." + name }

// targetSecretEnv is the environment variable of target name's password.
func targetSecretEnv(name string) string {
	return "FRIDOLIN_RESTIC_NODE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// targetSecrets reads and makes the targets' passwords.
type targetSecrets struct {
	getenv func(string) string
	// store is the host's secret store; nil in the container.
	store hostsecrets.Store
	// secretsDir is the data dir's secrets/ folder, for the age files.
	secretsDir string
	random     func() (string, error)
	log        *slog.Logger
}

func (s targetSecrets) ageFile(name string) string {
	return filepath.Join(s.secretsDir, "restic-node-"+name+".age")
}

// identities are the core's age identities: the post-quantum one
// (FRIDOLIN_AGE_IDENTITY_PQ), which new files are encrypted to, and the
// X25519 one of before (FRIDOLIN_AGE_IDENTITY), which still reads the
// files made for it. recipient is nil without the post-quantum one.
func (s targetSecrets) identities() (ids []age.Identity, recipient age.Recipient, err error) {
	if raw := strings.TrimSpace(s.getenv("FRIDOLIN_AGE_IDENTITY_PQ")); raw != "" {
		id, err := age.ParseHybridIdentity(raw)
		if err != nil {
			return nil, nil, errors.New("FRIDOLIN_AGE_IDENTITY_PQ holds no post-quantum age identity")
		}
		ids, recipient = append(ids, id), id.Recipient()
	}
	raw := strings.TrimSpace(s.getenv("FRIDOLIN_AGE_IDENTITY"))
	for line := range strings.SplitSeq(raw, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "AGE-SECRET-KEY-1") {
			id, err := age.ParseX25519Identity(line)
			if err != nil {
				return nil, nil, err
			}
			ids = append(ids, id)
		}
	}
	return ids, recipient, nil
}

// get returns target name's password, "" when there is none yet.
func (s targetSecrets) get(name string) (string, error) {
	if v := strings.TrimSpace(s.getenv(targetSecretEnv(name))); v != "" {
		return v, nil
	}
	if s.store != nil {
		v, err := s.store.Get(targetSecretName(name))
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
		case err != nil:
			return "", fmt.Errorf("reading %s from the host's secrets: %w", targetSecretName(name), err)
		default:
			return v, nil
		}
	}
	data, err := os.ReadFile(s.ageFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	ids, _, err := s.identities()
	if err != nil || len(ids) == 0 {
		return "", fmt.Errorf("%s needs the core's age identity (FRIDOLIN_AGE_IDENTITY_PQ) to be read: %v", s.ageFile(name), err)
	}
	r, err := age.Decrypt(bytes.NewReader(data), ids...)
	if err != nil {
		return "", fmt.Errorf("decrypting %s: %w", s.ageFile(name), err)
	}
	pw, err := io.ReadAll(r)
	return strings.TrimSpace(string(pw)), err
}

// set keeps a new password: in the host's store, else in an age file.
func (s targetSecrets) set(name, pw string) (string, error) {
	if s.store != nil {
		return targetSecretName(name), s.store.Set(targetSecretName(name), pw)
	}
	_, recipient, err := s.identities()
	if err != nil {
		return "", err
	}
	if recipient == nil {
		return "", fmt.Errorf("there is no host secret store here and no post-quantum age identity (FRIDOLIN_AGE_IDENTITY_PQ) to keep a new password with; set %s", targetSecretEnv(name))
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, pw+"\n"); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.secretsDir, 0o700); err != nil {
		return "", err
	}
	return s.ageFile(name), fsx.WriteFileAtomic(s.ageFile(name), buf.Bytes(), 0o600, false)
}

// ensure returns target name's password, making one when there is none
// and the repository does not exist yet (exists answers for the new one).
// A repository that exists without its password is an error: a new one
// would not open it.
func (s targetSecrets) ensure(ctx context.Context, name string, exists func(ctx context.Context, pw string) (bool, error)) (string, error) {
	pw, err := s.get(name)
	if err != nil || pw != "" {
		return pw, err
	}
	if pw, err = s.random(); err != nil {
		return "", err
	}
	found, err := exists(ctx, pw)
	if errors.Is(err, snapshot.ErrWrongPassword) || err == nil && found {
		return "", fmt.Errorf("the target's folder holds a restic repository, but its password (%s) is not here: restore it from the recovery sheet", targetSecretName(name))
	}
	if err != nil {
		return "", fmt.Errorf("looking for the repository before making its password: %w", err)
	}
	where, err := s.set(name, pw)
	if err != nil {
		return "", err
	}
	s.log.Warn("vault: made a new password for the node target's restic repository and kept it as " + where +
		". Write it on the recovery sheet now (docs/VAULT.md, \"Node targets\", says how to read it): without it nobody can restore from that target.")
	return pw, nil
}
