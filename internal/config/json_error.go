package config

import (
	"reflect"

	"github.com/Icewater1337/fridolin/internal/jsonx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// JSONError preserves the type names printed by config decoders before the
// package move. Callers still recover the original JSON error with errors.As.
// compat: these diagnostics are part of the CLI and dashboard contracts.
func JSONError(err error) error {
	err = jsonx.RenameTypeError(err, func(t reflect.Type) string {
		if t.PkgPath() == reflect.TypeFor[Config]().PkgPath() {
			return "main." + t.Name()
		}
		return ""
	}, nil)
	return manifest.JSONError(err)
}
