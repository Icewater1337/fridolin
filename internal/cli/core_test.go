package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/container"
	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/lease"
)

// mapSecrets is a host secret store in memory.
type mapSecrets map[string]string

func (m mapSecrets) Get(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", hostsecrets.ErrNotFound
	}
	return v, nil
}
func (m mapSecrets) Set(name, value string) error { m[name] = value; return nil }
func (m mapSecrets) Has(name string) (bool, error) {
	_, ok := m[name]
	return ok, nil
}

type ranCmd struct {
	name string
	args []string
	env  []string
}

func hostFixture(t *testing.T, secrets mapSecrets) (hostDeps, *[]ranCmd) {
	var ran []ranCmd
	return hostDeps{
		secrets: secrets, docker: "/usr/local/bin/docker", version: "v1.4.0+dirty", zone: "Europe/Zurich",
		composePath: filepath.Join(t.TempDir(), "fridolin", "compose.yaml"),
		baseEnv:     []string{"PATH=/usr/bin:/bin", "HOME=/Users/you"},
		newIdentity: func() (string, error) { return "AGE-SECRET-KEY-PQ-1NEW", nil },
		newKey:      func() (string, error) { return testLayerKey, nil },
		run: func(_ context.Context, name string, args, env []string, _, _ io.Writer) error {
			ran = append(ran, ranCmd{name, args, env})
			return nil
		},
	}, &ran
}

func TestCoreStartPassesSecretsOnlyThroughTheEnvironment(t *testing.T) {
	secrets := mapSecrets{
		hostsecrets.ClaudeOAuthToken: "oauth-token-value",
		hostsecrets.TailscaleAuthKey: "tskey-auth-key",
		hostsecrets.B2VaultKeyID:     "keyid",
		hostsecrets.B2VaultKey:       "b2-secret-key",
	}
	d, ran := hostFixture(t, secrets)
	var stdout, stderr bytes.Buffer
	if code := coreHost(context.Background(), "start", nil, coreEnv{stdout: &stdout, stderr: &stderr}, d); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(*ran) != 1 {
		t.Fatalf("ran %v", *ran)
	}
	cmd := (*ran)[0]
	if want := []string{"compose", "-f", d.composePath, "up", "-d"}; !slices.Equal(cmd.args, want) {
		t.Errorf("args %q, want %q", cmd.args, want)
	}
	for _, secret := range []string{"oauth-token-value", "tskey-auth-key", "b2-secret-key", "AGE-SECRET-KEY-PQ-1NEW", testLayerKey} {
		if slices.ContainsFunc(cmd.args, func(a string) bool { return strings.Contains(a, secret) }) {
			t.Errorf("%s is on the command line", secret)
		}
	}
	for _, kv := range []string{"CLAUDE_CODE_OAUTH_TOKEN=oauth-token-value", "TS_AUTHKEY=tskey-auth-key", "B2_VAULT_KEY_ID=keyid",
		"B2_VAULT_KEY=b2-secret-key", "FRIDOLIN_AGE_IDENTITY_PQ=AGE-SECRET-KEY-PQ-1NEW", "FRIDOLIN_EXPORT_KEY=" + testLayerKey,
		"FRIDOLIN_VAULT_SEAL_KEY=" + testLayerKey, "FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD=" + testLayerKey, "FRIDOLIN_LITESTREAM_SEAL_KEY=" + testLayerKey,
		"FRIDOLIN_VERSION=v1.4.0-dirty",
		"FRIDOLIN_IMAGE=fridolin-core", "PATH=/usr/bin:/bin"} {
		if !slices.Contains(cmd.env, kv) {
			t.Errorf("environment lacks %s: %q", kv, cmd.env)
		}
	}
	if secrets[hostsecrets.AgeIdentityPQ] != "AGE-SECRET-KEY-PQ-1NEW" || secrets[hostsecrets.ExportKey] != testLayerKey {
		t.Error("the new age identity and keys were not stored")
	}
	if _, made := secrets[hostsecrets.AgeIdentity]; made {
		t.Error("core start made an X25519 identity")
	}
	if !strings.Contains(stderr.String(), "Made "+hostsecrets.AgeIdentityPQ) || !strings.Contains(stderr.String(), hostsecrets.ResticPassword) {
		t.Errorf("stderr %q", stderr.String())
	}
	got, err := os.ReadFile(d.composePath)
	if err != nil || !bytes.Equal(got, container.Compose) {
		t.Errorf("compose file not written: %v", err)
	}
	// The compose file passes the same names.
	for _, s := range containerSecrets {
		if !bytes.Contains(container.Compose, []byte("- "+s.env+"\n")) {
			t.Errorf("compose.yaml does not pass %s", s.env)
		}
	}
}

