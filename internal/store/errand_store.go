package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

// migrateV10 adds how an errand reaches its person: by message, as
// before, or by a voice call (docs/CALLS.md).
func migrateV10(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE errands ADD COLUMN mode TEXT NOT NULL DEFAULT 'message'`)
	return err
}

// migrateV12 lets Fridolin talk with the people it knows (docs/BRAINS.md,
// "Conversations"): how it takes each one's messages and what it may
// always tell them, when it last warned them, and who began an errand.
func migrateV12(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE errand_contacts ADD COLUMN mode TEXT NOT NULL DEFAULT 'converse'`, // converse, forward
		`ALTER TABLE errand_contacts ADD COLUMN brief TEXT NOT NULL DEFAULT ''`,        // what Fridolin may always tell them
		`ALTER TABLE errand_contacts ADD COLUMN warned_at DATETIME`,                    // the last warning, which a second try within ErrandWarningKeep ends with a block
		`ALTER TABLE errands ADD COLUMN opened_by TEXT NOT NULL DEFAULT ''`,            // '' a brain, 'them' the person, who wrote first
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// How an errand reaches its person.
const (
	ErrandByMessage = "message"
	ErrandByCall    = "call"
)

// OpenedByThem is Errand.OpenedBy of a conversation the person began:
// they wrote while nothing was open with them.
const OpenedByThem = "them"

// How Fridolin takes the messages of a contact while nothing is open
// with them.
const (
	// ContactConverse talks with them: their message begins a
	// conversation of its own.
	ContactConverse = "converse"
	// ContactForward passes their message on to the owner.
	ContactForward = "forward"
)

// ErrandWarningKeep is how long a warning counts: a second try at making
// Fridolin misbehave within it ends the conversation and blocks them.
const ErrandWarningKeep = 30 * 24 * time.Hour

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
	// Mode is ErrandByMessage or ErrandByCall.
	Mode string
	// OpenedBy is OpenedByThem for a conversation the person began, ""
	// for an errand a brain asked for.
	OpenedBy string
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
	// Mode is ContactConverse or ContactForward.
	Mode string
	// Brief is what Fridolin may always tell them.
	Brief string
	// WarnedAt is when Fridolin last warned them, zero for never.
	WarnedAt time.Time
}

// Warned reports whether c was warned within ErrandWarningKeep of now.
func (c ErrandContact) Warned(now time.Time) bool {
	return !c.WarnedAt.IsZero() && now.Sub(c.WarnedAt) < ErrandWarningKeep
}

// ErrNoErrand is an errand, or a contact, the store does not have.
var ErrNoErrand = errors.New("no such errand")

const errandColumns = `id, brain, contact, name, goal, brief, message, state, approval, by_owner, run, chat_jid, days, sent, outcome, created_at, deadline, closed_at, mode, opened_by`

const contactColumns = `contact, lid, name, brain, state, reason, updated_at, mode, brief, warned_at`

func scanContact(row interface{ Scan(...any) error }) (ErrandContact, error) {
	var c ErrandContact
	var warned sql.NullTime
	err := row.Scan(&c.Contact, &c.LID, &c.Name, &c.Brain, &c.State, &c.Reason, &c.UpdatedAt, &c.Mode, &c.Brief, &warned)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNoErrand
	}
	c.WarnedAt = warned.Time
	return c, err
}

