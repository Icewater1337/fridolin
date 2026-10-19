package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The files and requests that other programs read and write have JSON
// Schemas in docs/schemas: config.json, brain.json, a line of a brain's
// conversation-history.jsonl, and the requests and responses of /send and
// /ingest. The tests here hold them to the Go types that read and write
// those files and to the fixtures in testdata, so that a schema cannot
// drift from what the bridge does.
//
// schemaSet validates with the part of JSON Schema 2020-12 the schemas use.
// It refuses a schema with any other keyword rather than skip it, so that
// a schema never says more than these tests check. It is stricter than
// the standard where Go is: an integer is a number without a fraction or
// exponent, as encoding/json reads one into an int, where the standard
// counts 1.0 and 1e3 too; and a pattern is Go's RE2, whose \s and \S know
// only ASCII spaces, where the standard's ECMA-262 counts NBSP and the
// other Unicode spaces too. So a standard validator takes {"version": 1.0},
// which the bridge refuses: what these tests say a schema refuses holds
// for this validator.

// schemaSet is the schemas of a folder, docs/schemas for the bridge's,
// loaded as they are referred to.
type schemaSet struct {
	t     testing.TB
	dir   string
	files map[string]any
}

// newSchemaSet is the schemas in docs/schemas.
func newSchemaSet(t testing.TB) *schemaSet {
	return &schemaSet{t: t, dir: filepath.Join(repoDir, "docs", "schemas"), files: map[string]any{}}
}

// file returns the schema in the file name.
func (s *schemaSet) file(name string) any {
	s.t.Helper()
	if doc, ok := s.files[name]; ok {
		return doc
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		s.t.Fatal(err)
	}
	doc, err := decodeJSONValue(data)
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	s.files[name] = doc
	return doc
}

// decodeJSONValue decodes data, one JSON value, with numbers kept as
// json.Number, so that 1 and 1.0 stay apart as they do for Go.
func decodeJSONValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("more than one JSON value")
	}
	return v, nil
}

// schemaAt is a schema and the file it is in, which a $ref in it is
// relative to.
type schemaAt struct {
	file   string
	schema any
}

// resolve returns the schema that ref, a $ref in file, names: "#/…" in the
// same file, "other.schema.json" or "other.schema.json#/…".
func (s *schemaSet) resolve(file, ref string) schemaAt {
	s.t.Helper()
	name, pointer, _ := strings.Cut(ref, "#")
	if name != "" {
		file = name
	}
	node := s.file(file)
	if pointer == "" {
		return schemaAt{file, node}
	}
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		var ok bool
		switch n := node.(type) {
		case map[string]any:
			node, ok = n[part]
		case []any:
			i, err := strconv.Atoi(part)
			if ok = err == nil && i >= 0 && i < len(n); ok {
				node = n[i]
			}
		default:
			s.t.Fatalf("%s: $ref %q goes through a value that is neither an object nor a list", file, ref)
		}
		if !ok {
			s.t.Fatalf("%s: $ref %q names nothing", file, ref)
		}
	}
	return schemaAt{file, node}
}

// Validate checks instance, a JSON value as decodeJSONValue decodes it,
// against the schema ref names (a file, or a file#pointer), and returns
// what is wrong with it, each with the JSON pointer of the part.
func (s *schemaSet) Validate(ref string, instance any) []string {
	s.t.Helper()
	file, _, _ := strings.Cut(ref, "#")
	return s.validate(s.resolve(file, ref), instance, "")
}

// ValidateJSON is Validate for JSON text.
func (s *schemaSet) ValidateJSON(ref string, data []byte) []string {
	s.t.Helper()
	v, err := decodeJSONValue(data)
	if err != nil {
		return []string{"not JSON: " + err.Error()}
	}
	return s.Validate(ref, v)
}

// schemaAnnotations are keywords that only describe.
var schemaAnnotations = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$defs": true,
	"title": true, "description": true, "default": true, "examples": true, "deprecated": true,
}

