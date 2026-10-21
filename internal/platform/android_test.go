package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeJDK makes a folder that looks like a JDK of version.
func fakeJDK(t *testing.T, root, name, version string) string {
	t.Helper()
	home := filepath.Join(root, name)
	java := "java"
	if runtime.GOOS == "windows" {
		java = "java.exe"
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", java), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(home, "release"), []byte("IMPLEMENTOR=\"x\"\nJAVA_VERSION=\""+version+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestPickJDK(t *testing.T) {
	root := t.TempDir()
	j8 := fakeJDK(t, root, "8", "1.8.0_402")
	j17 := fakeJDK(t, root, "17", "17.0.9")
	j21 := fakeJDK(t, root, "21", "21.0.5")
	j25 := fakeJDK(t, root, "25", "25.0.2")
	unknown := fakeJDK(t, root, "unknown", "")
	for _, c := range []struct {
		name  string
		javas []string
		want  string
		major int
	}{
		{"the newest Gradle runs on", []string{j25, j17, j21}, j21, 21},
		{"only a newer one", []string{j25, j8}, j25, 25},
		{"none new enough", []string{j8}, "", 0},
		{"a known one before an unknown", []string{unknown, j17}, j17, 17},
		{"an unknown one rather than none", []string{j8, unknown}, unknown, 0},
		{"missing folders", []string{"", filepath.Join(root, "none")}, "", 0},
	} {
		got, major := pickJDK(c.javas, runtime.GOOS)
		if got != c.want || major != c.major {
			t.Errorf("%s: %q %d, want %q %d", c.name, got, major, c.want, c.major)
		}
	}
	if JavaMajor(j8) != 8 || JavaMajor(j21) != 21 || JavaMajor(unknown) != 0 {
		t.Error("JavaMajor")
	}
}

func TestFindAndroidToolchain(t *testing.T) {
	root := t.TempDir()
	j17 := fakeJDK(t, root, "17", "17.0.9")
	sdk := filepath.Join(root, "sdk")
	env := map[string]string{"JAVA_HOME": j17}
	lookup := AndroidLookup{Getenv: func(k string) string { return env[k] }, Home: filepath.Join(root, "home"), GOOS: runtime.GOOS}
	env["ANDROID_HOME"] = sdk
	if err := os.MkdirAll(filepath.Join(sdk, "platforms"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindAndroidToolchain(lookup); err == nil {
		t.Error("found a toolchain with an SDK without platforms")
	}
	if err := os.MkdirAll(filepath.Join(sdk, "platforms", "android-35"), 0o755); err != nil {
		t.Fatal(err)
	}
	tc, err := FindAndroidToolchain(lookup)
	if err != nil {
		t.Fatal(err)
	}
	// JAVA_HOME wins while Gradle runs on it, whatever else is installed.
	if tc.JavaHome != j17 || tc.JavaMajor != 17 || tc.SDK != sdk || len(tc.Platforms) != 1 || tc.Platforms[0] != "android-35" {
		t.Errorf("toolchain %+v", tc)
	}
}
