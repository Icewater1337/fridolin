package nodeagent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// Android builds on the node (docs/NODES.md, "android-build"): the agent
// declares android_build while it finds a JDK 17 or newer and the Android
// SDK, and builds what the core sends with Gradle in a temp folder, which
// goes with the build properties (the Plaud developer keys among them)
// when the build ends. The APK is staged in the state dir until the core
// has read it and drops it, or for androidTransferTTL at most: it carries
// the same keys.

// androidTransfers is the folder in the state dir where built APKs wait
// for the core.
const androidTransfers = "android-transfers"

// Timing of android-build.
const (
	// androidCheckEvery is how often the agent looks for the toolchain
	// again (at most; it looks at each check of FrIDE that is due).
	androidCheckEvery = 5 * time.Minute
	// androidTransferTTL is how long a built APK waits unread.
	androidTransferTTL = time.Hour
	// Bounds of the project the core sends, unpacked.
	maxAndroidFiles      = 4096
	maxAndroidSourceSize = 64 << 20
)

// android is the agent's android-build.
type android struct {
	mu        sync.Mutex
	cap       *nodeproto.AndroidBuildCap
	tc        platform.AndroidToolchain
	checked   time.Time
	building  sync.Mutex
	lastError string
}

// androidOffered reports whether node.json lets the agent offer
// android-build: unless "android_build": false, and never on Windows,
// where the app's source has no gradlew.bat.
func (a *Agent) androidOffered() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return a.Config.AndroidBuild == nil || *a.Config.AndroidBuild
}

// findAndroidToolchain is where an agent without AndroidToolchain looks;
// the package's tests look nowhere.
var findAndroidToolchain = func() (platform.AndroidToolchain, error) {
	return platform.FindAndroidToolchain(platform.AndroidLookup{})
}

func (a *Agent) findToolchain() (platform.AndroidToolchain, error) {
	if a.AndroidToolchain != nil {
		return a.AndroidToolchain()
	}
	return findAndroidToolchain()
}

// androidCheck looks for the toolchain, when it is due or force says so,
// and reports whether what the node declares changed.
func (a *Agent) androidCheck(force bool) bool {
	ad := &a.android
	ad.mu.Lock()
	due := force || ad.checked.IsZero() || time.Since(ad.checked) >= androidCheckEvery
	ad.mu.Unlock()
	if !due {
		return false
	}
	var next *nodeproto.AndroidBuildCap
	var tc platform.AndroidToolchain
	msg := ""
	if a.androidOffered() {
		var err error
		tc, err = a.findToolchain()
		if err == nil {
			next = &nodeproto.AndroidBuildCap{Platforms: tc.Platforms}
			if tc.JavaMajor > 0 {
				next.Java = strconv.Itoa(tc.JavaMajor)
			}
			if next.Check() != nil {
				next.Platforms = nil
			}
		} else {
			msg = err.Error()
		}
	}
	ad.mu.Lock()
	defer ad.mu.Unlock()
	ad.checked = time.Now()
	old := ad.cap
	ad.cap, ad.tc = next, tc
	if msg != ad.lastError && a.androidOffered() && msg != "" && old != nil {
		a.logf("node: no longer offering android-build: %s", msg)
	}
	ad.lastError = msg
	changed := (old == nil) != (next == nil) || old != nil && (old.Java != next.Java || !slices.Equal(old.Platforms, next.Platforms))
	if changed && next != nil {
		a.logf("node: found JDK %s and the Android SDK (%s); offering android-build to the core", orUnknown(next.Java), strings.Join(next.Platforms, ", "))
	}
	return changed
}

func orUnknown(s string) string {
	if s == "" {
		return "(version unknown)"
	}
	return s
}

// androidDeclared is the android_build the node declares now.
func (a *Agent) androidDeclared() *nodeproto.AndroidBuildCap {
	a.android.mu.Lock()
	defer a.android.mu.Unlock()
	if a.android.cap == nil {
		return nil
	}
	c := *a.android.cap
	c.Platforms = slices.Clone(c.Platforms)
	return &c
}

// declChanged tells the session that what the node declares changed.
func (a *Agent) declChanged() {
	a.fride.init()
	select {
	case a.fride.changed <- struct{}{}:
	default:
	}
}

// androidCall serves an android.* method.
func (a *Agent) androidCall(ctx context.Context, method string, raw json.RawMessage) (any, *nodeproto.Error) {
	switch method {
	case nodeproto.MethodAndroidBuild:
		var bp nodeproto.AndroidBuildParams
		if err := json.Unmarshal(raw, &bp); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return a.androidBuild(ctx, bp)
	case nodeproto.MethodAndroidRead, nodeproto.MethodAndroidDrop:
		var p nodeproto.FrideChunkParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
			}
		}
		path, e := a.androidTransferPath(p.ID)
		if e != nil {
			return nil, e
		}
		if method == nodeproto.MethodAndroidDrop {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
			}
			return struct{}{}, nil
		}
		return readChunk(path, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

func (a *Agent) androidTransferPath(id string) (string, *nodeproto.Error) {
	if !transferIDRE.MatchString(id) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "transfer id %q", id)
	}
	if a.Dir == "" {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "the agent has no state dir")
	}
	dir := filepath.Join(a.Dir, androidTransfers)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	return filepath.Join(dir, id+".apk"), nil
}

