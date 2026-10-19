package config

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestJSONErrorKeepsPublishedTypeNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{`"wrong"`, "json: cannot unmarshal string into Go value of type main.Config"},
		{`{"sandbox":"wrong"}`, "json: cannot unmarshal string into Go struct field Config.sandbox of type main.SandboxConfig"},
		{`{"brain_dirs":"wrong"}`, "json: cannot unmarshal string into Go struct field Config.brain_dirs of type []main.BrainRef"},
		{`{"max_chunk_size":"wrong"}`, "json: cannot unmarshal string into Go struct field Config.max_chunk_size of type int"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var value File
			err := json.Unmarshal([]byte(tc.input), &value)
			got := JSONError(err)
			if got == nil || got.Error() != tc.want {
				t.Fatalf("JSONError = %v, want %q", got, tc.want)
			}
			var original, preserved *json.UnmarshalTypeError
			if !errors.As(err, &original) || !errors.As(got, &preserved) || original != preserved {
				t.Fatal("JSONError lost the original typed error")
			}
		})
	}
	if JSONError(nil) != nil {
		t.Fatal("a nil error became non-nil")
	}
	var value File
	err := json.Unmarshal([]byte(`{`), &value)
	if JSONError(err) != err {
		t.Fatal("an unrelated JSON error changed")
	}
}
