package nodeagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
)

// writeLinuxBinaries is a binaries folder for version holding content as
// the linux build for this machine's architecture.
func writeLinuxBinaries(t *testing.T, version string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(content)
	_ = zw.Close()
	file := "fridolin-linux-" + runtime.GOARCH + ".gz"
	if err := os.WriteFile(filepath.Join(dir, file), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	m := map[string]any{"version": version, "binaries": []map[string]any{{
		"os": "linux", "arch": runtime.GOARCH, "file": file, "sha256": hex.EncodeToString(sum[:]), "size": len(content),
	}}}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, nodes.BinariesManifest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A node set up for sandboxes declares no linux fridolin; once the owner
// approves it, the core sends its own, signed, and the node declares its
// version.
func TestSandboxBinFromTheCore(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sandbox is offered on macOS and Linux")
	}
	const vB = "v9.9.9"
	content := fakeBinary(vB + "-linux")
	c := startCoreWith(t, nodes.Options{Version: vB, BinariesDir: writeLinuxBinaries(t, vB, content)})
	n := newUpdNode(t, c, "v9.9.8")
	fake := t.TempDir()
	n.tweak = func(a *Agent) {
		st := newMemSecrets()
		_ = st.Set(hostsecrets.ClaudeOAuthToken, testClaudeToken)
		a.CoreSecrets = st
		a.SandboxDriver = sbxCLI{path: testExe(t), env: []string{"FAKE_SBX_DIR=" + fake}}
		a.checkSandboxBin = func(path, arch string) (string, error) {
			if b, _ := os.ReadFile(path); !bytes.Equal(b, content) || arch != runtime.GOARCH {
				return "", fmt.Errorf("not the binary sent")
			}
			return vB, nil
		}
		if err := a.saveSandboxState(sandboxState{Template: "fridolin-brain:v1"}); err != nil {
			t.Error(err)
		}
	}
	a := n.start()
	id := a.ID()
	waitFor(t, "the sandbox declared", func() bool { d := nodeOf(t, c, id).Declared.Sandbox; return d != nil && d.Bin == "" && d.Template != "" })
	approveUpd(t, c, id)
	waitFor(t, "the binary declared", func() bool { d := nodeOf(t, c, id).Declared.Sandbox; return d != nil && d.Bin == vB })
	got, err := os.ReadFile(filepath.Join(n.dir, "sandbox", "bin", "fridolin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("the binary in place: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(n.dir, "sandbox", "bin", "fridolin")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("its mode: %v %v", fi, err)
	}
	// A wrong signature is refused, and nothing changes.
	_, perr := a.sandboxBinApply(context.Background(), nodeproto.UpdateApplyParams{ID: "00000000000000000000000000000000", Version: "v1.0.0", OS: "linux", Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(make([]byte, 32)), TransferSHA256: hex.EncodeToString(make([]byte, 32)), Size: 10, Signature: make([]byte, 64)})
	if perr == nil || perr.Code != nodeproto.CodeDenied {
		t.Errorf("a forged binary: %v", perr)
	}
	if a.sandboxBinVersion() != vB {
		t.Errorf("version after a forged binary: %q", a.sandboxBinVersion())
	}
}

// checkLinuxBinary takes a fridolin for linux of the VM's architecture
// only. On Linux the test binary is one (a Go program of this module).
func TestCheckLinuxBinary(t *testing.T) {
	t.Parallel()
	text := filepath.Join(t.TempDir(), "fridolin")
	if err := os.WriteFile(text, fakeBinary("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := checkLinuxBinary(text, runtime.GOARCH); err == nil {
		t.Error("a text file passed")
	}
	if runtime.GOOS != "linux" {
		return
	}
	if v, err := checkLinuxBinary(testExe(t), runtime.GOARCH); err != nil || v != "" {
		t.Errorf("the test binary: %q %v", v, err)
	}
	other := map[string]string{"arm64": "amd64", "amd64": "arm64"}[runtime.GOARCH]
	if _, err := checkLinuxBinary(testExe(t), other); err == nil {
		t.Errorf("the test binary passed for %s", other)
	}
	if m := ldflagsVersionRe.FindStringSubmatch("-s -w -X github.com/Icewater1337/fridolin/internal/version.version=v1.2.3-4-gabc"); m == nil || m[1] != "v1.2.3-4-gabc" {
		t.Errorf("version from -ldflags: %v", m)
	}
}
