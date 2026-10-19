package plaud

import (
	"reflect"
	"strings"

	"github.com/Icewater1337/fridolin/internal/jsonx"
)

// plaudDecodeError keeps the on-disk and API decoder diagnostics stable when
// their types move packages. errors.As still exposes the original metadata.
func DecodeError(err error) error {
	names := map[reflect.Type]string{
		reflect.TypeFor[plaudState]():   "plaudState",
		reflect.TypeFor[plaudPending](): "plaudPending",
		reflect.TypeFor[plaudLogin]():   "plaudLogin",
		reflect.TypeFor[plaudGivenUp](): "plaudGivenUp",
		reflect.TypeFor[Forward]():      "PlaudForward",
		reflect.TypeFor[plaudDevices](): "plaudDevices",
		reflect.TypeFor[plaudDevice]():  "plaudDevice",
		reflect.TypeFor[plaudPairing](): "plaudPairing",
		reflect.TypeFor[plaudJWK]():     "plaudJWK",
		reflect.TypeFor[Developer]():    "plaudDeveloper",
	}
	return jsonx.RenameTypeError(err, func(t reflect.Type) string {
		name := t.String()
		for typ, legacy := range names {
			name = strings.ReplaceAll(name, typ.String(), "main."+legacy)
		}
		if name != t.String() {
			return name
		}
		return ""
	}, func(name string) string {
		for typ, legacy := range names {
			if name == typ.Name() {
				return legacy
			}
		}
		return ""
	})
}
