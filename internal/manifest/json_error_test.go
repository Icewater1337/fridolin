package manifest

import (
	"encoding/json"
	"errors"
	"testing"
)

// Expected strings were captured before extraction at 006287f8.
func TestDecodeKeepsLegacyJSONDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"[]", "json: cannot unmarshal array into Go value of type main.BrainManifest"},
		{"17", "json: cannot unmarshal number into Go value of type main.BrainManifest"},
		{"\"x\"", "json: cannot unmarshal string into Go value of type main.BrainManifest"},
		{"{\"id\":17}", "json: cannot unmarshal number into Go struct field BrainManifest.id of type string"},
		{"{\"routing\":false}", "json: cannot unmarshal bool into Go struct field BrainManifest.routing of type main.BrainRouting"},
		{"{\"routing\":{\"handles_dms\":\"yes\"}}", "json: cannot unmarshal string into Go struct field BrainManifest.routing.handles_dms of type bool"},
		{"{\"backup\":17}", "json: cannot unmarshal number into Go struct field BrainManifest.backup of type main.BrainBackup"},
		{"{\"backup\":{\"enabled\":[]}}", "json: cannot unmarshal array into Go struct field BrainManifest.backup.enabled of type bool"},
		{"{\"calendar\":\"x\"}", "json: cannot unmarshal string into Go struct field BrainManifest.calendar of type main.BrainCalendar"},
		{"{\"calendar\":{\"calendars\":42}}", "json: cannot unmarshal number into Go struct field BrainManifest.calendar.calendars of type []string"},
		{"{\"model\":[]}", "json: cannot unmarshal array into Go struct field BrainManifest.model of type string"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			_, err := Decode([]byte(tc.input))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Decode = %v; want %q", err, tc.want)
			}
			var raw Manifest
			original := json.Unmarshal([]byte(tc.input), &raw)
			var before, after *json.UnmarshalTypeError
			if !errors.As(original, &before) || !errors.As(err, &after) || before.Offset != after.Offset || before.Struct != after.Struct || before.Field != after.Field || before.Type != after.Type {
				t.Fatal("decoder metadata changed")
			}
		})
	}
}
