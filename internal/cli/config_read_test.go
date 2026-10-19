package cli

import (
	"encoding/json"
	"os"
	"testing"
)

// readRawConfig returns the top-level keys of the config file at path, as
// they are written.
func readRawConfig(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
