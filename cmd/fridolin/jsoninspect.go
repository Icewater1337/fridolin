package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// unknownKeys lists the keys in data, a JSON value for a t, that no field
// of t takes when data is decoded into one, each by its path from path:
// sandbox.enable, brain_dirs[0].approved.nmae. It looks into the objects
// of structs, and into arrays and objects that hold structs, but not into
// a type that decodes itself. Keys are matched as encoding/json matches
// them, an exact match first and then one that differs only in case.
func unknownKeys(data []byte, t reflect.Type, path string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil
	}
	var out []string
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil {
			return nil
		}
		fields := jsonFieldsOf(t)
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			ft, ok := fields[k]
			if !ok {
				for name, f := range fields {
					if strings.EqualFold(name, k) {
						ft, ok = f, true
						break
					}
				}
			}
			if !ok {
				out = append(out, unknownKeyPath(path, k))
				continue
			}
			out = append(out, unknownKeys(obj[k], ft, unknownKeyPath(path, k))...)
		}
	case reflect.Slice, reflect.Array:
		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) != nil {
			return nil
		}
		for i, el := range arr {
			out = append(out, unknownKeys(el, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil {
			return nil
		}
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			out = append(out, unknownKeys(obj[k], t.Elem(), unknownKeyPath(path, k))...)
		}
	}
	return out
}

// jsonFieldsOf maps the JSON keys of the struct type t to their types.
func jsonFieldsOf(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}

// unknownKeyPath is the path of key k in the object at path.
func unknownKeyPath(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}
