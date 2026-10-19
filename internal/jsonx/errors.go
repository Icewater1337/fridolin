package jsonx

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

// RenameTypeError preserves a decoder diagnostic across a Go type move.
// Callbacks return a replacement name, or an empty string to keep the
// original. The wrapper preserves errors.As and any diagnostic prefix.
// Errors without the decoder diagnostic as their suffix are left alone.
func RenameTypeError(err error, typeName func(reflect.Type) string, structName func(string) string) error {
	var typed *json.UnmarshalTypeError
	if !errors.As(err, &typed) {
		return err
	}
	copy := *typed
	if structName != nil {
		if name := structName(copy.Struct); name != "" {
			copy.Struct = name
		}
	}
	message := copy.Error()
	if name := renamedType(typed.Type, typeName); name != typed.Type.String() {
		message = strings.TrimSuffix(message, typed.Type.String()) + name
	}
	if message == typed.Error() {
		return err
	}
	prefix, ok := strings.CutSuffix(err.Error(), typed.Error())
	if !ok {
		return err
	}
	return renamedTypeError{cause: err, message: prefix + message}
}

type renamedTypeError struct {
	cause   error
	message string
}

func (e renamedTypeError) Error() string { return e.message }
func (e renamedTypeError) Unwrap() error { return e.cause }

func renamedType(t reflect.Type, name func(reflect.Type) string) string {
	if name == nil {
		return t.String()
	}
	if t.Name() != "" {
		if replacement := name(t); replacement != "" {
			return replacement
		}
		return t.String()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + renamedType(t.Elem(), name)
	case reflect.Slice:
		return "[]" + renamedType(t.Elem(), name)
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + renamedType(t.Elem(), name)
	case reflect.Map:
		return "map[" + renamedType(t.Key(), name) + "]" + renamedType(t.Elem(), name)
	}
	return t.String()
}