func (s *schemaSet) validate(at schemaAt, v any, path string) []string {
	s.t.Helper()
	switch sch := at.schema.(type) {
	case bool:
		if !sch {
			return []string{where(path) + "is not allowed here"}
		}
		return nil
	case map[string]any:
		var errs []string
		fail := func(format string, args ...any) {
			errs = append(errs, where(path)+fmt.Sprintf(format, args...))
		}
		for _, kw := range slices.Sorted(maps.Keys(sch)) {
			arg := sch[kw]
			switch kw {
			case "$ref":
				errs = append(errs, s.validate(s.resolve(at.file, arg.(string)), v, path)...)
			case "type":
				var types []string
				if one, ok := arg.(string); ok {
					types = []string{one}
				} else {
					for _, t := range arg.([]any) {
						types = append(types, t.(string))
					}
				}
				if !slices.ContainsFunc(types, func(t string) bool { return isJSONType(v, t) }) {
					fail("is %s, want %s", jsonTypeOf(v), strings.Join(types, " or "))
				}
			case "enum":
				if !slices.ContainsFunc(arg.([]any), func(e any) bool { return jsonEqual(e, v) }) {
					fail("is %s, want one of %s", compactJSON(v), compactJSON(arg))
				}
			case "const":
				if !jsonEqual(arg, v) {
					fail("is %s, want %s", compactJSON(v), compactJSON(arg))
				}
			case "properties":
				obj, ok := v.(map[string]any)
				if !ok {
					continue
				}
				for name, sub := range arg.(map[string]any) {
					if pv, ok := obj[name]; ok {
						errs = append(errs, s.validate(schemaAt{at.file, sub}, pv, path+"/"+name)...)
					}
				}
			case "additionalProperties":
				obj, ok := v.(map[string]any)
				if !ok {
					continue
				}
				props, _ := sch["properties"].(map[string]any)
				for _, name := range slices.Sorted(maps.Keys(obj)) {
					if _, known := props[name]; !known {
						errs = append(errs, s.validate(schemaAt{at.file, arg}, obj[name], path+"/"+name)...)
					}
				}
			case "propertyNames":
				obj, ok := v.(map[string]any)
				if !ok {
					continue
				}
				for _, name := range slices.Sorted(maps.Keys(obj)) {
					for _, e := range s.validate(schemaAt{at.file, arg}, name, path) {
						errs = append(errs, fmt.Sprintf("key %q: %s", name, e))
					}
				}
			case "required":
				obj, ok := v.(map[string]any)
				if !ok {
					continue
				}
				for _, name := range arg.([]any) {
					if _, ok := obj[name.(string)]; !ok {
						fail("has no %q", name)
					}
				}
			case "items":
				list, ok := v.([]any)
				if !ok {
					continue
				}
				for i, item := range list {
					errs = append(errs, s.validate(schemaAt{at.file, arg}, item, path+"/"+strconv.Itoa(i))...)
				}
			case "minItems", "maxItems":
				list, ok := v.([]any)
				if !ok {
					continue
				}
				if n := schemaInt(arg); kw == "minItems" && len(list) < n || kw == "maxItems" && len(list) > n {
					fail("has %d items, want %s %d", len(list), map[string]string{"minItems": "at least", "maxItems": "at most"}[kw], n)
				}
			case "minLength":
				if str, ok := v.(string); ok && len([]rune(str)) < schemaInt(arg) {
					fail("%q is shorter than %d", str, schemaInt(arg))
				}
			case "pattern":
				if str, ok := v.(string); ok {
					re, err := regexp.Compile(arg.(string))
					if err != nil {
						s.t.Fatalf("%s: pattern %q: %v", at.file, arg, err)
					}
					if !re.MatchString(str) {
						fail("%q does not match %s", str, arg)
					}
				}
			case "format":
				if arg != "date-time" {
					s.t.Fatalf("%s: format %q is not one these tests check", at.file, arg)
				}
				if str, ok := v.(string); ok {
					if _, err := time.Parse(time.RFC3339, str); err != nil {
						fail("%q is not an RFC 3339 time", str)
					}
				}
			case "minimum", "maximum":
				n, ok := v.(json.Number)
				if !ok {
					continue
				}
				got, _ := new(big.Rat).SetString(n.String())
				limit, _ := new(big.Rat).SetString(arg.(json.Number).String())
				if c := got.Cmp(limit); kw == "minimum" && c < 0 || kw == "maximum" && c > 0 {
					fail("is %s, want %s %s", n, map[string]string{"minimum": "at least", "maximum": "at most"}[kw], arg)
				}
			case "anyOf", "oneOf":
				var matched int
				var why []string
				for i, sub := range arg.([]any) {
					sub := s.validate(schemaAt{at.file, sub}, v, path)
					if len(sub) == 0 {
						matched++
					} else {
						why = append(why, fmt.Sprintf("[%d] %s", i, strings.Join(sub, "; ")))
					}
				}
				if matched == 0 {
					fail("matches none of %s: %s", kw, strings.Join(why, " | "))
				} else if kw == "oneOf" && matched > 1 {
					fail("matches %d of oneOf, want one", matched)
				}
			case "not":
				if len(s.validate(schemaAt{at.file, arg}, v, path)) == 0 {
					fail("matches what %s rules out: %s", kw, compactJSON(arg))
				}
			default:
				if !schemaAnnotations[kw] {
					s.t.Fatalf("%s: keyword %q is not one these tests check: add it to schemaSet, or leave it out", at.file, kw)
				}
			}
		}
		return errs
	}
	s.t.Fatalf("%s: a schema must be an object or a boolean, not %s", at.file, compactJSON(at.schema))
	return nil
}

func where(path string) string {
	if path == "" {
		return "the value "
	}
	return path + " "
}

func schemaInt(v any) int {
	n, err := v.(json.Number).Int64()
	if err != nil {
		panic(err)
	}
	return int(n)
}

