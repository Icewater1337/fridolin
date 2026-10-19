package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Expected strings were captured from request/config shapes before extraction.
func TestManifestJSONDiagnosticContracts(t *testing.T) {
	for _, tc := range []struct{ input, request, approved string }{
		{"[]", "json: cannot unmarshal array into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest"},
		{"17", "json: cannot unmarshal number into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest"},
		{"\"x\"", "json: cannot unmarshal string into Go struct field .manifest of type main.BrainManifest", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved of type main.BrainManifest"},
		{"{\"id\":17}", "json: cannot unmarshal number into Go struct field .manifest.id of type string", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.id of type string"},
		{"{\"routing\":false}", "json: cannot unmarshal bool into Go struct field .manifest.routing of type main.BrainRouting", "json: cannot unmarshal bool into Go struct field Config.brain_dirs.0.approved.routing of type main.BrainRouting"},
		{"{\"routing\":{\"handles_dms\":\"yes\"}}", "json: cannot unmarshal string into Go struct field .manifest.routing.handles_dms of type bool", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved.routing.handles_dms of type bool"},
		{"{\"backup\":17}", "json: cannot unmarshal number into Go struct field .manifest.backup of type main.BrainBackup", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.backup of type main.BrainBackup"},
		{"{\"backup\":{\"enabled\":[]}}", "json: cannot unmarshal array into Go struct field .manifest.backup.enabled of type bool", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved.backup.enabled of type bool"},
		{"{\"calendar\":\"x\"}", "json: cannot unmarshal string into Go struct field .manifest.calendar of type main.BrainCalendar", "json: cannot unmarshal string into Go struct field Config.brain_dirs.0.approved.calendar of type main.BrainCalendar"},
		{"{\"calendar\":{\"calendars\":42}}", "json: cannot unmarshal number into Go struct field .manifest.calendar.calendars of type []string", "json: cannot unmarshal number into Go struct field Config.brain_dirs.0.approved.calendar.calendars of type []string"},
		{"{\"model\":[]}", "json: cannot unmarshal array into Go struct field .manifest.model of type string", "json: cannot unmarshal array into Go struct field Config.brain_dirs.0.approved.model of type string"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			body := `{"manifest":` + tc.input + `}`
			bridge := &Bridge{}
			for _, endpoint := range []struct {
				name    string
				handler http.HandlerFunc
			}{{"update", bridge.apiUpdateBrain}, {"approve", bridge.apiApproveBrain}} {
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
			_, err := decodeConfigReplacement([]byte(`{"brain_dirs":[{"path":"/x","approved":` + tc.input + `}]}`))
			if err == nil || err.Error() != "invalid config: "+tc.approved {
				t.Errorf("config: %v; want %q", err, "invalid config: "+tc.approved)
			}
		})
	}
}
