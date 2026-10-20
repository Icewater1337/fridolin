package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Decoding happens before deletion: a visitor that panics must leave the
// queue available to the next start, as the original combined codec did.
func TestTakeInboundKeepsRowsWhenVisitorPanics(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	if err := s.KeepInbound(t.Context(), InboundRow{Kind: "held", Chat: "chat", Body: "opaque"}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if got := recover(); got != "decode stopped" {
				t.Errorf("visitor panic = %v", got)
			}
		}()
		_ = s.TakeInbound(t.Context(), func(InboundRow) { panic("decode stopped") })
	}()
	var rows []InboundRow
	if err := s.TakeInbound(t.Context(), func(row InboundRow) { rows = append(rows, row) }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Body != "opaque" {
		t.Fatalf("rows after rolled-back visit = %+v", rows)
	}
	if err := s.TakeInbound(t.Context(), func(row InboundRow) { t.Errorf("row survived successful take: %+v", row) }); err != nil {
		t.Fatal(err)
	}
}

// A concurrent WAL writer must not invalidate the snapshot between reading
// the queue and deleting it. Reserve the writer before invoking the visitor.
func TestTakeInboundReservesWriterBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, path)
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepInbound(t.Context(), InboundRow{Kind: "held", Chat: "chat", Body: "first"}); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	insert := func() error {
		_, err := other.Exec(`INSERT INTO inbound_queue (kind, chat_jid, body, sent_at, created_at) VALUES ('held', 'chat', 'later', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
		return err
	}
	var visited int
	err = s.TakeInbound(t.Context(), func(row InboundRow) {
		visited++
		if row.Body != "first" {
			t.Errorf("unexpected row: %+v", row)
		}
		err := insert()
		if !isBusy(err) {
			t.Errorf("competing writer = %v, want SQLITE_BUSY", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != 1 {
		t.Fatalf("visited %d rows, want 1", visited)
	}
	if err := insert(); err != nil {
		t.Fatalf("writer after replay: %v", err)
	}
	var rows []InboundRow
	if err := s.TakeInbound(t.Context(), func(row InboundRow) { rows = append(rows, row) }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Body != "later" {
		t.Fatalf("subsequent queue = %+v", rows)
	}
}

// isBusy reports whether err is SQLite's "database is locked".
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// bridge.db files written before the switch to the pure-Go driver hold times
// in the cgo driver's text format. They must read back as the same instant,
// and new rows must be written in that format so the two sort together.
func TestTimesKeepTheCgoDriversFormat(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "bridge.db"))
	if _, err := s.db.Exec(`INSERT INTO inbound_queue (kind, chat_jid, body, sent_at, created_at)
		VALUES ('held', 'old', 'before', '2026-10-20 09:15:00.123456789+00:00', '2026-10-20 09:15:00+00:00')`); err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2026, 10, 20, 9, 16, 0, 5000, time.UTC)
	if err := s.KeepInbound(t.Context(), InboundRow{Kind: "held", Chat: "new", Body: "after", SentAt: sent}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT CAST(sent_at AS TEXT) FROM inbound_queue WHERE chat_jid = 'new'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if want := "2026-10-20 09:16:00.000005+00:00"; raw != want {
		t.Errorf("sent_at written as %q, want %q", raw, want)
	}
	got := map[string]time.Time{}
	if err := s.TakeInbound(t.Context(), func(row InboundRow) { got[row.Chat] = row.SentAt }); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 20, 9, 15, 0, 123456789, time.UTC); !got["old"].Equal(want) {
		t.Errorf("old row's sent_at = %v, want %v", got["old"], want)
	}
	if !got["new"].Equal(sent) {
		t.Errorf("new row's sent_at = %v, want %v", got["new"], sent)
	}
}
