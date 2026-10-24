package app

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStoredChangeDiagnosticSurvivesPackageMove(t *testing.T) {
	t.Parallel()
	var out storedChange
	err := decodeStoredChange([]byte(`[]`), &out)
	if err == nil || err.Error() != "json: cannot unmarshal array into Go value of type main.storedChange" {
		t.Fatalf("diagnostic: %v", err)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Fatalf("lost typed cause: %v", err)
	}
}
