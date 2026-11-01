package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Errands (docs/BRAINS.md, "Errands"): the core brain has Fridolin write
// to someone who is not in allowed_numbers, and Fridolin carries the
// conversation with them. errands holds each errand, errand_lines its
// conversation, and errand_contacts the people Fridolin has written to
// (whom it may write to again without asking, when the owner asks in
// their own chat) and the ones it stopped listening to.

// migrateV9 adds errands, errand_lines and errand_contacts.
func migrateV9(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE errands (
			id          TEXT PRIMARY KEY,
			brain       TEXT NOT NULL,              -- the brain that asked for it
			contact     TEXT NOT NULL,              -- the person's phone number, digits
			name        TEXT NOT NULL,              -- what the brain called them
			goal        TEXT NOT NULL,
			brief       TEXT NOT NULL,              -- what Fridolin may tell them and agree to
			message     TEXT NOT NULL,              -- the first message, as the brain wrote it
			state       TEXT NOT NULL,              -- asking, open, done, aborted, declined, expired, stopped
			approval    TEXT NOT NULL DEFAULT '',   -- the owner's approval of it, '' for none needed
			by_owner    INTEGER NOT NULL DEFAULT 0, -- the owner asked for it in their own chat
			run         TEXT NOT NULL DEFAULT '',   -- the run that asked for it
			chat_jid    TEXT NOT NULL,              -- where the owner hears of it
			days        INTEGER NOT NULL,           -- how long it stays open once it is
			sent        INTEGER NOT NULL DEFAULT 0, -- Fridolin's messages so far
			outcome     TEXT NOT NULL DEFAULT '',   -- the summary or the reason it ended
			created_at  DATETIME NOT NULL,
			deadline    DATETIME NOT NULL,          -- when it ends unless it ends before
			closed_at   DATETIME
		)`,
		`CREATE INDEX errands_by_contact ON errands (contact, state)`,
		`CREATE INDEX errands_by_approval ON errands (approval)`,
		`CREATE TABLE errand_lines (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			errand  TEXT NOT NULL,                  -- errands.id
			sender  TEXT NOT NULL,                  -- you (Fridolin), them, owner
			text    TEXT NOT NULL,
			at      DATETIME NOT NULL
		)`,
		`CREATE INDEX errand_lines_by_errand ON errand_lines (errand, id)`,
		`CREATE TABLE errand_contacts (
			contact     TEXT PRIMARY KEY,           -- the phone number, digits
			lid         TEXT NOT NULL DEFAULT '',   -- the LID WhatsApp may address them by
			name        TEXT NOT NULL DEFAULT '',
			brain       TEXT NOT NULL DEFAULT '',   -- the brain of the last errand with them
			state       TEXT NOT NULL,              -- known, blocked
			reason      TEXT NOT NULL DEFAULT '',   -- why blocked
			updated_at  DATETIME NOT NULL
		)`,
		`CREATE INDEX errand_contacts_by_lid ON errand_contacts (lid)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// The states of an errand.
const (
	// ErrandAsking waits for the owner's approval of the first message.
	ErrandAsking = "asking"
	// ErrandOpen went out and takes the person's answers.
	ErrandOpen = "open"
	// ErrandDone reached its end: its goal, or that it cannot be reached.
	ErrandDone = "done"
	// ErrandAborted ended because the person tried to make Fridolin do
	// what it must not.
	ErrandAborted = "aborted"
	// ErrandDeclined is one the owner said no to, or did not answer.
	ErrandDeclined = "declined"
	// ErrandExpired ran out of time or of messages.
	ErrandExpired = "expired"
	// ErrandStopped was ended by the owner's brain.
	ErrandStopped = "stopped"
)

// The states of a contact.
const (
	// ContactKnown is someone Fridolin wrote to in an errand.
	ContactKnown = "known"
	// ContactBlocked is someone Fridolin stopped listening to.
	ContactBlocked = "blocked"
)

// Errand is one row of errands.
type Errand struct {
	ID        string
	Brain     string
	Contact   string
	Name      string
	Goal      string
	Brief     string
	Message   string
	State     string
	Approval  string
	ByOwner   bool
	Run       string
	Chat      string
	Days      int
	Sent      int
	Outcome   string
	CreatedAt time.Time
	Deadline  time.Time
	ClosedAt  time.Time
}

// Live reports whether the errand has not ended: it waits for the owner,
// or it is open.
func (e Errand) Live() bool { return e.State == ErrandAsking || e.State == ErrandOpen }

// ErrandLine is one message of an errand's conversation.
type ErrandLine struct {
	Sender string
	Text   string
	At     time.Time
}

// ErrandContact is one row of errand_contacts.
type ErrandContact struct {
	Contact   string
	LID       string
	Name      string
	Brain     string
	State     string
	Reason    string
	UpdatedAt time.Time
}

// ErrNoErrand is an errand, or a contact, the store does not have.
var ErrNoErrand = errors.New("no such errand")

const errandColumns = `id, brain, contact, name, goal, brief, message, state, approval, by_owner, run, chat_jid, days, sent, outcome, created_at, deadline, closed_at`

func scanErrand(row interface{ Scan(...any) error }) (Errand, error) {
	var e Errand
	var closed sql.NullTime
	err := row.Scan(&e.ID, &e.Brain, &e.Contact, &e.Name, &e.Goal, &e.Brief, &e.Message, &e.State, &e.Approval, &e.ByOwner, &e.Run, &e.Chat, &e.Days, &e.Sent, &e.Outcome, &e.CreatedAt, &e.Deadline, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNoErrand
	}
	e.ClosedAt = closed.Time
	return e, err
}

func (s *Store) queryErrands(ctx context.Context, q string, args ...any) ([]Errand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+errandColumns+` FROM errands `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Errand
	for rows.Next() {
		e, err := scanErrand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddErrand records e as it is.
func (s *Store) AddErrand(ctx context.Context, e Errand) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO errands (`+errandColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		e.ID, e.Brain, e.Contact, e.Name, e.Goal, e.Brief, e.Message, e.State, e.Approval, e.ByOwner, e.Run, e.Chat, e.Days, e.Sent, e.Outcome, e.CreatedAt.UTC(), e.Deadline.UTC())
	return err
}

// GetErrand reads errand id.
func (s *Store) GetErrand(ctx context.Context, id string) (Errand, error) {
	return scanErrand(s.db.QueryRowContext(ctx, `SELECT `+errandColumns+` FROM errands WHERE id = ?`, id))
}

// ErrandByApproval is the errand whose first message approval id asks for.
func (s *Store) ErrandByApproval(ctx context.Context, id string) (Errand, error) {
	if id == "" {
		return Errand{}, ErrNoErrand
	}
	return scanErrand(s.db.QueryRowContext(ctx, `SELECT `+errandColumns+` FROM errands WHERE approval = ?`, id))
}

// LiveErrandWith is the errand with contact that has not ended, if any.
func (s *Store) LiveErrandWith(ctx context.Context, contact string) (Errand, error) {
	return scanErrand(s.db.QueryRowContext(ctx, `SELECT `+errandColumns+` FROM errands WHERE contact = ? AND state IN (?, ?) ORDER BY created_at DESC LIMIT 1`, contact, ErrandAsking, ErrandOpen))
}

// OpenErrands lists the open errands, oldest first.
func (s *Store) OpenErrands(ctx context.Context) ([]Errand, error) {
	return s.queryErrands(ctx, `WHERE state = ? ORDER BY created_at`, ErrandOpen)
}

// RecentErrands lists brain's errands, newest first: those that have not
// ended, then the last limit of the others.
func (s *Store) RecentErrands(ctx context.Context, brain string, limit int) ([]Errand, error) {
	// One statement, so that an errand that ends meanwhile is listed once.
	return s.queryErrands(ctx, `WHERE brain = ? AND (state IN (?, ?) OR id IN (SELECT id FROM errands WHERE brain = ? AND state NOT IN (?, ?) ORDER BY created_at DESC LIMIT ?))
		ORDER BY state IN (?, ?) DESC, created_at DESC`, brain, ErrandAsking, ErrandOpen, brain, ErrandAsking, ErrandOpen, limit, ErrandAsking, ErrandOpen)
}

// OpenErrand opens errand id, which waited for the owner, until deadline.
// It reports false when the errand was no longer waiting.
func (s *Store) OpenErrand(ctx context.Context, id string, deadline time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE errands SET state = ?, deadline = ? WHERE id = ? AND state = ?`, ErrandOpen, deadline.UTC(), id, ErrandAsking)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// EndErrand ends errand id in state with outcome, unless it ended
// already. It reports whether it ended it.
func (s *Store) EndErrand(ctx context.Context, id, state, outcome string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE errands SET state = ?, outcome = ?, closed_at = ? WHERE id = ? AND state IN (?, ?)`, state, outcome, at.UTC(), id, ErrandAsking, ErrandOpen)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AddErrandLine adds a message to errand id's conversation; one of
// Fridolin's counts towards the errand's messages.
func (s *Store) AddErrandLine(ctx context.Context, id string, l ErrandLine, mine bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO errand_lines (errand, sender, text, at) VALUES (?, ?, ?, ?)`, id, l.Sender, l.Text, l.At.UTC()); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if mine {
		if _, err := tx.ExecContext(ctx, `UPDATE errands SET sent = sent + 1 WHERE id = ?`, id); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// ErrandLines is errand id's conversation, oldest first.
func (s *Store) ErrandLines(ctx context.Context, id string) ([]ErrandLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sender, text, at FROM errand_lines WHERE errand = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrandLine
	for rows.Next() {
		var l ErrandLine
		if err := rows.Scan(&l.Sender, &l.Text, &l.At); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ErrandContactOf is the contact whose number, or LID, is id.
func (s *Store) ErrandContactOf(ctx context.Context, id string) (ErrandContact, error) {
	var c ErrandContact
	err := s.db.QueryRowContext(ctx, `SELECT contact, lid, name, brain, state, reason, updated_at FROM errand_contacts WHERE contact = ? OR (lid != '' AND lid = ?) LIMIT 1`, id, id).
		Scan(&c.Contact, &c.LID, &c.Name, &c.Brain, &c.State, &c.Reason, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNoErrand
	}
	return c, err
}

// SaveErrandContact writes c, a new contact or one the store has.
func (s *Store) SaveErrandContact(ctx context.Context, c ErrandContact) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO errand_contacts (contact, lid, name, brain, state, reason, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (contact) DO UPDATE SET lid = excluded.lid, name = excluded.name, brain = excluded.brain, state = excluded.state, reason = excluded.reason, updated_at = excluded.updated_at`,
		c.Contact, c.LID, c.Name, c.Brain, c.State, c.Reason, c.UpdatedAt.UTC())
	return err
}

// ForgetErrandContact forgets contact: Fridolin asks the owner before it
// writes to them again, and does not listen to them meanwhile. It
// reports whether there was such a contact.
func (s *Store) ForgetErrandContact(ctx context.Context, contact string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM errand_contacts WHERE contact = ?`, contact)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
