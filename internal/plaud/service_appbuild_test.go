package plaud

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// appSourceDir is a plaud-android/ with what a build there leaves.
func appSourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"gradlew": "#!/bin/sh\n", "settings.gradle": "", "app/build.gradle": "android {\n    defaultConfig {\n        versionCode 7\n        versionName '0.7'\n    }\n}\n", "app/src/main/A.kt": "",
		"build/x": "", "app/build/y": "", ".gradle/z": "", "app/libs/plaud-sdk.aar": "", "local.properties": "sdk.dir=x", "app/old.apk": "",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
	}
	slices.Sort(names)
	return names
}

func TestAppSourceArchiveLeavesBuildsOut(t *testing.T) {
	data, err := AppSourceArchive(appSourceDir(t))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(tarNames(t, data), " ")
	if want := "app/build.gradle app/src/main/A.kt gradlew settings.gradle"; got != want {
		t.Errorf("archive holds %s, want %s", got, want)
	}
}

// nodeBuild is a node that builds the app, as the bridge reaches it.
type nodeBuild struct {
	mu     sync.Mutex
	id     string
	props  map[string]string
	source []byte
	fail   bool
}

func (n *nodeBuild) build(_ context.Context, id string, source []byte, props map[string]string, apk io.Writer) (string, error) {
	n.mu.Lock()
	n.id, n.props, n.source = id, props, source
	fail := n.fail
	n.mu.Unlock()
	if fail {
		return "FAILURE: Build failed with an exception.", errors.New("the build failed: exit status 1")
	}
	_, err := apk.Write(testAPK(props["version_code"]))
	return "", err
}

// testAPKCert is the certificate the test builds are signed with.
var testAPKCert = []byte("fake DER certificate of the builder's debug key")

// testAPK is a test build of versionCode code.
func testAPK(code string) []byte {
	return testutil.SignedAPK(testAPKCert, map[string]string{"AndroidManifest.xml": "versionCode " + code})
}

func appBuildManager(t *testing.T, builders []AppBuilder, node *nodeBuild) (*plaudServiceFixture, *Manager) {
	t.Helper()
	f, _ := plaudService(t, `, "phone_upload": true`)
	opts := f.options
	opts.SiteURL = func() string { return "https://example.com" }
	src := appSourceDir(t)
	opts.AppSource = func() (string, error) { return src, nil }
	opts.AndroidToolchain = func() (string, string, error) {
		return "", "", errors.New("no JDK 17 or newer found to build the app with: install one")
	}
	opts.AppEnviron = func(context.Context, string, string) []string { return nil }
	opts.AppBuilders = func() []AppBuilder { return builders }
	opts.BuildOnNode = node.build
	m := New(opts)
	t.Cleanup(m.Stop)
	if err := m.SaveDeveloper(Developer{ClientID: "fake-id", ClientSecret: "fake-secret", APIKey: "fake-key"}); err != nil {
		t.Fatal(err)
	}
	return f, m
}

func waitAppBuild(t *testing.T, m *Manager) *plaudAppJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b := m.appStatus().Build; b != nil && b.State != "running" {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatal("the build did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Without a JDK and Android SDK here, the app is built on a node that
// builds Android apps, with the site, the keys and a pairing code that
// works.
func TestBuildAppOnANode(t *testing.T) {
	node := &nodeBuild{}
	_, m := appBuildManager(t, []AppBuilder{{ID: "abc123", Name: "mac-mini"}, {ID: "def456", Name: "other"}}, node)
	if st := m.appStatus(); st.BuildsOn != "mac-mini" || st.NoBuilder != "" {
		t.Errorf("status before: builds on %q, no builder %q", st.BuildsOn, st.NoBuilder)
	}
	if err := m.BuildApp(); err != nil {
		t.Fatal(err)
	}
	job := waitAppBuild(t, m)
	if job.State != "done" || job.Node != "mac-mini" || job.Stage != "" || job.PairingExpires.IsZero() {
		t.Fatalf("job %+v", job)
	}
	if data, err := os.ReadFile(m.AppFile()); err != nil || !bytes.Equal(data, testAPK("7")) {
		t.Fatalf("the app: %q, %v", data, err)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.id != "abc123" || node.props["client_secret"] != "fake-secret" || node.props["site_url"] != "https://example.com" || node.props["api_key"] != "fake-key" {
		t.Errorf("built on %s with %v", node.id, node.props)
	}
	if !slices.Contains(tarNames(t, node.source), "gradlew") {
		t.Error("the source sent has no gradlew")
	}
	// The pairing code the app carries is one the bridge takes.
	st, err := m.loadDevices()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(node.props["pairing_code"]))
	if len(st.Pairings) != 1 || st.Pairings[0].Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("pairings %+v", st.Pairings)
	}
}

func TestBuildAppOnANodeThatFails(t *testing.T) {
	_, m := appBuildManager(t, []AppBuilder{{ID: "abc123", Name: "mac-mini"}}, &nodeBuild{fail: true})
	if err := m.BuildApp(); err != nil {
		t.Fatal(err)
	}
	job := waitAppBuild(t, m)
	if job.State != "error" || !strings.Contains(job.Error, "on node mac-mini") || !strings.Contains(job.Log, "FAILURE") {
		t.Errorf("job %+v", job)
	}
	if _, err := os.Stat(m.AppFile()); err == nil {
		t.Error("a failed build left an app")
	}
}

func TestBuildAppWithNothingToBuildIt(t *testing.T) {
	_, m := appBuildManager(t, nil, &nodeBuild{})
	err := m.BuildApp()
	if err == nil || !strings.Contains(err.Error(), "no JDK 17") || !strings.Contains(err.Error(), "android-build") {
		t.Fatalf("BuildApp = %v", err)
	}
	if st := m.appStatus(); st.BuildsOn != "" || !strings.Contains(st.NoBuilder, "android-build") {
		t.Errorf("status: builds on %q, no builder %q", st.BuildsOn, st.NoBuilder)
	}
}
