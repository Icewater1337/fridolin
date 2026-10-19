package manifest

import (
	"encoding/json"
	"errors"
	"testing"
)

// Expected strings preserve the pre-extraction names and each supported
// decoder's field context (baseline 006287f8).
func TestDecodeKeepsLegacyJSONDiagnostics(t *testing.T) {
	t.Parallel()
	innerContext := jsonUsesInnerStructContext(t)
	for _, tc := range []struct{ input, want, innerWant string }{
		{"[]", "json: cannot unmarshal array into Go value of type main.BrainManifest", ""},
		{"17", "json: cannot unmarshal number into Go value of type main.BrainManifest", ""},
		{"\"x\"", "json: cannot unmarshal string into Go value of type main.BrainManifest", ""},
		{"{\"id\":17}", "json: cannot unmarshal number into Go struct field BrainManifest.id of type string", ""},
		{"{\"routing\":false}", "json: cannot unmarshal bool into Go struct field BrainManifest.routing of type main.BrainRouting", ""},
		{"{\"routing\":{\"handles_dms\":\"yes\"}}", "json: cannot unmarshal string into Go struct field BrainManifest.routing.handles_dms of type bool", "json: cannot unmarshal string into Go struct field BrainRouting.routing.handles_dms of type bool"},
		{"{\"backup\":17}", "json: cannot unmarshal number into Go struct field BrainManifest.backup of type main.BrainBackup", ""},
		{"{\"backup\":{\"enabled\":[]}}", "json: cannot unmarshal array into Go struct field BrainManifest.backup.enabled of type bool", "json: cannot unmarshal array into Go struct field BrainBackup.backup.enabled of type bool"},
		{"{\"calendar\":\"x\"}", "json: cannot unmarshal string into Go struct field BrainManifest.calendar of type main.BrainCalendar", ""},
		{"{\"calendar\":{\"calendars\":42}}", "json: cannot unmarshal number into Go struct field BrainManifest.calendar.calendars of type []string", "json: cannot unmarshal number into Go struct field BrainCalendar.calendar.calendars of type []string"},
		{"{\"model\":[]}", "json: cannot unmarshal array into Go struct field BrainManifest.model of type string", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if innerContext && tc.innerWant != "" {
				tc.want = tc.innerWant
			}
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

// Go 1.26 reports the innermost struct in nested JSON type errors; Go 1.27
// reports the outer struct and includes slice indexes. Probe the decoder so
// both supported versions retain their own pre-extraction diagnostics.
func jsonUsesInnerStructContext(t *testing.T) bool {
	t.Helper()
	type inner struct {
		Value bool `json:"value"`
	}
	type outer struct {
		Inner inner `json:"inner"`
	}
	var v outer
	var typed *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"inner":{"value":"wrong"}}`), &v); !errors.As(err, &typed) {
		t.Fatalf("decoder context probe: %v", err)
	}
	switch typed.Struct {
	case "inner":
		return true
	case "outer":
		return false
	default:
		t.Fatalf("unexpected decoder struct context %q", typed.Struct)
		return false
	}
}
