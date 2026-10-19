package app

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStoredChangeDiagnosticSurvivesPackageMove(t *testing.T) {
	var out storedChange
	err := decodeStoredChange([]byte(`[]`), &out)
	if err == nil || err.Error() != "json: cannot unmarshal array into Go value of type main.storedChange" {
		t.Fatalf("diagnostic: %v", err)
	}
	var typed *json.UnmarshalTypeError
	if !errors.As(err, &typed) {
		t.Fatalf("lost typed cause: %v", err)
	}
}
