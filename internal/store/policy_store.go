package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The policy's tables: approvals (a run's request_approval or ask_user,
// pending or decided), skill_approvals (the owner's approved policy of
// each skill file), policy_state (lockdown) and the polls' ref, which
// ties a poll the bridge sent to the approval it asks for.

// migrateV5 adds them.
func migrateV5(tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE polls ADD COLUMN ref TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE approvals (
			id          TEXT PRIMARY KEY,
			kind        TEXT NOT NULL,             -- 'approval' or 'question'
			brain       TEXT NOT NULL,             -- the brain whose run asked
			notify      TEXT NOT NULL DEFAULT '',  -- the brain told of a late answer
			run         TEXT NOT NULL DEFAULT '',
			thread      TEXT NOT NULL DEFAULT '',
			chat_jid    TEXT NOT NULL,             -- where it was asked
			action      TEXT NOT NULL,             -- the action, or the question
			details     TEXT NOT NULL DEFAULT '',  -- the draft
			risk        TEXT NOT NULL DEFAULT '',
			skill       TEXT NOT NULL DEFAULT '',
			step        TEXT NOT NULL DEFAULT '',
			options     TEXT NOT NULL DEFAULT '[]', -- JSON array; [] asks for text
			status      TEXT NOT NULL DEFAULT 'pending',
			answer      TEXT NOT NULL DEFAULT '',
			decided_by  TEXT NOT NULL DEFAULT '',
			used        INTEGER NOT NULL DEFAULT 0, -- an approval a tool call took
			created_at  DATETIME NOT NULL,
			expires_at  DATETIME NOT NULL,
			decided_at  DATETIME
		)`,
		`CREATE INDEX approvals_status ON approvals (status, expires_at)`,
		`CREATE TABLE skill_approvals (
			brain       TEXT NOT NULL,
			path        TEXT NOT NULL,             -- skills/<name>.md or skills/<name>/SKILL.md
			hash        TEXT NOT NULL,             -- of the policy approved
			policy      TEXT NOT NULL,             -- that policy, as JSON
			approved_at DATETIME NOT NULL,
			PRIMARY KEY (brain, path)
		)`,
		`CREATE TABLE policy_state (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// The statuses of an approval.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalExpired  = "expired"
	ApprovalAnswered = "answered"
)

// Approval is a request_approval or ask_user of a run.
type Approval struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"` // "approval" or "question"
	Brain     string     `json:"brain"`
	Notify    string     `json:"notify"`
	Run       string     `json:"run"`
	Thread    string     `json:"thread"`
	Chat      string     `json:"chat"`
	Action    string     `json:"action"`
	Details   string     `json:"details"`
	Risk      string     `json:"risk"`
	Skill     string     `json:"skill"`
	Step      string     `json:"step"`
	Options   []string   `json:"options"`
	Status    string     `json:"status"`
	Answer    string     `json:"answer"`
	DecidedBy string     `json:"decided_by"`
	Used      bool       `json:"used"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	DecidedAt *time.Time `json:"decided_at"`
}

// AddApproval stores a new pending approval.
func (s *Store) AddApproval(ctx context.Context, a Approval) error {
	opts, err := json.Marshal(nonNil(a.Options))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO approvals
		(id, kind, brain, notify, run, thread, chat_jid, action, details, risk, skill, step, options, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Kind, a.Brain, a.Notify, a.Run, a.Thread, a.Chat, a.Action, a.Details, a.Risk, a.Skill, a.Step, string(opts),
		ApprovalPending, a.CreatedAt.UTC(), a.ExpiresAt.UTC())
	return err
}

const approvalCols = `id, kind, brain, notify, run, thread, chat_jid, action, details, risk, skill, step, options, status, answer, decided_by, used, created_at, expires_at, decided_at`

func scanApproval(sc interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	var opts string
	var decided sql.NullTime
	err := sc.Scan(&a.ID, &a.Kind, &a.Brain, &a.Notify, &a.Run, &a.Thread, &a.Chat, &a.Action, &a.Details, &a.Risk, &a.Skill, &a.Step,
		&opts, &a.Status, &a.Answer, &a.DecidedBy, &a.Used, &a.CreatedAt, &a.ExpiresAt, &decided)
	if err != nil {
		return Approval{}, err
	}
	a.Options = jsonStrings(opts)
	if decided.Valid {
		t := decided.Time
		a.DecidedAt = &t
	}
	return a, nil
}

// ApprovalByID returns approval id, or sql.ErrNoRows.
func (s *Store) ApprovalByID(ctx context.Context, id string) (Approval, error) {
	return scanApproval(s.db.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id))
}

// Approvals returns the pending approvals, oldest first, and up to limit
// decided ones, newest first.
func (s *Store) Approvals(ctx context.Context, limit int) (pending, decided []Approval, err error) {
	pending, err = s.queryApprovals(ctx, `SELECT `+approvalCols+` FROM approvals WHERE status = ? ORDER BY created_at, id`, ApprovalPending)
	if err != nil {
		return nil, nil, err
	}
	decided, err = s.queryApprovals(ctx, `SELECT `+approvalCols+` FROM approvals WHERE status != ? ORDER BY decided_at DESC, created_at DESC LIMIT ?`, ApprovalPending, limit)
	return pending, decided, err
}

func (s *Store) queryApprovals(ctx context.Context, q string, args ...any) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ErrNotPending is a decision on an approval that is decided already.
var ErrNotPending = errors.New("it is no longer pending")

// DecideApproval moves pending approval id to status, with the answer
// and who gave it. It returns ErrNotPending when it was decided already
// (by another vote, the dashboard or its expiry), and the approval as
// decided.
func (s *Store) DecideApproval(ctx context.Context, id, status, answer, by string, at time.Time) (Approval, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = ?, answer = ?, decided_by = ?, decided_at = ? WHERE id = ? AND status = ?`,
		status, answer, by, at.UTC(), id, ApprovalPending)
	if err != nil {
		return Approval{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		a, err := s.ApprovalByID(ctx, id)
		if err != nil {
			return Approval{}, err
		}
		return a, ErrNotPending
	}
	return s.ApprovalByID(ctx, id)
}

