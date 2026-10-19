package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// JSONError preserves the type names printed by config decoders before the
// package move. Callers still recover the original JSON error with errors.As.
// compat: these diagnostics are part of the CLI and dashboard contracts.
func JSONError(err error) error {
	var typed *json.UnmarshalTypeError
	if !errors.As(err, &typed) {
		return err
	}
	name := legacyJSONTypeName(typed.Type)
	if name == typed.Type.String() {
		return err
	}
	prefix, ok := strings.CutSuffix(err.Error(), typed.Type.String())
	if !ok {
		return err
	}
	return legacyJSONError{cause: err, message: prefix + name}
}

type legacyJSONError struct {
	cause   error
	message string
}

func (e legacyJSONError) Error() string { return e.message }
func (e legacyJSONError) Unwrap() error { return e.cause }

func legacyJSONTypeName(t reflect.Type) string {
	if t.Name() != "" {
		if t.PkgPath() == reflect.TypeFor[Config]().PkgPath() {
			return "main." + t.Name()
		}
		return t.String()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + legacyJSONTypeName(t.Elem())
	case reflect.Slice:
		return "[]" + legacyJSONTypeName(t.Elem())
	case reflect.Map:
		return "map[" + legacyJSONTypeName(t.Key()) + "]" + legacyJSONTypeName(t.Elem())
	}
	return t.String()
}
