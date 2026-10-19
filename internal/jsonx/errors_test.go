package jsonx

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type renamedFixture struct{ Count int }

func TestRenameTypeErrorPreservesCauseAndContext(t *testing.T) {
	t.Parallel()
	var value renamedFixture
	original := json.Unmarshal([]byte(`{"Count":"wrong"}`), &value)
	wrapped := fmt.Errorf("input jsonx.renamedFixture: %w", original)
	got := RenameTypeError(wrapped, nil, func(name string) string {
		if name == "renamedFixture" {
			return "OldFixture"
		}
		return ""
	})
	want := "input jsonx.renamedFixture: json: cannot unmarshal string into Go struct field OldFixture.Count of type int"
	if got.Error() != want {
		t.Fatalf("diagnostic = %q, want %q", got.Error(), want)
	}
	var before, after *json.UnmarshalTypeError
	if !errors.As(original, &before) || !errors.As(got, &after) || before != after || !errors.Is(got, wrapped) {
		t.Fatal("original error identity or metadata lost")
	}
	if before.Struct != "renamedFixture" {
		t.Fatal("the original decoder error was modified")
	}
	for _, err := range []error{nil, errors.New("jsonx.renamedFixture in an unrelated diagnostic"), fmt.Errorf("%w (extra context)", original)} {
		if RenameTypeError(err, nil, func(string) string { return "OldFixture" }) != err {
			t.Fatalf("unrelated error changed: %v", err)
		}
	}
}

func TestRenameTypeErrorIncludesContainerTypes(t *testing.T) {
	t.Parallel()
	var value map[string][]*[2]renamedFixture
	err := json.Unmarshal([]byte(`false`), &value)
	name := func(t reflect.Type) string {
		if t == reflect.TypeFor[renamedFixture]() {
			return "main.OldFixture"
		}
		return ""
	}
	got := RenameTypeError(err, name, nil)
	want := "json: cannot unmarshal bool into Go value of type map[string][]*[2]main.OldFixture"
	if got == nil || got.Error() != want {
		t.Fatalf("diagnostic = %v, want %q", got, want)
	}
	if RenameTypeError(err, func(reflect.Type) string { return "" }, nil) != err {
		t.Fatal("an unchanged type caused a wrapper")
	}
}
