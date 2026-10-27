package app

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The key checks apply only where a key is set, and warn, never fail,
// when the provider refuses it.
func TestDoctorConnectionChecks(t *testing.T) {
	t.Parallel()
	openai := connectionDoctorChecks[0]
	if openai.ID != "openai" || openai.Required || connectionDoctorChecks[1].ID != "elevenlabs" {
		t.Fatalf("checks %+v", connectionDoctorChecks)
	}
	s := healthyMac()
	if openai.Applies(s.doctorEnv(nil, errors.New("no config"))) {
		t.Error("applies without a config")
	}
	env := s.doctorEnv(doctorConfig(nil), nil)
	if openai.Applies(env) {
		t.Error("applies without a key")
	}
	s.files = map[string]fs.FileMode{"/home/you/.fridolin/connections/openai.json": 0o600}
	env = s.doctorEnv(doctorConfig(nil), nil)
	if !openai.Applies(env) {
		t.Fatal("does not apply with a key")
	}
	if r := openai.Run(context.Background(), env); r.Status != doctor.OK || r.Detail != "the key works" {
		t.Errorf("works: %+v", r)
	}
	s.connErr = map[string]error{"openai": errors.New("OpenAI says the key is wrong (401)")}
	if r := openai.Run(context.Background(), s.doctorEnv(doctorConfig(nil), nil)); r.Status != doctor.Warn || !strings.Contains(r.Detail, "401") || r.Fix == "" {
		t.Errorf("refused: %+v", r)
	}
	env.CheckConnection = nil
	if r := openai.Run(context.Background(), env); r.Status != doctor.Warn || r.Fix == "" {
		t.Errorf("no way to ask: %+v", r)
	}
}

// The GitLab check applies where the connection is kept on this machine,
// core/local-secrets, and warns when GitLab refuses the token.
func TestDoctorGitLabCheck(t *testing.T) {
	t.Parallel()
	gitlab := connectionDoctorChecks[2]
	if gitlab.ID != "gitlab" || gitlab.Required || gitlab.Title != "GitLab connection" {
		t.Fatalf("check %+v", gitlab)
	}
	s := healthyMac()
	if gitlab.Applies(s.doctorEnv(doctorConfig(nil), nil)) {
		t.Error("applies without a connection")
	}
	s.files = map[string]fs.FileMode{"/home/you/.fridolin/core/local-secrets/gitlab.json": 0o600}
	env := s.doctorEnv(doctorConfig(nil), nil)
	if !gitlab.Applies(env) {
		t.Fatal("does not apply with a connection")
	}
	s.connErr = map[string]error{"gitlab": errors.New("GitLab says the token is wrong or expired (401)")}
	if r := gitlab.Run(context.Background(), s.doctorEnv(doctorConfig(nil), nil)); r.Status != doctor.Warn || !strings.Contains(r.Detail, "401") || !strings.Contains(r.Fix, "write_repository") {
		t.Errorf("refused: %+v", r)
	}
	if _, err := checkConnectionKey(context.Background(), t.TempDir(), "gitlab"); err == nil || !strings.Contains(err.Error(), "no GitLab connection") {
		t.Errorf("without a connection: %v", err)
	}
}

// checkConnectionKey reads the key from the data dir's connections/;
// without one it asks no one. (With one it would ask the real provider,
// which no test may: internal/connections tests the check on fakes.)
func TestCheckConnectionKeyWithoutAKey(t *testing.T) {
	t.Parallel()
	if _, err := checkConnectionKey(context.Background(), t.TempDir(), "openai"); err == nil || !strings.Contains(err.Error(), "no OpenAI key") {
		t.Errorf("without a key: %v", err)
	}
}
