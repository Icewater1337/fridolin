package doctor

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// TestPolicyChecks: doctor warns about an approval that has waited more
// than an hour, and shows the lockdown, from the audit log alone.
func TestPolicyChecks(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, log          string
		approvals, lockdwn string // the statuses
		detail             string
	}{
		{name: "nothing", approvals: OK, lockdwn: OK, detail: "none waiting"},
		{name: "recent", log: `{"ts":"2026-10-20T11:30:00Z","kind":"approval_requested","id":"a1"}`, approvals: OK, lockdwn: OK, detail: "1 waiting for an answer, the oldest for 30m0s"},
		{name: "old", log: `{"ts":"2026-10-20T09:00:00Z","kind":"approval_requested","id":"a1"}
{"ts":"2026-10-20T11:00:00Z","kind":"approval_requested","id":"a2"}
{"ts":"2026-10-20T11:10:00Z","kind":"approval_decided","id":"a2","outcome":"denied"}`, approvals: Warn, lockdwn: OK, detail: "1 waiting for an answer, the oldest for 3h0m0s"},
		{name: "decided", log: `{"ts":"2026-10-20T09:00:00Z","kind":"approval_requested","id":"a1"}
{"ts":"2026-10-20T10:00:00Z","kind":"approval_decided","id":"a1","outcome":"expired"}`, approvals: OK, lockdwn: OK, detail: "none waiting"},
		{name: "locked", log: `{"ts":"2026-10-20T09:00:00Z","kind":"lockdown","by":"15551234567"}`, approvals: OK, lockdwn: Warn, detail: "none waiting"},
		{name: "unlocked", log: `{"ts":"2026-10-20T09:00:00Z","kind":"lockdown","by":"15551234567"}
{"ts":"2026-10-20T09:05:00Z","kind":"unlock","by":"dashboard"}`, approvals: OK, lockdwn: OK, detail: "none waiting"},
		{name: "a line that is no entry", log: `not json
{"ts":"2026-10-20T11:30:00Z","kind":"question_asked","id":"q1"}`, approvals: OK, lockdwn: OK, detail: "1 waiting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			env := s.doctorEnv(testConfig(nil), nil)
			file := "/home/you/.fridolin/audit/2026-10.jsonl"
			env.Glob = func(pattern string) ([]string, error) {
				if pattern != "/home/you/.fridolin/audit/*.jsonl" || tc.log == "" {
					return nil, nil
				}
				return []string{file}, nil
			}
			env.ReadFile = func(p string) ([]byte, error) { return []byte(tc.log + "\n"), nil }
			env.Now = func() time.Time { return now }
			rep := Run(context.Background(), env, allChecks())
			a, l := result(t, rep, "approvals"), result(t, rep, "lockdown")
			if a.Status != tc.approvals || !strings.Contains(a.Detail, tc.detail) {
				t.Errorf("approvals: %s %q", a.Status, a.Detail)
			}
			if l.Status != tc.lockdwn {
				t.Errorf("lockdown: %s %q", l.Status, l.Detail)
			}
			if tc.lockdwn == Warn && !strings.Contains(l.Detail, "by 15551234567") {
				t.Errorf("lockdown detail: %q", l.Detail)
			}
			if !rep.OK {
				t.Error("the policy checks never fail doctor")
			}
		})
	}
}

// TestPolicyChecksAuditLog: approvals read only the last two months' files, and
// a log that cannot be read warns; without the config nothing is read.
func TestPolicyChecksAuditLog(t *testing.T) {
	env := healthyMac().doctorEnv(testConfig(nil), nil)
	var read []string
	env.Glob = func(string) ([]string, error) {
		return []string{"/a/2026-10.jsonl", "/a/2026-08.jsonl", "/a/2026-09.jsonl"}, nil
	}
	env.ReadFile = func(p string) ([]byte, error) { read = append(read, p); return nil, nil }
	if r := checkPendingApprovals(context.Background(), env); r.Status != OK || strings.Join(read, " ") != "/a/2026-09.jsonl /a/2026-10.jsonl" {
		t.Errorf("read %q: %+v", read, r)
	}

	env.ReadFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
	if r := checkPendingApprovals(context.Background(), env); r.Status != Warn || !strings.Contains(r.Detail, "permission denied") {
		t.Errorf("approvals, unreadable: %+v", r)
	}
	env.Glob = func(string) ([]string, error) { return nil, errors.New("bad pattern") }
	if r := checkLockdown(context.Background(), env); r.Status != Warn || !strings.Contains(r.Detail, "bad pattern") {
		t.Errorf("lockdown, unreadable: %+v", r)
	}

	env = healthyMac().doctorEnv(nil, errors.New("bad"))
	for _, check := range []func(context.Context, *Env) Result{checkPendingApprovals, checkLockdown, checkBrainDrift} {
		if r := check(context.Background(), env); r != NoConfig {
			t.Errorf("without the config: %+v", r)
		}
	}
}