// ExpiredApprovals returns the pending approvals whose time is up at now.
func (s *Store) ExpiredApprovals(ctx context.Context, now time.Time) ([]Approval, error) {
	return s.queryApprovals(ctx, `SELECT `+approvalCols+` FROM approvals WHERE status = ? AND expires_at <= ? ORDER BY expires_at`, ApprovalPending, now.UTC())
}

// CountPending counts the pending approvals of kind with risk ("" any).
func (s *Store) CountPending(ctx context.Context, kind, risk string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE status = ? AND kind = ? AND (? = '' OR risk = ?)`,
		ApprovalPending, kind, risk, risk).Scan(&n)
	return n, err
}

// UseApproval marks approved approval id as taken by a tool call, once:
// it reports false when it is not approved or was taken already.
func (s *Store) UseApproval(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET used = 1 WHERE id = ? AND status = ? AND used = 0`, id, ApprovalApproved)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SkillApproval is the policy the owner approved for a skill file.
type SkillApproval struct {
	Brain, Path, Hash string
	Policy            json.RawMessage
	ApprovedAt        time.Time
}

// SkillApprovals returns brain's approved skill policies by path.
func (s *Store) SkillApprovals(ctx context.Context, brain string) (map[string]SkillApproval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT brain, path, hash, policy, approved_at FROM skill_approvals WHERE brain = ?`, brain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SkillApproval{}
	for rows.Next() {
		var a SkillApproval
		var policy string
		if err := rows.Scan(&a.Brain, &a.Path, &a.Hash, &policy, &a.ApprovedAt); err != nil {
			return nil, err
		}
		a.Policy = json.RawMessage(policy)
		out[a.Path] = a
	}
	return out, rows.Err()
}

// ApproveSkill records the policy approved for brain's skill path.
func (s *Store) ApproveSkill(ctx context.Context, a SkillApproval) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO skill_approvals (brain, path, hash, policy, approved_at) VALUES (?, ?, ?, ?, ?)`,
		a.Brain, a.Path, a.Hash, string(a.Policy), a.ApprovedAt.UTC())
	return err
}

// PolicyState returns the value of key, "" when unset.
func (s *Store) PolicyState(ctx context.Context, key string) (string, time.Time, error) {
	var v string
	var at time.Time
	err := s.db.QueryRowContext(ctx, `SELECT value, updated_at FROM policy_state WHERE key = ?`, key).Scan(&v, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return v, at, err
}

// SetPolicyState sets key to value.
func (s *Store) SetPolicyState(ctx context.Context, key, value string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO policy_state (key, value, updated_at) VALUES (?, ?, ?)`, key, value, at.UTC())
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
