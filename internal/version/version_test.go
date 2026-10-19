package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestStringIsTheStamp(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3"
	if got := String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want the stamp v1.2.3", got)
	}
	want := "fridolin v1.2.3 (" + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := Line(); got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

// Without a stamp a build names its commit, dev-<rev> with +dirty for a
// checkout with changes, when the go command recorded one, and is "dev"
// otherwise, as a test binary is.
func TestStringWithoutAStamp(t *testing.T) {
	got := String()
	if got != "dev" && !strings.HasPrefix(got, "dev-") {
		t.Errorf("String() = %q, want dev or dev-<rev>", got)
	}
}
