package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// brain-runtime leaves a setup on bubblewrap alone, says where each
// brain set to Docker Sandboxes runs, and warns when one falls back or
// the bridge does not answer.
func TestDoctorBrainRuntime(t *testing.T) {
	t.Parallel()
	docker := &manifest.Sandbox{Runtime: manifest.RuntimeDockerSandbox, Fallback: manifest.FallbackRefuse}
	cfg, plain := &config.Settings{}, &config.Settings{}
	cfg.BrainDirs = []config.BrainRef{
		{Path: "/x/main", Approved: &manifest.Manifest{ID: "main"}},
		{Path: "/x/web", Approved: &manifest.Manifest{ID: "web", Sandbox: docker}},
	}
	plain.BrainDirs = cfg.BrainDirs[:1]
	env := &doctor.Env{Config: cfg}
	if dockerBrainsSet(&doctor.Env{Config: plain}) {
		t.Error("brain-runtime applies to a setup on bubblewrap")
	}
	env.LiveBrains = func(context.Context, *config.Settings) ([]byte, error) {
		return []byte(`[{"id":"web","sandbox":{"configured":"docker-sandbox","effective":"refused","fallback":"refuse","node":"mini","reason":"mini is offline"}}]`), nil
	}
	res := checkBrainRuntime(context.Background(), env)
	if res.Status != doctor.Warn || res.Detail != "web: refused, as its fallback is refuse, because mini is offline" {
		t.Errorf("falling back: %+v", res)
	}
	env.LiveBrains = func(context.Context, *config.Settings) ([]byte, error) {
		return []byte(`[{"id":"web","sandbox":{"configured":"docker-sandbox","effective":"docker-sandbox","node":"mini","ready":true}}]`), nil
	}
	if res := checkBrainRuntime(context.Background(), env); res.Status != doctor.OK || res.Detail != "web: in a Docker Sandbox on mini" {
		t.Errorf("ready: %+v", res)
	}
	env.LiveBrains = func(context.Context, *config.Settings) ([]byte, error) { return nil, errors.New("connection refused") }
	if res := checkBrainRuntime(context.Background(), env); res.Status != doctor.Warn || !strings.Contains(res.Detail, "did not say") {
		t.Errorf("no bridge: %+v", res)
	}
}
