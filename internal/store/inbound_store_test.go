package store

import (
	"database/sql"
	"path/filepath"
	"testing"
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
	other, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=0")
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
		if err == nil || err.Error() != "database is locked" {
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
