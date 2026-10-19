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

func TestRenameTypeErrorIncludesAnonymousAggregate(t *testing.T) {
	t.Parallel()
	var value struct {
		Items []renamedFixture `json:"items"`
	}
	original := json.Unmarshal([]byte(`false`), &value)
	wrapped := fmt.Errorf("reading jsonx.renamedFixture: %w", original)
	name := func(typ reflect.Type) string {
		if typ == reflect.TypeOf(value) {
			return "struct { Items []main.OldFixture \"json:\\\"items\\\"\" }"
		}
		return ""
	}
	got := RenameTypeError(wrapped, name, nil)
	want := "reading jsonx.renamedFixture: json: cannot unmarshal bool into Go value of type struct { Items []main.OldFixture \"json:\\\"items\\\"\" }"
	if got.Error() != want {
		t.Fatalf("diagnostic = %q, want %q", got.Error(), want)
	}
	var before, after *json.UnmarshalTypeError
	if !errors.As(original, &before) || !errors.As(got, &after) || before != after || !errors.Is(got, wrapped) {
		t.Fatal("original error identity or metadata lost")
	}
	if before.Type != reflect.TypeOf(value) {
		t.Fatal("the original decoder error was modified")
	}
	if RenameTypeError(wrapped, func(reflect.Type) string { return "" }, nil) != wrapped {
		t.Fatal("an unchanged anonymous type caused a wrapper")
	}
}

func TestRenameTypeErrorComposesAcrossPackageBoundaries(t *testing.T) {
	t.Parallel()
	var value struct{ Item renamedFixture }
	original := json.Unmarshal([]byte(`{"Item":false}`), &value)
	var typed *json.UnmarshalTypeError
	if !errors.As(original, &typed) {
		t.Fatal(original)
	}
	typed.Struct = "File"
	for _, reverse := range []bool{false, true} {
		renameStruct := func(err error) error {
			return RenameTypeError(err, nil, func(s string) string {
				if s == "File" {
					return "Config"
				}
				return ""
			})
		}
		renameType := func(err error) error {
			return RenameTypeError(err, func(typ reflect.Type) string {
				if typ == reflect.TypeFor[renamedFixture]() {
					return "main.OldFixture"
				}
				return ""
			}, nil)
		}
		first, second := renameStruct, renameType
		if reverse {
			first, second = second, first
		}
		got := second(fmt.Errorf("invalid config: %w", first(original)))
		want := "invalid config: json: cannot unmarshal bool into Go struct field Config.Item of type main.OldFixture"
		if got.Error() != want {
			t.Fatalf("reverse=%v: %v, want %s", reverse, got, want)
		}
		var preserved *json.UnmarshalTypeError
		if !errors.As(got, &preserved) || preserved != typed || typed.Struct != "File" {
			t.Fatal("lost or modified original error")
		}
	}
}
