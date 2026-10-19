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
	oldType := typed.Type.String()
	oldDiagnostic := typed.Error()
	if previous, ok := errors.AsType[renamedTypeError](err); ok {
		copy, oldType = previous.diagnostic, previous.typeName
		oldDiagnostic = renderTypeError(copy, oldType)
	}
	if structName != nil {
		if name := structName(copy.Struct); name != "" {
			copy.Struct = name
		}
	}
	name := renamedType(typed.Type, typeName)
	if name == typed.Type.String() {
		name = oldType
	}
	message := renderTypeError(copy, name)
	if message == oldDiagnostic {
		return err
	}
	prefix, ok := strings.CutSuffix(err.Error(), oldDiagnostic)
	if !ok {
		return err
	}
	return renamedTypeError{cause: err, message: prefix + message, diagnostic: copy, typeName: name}
}

type renamedTypeError struct {
	cause      error
	message    string
	diagnostic json.UnmarshalTypeError
	typeName   string
}

func (e renamedTypeError) Error() string { return e.message }
func (e renamedTypeError) Unwrap() error { return e.cause }

func renderTypeError(err json.UnmarshalTypeError, typeName string) string {
	return strings.TrimSuffix(err.Error(), err.Type.String()) + typeName
}

func renamedType(t reflect.Type, name func(reflect.Type) string) string {
	if name == nil {
		return t.String()
	}
	if replacement := name(t); replacement != "" {
		return replacement
	}
	if t.Name() != "" {
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
