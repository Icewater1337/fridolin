package nodeproto

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// AndroidBuildCap (android-build) is a node that builds Android apps with
// Gradle: the agent declares it while it finds a JDK 17 or newer and the
// Android SDK there, so it comes and goes with them; approving it once
// covers it every time it comes back. The core uses it to build the
// Plaud Sync phone app (docs/PLAUD.md) where it has no toolchain itself.
//
// Approving it lets the core have the node build source the core sends:
// Gradle runs that source's build scripts, unsandboxed, as the node's user.
type AndroidBuildCap struct {
	// Java is the JDK's feature version ("21"), "" where unknown.
	Java string `json:"java,omitempty"`
	// Platforms are the SDK's platforms (android-35).
	Platforms []string `json:"platforms,omitempty"`
}

const maxAndroidPlatforms = 64

var androidPlatformRE = regexp.MustCompile(`^android-[A-Za-z0-9.]{1,16}$`)

// Check reports what is wrong with the declaration.
func (a *AndroidBuildCap) Check() error {
	if a == nil {
		return nil
	}
	if len(a.Java) > 8 || strings.Trim(a.Java, "0123456789") != "" {
		return fmt.Errorf("android_build: java %q is not a version", a.Java)
	}
	if len(a.Platforms) > maxAndroidPlatforms {
		return fmt.Errorf("android_build: at most %d platforms", maxAndroidPlatforms)
	}
	for _, p := range a.Platforms {
		if !androidPlatformRE.MatchString(p) {
			return fmt.Errorf("android_build: platform %q", p)
		}
	}
	return nil
}

// The android methods. Each needs the android_build capability.
const (
	// MethodAndroidBuild builds an Android project with Gradle and stages
	// the APK on the node (AndroidBuildParams → AndroidBuildResult); read
	// it with android.read, then drop it with android.drop.
	MethodAndroidBuild = "android.build"
	// MethodAndroidRead reads a chunk of a staged APK (FrideChunkParams →
	// FrideChunk).
	MethodAndroidRead = "android.read"
	// MethodAndroidDrop removes a staged APK (FrideChunkParams with the ID
	// → nothing).
	MethodAndroidDrop = "android.drop"
)

// AndroidMethods lists the android methods.
var AndroidMethods = []string{MethodAndroidBuild, MethodAndroidRead, MethodAndroidDrop}

// Bounds of a build.
const (
	// MaxAndroidSourceBytes bounds the gzip'd tar of the project.
	MaxAndroidSourceBytes = MaxFileBytes
	// MaxAndroidProperties bounds the build properties.
	MaxAndroidProperties = 32
	// AndroidBuildTimeoutSeconds is how long a build may take on the node.
	AndroidBuildTimeoutSeconds = 20 * 60
	// MaxAndroidLogBytes bounds the end of Gradle's output a failed build
	// returns.
	MaxAndroidLogBytes = 16 << 10
)

// AndroidBuildParams is a build: the project as a gzip'd tar, with
// gradlew at its root, and the properties of the build, which the node
// writes to a file it passes to Gradle as -PfridolinConfig (the app reads
// them into its BuildConfig) and removes with the rest of the build. The
// node runs `gradlew assembleDebug` and stages
// app/outputs/apk/debug/app-debug.apk of the build.
type AndroidBuildParams struct {
	Source     []byte            `json:"source"`
	Properties map[string]string `json:"properties,omitempty"`
}

var androidPropertyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Check reports what is wrong with the params. A property's value may not
// hold what would break the properties file or the code it ends up in.
func (p AndroidBuildParams) Check() error {
	if len(p.Source) == 0 {
		return fmt.Errorf("the source is empty")
	}
	if len(p.Source) > MaxAndroidSourceBytes {
		return fmt.Errorf("the source is larger than %d bytes", MaxAndroidSourceBytes)
	}
	if len(p.Properties) > MaxAndroidProperties {
		return fmt.Errorf("at most %d properties", MaxAndroidProperties)
	}
	for k, v := range p.Properties {
		if !androidPropertyRE.MatchString(k) {
			return fmt.Errorf("property name %q", k)
		}
		if len(v) > 2048 || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' || r == '"' }) {
			return fmt.Errorf("property %s: a value with a character a build property may not have", k)
		}
	}
	return nil
}

// AndroidBuildResult is how a build ended: the staged APK, or, when
// Gradle failed, Error and the end of its output.
type AndroidBuildResult struct {
	APK   *FrideTransfer `json:"apk,omitempty"`
	Error string         `json:"error,omitempty"`
	Log   string         `json:"log,omitempty"`
	// Java is the JDK's version it built with.
	Java       string `json:"java,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
