package container

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The image's npm CLIs come from the lockfile, every package with its hash
// from npm's registry, nested inside the tool that needs it (#490).
func TestImageNpmToolsComeFromTheLockfile(t *testing.T) {
	t.Parallel()
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	var lock struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	for file, v := range map[string]any{"npm/package.json": &pkg, "npm/package-lock.json": &lock} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	for name, version := range pkg.Dependencies {
		if strings.ContainsAny(version, "^~<>=* ") {
			t.Errorf("package.json: %s %q is a range, not an exact version", name, version)
		}
		if got := lock.Packages["node_modules/"+name].Version; got != version {
			t.Errorf("package-lock.json has %s %s, package.json %s", name, got, version)
		}
	}
	for path, p := range lock.Packages {
		if path == "" {
			continue
		}
		if !strings.HasPrefix(p.Resolved, "https://registry.npmjs.org/") || !strings.HasPrefix(p.Integrity, "sha512-") {
			t.Errorf("%s: resolved %q integrity %q", path, p.Resolved, p.Integrity)
		}
		top := strings.Join(strings.SplitN(strings.TrimPrefix(path, "node_modules/"), "/", 3)[:2], "/")
		if _, ok := pkg.Dependencies[top]; !ok {
			t.Errorf("%s lies outside the tools' own folders (hoisted?): .npmrc wants install-strategy=nested", path)
		}
	}
	npmrc, err := os.ReadFile("npm/.npmrc")
	if err != nil || !strings.Contains(string(npmrc), "\ninstall-strategy=nested\n") {
		t.Errorf("npm/.npmrc does not set install-strategy=nested (%v)", err)
	}
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "npm ci --ignore-scripts") {
		t.Error("container/Dockerfile does not install the npm CLIs with npm ci --ignore-scripts")
	}
	for line := range strings.Lines(string(dockerfile)) {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "npm install") {
			t.Errorf("container/Dockerfile installs without the lockfile: %s", strings.TrimSpace(line))
		}
	}
}
