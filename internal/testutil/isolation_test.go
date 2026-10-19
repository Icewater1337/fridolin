package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainIsolatesPathsAndConfigSelectors(t *testing.T) {
	if !sharedMainInstalled {
		t.Fatal("Main did not install the shared harness")
	}
	if home := os.Getenv("HOME"); home != sharedPaths.Home || !strings.Contains(filepath.Base(home), "fridolin-test-home-") {
		t.Fatalf("HOME = %q", home)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if cwd == sharedPaths.PackageDir || !strings.Contains(filepath.Base(cwd), "fridolin-test-wd-") {
		t.Fatalf("working directory = %q", cwd)
	}
	for _, name := range BridgeVariables() {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s remained set to %q", name, value)
		}
	}
	target, err := os.Readlink(filepath.Join(cwd, "testdata"))
	if err != nil || target != filepath.Join(sharedPaths.PackageDir, "testdata") {
		t.Errorf("testdata link = %q, %v", target, err)
	}
}
