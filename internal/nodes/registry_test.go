package nodes

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func hello(t *testing.T, code string, caps nodeproto.Capabilities) nodeproto.Hello {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return nodeproto.Hello{DeviceID: nodeproto.DeviceIDFor(pub), PublicKey: pub, Name: "laptop", OS: "windows", Arch: "amd64", Version: "v", Capabilities: caps, JoinCode: code}
}

func TestJoinCodes(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	r, err := OpenRegistry(dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	code, exp, err := r.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "fj-") || len(code) != 27 || !exp.Equal(clk.t.Add(JoinCodeTTL)) {
		t.Fatalf("code %q expires %v", code, exp)
	}
	data, err := os.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), strings.TrimPrefix(code, "fj-")) {
		t.Fatal("nodes.json holds the code itself")
	}
	if _, err := r.Hello(hello(t, "", nodeproto.Capabilities{}), "a"); !errors.Is(err, ErrBadJoinCode) {
		t.Fatalf("an unknown device without a code: %v", err)
	}
	if _, err := r.Hello(hello(t, "fj-nope", nodeproto.Capabilities{}), "a"); !errors.Is(err, ErrBadJoinCode) {
		t.Fatalf("a wrong code: %v", err)
	}
	h := hello(t, code, nodeproto.Capabilities{Host: true})
	n, err := r.Hello(h, "a")
	if err != nil {
		t.Fatal(err)
	}
	if n.Device != nodeproto.DevicePending || n.Commands() != nodeproto.CommandsPending || !n.Effective().Empty() || strings.Join(n.Tags, ",") != "windows" {
		t.Fatalf("a new node: %+v", n)
	}
	// The same device again needs no code; another one may not reuse it.
	if _, err := r.Hello(nodeproto.Hello{DeviceID: h.DeviceID, PublicKey: h.PublicKey, Name: "renamed", OS: "windows"}, "b"); err != nil {
		t.Fatalf("a known device: %v", err)
	}
	if _, err := r.Hello(hello(t, code, nodeproto.Capabilities{}), "a"); !errors.Is(err, ErrBadJoinCode) {
		t.Fatalf("a used code: %v", err)
	}
	// Expiry.
	code2, _, err := r.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(JoinCodeTTL)
	if _, err := r.Hello(hello(t, code2, nodeproto.Capabilities{}), "a"); !errors.Is(err, ErrBadJoinCode) {
		t.Fatalf("an expired code: %v", err)
	}
}

func TestApprovalStagesAndRevoke(t *testing.T) {
	r, err := OpenRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	caps := nodeproto.Capabilities{Exec: &nodeproto.ExecCap{Allow: nodeproto.DefaultExecAllow("linux")}, Host: true}
	h := hello(t, code, caps)
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApproveCommands(h.DeviceID); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("commands before the device: %v", err)
	}
	if _, err := r.ApproveDevice("nope"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("an unknown node: %v", err)
	}
	n, err := r.ApproveDevice(h.DeviceID)
	if err != nil || n.Device != nodeproto.DeviceApproved || n.Commands() != nodeproto.CommandsPending {
		t.Fatalf("%+v %v", n, err)
	}
	if n, err = r.ApproveCommands(h.DeviceID); err != nil || n.Commands() != nodeproto.CommandsApproved || !n.Effective().Host {
		t.Fatalf("%+v %v", n, err)
	}
	// The declaration grows: pending again, the approved part still there.
	h.JoinCode = ""
	h.Capabilities.Notify = true
	if n, err = r.Hello(h, ""); err != nil || n.Commands() != nodeproto.CommandsPending || !n.Effective().Host || n.Effective().Notify {
		t.Fatalf("a grown declaration: %+v %v", n, err)
	}
	// It shrinks: approved, without asking.
	h.Capabilities = nodeproto.Capabilities{Host: true}
	if n, err = r.Hello(h, ""); err != nil || n.Commands() != nodeproto.CommandsApproved || n.Effective().Exec != nil {
		t.Fatalf("a narrower declaration: %+v %v", n, err)
	}
	if n, err = r.Revoke(h.DeviceID); err != nil || n.Device != nodeproto.DeviceRevoked || !n.Effective().Empty() {
		t.Fatalf("revoked: %+v %v", n, err)
	}
	if _, err := r.Hello(h, ""); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a revoked node says hello: %v", err)
	}
	if _, err := r.ApproveDevice(h.DeviceID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("approving a revoked node: %v", err)
	}
	if st := n.State(); st.Device != nodeproto.DeviceRevoked || !st.Approved.Empty() {
		t.Fatalf("a revoked node's state: %+v", st)
	}
}

