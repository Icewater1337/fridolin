package nodeagent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// tarGz packs files (name → content) as the core sends a project; names
// in exec are executable.
func tarGz(t *testing.T, files map[string]string, exec ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, content := range files {
		mode := int64(0o644)
		for _, e := range exec {
			if e == name {
				mode = 0o755
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGradlew stands in for Gradle: it writes an APK where the build
// would, holding the properties it was given and the folder it ran in,
// padded past one chunk; with a file "fail" in the project it fails.
const fakeGradlew = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -PfridolinConfig=*) cfg="${a#-PfridolinConfig=}" ;;
    -PfridolinBuildDir=*) out="${a#-PfridolinBuildDir=}" ;;
  esac
done
echo "> Task :app:assembleDebug"
if [ -f fail ]; then echo "FAILURE: Build failed with an exception."; exit 1; fi
mkdir -p "$out/app/outputs/apk/debug"
apk="$out/app/outputs/apk/debug/app-debug.apk"
{ echo "APK"; cat "$cfg"; echo "dir=$PWD"; echo "java=$JAVA_HOME"; echo "sdk=$ANDROID_HOME"; } > "$apk"
head -c 9000000 /dev/zero >> "$apk"
`

// fakeToolchain is a JDK and an SDK that are only folders.
func fakeToolchain(t *testing.T) platform.AndroidToolchain {
	t.Helper()
	root := t.TempDir()
	return platform.AndroidToolchain{JavaHome: filepath.Join(root, "jdk"), JavaMajor: 21, SDK: filepath.Join(root, "sdk"), Platforms: []string{"android-35"}}
}

func approveAll(t *testing.T, c *core, a *Agent) {
	t.Helper()
	waitFor(t, "the agent to connect", func() bool { return c.svc.Online(a.ID()) })
	if _, err := c.svc.ApproveDevice(a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ApproveCommands(a.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "approved commands", func() bool { return a.State().Commands == nodeproto.CommandsApproved })
}

func TestAndroidBuildOnTheNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a node offers android-build on macOS and Linux only")
	}
	c := startCore(t, t.TempDir())
	a := joinNode(t, c, nodeproto.Capabilities{Host: true})
	tc := fakeToolchain(t)
	a.AndroidToolchain = func() (platform.AndroidToolchain, error) { return tc, nil }
	runAgent(t, a)
	approveAll(t, c, a)
	n, err := c.svc.Registry().Get(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if ab := n.Effective().AndroidBuild; ab == nil || ab.Java != "21" || len(ab.Platforms) != 1 {
		t.Fatalf("android-build offered: %+v", n.Effective())
	}
	if got := n.Effective().Summary(); !strings.Contains(got, "android-build") {
		t.Errorf("summary %q", got)
	}

	ctx := context.Background()
	src := map[string]string{"gradlew": fakeGradlew, "settings.gradle": "", "app/build.gradle": ""}
	props := map[string]string{"client_id": "fake-client", "client_secret": "fake-secret", "api_key": "fake-key", "site_url": "https://example.com", "pairing_code": "code123"}
	var apk bytes.Buffer
	res, err := c.svc.AndroidBuild(ctx, a.ID(), nodeproto.AndroidBuildParams{Source: tarGz(t, src, "gradlew"), Properties: props}, &apk)
	if err != nil || res.Error != "" {
		t.Fatalf("build: %+v, %v", res, err)
	}
	if apk.Len() < 9000000 || int64(apk.Len()) != res.APK.Size {
		t.Fatalf("the APK has %d bytes, the node staged %d", apk.Len(), res.APK.Size)
	}
	head := apk.String()[:2048]
	for _, want := range []string{"APK\n", "api_key=fake-key\n", "client_secret=fake-secret\n", "pairing_code=code123\n", "site_url=https://example.com\n", "java=" + tc.JavaHome, "sdk=" + tc.SDK} {
		if !strings.Contains(head, want) {
			t.Errorf("the APK lacks %q:\n%s", want, head[:200])
		}
	}
	// The build's folder, with the keys in it, is gone; so is the staged
	// APK, once the core has it.
	var dir string
	for _, line := range strings.Split(head, "\n") {
		if d, ok := strings.CutPrefix(line, "dir="); ok {
			dir = d
		}
	}
	if dir == "" {
		t.Fatal("the fake build said not where it ran")
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("the build folder %s is still there: %v", filepath.Dir(dir), err)
	}
	waitFor(t, "the staged APK to be dropped", func() bool {
		ents, _ := os.ReadDir(filepath.Join(a.Dir, androidTransfers))
		return len(ents) == 0
	})

	// A failed build: its error and the end of Gradle's output.
	src["fail"] = ""
	apk.Reset()
	res, err = c.svc.AndroidBuild(ctx, a.ID(), nodeproto.AndroidBuildParams{Source: tarGz(t, src, "gradlew"), Properties: props}, &apk)
	if err != nil || !strings.Contains(res.Error, "the build failed") || !strings.Contains(res.Log, "FAILURE: Build failed") || apk.Len() != 0 {
		t.Fatalf("failed build: %+v, %v", res, err)
	}

	// A property that would break the file is refused on both sides.
	bad := map[string]string{"client_id": "a\nsite_url=https://evil.example"}
	if _, err := c.svc.AndroidBuild(ctx, a.ID(), nodeproto.AndroidBuildParams{Source: tarGz(t, src, "gradlew"), Properties: bad}, &apk); !isCode(err, nodeproto.CodeBadRequest) {
		t.Errorf("a property with a newline: %v", err)
	}
	if e := a.handle(ctx, nodeproto.Request{Method: nodeproto.MethodAndroidBuild, Params: []byte(`{"source":"eA==","properties":{"k":"a\nb"}}`)}).Error; e == nil || e.Code != nodeproto.CodeBadRequest {
		t.Errorf("the node took a property with a newline: %v", e)
	}
}

func TestAndroidBuildNeedsTheToolchainAndTheApproval(t *testing.T) {
	c := startCore(t, t.TempDir())
	a := joinNode(t, c, nodeproto.Capabilities{Host: true})
	runAgent(t, a)
	approveAll(t, c, a)
	n, _ := c.svc.Registry().Get(a.ID())
	if n.Declared.AndroidBuild != nil {
		t.Fatal("a node without a JDK offers android-build")
	}
	var apk bytes.Buffer
	_, err := c.svc.AndroidBuild(context.Background(), a.ID(), nodeproto.AndroidBuildParams{Source: []byte("x")}, &apk)
	if !isCode(err, nodeproto.CodeDenied) {
		t.Errorf("a build on a node without android-build: %v", err)
	}
	// "android_build": false keeps it from offering it.
	off := false
	a.Config.AndroidBuild = &off
	a.AndroidToolchain = func() (platform.AndroidToolchain, error) { return fakeToolchain(t), nil }
	a.androidCheck(true)
	if a.declared().AndroidBuild != nil {
		t.Error(`"android_build": false offers it`)
	}
}

func TestConfigRefusesDeclaredAndroidBuild(t *testing.T) {
	c := Config{Core: "core:8766", Name: "n", Capabilities: nodeproto.Capabilities{AndroidBuild: &nodeproto.AndroidBuildCap{}}}
	if err := c.Check(); err == nil {
		t.Error("node.json may not declare android_build itself")
	}
}

func TestUnpackSourceStaysInside(t *testing.T) {
	for name, hdr := range map[string]*tar.Header{
		"a path out":     {Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"an absolute":    {Name: "/tmp/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"a symlink":      {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"a device":       {Name: "dev", Typeflag: tar.TypeChar},
		"too large file": {Name: "big", Typeflag: tar.TypeReg, Size: maxAndroidSourceSize + 1},
	} {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		_ = tw.WriteHeader(hdr)
		_ = tw.Close()
		_ = zw.Close()
		dir := filepath.Join(t.TempDir(), "src")
		if err := unpackSource(buf.Bytes(), dir); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	dir := filepath.Join(t.TempDir(), "src")
	if err := unpackSource(tarGz(t, map[string]string{"gradlew": "#!/bin/sh\n", "a/b.txt": "b"}, "gradlew"), dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "gradlew"))
	if err != nil || runtime.GOOS != "windows" && st.Mode()&0o100 == 0 {
		t.Errorf("gradlew: %v, %v", st, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a", "b.txt")); string(data) != "b" {
		t.Errorf("a/b.txt = %q", data)
	}
}

func TestPruneAndroid(t *testing.T) {
	dir := t.TempDir()
	old, fresh := filepath.Join(dir, "old.apk"), filepath.Join(dir, "fresh.apk")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * androidTransferTTL)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	pruneAndroid(dir, time.Now(), androidTransferTTL)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old APK stayed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh APK went")
	}
}

// TestRealAndroidBuild builds plaud-android/ of this checkout with the
// real Gradle through a node agent, with fake developer keys, where this
// machine has a JDK 17+ and the Android SDK. It takes minutes and the
// network, so it runs only when asked: FRIDOLIN_ANDROID_BUILD_TEST=1,
// never in CI and never with -short.
func TestRealAndroidBuild(t *testing.T) {
	if testing.Short() || os.Getenv("CI") != "" || os.Getenv("FRIDOLIN_ANDROID_BUILD_TEST") != "1" {
		t.Skip("set FRIDOLIN_ANDROID_BUILD_TEST=1 to build the real app (minutes, network)")
	}
	tc, err := platform.FindAndroidToolchain(platform.AndroidLookup{})
	if err != nil {
		t.Skipf("no toolchain here: %v", err)
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "plaud-android"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		switch rel {
		case ".gradle", "build", "app/build", "app/libs", "local.properties":
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := startCore(t, t.TempDir())
	a := joinNode(t, c, nodeproto.Capabilities{Host: true})
	a.AndroidToolchain = func() (platform.AndroidToolchain, error) { return tc, nil }
	runAgent(t, a)
	approveAll(t, c, a)
	props := map[string]string{"client_id": "fake-client-id", "client_secret": "fake-client-secret", "api_key": "fake-api-key", "site_url": "https://fridolin.example.com", "pairing_code": "fake-pairing-code"}
	var apk bytes.Buffer
	start := time.Now()
	res, err := c.svc.AndroidBuild(context.Background(), a.ID(), nodeproto.AndroidBuildParams{Source: tarGz(t, files, "gradlew"), Properties: props}, &apk)
	if err != nil || res.Error != "" {
		t.Fatalf("the real build: %v %s\n%s", err, res.Error, res.Log)
	}
	if apk.Len() < 1<<20 || !bytes.HasPrefix(apk.Bytes(), []byte("PK")) {
		t.Fatalf("the APK is %d bytes, starting %q", apk.Len(), apk.Bytes()[:min(4, apk.Len())])
	}
	t.Logf("built a %d-byte APK with JDK %s in %v", apk.Len(), res.Java, time.Since(start).Round(time.Second))
}
