package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
)

// These diagnostics were captured from the original command-owned storedInbound
// decoder under Go 1.27 and Go 1.26 before moving its representation.
func TestSavedInboundDecoderDiagnostics(t *testing.T) {
	t.Parallel()
	cases := []struct{ input, want, old string }{
		{"[]", "json: cannot unmarshal array into Go value of type main.storedInbound", "json: cannot unmarshal array into Go value of type main.storedInbound"},
		{"17", "json: cannot unmarshal number into Go value of type main.storedInbound", "json: cannot unmarshal number into Go value of type main.storedInbound"},
		{"\"x\"", "json: cannot unmarshal string into Go value of type main.storedInbound", "json: cannot unmarshal string into Go value of type main.storedInbound"},
		{"{\"jid\":17}", "json: cannot unmarshal number into Go struct field storedInbound.jid of type string", "json: cannot unmarshal number into Go struct field storedInbound.jid of type string"},
		{"{\"parts\":false}", "json: cannot unmarshal bool into Go struct field storedInbound.parts of type []main.storedInbound", "json: cannot unmarshal bool into Go struct field storedInbound.parts of type []main.storedInbound"},
		{"{\"parts\":[17]}", "json: cannot unmarshal number into storedInbound.parts.0 of type main.storedInbound", "json: cannot unmarshal number into Go struct field storedInbound.parts of type main.storedInbound"},
		{"{\"parts\":[{\"parts\":42}]}", "json: cannot unmarshal number into Go struct field storedInbound.parts.0.parts of type []main.storedInbound", "json: cannot unmarshal number into Go struct field storedInbound.parts.parts of type []main.storedInbound"},
		{"{\"parts\":[{\"jid\":17}]}", "json: cannot unmarshal number into Go struct field storedInbound.parts.0.jid of type string", "json: cannot unmarshal number into Go struct field storedInbound.parts.jid of type string"},
		{"{\"logged\":false}", "json: cannot unmarshal bool into Go struct field storedInbound.logged of type history.Entry", "json: cannot unmarshal bool into Go struct field storedInbound.logged of type history.Entry"},
		{"{\"logged\":{\"text\":17}}", "json: cannot unmarshal number into Go struct field storedInbound.logged.text of type string", "json: cannot unmarshal number into Go struct field Entry.logged.text of type string"},
		{"{\"quoted\":false}", "json: cannot unmarshal bool into Go struct field storedInbound.quoted of type chat.Quote", "json: cannot unmarshal bool into Go struct field storedInbound.quoted of type chat.Quote"},
		{"{\"parts\":{\"jid\":\"x\"}}", "json: cannot unmarshal object into Go struct field storedInbound.parts of type []main.storedInbound", "json: cannot unmarshal object into Go struct field storedInbound.parts of type []main.storedInbound"},
		{"{\"parts\":[{\"media\":false}]}", "json: cannot unmarshal bool into Go struct field storedInbound.parts.0.media of type []string", "json: cannot unmarshal bool into Go struct field storedInbound.parts.media of type []string"},
	}
	inner := jsonUsesInnerStructContext(t)
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			want := tc.want
			if inner {
				want = tc.old
			}
			var out brain.SavedInbound
			err := DecodeSavedInbound([]byte(tc.input), &out)
			if err == nil || err.Error() != want {
				t.Fatalf("diagnostic=%v; want %q", err, want)
			}
			var rawOut brain.SavedInbound
			raw := json.Unmarshal([]byte(tc.input), &rawOut)
			var actual, original *json.UnmarshalTypeError
			if !errors.As(err, &actual) || !errors.As(raw, &original) {
				t.Fatalf("typed cause lost: %v", err)
			}
			if !reflect.DeepEqual(actual, original) {
				t.Fatalf("typed metadata changed: got %+v want %+v", actual, original)
			}
		})
	}
	var out brain.SavedInbound
	raw := json.Unmarshal([]byte(`{"jid":`), &out)
	got := DecodeSavedInbound([]byte(`{"jid":`), &out)
	var syntax *json.SyntaxError
	if !errors.As(got, &syntax) || got.Error() != raw.Error() {
		t.Fatalf("syntax diagnostic changed: %v / %v", got, raw)
	}
}
