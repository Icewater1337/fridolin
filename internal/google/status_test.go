package google

import (
	"encoding/json"
	"testing"
)

func TestCalendarSnapshotPreservesOptionalMapFields(t *testing.T) {
	empty := ""
	no := false
	for _, full := range []bool{false, true} {
		v := CalendarSnapshot{Calendars: []CalendarEntry{}, Selected: []string{}, LeftoverClientCopies: []string{}}
		old := map[string]any{"calendars": v.Calendars, "client_configured": false, "connected": false, "leftover_client_copies": v.LeftoverClientCopies, "selected": v.Selected, "token_path": ""}
		if full {
			v.Account = &empty
			v.Default = &empty
			v.BroadAccess = true
			v.LastError = "failure"
			v.ListError = &empty
			v.ReconnectNeeded = &no
			old["account"] = ""
			old["default"] = ""
			old["broad_access"] = true
			old["last_error"] = "failure"
			old["list_error"] = ""
			old["reconnect_needed"] = false
		}
		got, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("full=%v\ngot %s\nwant %s", full, got, want)
		}
	}
}
