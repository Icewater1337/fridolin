package errand

import (
	"testing"
	"time"
)

func TestBlockedSpansAndOverlaps(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	mon := time.Date(2026, 11, 23, 7, 0, 0, 0, loc) // a Monday
	days := []time.Weekday{time.Monday, time.Tuesday}
	spans := BlockedSpans(mon, mon.AddDate(0, 0, 3), days, 8*60, 17*60, loc)
	if len(spans) != 2 || spans[0].Start.Hour() != 8 || spans[1].End.Day() != 24 {
		t.Fatalf("spans %v", spans)
	}
	at := func(h int) Span {
		return Span{Start: mon.Add(time.Duration(h-7) * time.Hour), End: mon.Add(time.Duration(h-6) * time.Hour)}
	}
	if !Overlaps(at(16), spans) || Overlaps(at(17), spans) || Overlaps(at(7), spans) {
		t.Error("overlap is wrong at the edges")
	}
}
