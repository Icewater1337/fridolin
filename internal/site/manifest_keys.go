package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// decodeManifest is json.Unmarshal for a module.json, minus the two ways Go
// reads one differently from the site (JSON.parse) and the approval review:
// Go matches a struct's keys case-insensitively ("Capabilities" fills
// capabilities), and a key written twice merges into one struct. Either would
// grant what the approver never saw, so a manifest with a key twice in one
// object, or a key that is a known field in other letters, does not decode.
func decodeManifest(data []byte, v any) error {
	if err := checkManifestKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeOf(v)); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// checkManifestKeys reads one JSON value from dec, checking its object keys
// against t (the Go type it decodes into; nil when unknown).
func checkManifestKeys(dec *json.Decoder, t reflect.Type) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('['):
		var elem reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			elem = t.Elem()
		}
		for dec.More() {
			if err := checkManifestKeys(dec, elem); err != nil {
				return err
			}
		}
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			k := kt.(string)
			if seen[k] {
				return fmt.Errorf("module.json has the key %q twice in one object", k)
			}
			seen[k] = true
			var sub reflect.Type
			switch {
			case t == nil:
			case t.Kind() == reflect.Map:
				sub = t.Elem()
			case t.Kind() == reflect.Struct:
				if f, name, ok := fieldFolding(t, k); ok {
					if name != k {
						return fmt.Errorf("module.json has the key %q; it is written %q", k, name)
					}
					sub = f.Type
				}
			}
			if err := checkManifestKeys(dec, sub); err != nil {
				return err
			}
		}
	default:
		return nil // a scalar
	}
	_, err = dec.Token() // the closing ] or }
	return err
}

// fieldFolding finds the field of struct t that encoding/json fills from key
// k, matching as it does (bytes.EqualFold), with that field's JSON name.
func fieldFolding(t reflect.Type, k string) (reflect.StructField, string, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if strings.EqualFold(name, k) {
			return f, name, true
		}
	}
	return reflect.StructField{}, "", false
}