func TestTagsAndHandEdits(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	h := hello(t, code, nodeproto.Capabilities{})
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	n, err := r.SetTags(h.DeviceID, []string{" GPU", "docker", "gpu", "memory:32gb"})
	if err != nil || strings.Join(n.Tags, ",") != "docker,gpu,memory:32gb" {
		t.Fatalf("%v %v", n.Tags, err)
	}
	for _, bad := range [][]string{{"no spaces"}, {"-dash"}, {strings.Repeat("x", 33)}, make([]string, MaxTags+1)} {
		if _, err := r.SetTags(h.DeviceID, bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("tags %q: %v", bad, err)
		}
	}
	// Edited by hand while the registry is open: the next read sees it.
	var f registryFile
	data, _ := os.ReadFile(r.Path())
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f.Nodes[0].Tags = []string{"xcode", "macos"}
	f.Nodes[0].Name = "edited and longer than before"
	data, _ = json.Marshal(f)
	if err := os.WriteFile(r.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(h.DeviceID)
	if err != nil || strings.Join(got.Tags, ",") != "macos,xcode" {
		t.Fatalf("after a hand edit: %v %v", got.Tags, err)
	}
	// And it survives the next change.
	if _, err := r.ApproveDevice(h.DeviceID); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = r2.Get(h.DeviceID)
	if strings.Join(got.Tags, ",") != "macos,xcode" || got.Device != nodeproto.DeviceApproved {
		t.Fatalf("reopened: %+v", got)
	}
}

func TestSeenWritesAtMostEveryMinute(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	r, err := OpenRegistry(t.TempDir(), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	h := hello(t, code, nodeproto.Capabilities{})
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.Path())
	clk.t = clk.t.Add(20 * time.Second)
	if err := r.Seen(h.DeviceID, nodeproto.HostStats{CPUs: 8}, false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(r.Path()); string(after) != string(before) {
		t.Error("a heartbeat 20s after the last write wrote the file")
	}
	if n, _ := r.Get(h.DeviceID); n.Resources == nil || n.Resources.CPUs != 8 || !n.LastSeen.Equal(clk.t) {
		t.Errorf("in memory: %+v", n)
	}
	clk.t = clk.t.Add(time.Minute)
	if err := r.Seen(h.DeviceID, nodeproto.HostStats{CPUs: 8}, false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(r.Path()); string(after) == string(before) {
		t.Error("a heartbeat a minute later did not write the file")
	}
}

func TestBrokenRegistryFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+RegistryFile, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegistry(dir, nil); err == nil {
		t.Fatal("a broken nodes.json opens")
	}
}

// fileLastSeen reads the node's last_seen as nodes.json holds it, as
// another process (fridolin doctor) does.
func fileLastSeen(t *testing.T, r *Registry, id string) time.Time {
	t.Helper()
	data, err := os.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	n := f.find(id)
	if n == nil || n.LastSeen == nil {
		t.Fatalf("no last_seen of %s in the file", id)
	}
	return *n.LastSeen
}

// Heartbeats every 20 seconds, as agents send them, reach the file at
// least once a minute: the file must not keep the time of the connect.
func TestSeenReachesTheFileWithFrequentHeartbeats(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 10, 9, 0, 0, time.UTC)}
	r, err := OpenRegistry(t.TempDir(), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	h := hello(t, code, nodeproto.Capabilities{})
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	for range 3 * 60 / 20 {
		clk.t = clk.t.Add(20 * time.Second)
		if err := r.Seen(h.DeviceID, nodeproto.HostStats{CPUs: 8, MemAvailable: 1 << 30}, false); err != nil {
			t.Fatal(err)
		}
		if got := fileLastSeen(t, r, h.DeviceID); clk.t.Sub(got) >= SeenWriteEvery {
			t.Fatalf("at %v the file says last seen %v", clk.t, got)
		}
	}
	data, _ := os.ReadFile(r.Path())
	if !strings.Contains(string(data), `"resources"`) {
		t.Errorf("the file has no resources: %s", data)
	}
}

// Flush writes the last heartbeat kept in memory, and only that.
func TestFlushWritesWhatSeenKept(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	r, err := OpenRegistry(t.TempDir(), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.NewJoinCode()
	h := hello(t, code, nodeproto.Capabilities{})
	if _, err := r.Hello(h, ""); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(20 * time.Second)
	if err := r.Seen(h.DeviceID, nodeproto.HostStats{CPUs: 8}, false); err != nil {
		t.Fatal(err)
	}
	if got := fileLastSeen(t, r, h.DeviceID); got.Equal(clk.t) {
		t.Fatal("a heartbeat 20s after the connect wrote the file")
	}
	if err := r.Flush(h.DeviceID); err != nil {
		t.Fatal(err)
	}
	if got := fileLastSeen(t, r, h.DeviceID); !got.Equal(clk.t) {
		t.Fatalf("after Flush the file says %v, want %v", got, clk.t)
	}
	st, _ := os.Stat(r.Path())
	if err := r.Flush(h.DeviceID); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(r.Path()); !st2.ModTime().Equal(st.ModTime()) || st2.Size() != st.Size() {
		t.Error("a second Flush with nothing new wrote the file")
	}
	if err := r.Flush("no-such-node"); err != nil {
		t.Errorf("Flush of an unknown node: %v", err)
	}
}

// A node keeps where its join code came from, for the owner's alert, and
// when what it offers last changed.
func TestJoinedViaAndDeclaredAt(t *testing.T) {
	clk := &clock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	r, err := OpenRegistry(t.TempDir(), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{JoinedFromChat, JoinedFromDashboard, ""} {
		code, _, err := r.NewJoinCodeFrom(origin)
		if err != nil {
			t.Fatal(err)
		}
		h := hello(t, code, nodeproto.Capabilities{Host: true})
		n, err := r.Hello(h, "a")
		if err != nil || n.JoinedVia != origin || n.DeclaredAt == nil || !n.DeclaredAt.Equal(clk.t) {
			t.Fatalf("joined with a code from %q: %+v %v", origin, n, err)
		}
		if origin != JoinedFromChat {
			continue
		}
		// The same offer again keeps the time; another one moves it.
		clk.t = clk.t.Add(time.Hour)
		if n, _ = r.Hello(nodeproto.Hello{DeviceID: h.DeviceID, PublicKey: h.PublicKey, Name: "laptop", Capabilities: h.Capabilities}, "a"); !n.DeclaredAt.Equal(clk.t.Add(-time.Hour)) {
			t.Errorf("the same offer moved DeclaredAt: %v", n.DeclaredAt)
		}
		if n, _ = r.Declare(h.DeviceID, nodeproto.Capabilities{Host: true, Notify: true}); !n.DeclaredAt.Equal(clk.t) {
			t.Errorf("another offer kept DeclaredAt: %v", n.DeclaredAt)
		}
	}
}
