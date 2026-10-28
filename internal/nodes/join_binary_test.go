package nodes

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// joinBinaries writes a binaries folder of version with a build for each
// of platforms, whose content names it, and returns the folder and the
// builds' contents by platform.
func joinBinaries(t *testing.T, version string, platforms ...string) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	builds := map[string][]byte{}
	var entries []string
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		content := []byte("fridolin " + version + " for " + p + "\n" + strings.Repeat("x", 4096))
		builds[p] = content
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(content)
		_ = zw.Close()
		file := "fridolin-" + goos + "-" + goarch + ".gz"
		if err := os.WriteFile(filepath.Join(dir, file), gz.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		entries = append(entries, fmt.Sprintf(`{"os":%q,"arch":%q,"file":%q,"sha256":%q,"size":%d}`, goos, goarch, file, hex.EncodeToString(sum[:]), len(content)))
	}
	manifest := fmt.Sprintf(`{"version":%q,"binaries":[%s]}`, version, strings.Join(entries, ","))
	if err := os.WriteFile(filepath.Join(dir, BinariesManifest), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, builds
}

// joinClock is a clock a test moves.
type joinClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *joinClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *joinClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// serveJoins serves a node port of version v2.0.0 with the binaries in
// bins ("" for none) and returns it with its address.
func serveJoins(t *testing.T, bins string, clock *joinClock) (*Service, string) {
	t.Helper()
	svc, err := New(Options{Dir: t.TempDir(), Version: "v2.0.0", BinariesDir: bins, Now: clock.Now, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc, svc.Addr()
}

func joinGet(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestJoinBinaryIsServedForAValidCodeWithoutUsingItUp(t *testing.T) {
	bins, builds := joinBinaries(t, "v2.0.0", "darwin/arm64", "windows/amd64")
	clock := &joinClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	svc, addr := serveJoins(t, bins, clock)
	code, _, err := svc.NewJoinCodeFrom(JoinedFromChat)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr + "/join/" + code + "/"

	resp, body := joinGet(t, http.MethodGet, base+"fridolin.sha256?os=darwin&arch=arm64")
	sum := sha256.Sum256(builds["darwin/arm64"])
	want := fmt.Sprintf("%s %d v2.0.0\n", hex.EncodeToString(sum[:]), len(builds["darwin/arm64"]))
	if resp.StatusCode != http.StatusOK || body != want {
		t.Fatalf("sha256: %d %q, want %q", resp.StatusCode, body, want)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}

	resp, body = joinGet(t, http.MethodGet, base+"fridolin?os=windows&arch=amd64")
	if resp.StatusCode != http.StatusOK || body != string(builds["windows/amd64"]) {
		t.Fatalf("binary: %d, %d bytes", resp.StatusCode, len(body))
	}
	sum = sha256.Sum256(builds["windows/amd64"])
	if resp.Header.Get("X-Fridolin-Sha256") != hex.EncodeToString(sum[:]) || resp.Header.Get("X-Fridolin-Version") != "v2.0.0" ||
		resp.ContentLength != int64(len(builds["windows/amd64"])) || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="fridolin.exe"`) {
		t.Fatalf("headers %v", resp.Header)
	}

	resp, body = joinGet(t, http.MethodHead, base+"fridolin?os=darwin&arch=arm64")
	if resp.StatusCode != http.StatusOK || body != "" || resp.ContentLength != int64(len(builds["darwin/arm64"])) {
		t.Fatalf("HEAD: %d %q %d", resp.StatusCode, body, resp.ContentLength)
	}

	// The download did not spend the code: the node joins with it, and
	// then it is used.
	if ok, err := svc.Registry().JoinCodeValid(code); !ok || err != nil {
		t.Fatalf("the code after the download: %v %v", ok, err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	pub := key.Public().(ed25519.PublicKey)
	if _, err := svc.Registry().Hello(nodeproto.Hello{DeviceID: nodeproto.DeviceIDFor(pub), PublicKey: pub, Name: "laptop", OS: "darwin", Arch: "arm64", JoinCode: code}, "127.0.0.1"); err != nil {
		t.Fatalf("joining with the code after the download: %v", err)
	}
	if resp, body := joinGet(t, http.MethodGet, base+"fridolin?os=darwin&arch=arm64"); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "unknown, used or expired") {
		t.Fatalf("a used code: %d %q", resp.StatusCode, body)
	}
}

func TestJoinBinaryRefusals(t *testing.T) {
	bins, _ := joinBinaries(t, "v2.0.0", "linux/amd64")
	clock := &joinClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	svc, addr := serveJoins(t, bins, clock)
	code, _, err := svc.NewJoinCodeFrom(JoinedFromDashboard)
	if err != nil {
		t.Fatal(err)
	}
	unknown := "fj-" + strings.Repeat("a", 24)
	for _, tc := range []struct {
		name, method, path string
		status             int
		body               string
	}{
		{"unknown code", "GET", "/join/" + unknown + "/fridolin?os=linux&arch=amd64", http.StatusForbidden, "unknown, used or expired"},
		{"not a code", "GET", "/join/../fridolin?os=linux&arch=amd64", http.StatusForbidden, "unknown, used or expired"},
		{"no code", "GET", "/join//fridolin?os=linux&arch=amd64", http.StatusForbidden, "unknown, used or expired"},
		{"bad os", "GET", "/join/" + code + "/fridolin?os=plan9&arch=amd64", http.StatusBadRequest, "no node binary for that platform"},
		{"no arch", "GET", "/join/" + code + "/fridolin?os=linux", http.StatusBadRequest, "no node binary for that platform"},
		{"a platform without a build", "GET", "/join/" + code + "/fridolin?os=windows&arch=arm64", http.StatusServiceUnavailable, "no node binary for windows/arm64"},
		{"another file", "GET", "/join/" + code + "/node.key?os=linux&arch=amd64", http.StatusNotFound, ""},
		{"the codes", "GET", "/join/", http.StatusNotFound, ""},
		{"beside the prefix", "GET", "/joinx", http.StatusNotFound, ""},
		{"POST", "POST", "/join/" + code + "/fridolin?os=linux&arch=amd64", http.StatusMethodNotAllowed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := joinGet(t, tc.method, "http://"+addr+tc.path)
			if resp.StatusCode != tc.status || !strings.Contains(body, tc.body) {
				t.Fatalf("%d %q, want %d with %q", resp.StatusCode, body, tc.status, tc.body)
			}
			if strings.Contains(body, code) {
				t.Fatal("the answer names the code")
			}
		})
	}

	clock.add(JoinCodeTTL)
	if resp, body := joinGet(t, http.MethodGet, "http://"+addr+"/join/"+code+"/fridolin.sha256?os=linux&arch=amd64"); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "send !join again") {
		t.Fatalf("an expired code: %d %q", resp.StatusCode, body)
	}
}

func TestJoinBinaryWithoutBinaries(t *testing.T) {
	clock := &joinClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	svc, addr := serveJoins(t, "", clock)
	code, _, err := svc.NewJoinCodeFrom(JoinedFromChat)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := joinGet(t, http.MethodGet, "http://"+addr+"/join/"+code+"/fridolin.sha256?os=linux&arch=arm64")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "no folder of node binaries") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	// An unknown code learns nothing about the binaries.
	resp, body = joinGet(t, http.MethodGet, "http://"+addr+"/join/fj-"+strings.Repeat("b", 24)+"/fridolin.sha256?os=linux&arch=arm64")
	if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "binaries") {
		t.Fatalf("an unknown code: %d %q", resp.StatusCode, body)
	}
}

func TestJoinBinaryIsRateLimited(t *testing.T) {
	clock := &joinClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	_, addr := serveJoins(t, "", clock)
	url := "http://" + addr + "/join/fj-" + strings.Repeat("c", 24) + "/fridolin?os=linux&arch=amd64"
	for i := range joinPerHost {
		if resp, _ := joinGet(t, http.MethodGet, url); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	resp, body := joinGet(t, http.MethodGet, url)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" || !strings.Contains(body, "too many") {
		t.Fatalf("over the limit: %d %q", resp.StatusCode, body)
	}
	// The WebSocket path is not limited by it.
	if resp, _ := joinGet(t, http.MethodGet, "http://"+addr+nodeproto.Path); resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("the node path is limited too")
	}
	clock.add(joinWindow)
	if resp, _ := joinGet(t, http.MethodGet, url); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("after the window: %d", resp.StatusCode)
	}
}

func TestJoinLimiter(t *testing.T) {
	var l joinLimiter
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	hosts := 0
	for n := 0; n < joinPerWindow; n++ {
		if n%joinPerHost == 0 {
			hosts++
		}
		if !l.allow(fmt.Sprintf("100.64.0.%d", hosts), now) {
			t.Fatalf("request %d refused", n)
		}
	}
	if l.allow("100.64.1.1", now) {
		t.Fatal("a new host over the limit of all hosts")
	}
	if !l.allow("100.64.1.1", now.Add(joinWindow)) {
		t.Fatal("a new window")
	}
	for range joinStreams {
		if !l.startStream() {
			t.Fatal("a stream refused")
		}
	}
	if l.startStream() {
		t.Fatal("one stream too many")
	}
	l.endStream()
	if !l.startStream() {
		t.Fatal("a stream given back")
	}
}