// pruneAndroid removes the APKs older than ttl.
func pruneAndroid(dir string, now time.Time, ttl time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > ttl {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// androidBuild builds the project of p in a temp folder and stages its
// APK. A build Gradle failed is a result with the end of its output.
func (a *Agent) androidBuild(ctx context.Context, p nodeproto.AndroidBuildParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if !a.android.building.TryLock() {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "this node is building an app already; try again when it is done")
	}
	defer a.android.building.Unlock()
	// The toolchain as it is now, not as the last check found it.
	if a.androidCheck(true) {
		a.declChanged()
	}
	a.android.mu.Lock()
	tc, ok := a.android.tc, a.android.cap != nil
	a.android.mu.Unlock()
	if !ok {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "this node has no JDK 17 or newer and Android SDK any more")
	}
	start := time.Now()
	tmp, err := os.MkdirTemp("", "fridolin-android-*")
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	// Everything the build was given or made goes, the keys with it.
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "src")
	if err := unpackSource(p.Source, src); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the source: %v", err)
	}
	gradlew := filepath.Join(src, "gradlew")
	if st, err := os.Lstat(gradlew); err != nil || !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the source has no gradlew at its root")
	}
	props := filepath.Join(tmp, "build.properties")
	if err := os.WriteFile(props, propertiesFile(p.Properties), 0o600); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	out := filepath.Join(tmp, "build")
	bctx, cancel := context.WithTimeout(ctx, time.Duration(nodeproto.AndroidBuildTimeoutSeconds)*time.Second)
	defer cancel()
	// Building what the core sends is this capability's purpose, which
	// the owner approved: the build's scripts are code, run as this user.
	cmd := exec.CommandContext(bctx, gradlew, "--no-daemon", "--console=plain", "-p", src,
		"--project-cache-dir", filepath.Join(tmp, "gradle"),
		"-PfridolinConfig="+props, "-PfridolinBuildDir="+out,
		// No Kotlin daemon outliving the build in its folder.
		"-Pkotlin.compiler.execution.strategy=in-process",
		"assembleDebug")
	cmd.Dir = src
	cmd.Env = androidEnv(cmd.Environ(), tc)
	log := &cappedBuffer{max: 1 << 20}
	cmd.Stdout, cmd.Stderr = log, log
	platform.IsolateProcessGroup(cmd)
	java := ""
	if tc.JavaMajor > 0 {
		java = strconv.Itoa(tc.JavaMajor)
	}
	a.logf("node: building an Android app (%d bytes of source) with JDK %s", len(p.Source), orUnknown(java))
	res := nodeproto.AndroidBuildResult{Java: java}
	if err := cmd.Run(); err != nil {
		res.DurationMS = time.Since(start).Milliseconds()
		res.Error = "the build failed: " + err.Error()
		if errors.Is(bctx.Err(), context.DeadlineExceeded) {
			res.Error = fmt.Sprintf("the build did not finish within %v", time.Duration(nodeproto.AndroidBuildTimeoutSeconds)*time.Second)
		}
		res.Log = logTail(log.buf.String(), 40, nodeproto.MaxAndroidLogBytes)
		a.logf("node: the Android build failed after %v", time.Since(start).Round(time.Second))
		return res, nil
	}
	tr, e := a.stageAPK(filepath.Join(out, "app", "outputs", "apk", "debug", "app-debug.apk"))
	if e != nil {
		return nil, e
	}
	res.APK, res.DurationMS = &tr, time.Since(start).Milliseconds()
	a.logf("node: built an Android app in %v (%d bytes)", time.Since(start).Round(time.Second), tr.Size)
	return res, nil
}

// stageAPK copies the built APK to a new transfer.
func (a *Agent) stageAPK(apk string) (nodeproto.FrideTransfer, *nodeproto.Error) {
	in, err := os.Open(apk)
	if err != nil {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "the build made no APK")
	}
	defer in.Close()
	id := newTransferID()
	path, e := a.androidTransferPath(id)
	if e != nil {
		return nodeproto.FrideTransfer{}, e
	}
	pruneAndroid(filepath.Dir(path), time.Now(), androidTransferTTL)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), in)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || n == 0 {
		_ = os.Remove(path)
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "staging the APK: %v", err)
	}
	return nodeproto.FrideTransfer{ID: id, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// androidEnv is the build's environment: the agent's, with the
// toolchain's JAVA_HOME and ANDROID_HOME in place of any it had.
func androidEnv(env []string, tc platform.AndroidToolchain) []string {
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "JAVA_HOME" || k == "ANDROID_HOME" || k == "ANDROID_SDK_ROOT"
	})
	return append(out, "JAVA_HOME="+tc.JavaHome, "ANDROID_HOME="+tc.SDK, "ANDROID_SDK_ROOT="+tc.SDK)
}

// propertiesFile is the build's properties as a Java properties file, in
// order of their names; Check keeps out what would break it.
func propertiesFile(props map[string]string) []byte {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k + "=" + props[k] + "\n")
	}
	return b.Bytes()
}

// logTail is the last lines of out, at most limit bytes.
func logTail(out string, lines, limit int) string {
	l := strings.Split(strings.TrimSpace(out), "\n")
	s := strings.Join(l[max(0, len(l)-lines):], "\n")
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	return s
}

// unpackSource unpacks a gzip'd tar into dir: regular files and folders
// only, each inside dir, at most maxAndroidFiles of them and
// maxAndroidSourceSize together. A file keeps whether it is executable.
func unpackSource(data []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	var files int
	var total int64
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(h.Name), "./")
		if name == "" || name == "." {
			continue
		}
		rel := filepath.FromSlash(strings.TrimSuffix(name, "/"))
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("%q lies outside the project", h.Name)
		}
		if files++; files > maxAndroidFiles {
			return fmt.Errorf("more than %d files", maxAndroidFiles)
		}
		target := filepath.Join(dir, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; h.Size < 0 || total > maxAndroidSourceSize {
				return fmt.Errorf("more than %d bytes unpacked", maxAndroidSourceSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if h.Mode&0o111 != 0 {
				mode = 0o700
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%q is not a file or a folder", h.Name)
		}
	}
}