func TestCoreStopAndUpdate(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"})
	env := coreEnv{stdout: io.Discard, stderr: io.Discard}
	if code := coreHost(context.Background(), "stop", []string{"--compose", "/x/compose.yaml"}, env, d); code != 0 {
		t.Fatal(code)
	}
	if code := coreHost(context.Background(), "update", []string{"--tag", "v2"}, env, d); code != 0 {
		t.Fatal(code)
	}
	if len(*ran) != 3 {
		t.Fatalf("ran %v", *ran)
	}
	stop := (*ran)[0]
	if !slices.Equal(stop.args, []string{"compose", "-f", "/x/compose.yaml", "down"}) {
		t.Errorf("stop ran %q", stop.args)
	}
	if slices.ContainsFunc(stop.env, func(kv string) bool { return strings.HasPrefix(kv, "FRIDOLIN_AGE_IDENTITY=") }) {
		t.Error("stop got the secrets")
	}
	if a := (*ran)[1].args; !slices.Equal(a[3:], []string{"pull"}) {
		t.Errorf("update ran %q first", a)
	}
	if a := (*ran)[2]; !slices.Equal(a.args[3:], []string{"up", "-d", "--force-recreate"}) || !slices.Contains(a.env, "FRIDOLIN_VERSION=v2") || !slices.Contains(a.env, "FRIDOLIN_AGE_IDENTITY=AGE-SECRET-KEY-1OLD") {
		t.Errorf("update ran %q with %q", a.args, a.env)
	}
}

func TestCoreRun(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"})
	env := coreEnv{stdout: io.Discard, stderr: io.Discard}
	if code := coreHost(context.Background(), "run", nil, env, d); code != 2 {
		t.Errorf("run without a command: exit %d", code)
	}
	if code := coreHost(context.Background(), "run", []string{"--", "core", "import", "--latest"}, env, d); code != 0 {
		t.Fatal(code)
	}
	got := (*ran)[0]
	if !slices.Equal(got.args[3:], []string{"run", "--rm", "--no-deps", "core", "core", "import", "--latest"}) || !slices.Contains(got.env, "FRIDOLIN_AGE_IDENTITY=AGE-SECRET-KEY-1OLD") {
		t.Errorf("ran %q with %q", got.args, got.env)
	}
}