// isJSONType reports whether v is of the JSON Schema type t. An integer is
// a number without a fraction or exponent, the numbers Go reads into an
// int.
func isJSONType(v any, t string) bool {
	switch t {
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(n.String(), 10, 64)
		return err == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	}
	return jsonTypeOf(v) == t
}

func jsonTypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func jsonEqual(a, b any) bool { return compactJSON(a) == compactJSON(b) }

func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// TestSchemaSet: the validator itself, on a schema with every keyword it
// knows.
func TestSchemaSet(t *testing.T) {
	dir := t.TempDir()
	schemas := map[string]string{
		"thing.schema.json": `{
			"type": "object",
			"additionalProperties": false,
			"required": ["name"],
			"properties": {
				"name": {"type": "string", "minLength": 1, "pattern": "^[a-z]+$"},
				"count": {"type": "integer", "minimum": 0, "maximum": 10},
				"kind": {"enum": ["a", "b"]},
				"fixed": {"const": true},
				"tags": {"type": ["array", "null"], "items": {"$ref": "#/$defs/tag"}, "minItems": 1, "maxItems": 2},
				"when": {"type": "string", "format": "date-time"},
				"other": {"$ref": "other.schema.json"},
				"either": {"oneOf": [{"type": "string"}, {"type": "integer"}]},
				"any": {"anyOf": [{"type": "string"}, {"type": "boolean"}]},
				"notx": {"not": {"const": "x"}},
				"byname": {"type": "object", "propertyNames": {"pattern": "^[a-z]+$"}, "additionalProperties": {"type": "integer"}}
			},
			"$defs": {"tag": {"type": "string"}}
		}`,
		"other.schema.json": `{"type": "object", "properties": {"x": {"type": "number"}}, "additionalProperties": {"type": "string"}}`,
	}
	for name, body := range schemas {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name, instance string
		want           []string // part of each error, keywords in sorted order; none for a valid instance
	}{
		{"valid", `{"name": "ab", "count": 3, "kind": "a", "fixed": true, "tags": ["x"], "when": "2026-01-02T10:00:00Z", "other": {"x": 1.5, "y": "z"}, "either": 2, "any": false, "notx": "y", "byname": {"a": 1}}`, nil},
		{"null where allowed", `{"name": "a", "tags": null}`, nil},
		{"not an object", `[]`, []string{"the value is array, want object"}},
		{"required", `{}`, []string{`has no "name"`}},
		{"unknown key", `{"name": "a", "nope": 1}`, []string{"/nope is not allowed here"}},
		{"wrong type", `{"name": 1}`, []string{"/name is number, want string"}},
		{"too short", `{"name": ""}`, []string{`/name "" is shorter than 1`, `/name "" does not match`}},
		{"pattern", `{"name": "A"}`, []string{`/name "A" does not match ^[a-z]+$`}},
		{"a fraction is no integer", `{"name": "a", "count": 1.0}`, []string{"/count is number, want integer"}},
		{"minimum", `{"name": "a", "count": -1}`, []string{"/count is -1, want at least 0"}},
		{"maximum", `{"name": "a", "count": 11}`, []string{"/count is 11, want at most 10"}},
		{"enum", `{"name": "a", "kind": "c"}`, []string{`/kind is "c", want one of ["a","b"]`}},
		{"const", `{"name": "a", "fixed": false}`, []string{"/fixed is false, want true"}},
		{"items through a $ref", `{"name": "a", "tags": [1]}`, []string{"/tags/0 is number, want string"}},
		{"minItems", `{"name": "a", "tags": []}`, []string{"/tags has 0 items, want at least 1"}},
		{"maxItems", `{"name": "a", "tags": ["x", "y", "z"]}`, []string{"/tags has 3 items, want at most 2"}},
		{"date-time", `{"name": "a", "when": "2026-01-02 10:00"}`, []string{`/when "2026-01-02 10:00" is not an RFC 3339 time`}},
		{"another file", `{"name": "a", "other": {"x": "1", "y": 2}}`, []string{"/other/y is number, want string", "/other/x is string, want number"}},
		{"oneOf none", `{"name": "a", "either": true}`, []string{"/either matches none of oneOf"}},
		{"anyOf none", `{"name": "a", "any": 1}`, []string{"/any matches none of anyOf"}},
		{"not", `{"name": "a", "notx": "x"}`, []string{`/notx matches what not rules out: {"const":"x"}`}},
		{"propertyNames", `{"name": "a", "byname": {"B": 1, "c": "2"}}`, []string{"/byname/c is string, want integer", `key "B": /byname "B" does not match ^[a-z]+$`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set := &schemaSet{t: t, dir: dir, files: map[string]any{}}
			got := set.ValidateJSON("thing.schema.json", []byte(tc.instance))
			if len(got) != len(tc.want) {
				t.Fatalf("errors %q, want %d: %q", got, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("error %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}
