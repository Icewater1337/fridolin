package backup

import (
	"reflect"

	"github.com/Icewater1337/fridolin/internal/jsonx"
)

// backupJSONError keeps diagnostic names stable when the service moves. It only
// changes presentation: callers can still recover the original decoder error.
func backupJSONError(err error) error {
	return jsonx.RenameTypeError(err, func(t reflect.Type) string {
		switch t {
		case reflect.TypeFor[backupState]():
			return "main.backupState"
		case reflect.TypeFor[Result]():
			return "main.BackupResult"
		case reflect.TypeFor[RestoreResult]():
			return "main.RestoreResult"
		}
		return ""
	}, func(name string) string {
		if name == "Result" {
			return "BackupResult"
		}
		return ""
	})
}