// TestGoldenCoreHostCommands pins what each host command runs: docker's
// arguments, and the environment compose gets, with the secrets' values
// masked (they must be there by name, never on the command line). A
// change here changes how the container is started on every machine.
func TestGoldenCoreHostCommands(t *testing.T) {
	secrets := mapSecrets{}
	for _, s := range containerSecrets {
		secrets[s.secret] = "value-of-" + s.secret
	}
	d, ran := hostFixture(t, secrets)
	env := coreEnv{stdout: io.Discard, stderr: io.Discard}
	var out strings.Builder
	for _, args := range [][]string{
		{"start"}, {"stop"}, {"update"}, {"start", "--image", "registry.example/fridolin-core", "--tag", "v2"},
		{"run", "--", "core", "import", "--latest"}, {"run", "core", "promote"},
	} {
		*ran = nil
		if code := coreHost(context.Background(), args[0], args[1:], env, d); code != 0 {
			t.Fatalf("%q: exit %d", args, code)
		}
		fmt.Fprintf(&out, "$ fridolin core %s\n", strings.Join(args, " "))
		for _, c := range *ran {
			cmd := strings.ReplaceAll(strings.Join(append([]string{c.name}, c.args...), " "), d.composePath, "<COMPOSE>")
			fmt.Fprintf(&out, "  %s\n", cmd)
			for _, kv := range c.env {
				name, value, _ := strings.Cut(kv, "=")
				if strings.HasPrefix(value, "value-of-") {
					value = "<" + strings.TrimPrefix(value, "value-of-") + ">"
				}
				fmt.Fprintf(&out, "    %s=%s\n", name, value)
			}
		}
	}
	assertGolden(t, "cli/core-host-commands.golden", []byte(out.String()))
	seccomp, err := os.ReadFile(filepath.Join(filepath.Dir(d.composePath), "seccomp.json"))
	if err != nil || !bytes.Equal(seccomp, container.Seccomp) {
		t.Errorf("seccomp.json beside the compose file: %v", err)
	}
	if !bytes.Contains(container.Compose, []byte("seccomp=${FRIDOLIN_SECCOMP:-./seccomp.json}")) {
		t.Error("compose.yaml does not name seccomp.json")
	}
}

func TestCoreHostWithoutDocker(t *testing.T) {
	d, _ := hostFixture(t, mapSecrets{})
	d.docker = ""
	var stderr bytes.Buffer
	if code := coreHost(context.Background(), "start", nil, coreEnv{stdout: io.Discard, stderr: &stderr}, d); code != 1 || !strings.Contains(stderr.String(), "docker is not installed") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

func TestCoreHostFailure(t *testing.T) {
	d, _ := hostFixture(t, mapSecrets{})
	d.run = func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
		return errors.New("exit status 1")
	}
	if code := coreHost(context.Background(), "start", nil, coreEnv{stdout: io.Discard, stderr: io.Discard}, d); code != 1 {
		t.Errorf("exit %d", code)
	}
}

func TestImageTag(t *testing.T) {
	for in, want := range map[string]string{"v1.2.3": "v1.2.3", "dev-abc1234+dirty": "dev-abc1234-dirty", "dev": "dev"} {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q", in, got)
		}
	}
}

