package platform

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// The JDKs the Plaud Sync app (plaud-android/) builds with: the Android
// Gradle plugin needs 17 or newer, and the Gradle of the app's wrapper
// (8.10, gradle-wrapper.properties) runs on at most 23. A newer JDK is
// taken only when no JDK in that range is there.
const (
	MinAndroidJDK = 17
	maxGradleJDK  = 23
)

// AndroidToolchain is a JDK and an Android SDK to build an app with.
type AndroidToolchain struct {
	JavaHome string
	// JavaMajor is the JDK's feature version (21 for 21.0.5), 0 where its
	// release file does not say.
	JavaMajor int
	SDK       string
	// Platforms are the SDK's platforms (android-35), sorted.
	Platforms []string
}

// AndroidLookup is where FindAndroidToolchain looks: the environment and
// the home folder. Zero fields are the process's own.
type AndroidLookup struct {
	Getenv func(string) string
	Home   string
	GOOS   string
}

// FindAndroidToolchain finds a JDK (17 or newer) and the Android SDK:
// JAVA_HOME and ANDROID_HOME (or ANDROID_SDK_ROOT) of the environment,
// else where macOS, Homebrew, Android Studio and Linux distributions put
// them. JAVA_HOME wins when Gradle runs on it; else, of the JDKs found,
// the newest one Gradle runs on is taken.
func FindAndroidToolchain(l AndroidLookup) (AndroidToolchain, error) {
	if l.Getenv == nil {
		l.Getenv = os.Getenv
	}
	if l.GOOS == "" {
		l.GOOS = runtime.GOOS
	}
	if l.Home == "" {
		l.Home, _ = os.UserHomeDir()
	}
	home := l.Home
	javas := []string{l.Getenv("JAVA_HOME")}
	sdks := []string{l.Getenv("ANDROID_HOME"), l.Getenv("ANDROID_SDK_ROOT")}
	if home != "" {
		sdks = append(sdks, filepath.Join(home, "Library", "Android", "sdk"), filepath.Join(home, "Android", "Sdk"))
	}
	if l.GOOS == "darwin" {
		if out, err := exec.Command("/usr/libexec/java_home").Output(); err == nil {
			javas = append(javas, strings.TrimSpace(string(out)))
		}
	}
	if l.GOOS == "windows" {
		if local := l.Getenv("LOCALAPPDATA"); local != "" {
			sdks = append(sdks, filepath.Join(local, "Android", "Sdk"))
		}
	}
	prefixes := []string{"/opt/homebrew", "/usr/local"}
	if home != "" {
		prefixes = append(prefixes, filepath.Join(home, "homebrew"))
	}
	for _, prefix := range prefixes {
		for _, jdk := range []string{"openjdk", "openjdk@21", "openjdk@17"} {
			javas = append(javas, filepath.Join(prefix, "opt", jdk, "libexec", "openjdk.jdk", "Contents", "Home"))
		}
		sdks = append(sdks, filepath.Join(prefix, "share", "android-commandlinetools"))
	}
	javas = append(javas, "/Applications/Android Studio.app/Contents/jbr/Contents/Home")
	globs := []string{"/Library/Java/JavaVirtualMachines/*/Contents/Home", "/usr/lib/jvm/*"}
	if home != "" {
		globs = append(globs, filepath.Join(home, "Library", "Java", "JavaVirtualMachines", "*", "Contents", "Home"))
	}
	for _, g := range globs {
		if m, _ := filepath.Glob(g); len(m) > 0 {
			javas = append(javas, m...)
		}
	}
	var tc AndroidToolchain
	// JAVA_HOME, when set, wins whenever Gradle runs on it.
	if j, major := pickJDK(javas[:1], l.GOOS); j != "" && (major == 0 || major <= maxGradleJDK) {
		tc.JavaHome, tc.JavaMajor = j, major
	} else {
		tc.JavaHome, tc.JavaMajor = pickJDK(javas, l.GOOS)
	}
	for _, s := range sdks {
		if s != "" && IsDir(filepath.Join(s, "platforms")) {
			tc.SDK = s
			break
		}
	}
	switch {
	case tc.JavaHome == "":
		return tc, errors.New("no JDK 17 or newer found to build the app with: install one (brew install openjdk@21), or set JAVA_HOME in the service's environment")
	case tc.SDK == "":
		return tc, errors.New(`no Android SDK found to build the app with: brew install --cask android-commandlinetools, then sdkmanager "platforms;android-35" "build-tools;35.0.0"; or set ANDROID_HOME in the service's environment`)
	}
	if ents, err := os.ReadDir(filepath.Join(tc.SDK, "platforms")); err == nil {
		for _, e := range ents {
			if e.IsDir() && strings.HasPrefix(e.Name(), "android-") {
				tc.Platforms = append(tc.Platforms, e.Name())
			}
		}
		slices.Sort(tc.Platforms)
	}
	if len(tc.Platforms) == 0 {
		return tc, errors.New(`the Android SDK at ` + tc.SDK + ` has no platform: sdkmanager "platforms;android-35" "build-tools;35.0.0"`)
	}
	return tc, nil
}

// pickJDK takes, of the folders that hold a java, the first with the
// newest version Gradle runs on, else the first with the newest version
// at all; one whose version is unknown counts as good enough (the first
// such, after those with a known good version).
func pickJDK(javas []string, goos string) (string, int) {
	java := "java"
	if goos == "windows" {
		java = "java.exe"
	}
	best, bestMajor, unknown := "", 0, ""
	for _, j := range javas {
		if j == "" || !isFile(filepath.Join(j, "bin", java)) {
			continue
		}
		major := JavaMajor(j)
		switch {
		case major == 0:
			if unknown == "" {
				unknown = j
			}
		case major < MinAndroidJDK:
		case best == "", jdkRank(major) > jdkRank(bestMajor):
			best, bestMajor = j, major
		}
	}
	if best == "" && unknown != "" {
		return unknown, 0
	}
	return best, bestMajor
}

// jdkRank orders JDKs: those Gradle runs on above the newer ones, and
// newer above older within each.
func jdkRank(major int) int {
	if major <= maxGradleJDK {
		return 1000 + major
	}
	return major
}

// JavaMajor reads the feature version of the JDK at home from its release
// file (JAVA_VERSION="21.0.5", or "1.8.0_402" for 8); 0 when it cannot.
func JavaMajor(home string) int {
	f, err := os.Open(filepath.Join(home, "release"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "JAVA_VERSION=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		v = strings.TrimPrefix(v, "1.")
		if i := strings.IndexAny(v, "._-+"); i >= 0 {
			v = v[:i]
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
