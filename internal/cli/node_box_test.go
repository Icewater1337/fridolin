package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/container"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/tailnet"
)

func TestBoxJoinFlagsOfferNoCommandsOrNotifications(t *testing.T) {
	c, err := joinFlags{box: true}.capabilities("linux")
	if err != nil {
		t.Fatal(err)
	}
	if c.Exec != nil || c.Notify || !c.Host {
		t.Fatalf("a box's defaults: %+v", c)
	}
	// /backups is no folder on the test machine, so no backups by default.
	if c.Backup != nil {
		t.Fatalf("a box without /backups offers backups: %+v", c.Backup)
	}
	dir := t.TempDir()
	c, err = joinFlags{box: true, allow: multiFlag{"git=status"}, backupRoots: multiFlag{dir}}.capabilities("linux")
	if err != nil || c.Exec == nil || len(c.Exec.Allow) != 1 || c.Backup == nil || c.Backup.Roots[0] != dir {
		t.Fatalf("a box's flags: %+v, %v", c, err)
	}
}

func TestReadPairCode(t *testing.T) {
	for in, want := range map[string]string{
		"fp-abc123\n":                           "fp-abc123",
		`{"code":"fp-abc123","expires":"soon"}`: "fp-abc123",
	} {
		got, err := readPairCode(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readPairCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "two words", `{"nocode":1}`} {
		if _, err := readPairCode(strings.NewReader(in)); err == nil {
			t.Errorf("readPairCode(%q): no error", in)
		}
	}
}

func TestBoxComposeIsTheEmbeddedFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := boxCmd([]string{"compose"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !bytes.Equal(out.Bytes(), container.Box) {
		t.Fatal("box compose printed something else than container/box.yaml")
	}
	for _, want := range []string{"name: fridolin-box", "fridolin-node:/node", "fride-home:/var/lib/fride", "no-new-privileges:true"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the compose file lacks %q", want)
		}
	}
	if strings.Contains(out.String(), "docker.sock") {
		t.Error("the box's compose file mounts the Docker socket")
	}
	if code := boxCmd([]string{"nope"}, &out, &errOut); code != 2 {
		t.Errorf("an unknown box command: exit %d", code)
	}
}

func TestNodeTailnetUsesTheBoxDevice(t *testing.T) {
	fake := &tailnet.Fake{Status: tailnet.Status{DNSName: "fridolin-box-icebase.example.ts.net", IPs: []string{"100.64.0.9"}}}
	var got tailnet.Options
	old := newBoxTailnet
	newBoxTailnet = func(o tailnet.Options) (tailnet.Node, error) { got = o; return fake, nil }
	t.Cleanup(func() { newBoxTailnet = old })
	t.Setenv("TS_AUTHKEY", "tskey-test")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := nodeCmd([]string{"tailnet", "--state-dir", dir, "--name", "IceBase"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Hostname != "fridolin-box-icebase" || got.Dir != filepath.Join(dir, "tailnet") || got.AuthKey != "tskey-test" {
		t.Fatalf("the tailnet device: %+v", got)
	}
	if !strings.Contains(out.String(), "fridolin-box-icebase.example.ts.net") {
		t.Fatalf("output: %s", out.String())
	}
	if _, err := fake.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// composeTree reads the subset of YAML the box's compose files use: each
// key's path ("services.node.volumes") with its scalar value, or the
// items of its list. Comments and blank lines are skipped.
func composeTree(t *testing.T, data []byte) map[string][]string {
	t.Helper()
	type level struct {
		indent int
		key    string
	}
	var stack []level
	out := map[string][]string{}
	path := func() string {
		keys := make([]string, len(stack))
		for i, l := range stack {
			keys[i] = l.key
		}
		return strings.Join(keys, ".")
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if item, ok := strings.CutPrefix(trimmed, "- "); ok {
			item, _, _ = strings.Cut(item, " #")
			out[path()] = append(out[path()], strings.Trim(strings.TrimSpace(item), `"`))
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			t.Fatalf("not a key: %q", line)
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, level{indent, key})
		value, _, _ = strings.Cut(value, " #")
		if value = strings.TrimSpace(value); value != "" {
			out[path()] = append(out[path()], value)
		}
	}
	return out
}

// FrIDE's service never sees the core: with the core's settings on
// (container/box-core.yaml), only the node service gets the core's data
// and home volumes, the host secrets reach the core through the node's
// sealed store and never the compose files, nobody gets the Docker
// socket, and the published ports are the host's loopback only.
func TestBoxFrideNeverSeesTheCore(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := boxCmd([]string{"core-compose"}, &out, &errOut); code != 0 || !bytes.Equal(out.Bytes(), container.BoxCore) {
		t.Fatalf("box core-compose: exit %d: %s", code, errOut.String())
	}
	out.Reset()
	if code := boxCmd([]string{"seccomp"}, &out, &errOut); code != 0 || !bytes.Equal(out.Bytes(), container.Seccomp) {
		t.Fatalf("box seccomp: exit %d: %s", code, errOut.String())
	}
	base, core := composeTree(t, container.Box), composeTree(t, container.BoxCore)
	for name, data := range map[string][]byte{"box.yaml": container.Box, "box-core.yaml": container.BoxCore} {
		if bytes.Contains(data, []byte("docker.sock")) {
			t.Errorf("%s mounts the Docker socket", name)
		}
	}
	// The overlay touches the node service only.
	for k := range core {
		if strings.HasPrefix(k, "services.") && !strings.HasPrefix(k, "services.node.") {
			t.Errorf("box-core.yaml sets %s", k)
		}
	}
	// FrIDE's volumes are its own two.
	fride := base["services.fride.volumes"]
	if !slices.Equal(fride, []string{"fride-home:/var/lib/fride", "fride-workspaces:/workspaces"}) {
		t.Errorf("FrIDE's volumes: %q", fride)
	}
	// FrIDE's environment names no host secret and nothing of the core.
	for _, kv := range base["services.fride.environment"] {
		name, _, _ := strings.Cut(kv, "=")
		if name == nodeagent.BoxCoreEnv || slices.ContainsFunc(hostsecrets.CoreEnv, func(e hostsecrets.EnvSecret) bool { return e.Env == name }) {
			t.Errorf("FrIDE's environment has %s", name)
		}
	}
	// Neither file passes a secret: the node's env is set by the agent.
	for _, kv := range append(base["services.node.environment"], core["services.node.environment"]...) {
		name, _, _ := strings.Cut(kv, "=")
		if slices.ContainsFunc(hostsecrets.CoreEnv, func(e hostsecrets.EnvSecret) bool { return e.Env == name }) {
			t.Errorf("the box's compose files pass %s", name)
		}
	}
	// The node service: the core's volumes, under names a core container
	// on the same machine does not use.
	vols := core["services.node.volumes"]
	if !slices.Contains(vols, "fridolin-data:/data") || !slices.Contains(vols, "fridolin-home:/var/lib/fridolin") {
		t.Errorf("the core's volumes: %q", vols)
	}
	for key, want := range map[string]string{"volumes.fridolin-data.name": "fridolin-box-data", "volumes.fridolin-home.name": "fridolin-box-home"} {
		if got := core[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s: %q, want %s", key, got, want)
		}
	}
	if strings.Contains(string(container.Compose), "fridolin-box-data") {
		t.Error("the core container's compose file uses the box's volume")
	}
	if !slices.Contains(core["services.node.environment"], nodeagent.BoxCoreEnv+"=1") {
		t.Errorf("box-core.yaml does not tell the agent it may run the core")
	}
	// The core container's profile, for bubblewrap.
	sec := core["services.node.security_opt"]
	for _, want := range []string{"seccomp=./seccomp.json", "systempaths=unconfined"} {
		if !slices.Contains(sec, want) {
			t.Errorf("the node's security_opt lacks %s: %q", want, sec)
		}
	}
	if !slices.Contains(base["services.node.security_opt"], "no-new-privileges:true") || !slices.Contains(base["services.node.cap_drop"], "ALL") {
		t.Error("the node service lost no-new-privileges or cap_drop ALL")
	}
	// The ports: the host's loopback, the dashboard and the site.
	ports := core["services.node.ports"]
	if len(ports) != len(nodeagent.BoxCorePorts) {
		t.Fatalf("the published ports: %q", ports)
	}
	for i, p := range ports {
		if !strings.HasPrefix(p, "127.0.0.1:") || !strings.HasSuffix(p, ":"+strconv.Itoa(nodeagent.BoxCorePorts[i])) {
			t.Errorf("port %q is not on the host's loopback, or not %d", p, nodeagent.BoxCorePorts[i])
		}
	}
	if _, ok := base["services.fride.ports"]; ok {
		t.Error("FrIDE publishes a port")
	}
}

// compose merges box-core.yaml's security options into box.yaml's and
// refuses one that is then there twice, which stopped every compose
// command of a box: with FRIDOLIN_BOX_APPARMOR unset, the last line
// repeated box.yaml's no-new-privileges:true.
func TestBoxCoreRepeatsNoSecurityOption(t *testing.T) {
	alt := regexp.MustCompile(`\$\{FRIDOLIN_BOX_APPARMOR:\+([^}]*)\}`)
	def := regexp.MustCompile(`\$\{FRIDOLIN_BOX_APPARMOR:-([^}]*)\}`)
	base := composeTree(t, container.Box)["services.node.security_opt"]
	core := composeTree(t, container.BoxCore)["services.node.security_opt"]
	for _, profile := range []string{"", "fridolin-core"} {
		merged := slices.Clone(base)
		for _, o := range core {
			if profile == "" {
				o = def.ReplaceAllString(alt.ReplaceAllString(o, ""), "$1")
			} else {
				o = def.ReplaceAllString(alt.ReplaceAllString(o, "$1"), profile)
			}
			merged = append(merged, o)
		}
		for i, o := range merged {
			if slices.Contains(merged[i+1:], o) {
				t.Errorf("FRIDOLIN_BOX_APPARMOR=%q: the node's security_opt has %s twice: %q", profile, o, merged)
			}
		}
	}
}
