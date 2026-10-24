package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testPlan(now time.Time) MovePlan {
	return MovePlan{Move: "m-20261024-1200-beef", Source: "aaaaaaaaaaaa", Target: "bbbbbbbbbbbb", SourceDevice: strings.Repeat("c", 32),
		Version: "v1", IssuedAt: now, HandoffDeadline: now.Add(15 * time.Minute), ReadyWithin: 600}
}

func TestMovePlanSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC().Truncate(time.Second)
	s, err := SignMovePlan(key, testPlan(now))
	if err != nil {
		t.Fatal(err)
	}
	p, err := OpenMovePlan(pub, s)
	if err != nil || p.Move != "m-20261024-1200-beef" || !p.IssuedAt.Equal(now) {
		t.Fatalf("open: %+v %v", p, err)
	}
	// A changed plan, another key, a field the plan does not know.
	var m map[string]any
	_ = json.Unmarshal(s.Plan, &m)
	m["target"] = "dddddddddddd"
	changed := s
	changed.Plan, _ = json.Marshal(m)
	if _, err := OpenMovePlan(pub, changed); err == nil {
		t.Error("a changed plan opened")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := OpenMovePlan(other, s); err == nil {
		t.Error("a plan opened with another key")
	}
	m["target"], m["extra"] = "bbbbbbbbbbbb", 1
	raw, _ := json.Marshal(m)
	extra := SignedMovePlan{Plan: raw, Signature: ed25519.Sign(key, append([]byte(movePlanDomain), raw...))}
	if _, err := OpenMovePlan(pub, extra); err == nil {
		t.Error("a plan with an unknown field opened")
	}
	// The signature is not an update's or an offer's.
	if ed25519.Verify(pub, s.Plan, s.Signature) {
		t.Error("the signature does not separate its domain")
	}
}

func TestMovePlanCheck(t *testing.T) {
	now := time.Now().UTC()
	for name, mut := range map[string]func(*MovePlan){
		"same":       func(p *MovePlan) { p.Target = p.Source },
		"device":     func(p *MovePlan) { p.SourceDevice = "x" },
		"no version": func(p *MovePlan) { p.Version = "" },
		"image":      func(p *MovePlan) { p.Image = "fridolin-core:latest" },
		"deadline":   func(p *MovePlan) { p.HandoffDeadline = p.IssuedAt.Add(2 * time.Hour) },
		"ready":      func(p *MovePlan) { p.ReadyWithin = 0 },
		"move":       func(p *MovePlan) { p.Move = "move-1" },
	} {
		p := testPlan(now)
		mut(&p)
		if p.Check() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCheckMovePlanFor(t *testing.T) {
	now := time.Now().UTC()
	p := testPlan(now)
	if err := CheckMovePlanFor(p, MoveSource, "aaaaaaaaaaaa", "v1", now); err != nil {
		t.Errorf("the source: %v", err)
	}
	if err := CheckMovePlanFor(p, MoveTarget, "bbbbbbbbbbbb", "v1", now); err != nil {
		t.Errorf("the target: %v", err)
	}
	for name, c := range map[string]struct {
		role, self, version string
		at                  time.Time
		want                string
	}{
		"foreign":  {MoveSource, "bbbbbbbbbbbb", "v1", now, "not this node"},
		"role":     {MoveTarget, "aaaaaaaaaaaa", "v1", now, "not this node"},
		"stale":    {MoveSource, "aaaaaaaaaaaa", "v1", now.Add(16 * time.Minute), "stale"},
		"future":   {MoveSource, "aaaaaaaaaaaa", "v1", now.Add(-2 * time.Minute), "future"},
		"version":  {MoveTarget, "bbbbbbbbbbbb", "v2", now, "for version v1"},
		"deadline": {MoveTarget, "bbbbbbbbbbbb", "v1", now.Add(15 * time.Minute), "has passed"},
	} {
		if err := CheckMovePlanFor(p, c.role, c.self, c.version, c.at); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
