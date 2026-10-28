package site

import (
	"os"
	"path/filepath"
	"testing"
)

// A key in other letters or written twice would grant what the site and the
// approval review do not read, so readModuleCaps leaves such a module out.
func TestSiteManifestKeysAreExact(t *testing.T) {
	t.Parallel()
	const head = `{"version":1,"id":"m","title":"M","route":"/m","app":{"dist":"d","base":"built"},`
	for name, tc := range map[string]struct {
		body string
		ok   bool
	}{
		"exact":            {`"capabilities":{"commands":["git"]}}`, true},
		"other letters":    {`"Capabilities":{"commands":["sh"]}}`, false},
		"nested letters":   {`"capabilities":{"Commands":["sh"]}}`, false},
		"case twin":        {`"capabilities":{"commands":["git"]},"CAPABILITIES":{"commands":["sh"]}}`, false},
		"twice":            {`"capabilities":{"commands":["sh"]},"capabilities":{"secrets":["k"]}}`, false},
		"connection field": {`"capabilities":{"openai":{"USE":["transcribe"]}}}`, true},
		"file names free":  {`"capabilities":{"files":{"Ledger":{"path":"~/l","access":"read"},"ledger":{"path":"~/m","access":"read"}}}}`, true},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "m"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "m", "module.json"), []byte(head+tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		mods := readModuleCaps(dir, t.TempDir())
		if got := len(mods) == 1; got != tc.ok {
			t.Errorf("%s: module read = %v, want %v (%+v)", name, got, tc.ok, mods)
		}
		if name == "connection field" && len(mods) == 1 && len(mods[0].Connections) != 0 {
			t.Errorf("%s: USE granted a connection: %+v", name, mods[0].Connections)
		}
	}
	var v map[string]any
	if decodeManifest([]byte(`{"a":1,"a":2}`), &v) == nil {
		t.Error("a key twice decoded for the review")
	}
}
