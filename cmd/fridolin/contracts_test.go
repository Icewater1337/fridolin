package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/trigger"
)

// The schemas in docs/schemas are kept to the code in two ways. Each lists
// exactly the JSON keys of the Go type that reads its file or request,
// with a type for each that holds what Go writes there
// (TestSchemasFollowTheGoTypes). And the fixtures in testdata, what the
// bridge writes from them, and what it refuses, are checked against them
// (the tests below).

// schemaType is a schema and the Go type behind it.
type schemaType struct {
	schema string
	typ    reflect.Type
	// closed says the schema refuses keys it does not list, at every
	// level: the files the bridge writes. A request schema is open, as the
	// bridge ignores keys it does not know.
	closed bool
	// extra are keys the schema has besides the type's, such as the
	// retired keys of a version 1 config.json; refused are keys of the
	// type that the schema rules out rather than lists, such as the scope
	// /ingest refuses.
	extra, refused []string
}

// schemaTypes are the schemas of docs/schemas and their Go types.
var schemaTypes = []schemaType{
	{schema: "config.schema.json", typ: reflect.TypeFor[Config](), closed: true, extra: retiredKeyNames()},
	{schema: "brain.schema.json", typ: reflect.TypeFor[manifest.Manifest](), closed: true},
	{schema: "history-line.schema.json", typ: reflect.TypeFor[history.Entry](), closed: true},
	{schema: "send-request.schema.json", typ: reflect.TypeFor[sendRequest]()},
	{schema: "ingest-request.schema.json", typ: reflect.TypeFor[ingestRequest](), refused: []string{"scope"}},
}

func retiredKeyNames() []string {
	var names []string
	for _, rk := range retiredKeys {
		names = append(names, rk.name)
	}
	return names
}

// TestSchemasFollowTheGoTypes: every key of each Go type is in its schema,
// one level down too for sections and lists of them, and the schema has
// no key the type lacks; each key's schema takes every JSON type Go
// writes for the field (null for a pointer, slice or map without
// omitempty, which a nil one writes).
func TestSchemasFollowTheGoTypes(t *testing.T) {
	for _, st := range schemaTypes {
		t.Run(st.schema, func(t *testing.T) {
			set := newSchemaSet(t)
			for _, problem := range compareSchema(set, schemaAt{st.schema, set.file(st.schema)}, st.typ, "", st) {
				t.Error(problem)
			}
		})
	}
}

// compareSchema compares the object schema at with struct type typ, whose
// keys are at path, and returns what does not match.
func compareSchema(set *schemaSet, at schemaAt, typ reflect.Type, path string, st schemaType) []string {
	obj, ok := objectSchema(set, at)
	if !ok {
		return []string{fmt.Sprintf("%s: the schema of %s is no object schema", at.file, keyPath(path))}
	}
	sch := obj.schema.(map[string]any)
	props, _ := sch["properties"].(map[string]any)
	var problems []string
	if st.closed && sch["additionalProperties"] != false {
		problems = append(problems, fmt.Sprintf("%s: %s does not refuse keys it does not list (additionalProperties: false)", at.file, keyPath(path)))
	}
	fields := jsonFieldTags(typ)
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		f := fields[key]
		if path == "" && slices.Contains(st.refused, key) {
			if _, listed := props[key]; listed {
				problems = append(problems, fmt.Sprintf("%s: %s is refused, but the schema lists it", at.file, key))
			}
			continue
		}
		prop, ok := props[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s is not in the schema", at.file, keyPath(path+"."+key)))
			continue
		}
		pat := schemaAt{obj.file, prop}
		if writesOwnJSON(f.Type) {
			continue
		}
		for _, jt := range goJSONTypes(f.Type, f.omitempty) {
			if !schemaAdmits(set, pat, jt) {
				problems = append(problems, fmt.Sprintf("%s: %s takes no %s, which Go writes for its %s", at.file, keyPath(path+"."+key), jt, f.Type))
			}
		}
		sub := structOf(f.Type)
		if sub == nil {
			continue
		}
		if f.Type.Kind() == reflect.Slice || f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Slice {
			list, ok := objectSchema(set, pat)
			var items any
			if ok {
				items, ok = list.schema.(map[string]any)["items"]
			}
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: %s has no items", at.file, keyPath(path+"."+key)))
				continue
			}
			pat = schemaAt{list.file, items}
		}
		problems = append(problems, compareSchema(set, pat, sub, path+"."+key, st)...)
	}
	for _, key := range slices.Sorted(maps.Keys(props)) {
		if _, ok := fields[key]; !ok && !(path == "" && slices.Contains(st.extra, key)) {
			problems = append(problems, fmt.Sprintf("%s: %s is in the schema, but %s has no such key", at.file, keyPath(path+"."+key), typ))
		}
	}
	return problems
}

