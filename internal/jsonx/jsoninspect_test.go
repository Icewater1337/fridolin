package jsonx

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

type inspectedLeaf struct {
	Name string `json:"name"`
}

type inspectedCustom string

func (*inspectedCustom) UnmarshalJSON([]byte) error { return nil }

func TestUnknownKeysKeepsRecursivePathsAndJSONMatching(t *testing.T) {
	type value struct {
		Leaf   *inspectedLeaf           `json:"leaf"`
		List   []inspectedLeaf          `json:"list"`
		Array  [1]inspectedLeaf         `json:"array"`
		Map    map[string]inspectedLeaf `json:"map"`
		Custom inspectedCustom          `json:"custom"`
		Raw    json.RawMessage          `json:"raw"`
		Open   map[string]any           `json:"open"`
		Hidden string                   `json:"-"`
		Plain  string
		secret string
	}
	_ = value{}.secret // The reflection fixture deliberately includes an unexported field.
	data := []byte(`{
		"z": 1, "leaf": {"NAME": "ok", "nmae": "bad"},
		"list": [{"name": "ok"}, {"extra": true}],
		"array": [{"oops": 1}], "map": {"z": {"oops": 2}, "a": {"nope": 3}},
		"custom": {"anything": true}, "raw": {"anything": true},
		"open": {"anything": true}, "Plain": "ok", "Hidden": "bad", "secret": "bad"
	}`)
	want := []string{"Hidden", "array[0].oops", "leaf.nmae", "list[1].extra", "map.a.nope", "map.z.oops", "secret", "z"}
	if got := UnknownKeys(data, reflect.TypeFor[*value](), ""); !slices.Equal(got, want) {
		t.Fatalf("unknown keys = %v, want %v", got, want)
	}
	if got := UnknownKeys([]byte(`{"extra": 1}`), reflect.TypeFor[inspectedLeaf](), "approved"); !slices.Equal(got, []string{"approved.extra"}) {
		t.Fatalf("prefixed keys = %v", got)
	}
}

func TestUnknownKeysLeavesInvalidValuesToTheDecoder(t *testing.T) {
	for _, data := range []string{"{", "null", "42", "[]"} {
		if got := UnknownKeys([]byte(data), reflect.TypeFor[inspectedLeaf](), ""); len(got) != 0 {
			t.Errorf("%q: unknown keys = %v", data, got)
		}
	}
}
