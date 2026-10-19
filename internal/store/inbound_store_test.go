package store

import (
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
