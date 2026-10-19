package manifest

import (
	"reflect"

	"github.com/Icewater1337/fridolin/internal/jsonx"
)

// JSONError keeps the pre-extraction names in malformed manifest diagnostics.
// It also handles manifests nested in configuration and dashboard requests.
// compat: errors.As still exposes the original decoder error and its offsets.
func JSONError(err error) error {
	return jsonx.RenameTypeError(err, func(t reflect.Type) string {
		if t.PkgPath() != reflect.TypeFor[Manifest]().PkgPath() {
			return ""
		}
		if name := legacyStructName(t.Name()); name != "" {
			return "main." + name
		}
		return ""
	}, legacyStructName)
}

func legacyStructName(name string) string {
	switch name {
	case "Manifest":
		return "BrainManifest"
	case "Routing":
		return "BrainRouting"
	case "Backup":
		return "BrainBackup"
	case "Calendar":
		return "BrainCalendar"
	}
	return ""
}
