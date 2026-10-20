package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestApprovals(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	a := Approval{ID: "a1", Kind: "approval", Brain: "taxes", Notify: "taxes", Run: "r1", Thread: "c", Chat: "c", Action: "Submit", Risk: "irreversible", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.AddApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddApproval(ctx, Approval{ID: "q1", Kind: "question", Brain: "taxes", Chat: "c", Action: "Which?", Options: []string{"A", "B"}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountPending(ctx, "approval", "irreversible"); n != 1 {
		t.Errorf("pending irreversible: %d", n)
	}
	exp, err := s.ExpiredApprovals(ctx, now.Add(2*time.Minute))
	if err != nil || len(exp) != 1 || exp[0].ID != "q1" || exp[0].Options[1] != "B" {
		t.Errorf("expired: %+v %v", exp, err)
	}
	if ok, _ := s.UseApproval(ctx, "a1"); ok {
		t.Error("a pending approval was used")
	}
	got, err := s.DecideApproval(ctx, "a1", ApprovalApproved, "yes", "15551234567", now)
	if err != nil || got.Status != ApprovalApproved || got.DecidedBy != "15551234567" || got.DecidedAt == nil {
		t.Fatalf("decide: %+v %v", got, err)
	}
	if _, err := s.DecideApproval(ctx, "a1", ApprovalDenied, "", "x", now); !errors.Is(err, ErrNotPending) {
		t.Errorf("a second decision: %v", err)
	}
	if ok, _ := s.UseApproval(ctx, "a1"); !ok {
		t.Error("an approved approval could not be used")
	}
	if ok, _ := s.UseApproval(ctx, "a1"); ok {
		t.Error("an approval was used twice")
	}
	pending, decided, err := s.Approvals(ctx, 10)
	if err != nil || len(pending) != 1 || len(decided) != 1 || !decided[0].Used {
		t.Errorf("approvals: %+v %+v %v", pending, decided, err)
	}
}

func TestSkillApprovalsAndPolicyState(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	ctx := context.Background()
	if err := s.ApproveSkill(ctx, SkillApproval{Brain: "b", Path: "skills/a.md", Hash: "h", Policy: json.RawMessage(`{"risk":"write"}`), ApprovedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SkillApprovals(ctx, "b")
	if err != nil || got["skills/a.md"].Hash != "h" || string(got["skills/a.md"].Policy) != `{"risk":"write"}` {
		t.Errorf("%+v %v", got, err)
	}
	if v, _, err := s.PolicyState(ctx, "lockdown"); err != nil || v != "" {
		t.Errorf("unset: %q %v", v, err)
	}
	_ = s.SetPolicyState(ctx, "lockdown", "15551234567", time.Now())
	if v, _, _ := s.PolicyState(ctx, "lockdown"); v != "15551234567" {
		t.Errorf("set: %q", v)
	}
	if err := s.SavePollRef("m1", "c", "", "Q", []string{"A", "B"}, "approval:a1"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.SentPoll(ctx, "m1"); err != nil || p.Ref != "approval:a1" {
		t.Errorf("poll ref: %+v %v", p, err)
	}
}
