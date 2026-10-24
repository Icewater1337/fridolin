package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
)

// Expected strings preserve request/config shapes before extraction on both
// supported decoder contexts. Production formatting never changes the context.
func TestManifestJSONDiagnosticContracts(t *testing.T) {
	t.Parallel()
	innerContext := jsonUsesInnerStructContext(t)
	for _, tc := range []struct{ input, request, approved, innerRequest, innerApproved string }{
		{"[]", "json: cannot unmarshal array into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest", "", "json: cannot unmarshal array into Go struct field BrainRef.brain_dirs.approved of type main.BrainManifest"},
		{"17", "json: cannot unmarshal number into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest", "", "json: cannot unmarshal number into Go struct field BrainRef.brain_dirs.approved of type main.BrainManifest"},
		{"\"x\"", "json: cannot unmarshal string into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest", "", "json: cannot unmarshal string into Go struct field BrainRef.brain_dirs.approved of type main.BrainManifest"},
		{"{\"id\":17}", "json: cannot unmarshal number into Go struct field .manifest.id of type string", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.id of type string", "json: cannot unmarshal number into Go struct field BrainManifest.manifest.id of type string", "json: cannot unmarshal number into Go struct field BrainManifest.brain_dirs.approved.id of type string"},
		{"{\"routing\":false}", "json: cannot unmarshal bool into Go struct field .manifest.routing of type main.BrainRouting", "json: cannot unmarshal bool into Go struct field Config.brain_dirs.0.approved.routing of type main.BrainRouting", "json: cannot unmarshal bool into Go struct field BrainManifest.manifest.routing of type main.BrainRouting", "json: cannot unmarshal bool into Go struct field BrainManifest.brain_dirs.approved.routing of type main.BrainRouting"},
		{"{\"routing\":{\"handles_dms\":\"yes\"}}", "json: cannot unmarshal string into Go struct field .manifest.routing.handles_dms of type bool", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved.routing.handles_dms of type bool", "json: cannot unmarshal string into Go struct field BrainRouting.manifest.routing.handles_dms of type bool", "json: cannot unmarshal string into Go struct field BrainRouting.brain_dirs.approved.routing.handles_dms of type bool"},
		{"{\"backup\":17}", "json: cannot unmarshal number into Go struct field .manifest.backup of type main.BrainBackup", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.backup of type main.BrainBackup", "json: cannot unmarshal number into Go struct field BrainManifest.manifest.backup of type main.BrainBackup", "json: cannot unmarshal number into Go struct field BrainManifest.brain_dirs.approved.backup of type main.BrainBackup"},
		{"{\"backup\":{\"enabled\":[]}}", "json: cannot unmarshal array into Go struct field .manifest.backup.enabled of type bool", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved.backup.enabled of type bool", "json: cannot unmarshal array into Go struct field BrainBackup.manifest.backup.enabled of type bool", "json: cannot unmarshal array into Go struct field BrainBackup.brain_dirs.approved.backup.enabled of type bool"},
		{"{\"calendar\":\"x\"}", "json: cannot unmarshal string into Go struct field .manifest.calendar of type main.BrainCalendar", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved.calendar of type main.BrainCalendar", "json: cannot unmarshal string into Go struct field BrainManifest.manifest.calendar of type main.BrainCalendar", "json: cannot unmarshal string into Go struct field BrainManifest.brain_dirs.approved.calendar of type main.BrainCalendar"},
		{"{\"calendar\":{\"calendars\":42}}", "json: cannot unmarshal number into Go struct field .manifest.calendar.calendars of type []string", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.calendar.calendars of type []string", "json: cannot unmarshal number into Go struct field BrainCalendar.manifest.calendar.calendars of type []string", "json: cannot unmarshal number into Go struct field BrainCalendar.brain_dirs.approved.calendar.calendars of type []string"},
		{"{\"model\":[]}", "json: cannot unmarshal array into Go struct field .manifest.model of type string", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved.model of type string", "json: cannot unmarshal array into Go struct field BrainManifest.manifest.model of type string", "json: cannot unmarshal array into Go struct field BrainManifest.brain_dirs.approved.model of type string"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if innerContext {
				if tc.innerRequest != "" {
					tc.request = tc.innerRequest
				}
				if tc.innerApproved != "" {
					tc.approved = tc.innerApproved
				}
			}
			body := `{"manifest":` + tc.input + `}`
			bridge := &App{}
			for _, endpoint := range []struct {
				name    string
				handler http.HandlerFunc
			}{{"update", bridge.newBrainsAPI().Update}, {"approve", bridge.newBrainsAPI().Approve}} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				endpoint.handler(rec, req)
				var response struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusBadRequest || response.Error != "invalid JSON: "+tc.request {
					t.Errorf("%s: %d %q; want %q", endpoint.name, rec.Code, response.Error, "invalid JSON: "+tc.request)
				}
			}
			_, err := config.DecodeReplacement([]byte(`{"brain_dirs":[{"path":"/x","approved":` + tc.input + `}]}`))
			if err == nil || err.Error() != "invalid config: "+tc.approved {
				t.Errorf("config: %v; want %q", err, "invalid config: "+tc.approved)
			}
		})
	}
}

// Add decodes a raw manifest after the outer request, so the diagnostic has
// the direct manifest type context captured before package extraction.
func TestAddBrainManifestJSONDiagnosticContracts(t *testing.T) {
	t.Parallel()
	innerContext := jsonUsesInnerStructContext(t)
	for _, tc := range []struct{ input, want, innerWant string }{
		{"[]", "manifest: invalid JSON: json: cannot unmarshal array into Go value of type main.BrainManifest", ""},
		{"17", "manifest: invalid JSON: json: cannot unmarshal number into Go value of type main.BrainManifest", ""},
		{"\"x\"", "manifest: invalid JSON: json: cannot unmarshal string into Go value of type main.BrainManifest", ""},
		{"{\"id\":17}", "manifest: invalid JSON: json: cannot unmarshal number into Go struct field BrainManifest.id of type string", ""},
		{"{\"routing\":false}", "manifest: invalid JSON: json: cannot unmarshal bool into Go struct field BrainManifest.routing of type main.BrainRouting", ""},
		{"{\"routing\":{\"handles_dms\":\"yes\"}}", "manifest: invalid JSON: json: cannot unmarshal string into Go struct field BrainManifest.routing.handles_dms of type bool", "manifest: invalid JSON: json: cannot unmarshal string into Go struct field BrainRouting.routing.handles_dms of type bool"},
		{"{\"backup\":17}", "manifest: invalid JSON: json: cannot unmarshal number into Go struct field BrainManifest.backup of type main.BrainBackup", ""},
		{"{\"backup\":{\"enabled\":[]}}", "manifest: invalid JSON: json: cannot unmarshal array into Go struct field BrainManifest.backup.enabled of type bool", "manifest: invalid JSON: json: cannot unmarshal array into Go struct field BrainBackup.backup.enabled of type bool"},
		{"{\"calendar\":\"x\"}", "manifest: invalid JSON: json: cannot unmarshal string into Go struct field BrainManifest.calendar of type main.BrainCalendar", ""},
		{"{\"calendar\":{\"calendars\":42}}", "manifest: invalid JSON: json: cannot unmarshal number into Go struct field BrainManifest.calendar.calendars of type []string", "manifest: invalid JSON: json: cannot unmarshal number into Go struct field BrainCalendar.calendar.calendars of type []string"},
		{"{\"model\":[]}", "manifest: invalid JSON: json: cannot unmarshal array into Go struct field BrainManifest.model of type string", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if innerContext && tc.innerWant != "" {
				tc.want = tc.innerWant
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"manifest":`+tc.input+`}`))
			(&App{}).newBrainsAPI().Add(rec, req)
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusBadRequest || response.Error != tc.want {
				t.Fatalf("add: %d %q; want %q", rec.Code, response.Error, tc.want)
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