// TestLockdownOlderMonths: a lockdown lasts until an unlock, however many
// monthly files lie between; approvals still look at the last two files.
func TestLockdownOlderMonths(t *testing.T) {
	const (
		lockdown = `{"ts":"2026-08-03T09:00:00Z","kind":"lockdown","by":"15551234567"}` + "\n"
		unlock   = `{"ts":"2026-10-03T09:00:00Z","kind":"unlock","by":"dashboard"}` + "\n"
		relock   = `{"ts":"2026-08-04T09:00:00Z","kind":"lockdown","by":"dashboard"}` + "\n"
		asked    = `{"ts":"2026-08-03T10:00:00Z","kind":"approval_requested","id":"a1"}` + "\n"
		cold     = `{"files":[{"name":"2026-08.jsonl"}]}`
	)
	const dir = "/home/you/.fridolin/audit/"
	for _, tc := range []struct {
		name   string
		files  map[string]string
		status string
		detail string
		read   string // the files read, when it matters
	}{
		{name: "old lockdown, empty months after it", files: map[string]string{"2026-08.jsonl": lockdown, "2026-10.jsonl": "", "2026-11.jsonl": ""},
			status: Warn, detail: "on since 2026-08-03T09:00:00Z (by 15551234567)"},
		{name: "unlock in a later file", files: map[string]string{"2026-08.jsonl": lockdown, "2026-10.jsonl": unlock, "2026-11.jsonl": ""},
			status: OK, detail: "off"},
		{name: "last line of a file wins", files: map[string]string{"2026-08.jsonl": lockdown + unlock + relock},
			status: Warn, detail: "on since 2026-08-04T09:00:00Z (by dashboard)"},
		{name: "stops at the newest file with a line", files: map[string]string{"2026-08.jsonl": lockdown, "2026-10.jsonl": unlock, "2026-11.jsonl": ""},
			status: OK, detail: "off", read: "2026-11.jsonl 2026-10.jsonl"},
		{name: "older months only in the cold tier", files: map[string]string{"2026-11.jsonl": "", ".cold.json": cold},
			status: OK, detail: "1 older month(s)"},
		{name: "a line on disk needs no cold months", files: map[string]string{"2026-11.jsonl": unlock, ".cold.json": cold},
			status: OK, detail: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := healthyMac().doctorEnv(testConfig(nil), nil)
			var read []string
			env.Glob = func(string) ([]string, error) {
				var out []string
				for name := range tc.files {
					if strings.HasSuffix(name, ".jsonl") {
						out = append(out, dir+name)
					}
				}
				return out, nil
			}
			env.ReadFile = func(p string) ([]byte, error) {
				name := strings.TrimPrefix(p, dir)
				data, ok := tc.files[name]
				if !ok {
					return nil, fs.ErrNotExist
				}
				if name != ".cold.json" {
					read = append(read, name)
				}
				return []byte(data), nil
			}
			r := checkLockdown(context.Background(), env)
			if r.Status != tc.status || !strings.Contains(r.Detail, tc.detail) {
				t.Errorf("%s %q", r.Status, r.Detail)
			}
			if tc.read != "" && strings.Join(read, " ") != tc.read {
				t.Errorf("read %q, want %q", read, tc.read)
			}
		})
	}

	t.Run("approvals older than the last two files do not count", func(t *testing.T) {
		env := healthyMac().doctorEnv(testConfig(nil), nil)
		env.Glob = func(string) ([]string, error) {
			return []string{dir + "2026-08.jsonl", dir + "2026-10.jsonl", dir + "2026-11.jsonl"}, nil
		}
		env.ReadFile = func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "2026-08.jsonl") {
				return []byte(asked), nil
			}
			return nil, nil
		}
		if r := checkPendingApprovals(context.Background(), env); r.Status != OK || r.Detail != "none waiting" {
			t.Errorf("%+v", r)
		}
	})

	t.Run("an unreadable older file warns, it does not say off", func(t *testing.T) {
		env := healthyMac().doctorEnv(testConfig(nil), nil)
		env.Glob = func(string) ([]string, error) { return []string{dir + "2026-08.jsonl", dir + "2026-11.jsonl"}, nil }
		env.ReadFile = func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "2026-08.jsonl") {
				return nil, errors.New("permission denied")
			}
			return nil, nil
		}
		if r := checkLockdown(context.Background(), env); r.Status != Warn || !strings.Contains(r.Detail, "permission denied") {
			t.Errorf("%+v", r)
		}
	})
}