// The data-dir commands, end to end on a folder store: export one data
// dir, import it into another, promote both, status.
func TestCoreExportImportPromoteStatus(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	storeDir := t.TempDir()
	src := t.TempDir()
	makeFiles(t, src, "dashboard.token", "secrets/a.age", "runs/1.json")
	cfg := writeConfig(t, `{"data_dir": "`+src+`"}`)
	run := func(dataDir string, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		env := coreEnv{environ: []string{"FRIDOLIN_AGE_IDENTITY_PQ=" + id.String(), "FRIDOLIN_EXPORT_KEY=" + testLayerKey, "FRIDOLIN_HOME=" + dataDir}, stdout: &stdout, stderr: &stderr}
		code := runCore(append(args, "--store", storeDir, "--config", cfg), env)
		return code, stdout.String(), stderr.String()
	}
	if code, out, errOut := run(src, "export"); code != 0 || !strings.Contains(out, "core/exports/") {
		t.Fatalf("export: %d %s %s", code, out, errOut)
	}
	dst := filepath.Join(t.TempDir(), "volume")
	if code, out, errOut := run(dst, "import"); code != 2 {
		t.Errorf("import without --latest: %d %s %s", code, out, errOut)
	}
	if code, out, errOut := run(dst, "import", "--latest"); code != 0 {
		t.Fatalf("import: %d %s %s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dst, "secrets", "a.age")); err != nil {
		t.Error("secrets not imported")
	}
	if _, err := os.Stat(filepath.Join(dst, "runs")); err == nil {
		t.Error("runs/ imported")
	}
	if code, _, errOut := run(dst, "import", "--latest"); code != 1 || !strings.Contains(errOut, "--force") {
		t.Errorf("a second import over data: %d %s", code, errOut)
	}
	if code, out, errOut := run(dst, "promote"); code != 0 || !strings.Contains(out, "holds the core lease") {
		t.Fatalf("promote: %d %s %s", code, out, errOut)
	}
	// The old copy has a device id of its own, and is refused.
	if code, _, errOut := run(src, "promote"); code != 3 || !strings.Contains(errOut, "the core lease is held") {
		t.Errorf("promote of the old copy: %d %s", code, errOut)
	}
	code, out, errOut := run(dst, "status")
	if code != 0 || !strings.Contains(out, "lease: live") || !strings.Contains(out, "latest export: core/exports/") {
		t.Errorf("status: %d %s %s", code, out, errOut)
	}
	srcID, _ := lease.DeviceID(src)
	dstID, _ := lease.DeviceID(dst)
	if srcID == dstID {
		t.Error("the imported copy kept the old device id")
	}
}

func TestCoreNeedsTheIdentity(t *testing.T) {
	var stderr bytes.Buffer
	code := runCore([]string{"export", "--store", t.TempDir()}, coreEnv{stdout: io.Discard, stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "FRIDOLIN_AGE_IDENTITY_PQ is not set") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	code = runCore([]string{"export", "--store", t.TempDir()}, coreEnv{environ: []string{"FRIDOLIN_AGE_IDENTITY_PQ=" + id.String()}, stdout: io.Discard, stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "FRIDOLIN_EXPORT_KEY is not set") {
		t.Errorf("without the export key: exit %d: %s", code, stderr.String())
	}
	stderr.Reset()
	code = runCore([]string{"import", "--latest", "--store", t.TempDir()}, coreEnv{stdout: io.Discard, stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "neither FRIDOLIN_AGE_IDENTITY_PQ nor FRIDOLIN_AGE_IDENTITY") {
		t.Errorf("import: exit %d: %s", code, stderr.String())
	}
}

// testLayerKey is a second layer's key as core start makes one.
const testLayerKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

func TestCoreUsage(t *testing.T) {
	var out bytes.Buffer
	if code := runCore([]string{"--help"}, coreEnv{stdout: &out, stderr: io.Discard}); code != 0 {
		t.Errorf("exit %d", code)
	}
	assertGolden(t, "cli/core-usage.golden", out.Bytes())
	var stderr bytes.Buffer
	if code := runCore([]string{"bogus"}, coreEnv{stdout: io.Discard, stderr: &stderr}); code != 2 || !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

func TestUsageGolden(t *testing.T) {
	assertGolden(t, "cli/usage.golden", []byte(usage))
}

func TestCoreStoreFrom(t *testing.T) {
	if _, _, err := coreStoreFrom("", "fridolin-vault", nil); err == nil || !strings.Contains(err.Error(), "B2_VAULT_KEY_ID") {
		t.Errorf("no store: %v", err)
	}
	st, where, err := coreStoreFrom("", "", []string{"FRIDOLIN_CORE_STORE=/tmp/x"})
	if d, ok := st.(*cloud.Dir); err != nil || !ok || d.Root != "/tmp/x" || where != "folder /tmp/x" {
		t.Errorf("FRIDOLIN_CORE_STORE: %v %q %v", st, where, err)
	}
	ds := &cloud.Dir{Root: t.TempDir()}
	if err := ds.Put(context.Background(), "../escape", strings.NewReader("x")); err == nil {
		t.Error("the folder store wrote outside its folder")
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		return
	}
	st, where, err = coreStoreFrom("", "fridolin-vault", []string{"PATH=/usr/bin", "B2_VAULT_KEY_ID=id", "B2_VAULT_KEY=k", "SECRET=x"})
	r, ok := st.(*cloud.Rclone)
	if err != nil || !ok || r.Remote != "fridolinvault:fridolin-vault" || where != "b2 bucket fridolin-vault" {
		t.Fatalf("B2: %v %q %v", st, where, err)
	}
	if slices.Contains(r.Env, "SECRET=x") || !slices.Contains(r.Env, "RCLONE_CONFIG_FRIDOLINVAULT_ACCOUNT=id") {
		t.Errorf("rclone's environment = %q", r.Env)
	}
}
