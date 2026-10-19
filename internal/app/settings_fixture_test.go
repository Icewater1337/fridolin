package app

import (
	"github.com/Icewater1337/fridolin/internal/config"
	"testing"
)

// resolvedSettings builds value-policy fixtures. Pure sandbox projection tests
// can describe macOS policy on either host; platform rejection has its own tests.
func resolvedSettings(t testing.TB, file *config.File) *config.Settings {
	t.Helper()
	if file == nil {
		return nil
	}
	s, err := config.Resolve(file, ConfigPaths(), "darwin")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
