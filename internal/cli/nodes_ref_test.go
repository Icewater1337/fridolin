package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// nodesCore is a core's node API on a local port, with its registry, for
// fridolin nodes to ask; it returns the config file that points there.
func nodesCore(t *testing.T) (*nodes.Service, string) {
	t.Helper()
	data := t.TempDir()
	layout := config.Layout{Root: data}
	svc, err := nodes.New(nodes.Options{Dir: layout.Nodes(), Version: "v1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	api := &httpapi.NodesAPI{Nodes: func() httpapi.NodesService { return svc }, Enabled: true, Listen: func() string { return "" }, CoreAddress: func() string { return "" }, Version: "v1"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes/{id}/approve", api.ApproveDevice)
	mux.HandleFunc("PUT /api/nodes/{id}/tags", api.Tags)
	mux.HandleFunc("POST /api/nodes/move-check", api.MoveCheck)
	mux.HandleFunc("POST /api/nodes/core-secrets-transfer", api.CoreSecretsTransfer)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := os.WriteFile(layout.DashboardToken(), []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, `{"data_dir": "`+filepath.ToSlash(data)+`", "http_listen": "`+strings.TrimPrefix(srv.URL, "http://")+`"}`)
	return svc, cfg
}

// joinNode joins a node called name to the core's registry and returns
// its id.
func joinNode(t *testing.T, svc *nodes.Service, name string) string {
	t.Helper()
	code, _, err := svc.Registry().NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.Registry().Hello(nodeproto.Hello{DeviceID: nodeproto.DeviceIDFor(pub), PublicKey: pub, Name: name, OS: "linux", Arch: "amd64", Version: "v1", JoinCode: code}, "")
	if err != nil {
		t.Fatal(err)
	}
	return n.ID
}

// TestNodesCommandsTakeNames: fridolin nodes approve, tags, move-check
// and core-secrets-transfer name a node by its id or by its name in any
// case, as the owner and the runbooks do (#251). An unknown name is no
// such node, and a name two nodes have is refused with both listed.
func TestNodesCommandsTakeNames(t *testing.T) {
	svc, cfg := nodesCore(t)
	mini := joinNode(t, svc, "mac-mini")
	droplet := joinNode(t, svc, "fridolin-droplet")
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := nodesCmd(append(args, "--config", cfg), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	// By name, in another case, and by id.
	if code, out, errOut := run("approve", "Mac-Mini"); code != 0 || out != "Approved the device mac-mini ("+mini+").\n" {
		t.Fatalf("approve Mac-Mini: %d %q %q", code, out, errOut)
	}
	if code, out, errOut := run("approve", droplet); code != 0 || !strings.Contains(out, "fridolin-droplet ("+droplet+")") {
		t.Fatalf("approve by id: %d %q %q", code, out, errOut)
	}
	if code, out, errOut := run("tags", "MAC-MINI", "gpu"); code != 0 || out != "mac-mini ("+mini+"): gpu\n" {
		t.Errorf("tags MAC-MINI: %d %q %q", code, out, errOut)
	}
	if n, err := svc.Registry().Get(mini); err != nil || n.Device != nodeproto.DeviceApproved {
		t.Errorf("mac-mini after approve: %+v %v", n, err)
	}

	// The move's pre-flight finds both by name; offline, they block it.
	code, out, errOut := run("move-check", "mac-mini", "Fridolin-Droplet")
	if code != 1 || errOut != "" {
		t.Errorf("move-check: %d %q", code, errOut)
	}
	for _, want := range []string{"From mac-mini (" + mini + ")", "To   fridolin-droplet (" + droplet + ")", "It cannot run:"} {
		if !strings.Contains(out, want) {
			t.Errorf("move-check lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run("core-secrets-transfer", "MAC-MINI", "fridolin-droplet")
	if code != 1 || !strings.Contains(out, "From mac-mini ("+mini+")") || !strings.Contains(out, "To   fridolin-droplet ("+droplet+")") {
		t.Errorf("core-secrets-transfer: %d\n%s", code, out)
	}

	// An unknown name, as before.
	if code, _, errOut := run("move-check", "nope", "fridolin-droplet"); code != 1 || errOut != "fridolin nodes move-check: no such node \"nope\"\n" {
		t.Errorf("move-check nope: %d %q", code, errOut)
	}
	if code, _, errOut := run("approve", "nope"); code != 1 || errOut != "fridolin nodes approve: no such node \"nope\"\n" {
		t.Errorf("approve nope: %d %q", code, errOut)
	}

	// A second mac-mini: the name is refused, and each id still works.
	other := joinNode(t, svc, "MAC-mini")
	for _, args := range [][]string{{"approve", "mac-mini"}, {"move-check", "mac-mini", "fridolin-droplet"}} {
		code, out, errOut := run(args...)
		if code != 1 || out != "" {
			t.Errorf("%v with two mac-minis: %d %q", args, code, out)
		}
		for _, want := range []string{`2 nodes are called "mac-mini"`, "MAC-mini (" + other + ")", "mac-mini (" + mini + ")", "name one by its id"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v: %q lacks %q", args, errOut, want)
			}
		}
	}
	if n, err := svc.Registry().Get(other); err != nil || n.Device != nodeproto.DevicePending {
		t.Errorf("the second mac-mini after a refused approve: %+v %v", n, err)
	}
	if code, out, errOut := run("approve", other); code != 0 || !strings.Contains(out, other) {
		t.Errorf("approve by id: %d %q %q", code, out, errOut)
	}
}
