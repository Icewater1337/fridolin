package nodeagent

import (
	"path/filepath"
	"testing"
)

func TestCheckDockerDisk(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"": "40g", "40g": "40g", "20G": "20g", "512m": "512m",
		"0g": "", "40": "", "40gb": "", "-1g": "", "1t": "", "40g;x": "",
	} {
		got, err := checkDockerDisk(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

// The caches get their folders; "" is none; a folder that holds the
// node's state dir, or lies in it, is refused: the tokens wait there.
func TestPrepareCaches(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	state := filepath.Join(root, "node")
	if got, err := prepareCaches("", state); got != "" || err != nil {
		t.Errorf("none: %q %v", got, err)
	}
	got, err := prepareCaches(filepath.Join(root, "caches", "work"), state)
	if err != nil || got != filepath.Join(root, "caches", "work") {
		t.Fatalf("%q %v", got, err)
	}
	if env := frideInstanceCacheEnv(got); len(env) != 4 || env[0] != "GOMODCACHE="+got+"/gomod" {
		t.Errorf("env %v", env)
	}
	for _, bad := range []string{filepath.Join(state, "caches"), root} {
		if _, err := prepareCaches(bad, state); err == nil {
			t.Errorf("%s was taken with the state dir %s", bad, state)
		}
	}
}
