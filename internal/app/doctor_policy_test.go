package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDoctorPolicyChecks: doctor warns about an approval that has waited
// more than an hour, and shows the lockdown, from the audit log alone.
func TestDoctorPolicyChecks(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, log          string
		approvals, lockdwn string // the statuses
		detail             string
	}{
		{name: "nothing", approvals: DoctorOK, lockdwn: DoctorOK, detail: "none waiting"},
		{name: "recent", log: `{"ts":"2026-10-20T11:30:00Z","kind":"approval_requested","id":"a1"}`, approvals: DoctorOK, lockdwn: DoctorOK, detail: "1 waiting for an answer, the oldest for 30m0s"},
		{name: "old", log: `{"ts":"2026-10-20T09:00:00Z","kind":"approval_requested","id":"a1"}
{"ts":"2026-10-20T11:00:00Z","kind":"approval_requested","id":"a2"}
{"ts":"2026-10-20T11:10:00Z","kind":"approval_decided","id":"a2","outcome":"denied"}`, approvals: DoctorWarn, lockdwn: DoctorOK, detail: "1 waiting for an answer, the oldest for 3h0m0s"},
		{name: "decided", log: `{"ts":"2026-10-20T09:00:00Z","kind":"approval_requested","id":"a1"}
{"ts":"2026-10-20T10:00:00Z","kind":"approval_decided","id":"a1","outcome":"expired"}`, approvals: DoctorOK, lockdwn: DoctorOK, detail: "none waiting"},
		{name: "locked", log: `{"ts":"2026-10-20T09:00:00Z","kind":"lockdown","by":"15551234567"}`, approvals: DoctorOK, lockdwn: DoctorWarn, detail: "none waiting"},
		{name: "unlocked", log: `{"ts":"2026-10-20T09:00:00Z","kind":"lockdown","by":"15551234567"}
{"ts":"2026-10-20T09:05:00Z","kind":"unlock","by":"dashboard"}`, approvals: DoctorOK, lockdwn: DoctorOK, detail: "none waiting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			env := s.doctorEnv(doctorConfig(nil), nil)
			file := "/home/you/.fridolin/audit/2026-10.jsonl"
			env.Glob = func(pattern string) ([]string, error) {
				if pattern != "/home/you/.fridolin/audit/*.jsonl" || tc.log == "" {
					return nil, nil
				}
				return []string{file}, nil
			}
			env.ReadFile = func(p string) ([]byte, error) { return []byte(tc.log + "\n"), nil }
			env.Now = func() time.Time { return now }
			rep := RunDoctor(context.Background(), env, DoctorRegistry)
			a, l := result(t, rep, "approvals"), result(t, rep, "lockdown")
			if a.Status != tc.approvals || !strings.Contains(a.Detail, tc.detail) {
				t.Errorf("approvals: %s %q", a.Status, a.Detail)
			}
			if l.Status != tc.lockdwn {
				t.Errorf("lockdown: %s %q", l.Status, l.Detail)
			}
			if tc.lockdwn == DoctorWarn && !strings.Contains(l.Detail, "by 15551234567") {
				t.Errorf("lockdown detail: %q", l.Detail)
			}
			if !rep.OK {
				t.Error("the policy checks never fail doctor")
			}
		})
	}
}
