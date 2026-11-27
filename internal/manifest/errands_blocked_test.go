package manifest

import (
	"testing"
	"time"
)

// TestErrandBlockedDefaultsAndValidation: no in_person_blocked means
// Monday to Friday, 08:00 to 17:00; a bad one is refused.
func TestErrandBlockedDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	days, from, to := ErrandCalendar{Mode: "offer"}.Blocked()
	if len(days) != 5 || days[0] != time.Monday || from != 8*60 || to != 17*60 {
		t.Errorf("defaults: %v %d %d", days, from, to)
	}
	_, from, to = ErrandCalendar{InPersonBlocked: &ErrandBlocked{From: "09:00", To: "12:30"}}.Blocked()
	if from != 9*60 || to != 12*60+30 {
		t.Errorf("set: %d %d", from, to)
	}
	for _, b := range []ErrandBlocked{{From: "9"}, {Days: []string{"x"}}, {From: "17:00", To: "08:00"}} {
		if err := validateErrandCalendar(ErrandCalendar{Mode: "offer", InPersonBlocked: &b}); err == nil {
			t.Errorf("%+v was accepted", b)
		}
	}
}