func (s *Store) queryContacts(ctx context.Context, q string, args ...any) ([]ErrandContact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactColumns+` FROM errand_contacts `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrandContact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanErrand(row interface{ Scan(...any) error }) (Errand, error) {
	var e Errand
	var closed sql.NullTime
	err := row.Scan(&e.ID, &e.Brain, &e.Contact, &e.Name, &e.Goal, &e.Brief, &e.Message, &e.State, &e.Approval, &e.ByOwner, &e.Run, &e.Chat, &e.Days, &e.Sent, &e.Outcome, &e.CreatedAt, &e.Deadline, &closed, &e.Mode, &e.OpenedBy)
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
	mode := e.Mode
	if mode == "" {
		mode = ErrandByMessage
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO errands (`+errandColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		e.ID, e.Brain, e.Contact, e.Name, e.Goal, e.Brief, e.Message, e.State, e.Approval, e.ByOwner, e.Run, e.Chat, e.Days, e.Sent, e.Outcome, e.CreatedAt.UTC(), e.Deadline.UTC(), mode, e.OpenedBy)
	return err
}

// ErrandsOpenedByThemSince counts the conversations contact began since t.
func (s *Store) ErrandsOpenedByThemSince(ctx context.Context, contact string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM errands WHERE contact = ? AND opened_by = ? AND created_at >= ?`, contact, OpenedByThem, t.UTC()).Scan(&n)
	return n, err
}

// ErrandLinesFromSince counts the messages sender wrote in contact's
// errands since t.
func (s *Store) ErrandLinesFromSince(ctx context.Context, contact, sender string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM errand_lines l JOIN errands e ON e.id = l.errand WHERE e.contact = ? AND l.sender = ? AND l.at >= ?`, contact, sender, t.UTC()).Scan(&n)
	return n, err
}

// EarlierErrandLines is the latest limit lines of contact's errands other
// than id, oldest first.
func (s *Store) EarlierErrandLines(ctx context.Context, contact, id string, limit int) ([]ErrandLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.sender, l.text, l.at FROM errand_lines l JOIN errands e ON e.id = l.errand
		WHERE e.contact = ? AND e.id != ? ORDER BY l.id DESC LIMIT ?`, contact, id, limit)
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
	slices.Reverse(out)
	return out, rows.Err()
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

// ErrandCallsSince counts the errands by call made since t, except the one
// with id except: those still asking or declined made no call.
func (s *Store) ErrandCallsSince(ctx context.Context, t time.Time, except string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM errands WHERE mode = ? AND created_at >= ? AND state NOT IN (?, ?) AND id != ?`, ErrandByCall, t.UTC(), ErrandDeclined, ErrandAsking, except).Scan(&n)
	return n, err
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

// AddErrandLineWhileOpen adds one of Fridolin's messages to errand id's
// conversation in the one statement that checks the errand is still open,
// and counts it. It reports false, and adds nothing, once the errand ended;
// line is what TakeBackErrandLine takes.
func (s *Store) AddErrandLineWhileOpen(ctx context.Context, id string, l ErrandLine) (line int64, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE errands SET sent = sent + 1 WHERE id = ? AND state = ?`, id, ErrandOpen)
	if err != nil {
		return 0, false, errors.Join(err, tx.Rollback())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, false, tx.Rollback()
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO errand_lines (errand, sender, text, at) VALUES (?, ?, ?, ?)`, id, l.Sender, l.Text, l.At.UTC())
	if err != nil {
		return 0, false, errors.Join(err, tx.Rollback())
	}
	if line, err = res.LastInsertId(); err != nil {
		return 0, false, errors.Join(err, tx.Rollback())
	}
	return line, true, tx.Commit()
}

// TakeBackErrandLine undoes AddErrandLineWhileOpen for a message that could
// not be sent: the line goes, and no longer counts.
func (s *Store) TakeBackErrandLine(ctx context.Context, id string, line int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM errand_lines WHERE id = ? AND errand = ?`, line, id)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return tx.Rollback()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE errands SET sent = sent - 1 WHERE id = ?`, id); err != nil {
		return errors.Join(err, tx.Rollback())
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
	return scanContact(s.db.QueryRowContext(ctx, `SELECT `+contactColumns+` FROM errand_contacts WHERE contact = ? OR (lid != '' AND lid = ?) LIMIT 1`, id, id))
}

// SetErrandContactWays sets how Fridolin takes contact's messages (mode)
// and what it may always tell them (brief). It reports whether there was
// such a contact.
func (s *Store) SetErrandContactWays(ctx context.Context, contact, mode, brief string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE errand_contacts SET mode = ?, brief = ?, updated_at = ? WHERE contact = ?`, mode, brief, at.UTC(), contact)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// WarnErrandContact records that Fridolin warned contact at at.
func (s *Store) WarnErrandContact(ctx context.Context, contact string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE errand_contacts SET warned_at = ? WHERE contact = ?`, at.UTC(), contact)
	return err
}

// UnblockErrandContact lets Fridolin listen to contact again: someone it
// knows, whose messages go to the owner until the owner lets it talk
// with them, with no warning held against them. It reports whether
// contact was blocked.
func (s *Store) UnblockErrandContact(ctx context.Context, contact string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE errand_contacts SET state = ?, mode = ?, reason = '', warned_at = NULL, updated_at = ? WHERE contact = ? AND state = ?`,
		ContactKnown, ContactForward, at.UTC(), contact, ContactBlocked)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ErrandContacts lists everyone the errands know, blocked or not, the
// latest first.
func (s *Store) ErrandContacts(ctx context.Context) ([]ErrandContact, error) {
	return s.queryContacts(ctx, `ORDER BY updated_at DESC, contact`)
}

// SaveErrandContact writes c, a new contact or one the store has; a
// new one takes the messages of ContactConverse, and the mode, brief and
// warning of one the store has stay as they are.
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

// AllErrands lists every brain's errands, newest first: those that have
// not ended, then the last ended of the others, for the dashboard.
func (s *Store) AllErrands(ctx context.Context, ended int) ([]Errand, error) {
	// One statement, so that an errand that ends meanwhile is listed once.
	return s.queryErrands(ctx, `WHERE state IN (?, ?) OR id IN (SELECT id FROM errands WHERE state NOT IN (?, ?) ORDER BY created_at DESC LIMIT ?)
		ORDER BY state IN (?, ?) DESC, created_at DESC`, ErrandAsking, ErrandOpen, ErrandAsking, ErrandOpen, ended, ErrandAsking, ErrandOpen)
}

// BlockedErrandContacts lists the people the errands stopped listening
// to, the latest first.
func (s *Store) BlockedErrandContacts(ctx context.Context) ([]ErrandContact, error) {
	return s.queryContacts(ctx, `WHERE state = ? ORDER BY updated_at DESC, contact`, ContactBlocked)
}