func keyPath(path string) string {
	if path == "" {
		return "the top level"
	}
	return strings.TrimPrefix(path, ".")
}

// fieldTag is a struct field as encoding/json writes it.
type fieldTag struct {
	reflect.Type
	omitempty bool
}

// jsonFieldTags is jsonFields with each field's omitempty.
func jsonFieldTags(typ reflect.Type) map[string]fieldTag {
	out := map[string]fieldTag{}
	for key, ft := range jsonFields(typ) {
		out[key] = fieldTag{Type: ft}
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		if ft, ok := out[name]; ok && ft.Type == f.Type {
			ft.omitempty = slices.Contains(strings.Split(opts, ","), "omitempty")
			out[name] = ft
		}
	}
	return out
}

// writesOwnJSON reports whether a value of typ is written and read by its
// own MarshalJSON and UnmarshalJSON, whose JSON the schema describes on
// its own.
func writesOwnJSON(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Unmarshaler]())
}

// goJSONTypes are the JSON types encoding/json writes for a field of typ.
func goJSONTypes(typ reflect.Type, omitempty bool) []string {
	var out []string
	nilable := false
	for typ.Kind() == reflect.Pointer {
		nilable = true
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.String:
		out = []string{"string"}
	case reflect.Bool:
		out = []string{"boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		out = []string{"integer"}
	case reflect.Float32, reflect.Float64:
		out = []string{"number"}
	case reflect.Slice, reflect.Array:
		nilable = nilable || typ.Kind() == reflect.Slice
		out = []string{"array"}
	case reflect.Map:
		nilable = true
		out = []string{"object"}
	case reflect.Struct:
		out = []string{"object"}
	default:
		panic("no JSON type for " + typ.String())
	}
	if nilable && !omitempty {
		out = append(out, "null")
	}
	return out
}

// objectSchema follows at through $ref and, in a oneOf or anyOf, to its
// one branch that is not null, to the schema of an object or a list.
func objectSchema(set *schemaSet, at schemaAt) (schemaAt, bool) {
	for range 10 {
		sch, ok := at.schema.(map[string]any)
		if !ok {
			return at, false
		}
		if ref, ok := sch["$ref"].(string); ok {
			at = set.resolve(at.file, ref)
			continue
		}
		if _, ok := sch["properties"]; ok {
			return at, true
		}
		if _, ok := sch["items"]; ok {
			return at, true
		}
		var branches []any
		for _, kw := range []string{"oneOf", "anyOf"} {
			if b, ok := sch[kw].([]any); ok {
				for _, branch := range b {
					if bs, ok := branch.(map[string]any); !ok || bs["type"] != "null" {
						branches = append(branches, branch)
					}
				}
			}
		}
		if len(branches) != 1 {
			return at, false
		}
		at = schemaAt{at.file, branches[0]}
	}
	return at, false
}

// schemaAdmits reports whether the schema at takes some value of JSON type
// jt, by its type, const or enum, through $ref, oneOf and anyOf. A schema
// that says nothing of the type takes every one.
func schemaAdmits(set *schemaSet, at schemaAt, jt string) bool {
	sch, ok := at.schema.(map[string]any)
	if !ok {
		return at.schema == true
	}
	if ref, ok := sch["$ref"].(string); ok && !schemaAdmits(set, set.resolve(at.file, ref), jt) {
		return false
	}
	matches := func(v any) bool { return jsonTypeOf(v) == jt || jt == "integer" && isJSONType(v, "integer") }
	if c, ok := sch["const"]; ok && !matches(c) {
		return false
	}
	if e, ok := sch["enum"].([]any); ok && !slices.ContainsFunc(e, matches) {
		return false
	}
	switch types := sch["type"].(type) {
	case string:
		if types != jt && !(types == "number" && jt == "integer") {
			return false
		}
	case []any:
		if !slices.ContainsFunc(types, func(t any) bool { return t == jt || t == "number" && jt == "integer" }) {
			return false
		}
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if branches, ok := sch[kw].([]any); ok && !slices.ContainsFunc(branches, func(b any) bool { return schemaAdmits(set, schemaAt{at.file, b}, jt) }) {
			return false
		}
	}
	return true
}

// TestSchemaCompare: the comparison finds a key the schema lacks or has
// too many, a type it does not take, the null of a nil list, and the same
// one level down.
func TestSchemaCompare(t *testing.T) {
	type section struct {
		A string `json:"a"`
	}
	type thing struct {
		Name    string    `json:"name"`
		Count   *int      `json:"count,omitempty"`
		List    []string  `json:"list"`
		Section section   `json:"section"`
		Items   []section `json:"items,omitempty"`
	}
	good := `{
		"type": "object", "additionalProperties": false,
		"properties": {
			"name": {"type": "string"},
			"count": {"anyOf": [{"const": 0}, {"type": "integer", "minimum": 5}]},
			"list": {"type": ["array", "null"]},
			"section": {"$ref": "#/$defs/section"},
			"items": {"type": "array", "items": {"$ref": "#/$defs/section"}}
		},
		"$defs": {"section": {"type": "object", "additionalProperties": false, "properties": {"a": {"enum": ["x", "y"]}}}}
	}`
	tests := []struct {
		name, schema string
		want         []string // part of each problem, keys in sorted order
	}{
		{"matches", good, nil},
		{"a key it lacks", strings.Replace(good, `"name": {"type": "string"},`, "", 1), []string{"name is not in the schema"}},
		{"a key too many", strings.Replace(good, `"name": {"type": "string"},`, `"name": {"type": "string"}, "old": {},`, 1), []string{"old is in the schema"}},
		{"a type it does not take", strings.Replace(good, `"name": {"type": "string"}`, `"name": {"type": "integer"}`, 1), []string{"name takes no string"}},
		{"the null of a nil list", strings.Replace(good, `"list": {"type": ["array", "null"]}`, `"list": {"type": "array"}`, 1), []string{"list takes no null"}},
		{"one level down", strings.Replace(good, `"properties": {"a": {"enum": ["x", "y"]}}`, `"properties": {"a": {"enum": [1]}, "b": {}}`, 1),
			[]string{"items.a takes no string", "items.b is in the schema", "section.a takes no string", "section.b is in the schema"}},
		{"open", strings.Replace(good, `"type": "object", "additionalProperties": false,`, `"type": "object",`, 1), []string{"the top level does not refuse keys"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "thing.schema.json"), []byte(tc.schema), 0o600); err != nil {
				t.Fatal(err)
			}
			set := &schemaSet{t: t, dir: dir, files: map[string]any{}}
			got := compareSchema(set, schemaAt{"thing.schema.json", set.file("thing.schema.json")}, reflect.TypeFor[thing](), "", schemaType{closed: true})
			if len(got) != len(tc.want) {
				t.Fatalf("problems %q, want %d: %q", got, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

// assertValid fails t for each way data does not follow the schema ref.
func assertValid(t testing.TB, set *schemaSet, ref, what string, data []byte) {
	t.Helper()
	for _, problem := range set.ValidateJSON(ref, data) {
		t.Errorf("%s does not follow %s: %s", what, ref, problem)
	}
}

// TestConfigSchemaVersion: the schema's version is the one the bridge
// writes.
func TestConfigSchemaVersion(t *testing.T) {
	version := newSchemaSet(t).resolve("config.schema.json", "#/properties/version").schema.(map[string]any)
	if got := fmt.Sprint(version["maximum"]); got != fmt.Sprint(currentConfigVersion) {
		t.Errorf("config.schema.json reads up to version %s, the bridge %d", got, currentConfigVersion)
	}
}

// TestConfigFilesFollowTheSchema: the config fixtures, what a save writes
// of each, and the starter config follow config.schema.json.
func TestConfigFilesFollowTheSchema(t *testing.T) {
	set := newSchemaSet(t)
	for _, name := range []string{"config-v1", "config-legacy"} {
		t.Run(name, func(t *testing.T) {
			root, _ := fixtureHome(t)
			data := fixture(t, name+".json")
			assertValid(t, set, "config.schema.json", name+".json", data)
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cs, err := OpenConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cs.Update(cs.ETag(), func(*Config) error { return nil }); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertValid(t, set, "config.schema.json", "the saved "+name+".json", saved)
		})
	}
	t.Run("starter", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		path := filepath.Join(t.TempDir(), "config.json")
		if err := ensureConfigFile(path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assertValid(t, set, "config.schema.json", "the starter config", data)
	})
	t.Run("example", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(repoDir, "config.example.json"))
		if err != nil {
			t.Fatal(err)
		}
		assertValid(t, set, "config.schema.json", "config.example.json", data)
	})
}

// TestConfigSchemaRefusesWhatTheBridgeRefuses: each of these configs
// stops the bridge, and the schema refuses it too. A setting whose value
// the bridge only warns about, such as a group JID with a typo, is not one
// of them.
func TestConfigSchemaRefusesWhatTheBridgeRefuses(t *testing.T) {
	set := newSchemaSet(t)
	for _, config := range []string{
		`{"version": 3}`,
		`{"version": 0}`,
		`{"version": "2"}`,
		`{"allowed_numbers": "15551234567"}`,
		`{"allow_any_dm": "yes"}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000": ["*"]}}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": ["*", "15551234567"]}}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": ["call me"]}}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": ["12345"]}}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": ["15551234567@lid"]}}`,
		`{"allowed_groups": ["120363000000000000@g.us"], "group_members": {"120363000000000000@g.us": "*"}}`,
		`{"triggers": "@claude"}`,
		`{"triggers": [{"text": "@c", "match": "end"}]}`,
		`{"brain_effort": "extreme"}`,
		`{"agent_provider": "other"}`,
		`{"codex_effort": "extreme"}`,
		`{"max_chunk_size": 50}`,
		`{"max_media_mb": 5000}`,
		`{"max_media_mb": -1}`,
		`{"run_timeout_minutes": -1}`,
		`{"run_timeout_minutes": 1441}`,
		`{"max_concurrent_runs": -1}`,
		`{"max_concurrent_runs": 65}`,
		`{"child_env_passthrough": ["1BAD"]}`,
		`{"http_listen": "0.0.0.0:8765"}`,
		`{"brain_mode": false}`,
		`{"brains": [{"name": "a", "work_dir": "~/a"}]}`,
		`{"sandbox": {"deny_paths": ["clients"]}}`,
		`{"sandbox": {"allow_paths": ["./shared"]}}`,
		`{"sandbox": {"git_config": "write"}}`,
		`{"sandbox": {"credentials_dir": "creds"}}`,
		`{"queue": {"mode": "sometimes"}}`,
		`{"backup": {"daily_at": "25:00"}}`,
		`{"backup": {"remote": "my remote"}}`,
		`{"backup": {"root": "/backups"}}`,
		`{"backup": {"root": "a/../b"}}`,
		`{"backup": {"keep_versions_days": -1}}`,
		`{"plaud": {"brain": "Not An Id"}}`,
		`{"plaud": {"interval_seconds": 10}}`,
		`{"plaud": {"phone_dir": "PlaudSync"}}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if _, err := LoadConfig(writeConfig(t, config)); err == nil {
				t.Fatal("the bridge loads it")
			}
			if len(set.ValidateJSON("config.schema.json", []byte(config))) == 0 {
				t.Error("the schema takes it")
			}
		})
	}
}

// TestConfigSchemaTakesWhatTheBridgeTakes: the bridge loads each of these
// configs, written in ways the fixtures are not, and the schema takes it
// too, but for the few that docs/CONFIGURATION.md names as ones the
// schema refuses all the same.
func TestConfigSchemaTakesWhatTheBridgeTakes(t *testing.T) {
	set := newSchemaSet(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		config  string
		refused bool // by the schema, as docs/CONFIGURATION.md says
	}{
		{`{}`, false},
		{`{"version": null}`, false},
		{`{"version": null, "model": "sonnet", "work_dir": "~/w", "brains": []}`, false},
		{`{"version": 1, "brains": null}`, false},
		{`{"version": 2, "allowed_numbers": null, "allowed_groups": null}`, false},
		{`{"http_listen": "localhost:8765"}`, false},
		{`{"http_listen": "[::1]:8765"}`, false},
		{`{"http_listen": "127.0.0.2:9000"}`, false},
		{`{"http_listen": "off"}`, false},
		{`{"triggers": ["@claude", {"text": "@c", "match": "start"}]}`, false},
		{`{"not_a_setting": true}`, true},
		{`{"version": 2, "model": "sonnet"}`, true},
		{`{"log_level": "DEBUG"}`, true},
		{`{"log_level": "verbose"}`, true},
		{`{"http_listen": "[::ffff:127.0.0.1]:8765"}`, true},
		{`{"http_listen": "127.0.0.1:"}`, true},
	} {
		t.Run(tc.config, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, tc.config)); err != nil {
				t.Fatalf("the bridge refuses it: %v", err)
			}
			if problems := set.ValidateJSON("config.schema.json", []byte(tc.config)); len(problems) > 0 != tc.refused {
				t.Errorf("the schema's problems with it: %q, want some: %v", problems, tc.refused)
			}
		})
	}
}

// TestSchemaEnumsAreTheBridgesValues: for each setting with a fixed set
// of values, the schema lists exactly the values the bridge takes, ""
// for the default among them, and a file that sets any one of them loads
// and follows the schema. The tests above catch a schema that takes a
// value the bridge refuses; this one catches a value the bridge has come
// to take, such as a new effort of the Claude CLI, that the schema still
// refuses.
func TestSchemaEnumsAreTheBridgesValues(t *testing.T) {
	keys := func(m map[string]bool) []string { return slices.Collect(maps.Keys(m)) }
	var matches, levels []string
	for _, m := range trigger.MatchValues() {
		matches = append(matches, string(m))
	}
	// The bridge takes a level in any case; the docs write it in lower.
	for _, l := range logx.Levels() {
		levels = append(levels, strings.ToLower(l))
	}
	tests := []struct {
		setting string
		enum    string // the schema that lists the values
		values  []string
		file    string // a file that sets it, %s for the value as JSON
	}{
		{"brain_effort", "config.schema.json#/properties/brain_effort", manifest.Efforts(), `{"brain_effort": %s}`},
		{"agent_provider", "config.schema.json#/properties/agent_provider", []string{"", "claude", "codex"}, `{"agent_provider": %s}`},
		{"codex_effort", "config.schema.json#/properties/codex_effort", manifest.Efforts(), `{"codex_effort": %s}`},
		{"queue.mode", "config.schema.json#/properties/queue/properties/mode", keys(validQueueModes), `{"queue": {"mode": %s}}`},
		{"sandbox.git_config", "config.schema.json#/properties/sandbox/properties/git_config", keys(validGitConfigs), `{"sandbox": {"git_config": %s}}`},
		{"triggers match", "config.schema.json#/properties/triggers/items/oneOf/1/properties/match", matches, `{"triggers": [{"text": "@c", "match": %s}]}`},
		{"log_level", "config.schema.json#/properties/log_level", levels, `{"log_level": %s}`},
		{"brain.json effort", "brain.schema.json#/properties/effort", manifest.Efforts(), `{"id": "a", "name": "A", "effort": %s}`},
		{"brain.json observe", "brain.schema.json#/properties/observe", manifest.ObserveModes(), `{"id": "a", "name": "A", "observe": %s}`},
	}
	set := newSchemaSet(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range tests {
		t.Run(tc.setting, func(t *testing.T) {
			schema, _, _ := strings.Cut(tc.enum, "#")
			var listed []string
			enum, _ := set.resolve(schema, tc.enum).schema.(map[string]any)["enum"].([]any)
			for _, v := range enum {
				listed = append(listed, compactJSON(v))
			}
			var want []string
			for _, v := range append(tc.values, "") {
				want = append(want, compactJSON(v))
			}
			slices.Sort(listed)
			slices.Sort(want)
			if want = slices.Compact(want); !slices.Equal(listed, want) {
				t.Errorf("%s lists %s; the bridge takes %s", tc.enum, listed, want)
			}
			for _, value := range want {
				doc := fmt.Sprintf(tc.file, value)
				var err error
				if schema == "brain.schema.json" {
					var m *manifest.Manifest
					if m, err = manifest.Decode([]byte(doc)); err == nil {
						err = m.Validate()
					}
				} else {
					_, err = LoadConfig(writeConfig(t, doc))
				}
				if err != nil {
					t.Errorf("the bridge refuses %s: %v", doc, err)
				}
				assertValid(t, set, schema, doc, []byte(doc))
			}
		})
	}
}

// TestConfigSchemaTakesPhoneNumbersAsWritten: the numbers group_members
// takes, written as people write them, the schema takes too.
func TestConfigSchemaTakesPhoneNumbersAsWritten(t *testing.T) {
	set := newSchemaSet(t)
	for _, n := range []string{"15551234567", "+1 555 123 4567", "+1 (555) 123-4567", "+1\u00a0555\u00a0123\u00a04567", "1.555.123.4567", "15551234567@s.whatsapp.net", " +15551234567 ", "155512"} {
		t.Run(n, func(t *testing.T) {
			if _, err := phoneNumber(n); err != nil {
				t.Fatalf("the bridge refuses it: %v", err)
			}
			data, err := json.Marshal(n)
			if err != nil {
				t.Fatal(err)
			}
			assertValid(t, set, "config.schema.json#/$defs/phone_number", "the number", data)
		})
	}
}

// TestBrainFilesFollowTheSchema: brain-v1.json, a new brain's brain.json
// and the copy config.json holds of it follow brain.schema.json.
func TestBrainFilesFollowTheSchema(t *testing.T) {
	set := newSchemaSet(t)
	assertValid(t, set, "brain.schema.json", "brain-v1.json", fixture(t, "brain-v1.json"))

	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "notes", Name: "Notes", Routing: manifest.Routing{HandlesDMs: true}})
	data, err := os.ReadFile(filepath.Join(dir, manifest.File))
	if err != nil {
		t.Fatal(err)
	}
	assertValid(t, set, "brain.schema.json", "a new brain's brain.json", data)
	saved, err := os.ReadFile(h.Config().Path)
	if err != nil {
		t.Fatal(err)
	}
	assertValid(t, set, "config.schema.json", "config.json with a brain", saved)
}

// TestBrainSchemaRefusesWhatTheBridgeRefuses: each of these brain.json
// files is refused when the brain is added or loaded, and by the schema.
func TestBrainSchemaRefusesWhatTheBridgeRefuses(t *testing.T) {
	set := newSchemaSet(t)
	for _, manifestJSON := range []string{
		`{"name": "No id"}`,
		`{"id": "Bad Id", "name": "Bad"}`,
		`{"id": "a"}`,
		`{"id": "a", "name": " "}`,
		`{"id": "a", "name": "A", "effort": "extreme"}`,
		`{"id": "a", "name": "A", "observe": "always"}`,
		`{"id": "a", "name": "A", "prompt_file": "../prompt.md"}`,
		`{"id": "a", "name": "A", "prompt_file": "/etc/prompt.md"}`,
		`{"id": "a", "name": "A", "routing": {"groups": ["15551234567@s.whatsapp.net"]}}`,
		`{"id": "a", "name": "A", "routing": {"handles_dms": "yes"}}`,
		`{"id": "a", "name": "A", "calendar": {"account": "you@example.com", "calendars": ["a,b"], "default": ""}}`,
		`{"id": "a", "name": "A", "backup": {"enabled": true, "exclude": ["+ keep"]}}`,
	} {
		t.Run(manifestJSON, func(t *testing.T) {
			if m, err := manifest.Decode([]byte(manifestJSON)); err == nil && m.Validate() == nil {
				t.Fatal("the bridge takes it")
			}
			if len(set.ValidateJSON("brain.schema.json", []byte(manifestJSON))) == 0 {
				t.Error("the schema takes it")
			}
		})
	}
}

// TestHistoryLinesFollowTheSchema: every line of history-v1.jsonl, and the
// lines a brain writes for a message, its answer and a !reset, follow
// history-line.schema.json.
func TestHistoryLinesFollowTheSchema(t *testing.T) {
	set := newSchemaSet(t)
	for i, line := range strings.Split(strings.TrimSpace(string(fixture(t, "history-v1.jsonl"))), "\n") {
		assertValid(t, set, "history-line.schema.json", fmt.Sprintf("line %d of history-v1.jsonl", i+1), []byte(line))
	}

	h := newHarness(t, "")
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	h.Answer(inbound{JID: harnessDM, Sender: "15551234567", Text: "hello"})
	h.WaitFor("the answer in the history", func() bool { return len(historyLines(t, dir)) == 2 })
	h.Answer(inbound{JID: harnessDM, Sender: "15551234567", Text: "!reset"})
	h.WaitFor("the reset in the history", func() bool { return len(historyLines(t, dir)) == 3 })
	for i, line := range historyLines(t, dir) {
		assertValid(t, set, "history-line.schema.json", fmt.Sprintf("history line %d", i+1), []byte(line))
	}
}

// readerSchemas are the schemas of what the bridge writes for other
// programs to read.
var readerSchemas = []string{"history-line.schema.json", "send-response.schema.json", "ingest-response.schema.json"}

// TestReaderSchemasSayTheyAreClosed: a schema of what other programs read
// refuses what it does not list, which keeps it to the code, while those
// programs are to ignore what a later version adds. So its description
// tells a reader that validates against it to allow that.
func TestReaderSchemasSayTheyAreClosed(t *testing.T) {
	set := newSchemaSet(t)
	for _, name := range readerSchemas {
		desc, _ := set.file(name).(map[string]any)["description"].(string)
		if !strings.Contains(desc, "should allow unknown") {
			t.Errorf("%s does not tell a reader that validates against it to allow what is new: %q", name, desc)
		}
	}
}

// assertScriptAPIContract checks a call of /send or /ingest at path
// against the schemas: a JSON answer follows the response schema, as the
// success or the error its status says; a request the bridge took follows
// the request schema; and the history lines it added follow theirs. The
// guards in front of the API answer in plain text, which no schema
// describes.
func assertScriptAPIContract(t *testing.T, path, body string, w *httptest.ResponseRecorder, lines []string) {
	t.Helper()
	set := newSchemaSet(t)
	endpoint := strings.TrimPrefix(path, "/")
	if w.Header().Get("Content-Type") == "application/json" {
		def := "#/$defs/error"
		if w.Code < 300 {
			def = map[string]string{"send": "#/$defs/sent", "ingest": "#/$defs/accepted"}[endpoint]
		}
		assertValid(t, set, endpoint+"-response.schema.json", "the answer", w.Body.Bytes())
		assertValid(t, set, endpoint+"-response.schema.json"+def, fmt.Sprintf("the %d answer", w.Code), w.Body.Bytes())
	}
	if w.Code < 300 {
		assertValid(t, set, endpoint+"-request.schema.json", "the request", []byte(body))
	}
	for i, line := range lines {
		assertValid(t, set, "history-line.schema.json", fmt.Sprintf("history line %d", i+1), []byte(line))
	}
}

// TestSendSchemaTakesTheOTPRequest: the answer of a /send that asks for a
// one-time code, which carries an otp_request, follows the response
// schema, and so does the history line it writes.
func TestSendSchemaTakesTheOTPRequest(t *testing.T) {
	h := newHarness(t, `{"otp_guard": true}`)
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	body := `{"jid": "` + harnessDM + `", "text": "Please send me the 6-digit verification code."}`
	w := h.Script("POST", "/send", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"otp_request"`) {
		t.Fatalf("send: %d %s, want 200 with an otp_request", w.Code, w.Body.String())
	}
	assertScriptAPIContract(t, "/send", body, w, historyLines(t, dir))
}

// TestScriptAPISchemasTakeWhatTheBridgeTakes: requests the bridge takes,
// written in ways the golden calls are not, and the history lines they
// add, follow the schemas.
func TestScriptAPISchemasTakeWhatTheBridgeTakes(t *testing.T) {
	h := newHarness(t, `{"ingest": {"enabled": true}}`)
	dir := h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	for _, tc := range []struct{ path, body string }{
		{"/send", `{"jid": "15551234567", "text": "to a number", "comment": "ignored"}`},
		{"/ingest", `{"brain": "main", "text": "an empty scope", "scope": ""}`},
		{"/ingest", `{"brain": "main", "text": "a null scope", "scope": null}`},
		{"/ingest", `{"brain": "main", "text": "no paths", "paths": null}`},
	} {
		t.Run(tc.path+" "+tc.body, func(t *testing.T) {
			before := len(historyLines(t, dir))
			w := h.Script("POST", tc.path, tc.body)
			if w.Code >= 300 {
				t.Fatalf("the bridge refuses it: %d %s", w.Code, w.Body.String())
			}
			if w.Code == http.StatusAccepted {
				// /ingest answers at once and runs the brain afterwards.
				h.WaitFor("the brain's answer", func() bool { return len(historyLines(t, dir)) >= before+2 })
			}
			assertScriptAPIContract(t, tc.path, tc.body, w, historyLines(t, dir)[before:])
		})
	}
}

// TestScriptAPISchemasRefuseBadRequests: the requests the bridge refuses
// for what they are, rather than what they name, the request schemas
// refuse too. One for a chat or a brain the bridge does not know is not
// among them.
func TestScriptAPISchemasRefuseBadRequests(t *testing.T) {
	set := newSchemaSet(t)
	for _, tc := range []struct{ endpoint, body string }{
		{"send", `{"jid":`},
		{"send", `["15551234567", "hello"]`},
		{"send", `{"text": "hello"}`},
		{"send", `{"jid": "", "text": "hello"}`},
		{"send", `{"jid": "15551234567@s.whatsapp.net"}`},
		{"send", `{"jid": "15551234567@s.whatsapp.net", "text": " \n\t"}`},
		{"send", `{"jid": 15551234567, "text": "hello"}`},
		{"ingest", `{"brain":`},
		{"ingest", `{"scope": "main", "text": "x"}`},
		{"ingest", `{"brain": "main"}`},
		{"ingest", `{"brain": "main", "paths": []}`},
		{"ingest", `{"brain": "main", "text": "  "}`},
		{"ingest", `{"brain": "main", "paths": ["notes.txt"]}`},
		{"ingest", `{"brain": "main", "paths": "/m/notes.txt"}`},
	} {
		t.Run(tc.endpoint+" "+tc.body, func(t *testing.T) {
			if len(set.ValidateJSON(tc.endpoint+"-request.schema.json", []byte(tc.body))) == 0 {
				t.Error("the schema takes it")
			}
		})
	}
}

// TestContractDocsLinkWhatExists: every schema in docs/schemas is checked
// by these tests and linked from the docs, and the links of
// docs/COMPATIBILITY.md, which names the contracts, lead to a file and,
// with a #, to a heading in it.
func TestContractDocsLinkWhatExists(t *testing.T) {
	schemas, err := filepath.Glob(filepath.Join(repoDir, "docs", "schemas", "*.json"))
	if err != nil || len(schemas) == 0 {
		t.Fatalf("no schemas: %v", err)
	}
	tests, err := os.ReadFile(filepath.Join(pkgDir, "contracts_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var docs strings.Builder
	mds, _ := filepath.Glob(filepath.Join(repoDir, "docs", "*.md"))
	for _, md := range mds {
		data, err := os.ReadFile(md)
		if err != nil {
			t.Fatal(err)
		}
		docs.Write(data)
	}
	for _, path := range schemas {
		name := filepath.Base(path)
		stem := strings.TrimSuffix(name, ".schema.json")
		if !strings.Contains(string(tests), `"`+name) && !strings.Contains(string(tests), `"`+strings.TrimSuffix(stem, "-response")+`"`) {
			t.Errorf("no test checks %s", name)
		}
		if !strings.Contains(docs.String(), "schemas/"+name) {
			t.Errorf("no doc in docs/ links schemas/%s", name)
		}
	}

	doc := filepath.Join(repoDir, "docs", "COMPATIBILITY.md")
	data, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\(([^)#]*)(#[^)]*)?\)`).FindAllStringSubmatch(string(data), -1)
	if len(links) == 0 {
		t.Fatal("COMPATIBILITY.md links nothing")
	}
	for _, l := range links {
		target, anchor := l[1], strings.TrimPrefix(l[2], "#")
		if strings.Contains(target, "://") {
			continue
		}
		file := doc
		if target != "" {
			file = filepath.Join(filepath.Dir(doc), filepath.FromSlash(target))
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("COMPATIBILITY.md links %s, which is not there: %v", l[0], err)
			continue
		}
		if anchor != "" && !slices.Contains(headingAnchors(string(body)), anchor) {
			t.Errorf("COMPATIBILITY.md links %s, but %s has no heading #%s", l[0], target, anchor)
		}
	}
}

// headingAnchors are the anchors GitHub gives the headings of a Markdown
// document: lower case, without punctuation but - and _, spaces made -.
func headingAnchors(md string) []string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		text, ok := strings.CutPrefix(strings.TrimLeft(line, "#"), " ")
		if !ok || !strings.HasPrefix(line, "#") {
			continue
		}
		anchor := strings.Map(func(r rune) rune {
			switch {
			case r == ' ':
				return '-'
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				return unicode.ToLower(r)
			}
			return -1
		}, strings.TrimSpace(text))
		out = append(out, anchor)
	}
	return out
}

// TestRestartSettingsAreMarked: the settings that wait for a restart
// (restartFields) are the ones config.schema.json says need one, and
// docs/CONFIGURATION.md says so for each: in a heading marked *(restart)*,
// or in a paragraph that names it and the restart. A heading marked so
// names one of them.
func TestRestartSettingsAreMarked(t *testing.T) {
	set := newSchemaSet(t)
	restart := map[string]bool{}
	for _, f := range restartFields {
		restart[f.name] = true
	}

	var described func(at schemaAt, path string)
	described = func(at schemaAt, path string) {
		sch, _ := at.schema.(map[string]any)
		props, _ := sch["properties"].(map[string]any)
		for _, key := range slices.Sorted(maps.Keys(props)) {
			name := strings.TrimPrefix(path+"."+key, ".")
			prop, _ := props[key].(map[string]any)
			says := strings.Contains(strings.ToLower(fmt.Sprint(prop["description"])), "restart")
			if says != restart[name] {
				t.Errorf("config.schema.json: %s says it needs a restart: %v; restartFields: %v", name, says, restart[name])
			}
			described(schemaAt{at.file, prop}, name)
		}
	}
	described(schemaAt{"config.schema.json", set.file("config.schema.json")}, "")

	data, err := os.ReadFile(filepath.Join(repoDir, "docs", "CONFIGURATION.md"))
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile("`([a-z_.]+)`")
	marked := map[string]bool{}
	for _, para := range strings.Split(string(data), "\n\n") {
		if strings.HasPrefix(para, "#") {
			if strings.Contains(para, "*(restart)*") {
				k := key.FindStringSubmatch(para)
				if k == nil || !restart[k[1]] {
					t.Errorf("CONFIGURATION.md marks %q as needing a restart, which restartFields does not list", para)
					continue
				}
				marked[k[1]] = true
			}
			continue
		}
		for name := range restart {
			if strings.Contains(para, "`"+name+"`") && strings.Contains(strings.ToLower(para), "restart") {
				marked[name] = true
			}
		}
	}
	for _, f := range restartFields {
		if !marked[f.name] {
			t.Errorf("CONFIGURATION.md does not say that %s needs a restart", f.name)
		}
	}
}
