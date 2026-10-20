package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// mapSecrets is a host secret store in memory.
type mapSecrets map[string]string

func (m mapSecrets) Get(name string) (string, error) {
	if v, ok := m[name]; ok {
		return v, nil
	}
	return "", hostsecrets.ErrNotFound
}
func (m mapSecrets) Set(name, value string) error { m[name] = value; return nil }
func (m mapSecrets) Has(name string) (bool, error) {
	_, ok := m[name]
	return ok, nil
}

func TestVaultSecrets(t *testing.T) {
	env := map[string]string{"B2_VAULT_KEY_ID": "env-id", "RESTIC_PASSWORD": ""}
	store := mapSecrets{hostsecrets.B2VaultKeyID: "store-id", hostsecrets.B2VaultKey: "store-key"}
	var logs bytes.Buffer
	n := 0
	src := vaultSecretSource{getenv: func(k string) string { return env[k] }, store: store,
		random: func() (string, error) { n++; return "generated" + string(rune('0'+n)), nil },
		log:    slog.New(slog.NewTextHandler(&logs, nil))}
	c, err := src.read()
	if err != nil {
		t.Fatal(err)
	}
	if c.VaultKeyID != "env-id" || c.VaultKey != "store-key" || c.CryptPassword != "" {
		t.Fatalf("read: the environment wins, the store fills in: %+v", c)
	}

	// The cold tier has files: no new passwords, which could not read them.
	err = src.ensureCrypt(context.Background(), &c, func(context.Context) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "recovery sheet") || store[hostsecrets.VaultCryptPassword] != "" {
		t.Fatalf("crypt passwords over a full cold tier: %v, store %v", err, store)
	}
	// An empty cold tier: made, stored, and only named in the log.
	if err := src.ensureCrypt(context.Background(), &c, func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if c.CryptPassword != "generated1" || c.CryptSalt != "generated2" || store[hostsecrets.VaultCryptPassword] != "generated1" || store[hostsecrets.VaultCryptSalt] != "generated2" {
		t.Fatalf("crypt passwords: %+v, store %v", c, store)
	}
	if strings.Contains(logs.String(), "generated") || !strings.Contains(logs.String(), "recovery sheet") {
		t.Errorf("the log: %s", logs.String())
	}

	// restic: a repository that exists keeps its password missing.
	for _, tc := range []struct {
		exists bool
		err    error
		ok     bool
	}{{true, nil, false}, {false, snapshot.ErrWrongPassword, false}, {false, errors.New("network"), false}, {false, nil, true}} {
		c.ResticPassword = ""
		delete(store, hostsecrets.ResticPassword)
		err := src.ensureRestic(context.Background(), &c, func(context.Context, string) (bool, error) { return tc.exists, tc.err })
		if (err == nil) != tc.ok || (tc.ok && (c.ResticPassword == "" || store[hostsecrets.ResticPassword] != c.ResticPassword)) {
			t.Errorf("exists %v, err %v: %v, password %q", tc.exists, tc.err, err, c.ResticPassword)
		}
	}

	// In the container there is no store: nothing is made there.
	inContainer := vaultSecretSource{getenv: func(string) string { return "" }, random: src.random, log: src.log}
	c = vaultCreds{}
	if err := inContainer.ensureCrypt(context.Background(), &c, func(context.Context) (bool, error) { return true, nil }); err == nil {
		t.Error("made crypt passwords without a host store")
	}
	if err := inContainer.ensureRestic(context.Background(), &c, func(context.Context, string) (bool, error) { return false, nil }); err == nil {
		t.Error("made a restic password without a host store")
	}
}

func TestNewVaultPasswordFitsTheKeychain(t *testing.T) {
	pw, err := newVaultPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 40 || strings.ContainsAny(pw, " \"'\\$") {
		t.Errorf("password %q", pw)
	}
}
