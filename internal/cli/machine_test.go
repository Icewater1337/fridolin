package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
)

func TestGoldenJoinCode(t *testing.T) {
	var out bytes.Buffer
	sh, ps := httpapi.JoinCommands("fj-abcd1234", "fridolin-core.example.ts.net:8766")
	writeJoinCode(&out, httpapi.JoinCodeResponse{
		Code: "fj-abcd1234", ExpiresAt: time.Date(2026, 10, 21, 14, 5, 0, 0, time.Local),
		Core: "fridolin-core.example.ts.net:8766", Command: sh, CommandPowerShell: ps,
	})
	assertGolden(t, "cli/nodes-join-code.golden", out.Bytes())
	// A core that knows where its box script is published: the box first.
	var box bytes.Buffer
	bsh, bps := httpapi.BoxCommands("fj-abcd1234", "fridolin-core.example.ts.net:8766",
		httpapi.BoxSource{Source: "https://raw.githubusercontent.com/OWNER/fridolin/COMMIT", Registry: "ghcr.io/owner", Version: "abcd1234"})
	writeJoinCode(&box, httpapi.JoinCodeResponse{
		Code: "fj-abcd1234", ExpiresAt: time.Date(2026, 10, 21, 14, 5, 0, 0, time.Local),
		Core: "fridolin-core.example.ts.net:8766", Command: sh, CommandPowerShell: ps, BoxCommand: bsh, BoxCommandPowerShell: bps,
	})
	assertGolden(t, "cli/nodes-join-code-box.golden", box.Bytes())
	// A core from before the PowerShell line: built from its address.
	var old bytes.Buffer
	writeJoinCode(&old, httpapi.JoinCodeResponse{Code: "fj-x", Core: "mini:8766", Command: "fridolin node join fj-x --core mini:8766"})
	if !strings.Contains(old.String(), `& "$env:ProgramFiles\fridolin\fridolin.exe" node join fj-x --core mini:8766`) {
		t.Errorf("%s", old.String())
	}
}

func TestComposeBaseEnv(t *testing.T) {
	environ := []string{"Path=C:\\Windows", "SystemRoot=C:\\Windows", "USERPROFILE=C:\\Users\\you", "LOCALAPPDATA=x",
		"DOCKER_HOST=npipe:////./pipe/docker", "SECRET=x", "B2_VAULT_KEY=k", "HOME=/h", "PATH=/usr/bin"}
	win := composeBaseEnv("windows", environ)
	for _, kv := range []string{"Path=C:\\Windows", "SystemRoot=C:\\Windows", "USERPROFILE=C:\\Users\\you", "LOCALAPPDATA=x", "DOCKER_HOST=npipe:////./pipe/docker", "PATH=/usr/bin"} {
		if !slices.Contains(win, kv) {
			t.Errorf("windows: lacks %s: %q", kv, win)
		}
	}
	for _, kv := range []string{"SECRET=x", "B2_VAULT_KEY=k", "HOME=/h"} {
		if slices.Contains(win, kv) {
			t.Errorf("windows: passes %s", kv)
		}
	}
	unix := composeBaseEnv("darwin", environ)
	if !slices.Equal(unix, []string{"DOCKER_HOST=npipe:////./pipe/docker", "HOME=/h", "PATH=/usr/bin"}) {
		t.Errorf("darwin: %q", unix)
	}
	if p := composePathFor("windows"); !strings.HasSuffix(p, filepath.Join("fridolin", "compose.yaml")) {
		t.Errorf("windows compose path %s", p)
	}
}

func TestCoreMigrateRefusedOnWindows(t *testing.T) {
	d, _ := hostFixture(t, mapSecrets{})
	d.goos = "windows"
	var stderr bytes.Buffer
	if code := coreMigrate(context.Background(), []string{"--from-native"}, coreEnv{stdout: io.Discard, stderr: &stderr}, d); code != 2 || !strings.Contains(stderr.String(), "core import --latest") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

func TestCoreBuild(t *testing.T) {
	d, ran := hostFixture(t, mapSecrets{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"})
	repo := t.TempDir()
	env := coreEnv{stdout: io.Discard, stderr: io.Discard}
	if code := coreHost(context.Background(), "build", []string{"--repo", repo}, env, d); code != 2 {
		t.Errorf("a folder without container/Dockerfile: exit %d", code)
	}
	makeFiles(t, repo, "container/Dockerfile")
	if code := coreHost(context.Background(), "build", []string{"--repo", repo}, env, d); code != 0 {
		t.Fatal(code)
	}
	got := (*ran)[0]
	want := []string{"build", "-f", filepath.Join(repo, "container", "Dockerfile"), "--build-arg", "VERSION=v1.4.0+dirty", "-t", "fridolin-core:v1.4.0-dirty", repo}
	if !slices.Equal(got.args, want) || slices.ContainsFunc(got.env, func(kv string) bool { return strings.HasPrefix(kv, "FRIDOLIN_AGE_IDENTITY=") }) {
		t.Errorf("ran %q with %q", got.args, got.env)
	}

	// A clean checkout of a GitHub repository: the box source of its HEAD.
	head := strings.Repeat("ab", 20)
	d.git = func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "remote":
			return "git@github.com:Owner/fridolin.git", nil
		case "rev-parse":
			return head, nil
		}
		return "", nil
	}
	if code := coreHost(context.Background(), "build", []string{"--repo", repo}, env, d); code != 0 {
		t.Fatal(code)
	}
	want = []string{"build", "-f", filepath.Join(repo, "container", "Dockerfile"), "--build-arg", "VERSION=v1.4.0+dirty",
		"--build-arg", "FRIDOLIN_BOX_SOURCE=https://raw.githubusercontent.com/Owner/fridolin/" + head,
		"--build-arg", "FRIDOLIN_BOX_REGISTRY=ghcr.io/owner", "-t", "fridolin-core:v1.4.0-dirty", repo}
	if got := (*ran)[1].args; !slices.Equal(got, want) {
		t.Errorf("ran %q", got)
	}
	for _, remote := range []string{"https://gitlab.com/owner/fridolin.git", "/srv/git/fridolin"} {
		if src, _ := boxBuildSource(repo, hostDeps{git: func(_ string, args ...string) (string, error) {
			if args[0] == "remote" {
				return remote, nil
			}
			return head, nil
		}}); src != "" {
			t.Errorf("%s: box source %q", remote, src)
		}
	}
}

func TestCoreSecrets(t *testing.T) {
	secrets := mapSecrets{hostsecrets.AgeIdentity: "AGE-SECRET-KEY-1OLD"}
	d, _ := hostFixture(t, secrets)
	d.secretsPlace = "the test store"
	run := func(stdin string, term terminalIO, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := coreSecrets(args, coreEnv{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb, term: term}, d)
		return code, out.String(), errb.String()
	}
	code, out, _ := run("", terminalIO{})
	if code != 0 || !strings.Contains(out, "stored   fridolin.age.identity") || !strings.Contains(out, "missing  fridolin.b2.vault.key ") || strings.Contains(out, "AGE-SECRET-KEY-1OLD") {
		t.Errorf("list: %d %s", code, out)
	}
	// From stdin, by its short name.
	if code, out, errOut := run("K001secret\n", terminalIO{}, "set", "b2.vault.key"); code != 0 || secrets[hostsecrets.B2VaultKey] != "K001secret" || strings.Contains(out, "K001secret") {
		t.Errorf("set from stdin: %d %s %s", code, out, errOut)
	}
	// From the terminal, without echo.
	hidden := terminalIO{readHidden: func(p string) (string, error) { return "claude-token-x", nil }}
	if code, _, errOut := run("", hidden, "set", "fridolin.claude.oauth.token"); code != 0 || secrets[hostsecrets.ClaudeOAuthToken] != "claude-token-x" {
		t.Errorf("set from the terminal: %d %s", code, errOut)
	}
	for name, c := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"already there":    {"AGE-SECRET-KEY-1NEW", []string{"set", "age.identity"}, "--replace"},
		"not an identity":  {"nope", []string{"set", "age.identity", "--replace"}, "not an age identity"},
		"unknown name":     {"x", []string{"set", "gitlab.token"}, "not a secret the core uses"},
		"empty":            {"\n", []string{"set", "restic.password"}, "empty"},
		"a space":          {"a b", []string{"set", "restic.password"}, "spaces"},
		"too many":         {"", []string{"set", "a", "b"}, "usage"},
		"in the container": {"", []string{"list"}, "not in the container"},
	} {
		env := coreEnv{stdin: strings.NewReader(c.stdin), stdout: io.Discard}
		var errb bytes.Buffer
		env.stderr = &errb
		if name == "in the container" {
			env.environ = []string{"FRIDOLIN_CONTAINER=1"}
		}
		if code := coreSecrets(c.args, env, d); code == 0 || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%s: exit %d: %s", name, code, errb.String())
		}
	}
	if secrets[hostsecrets.AgeIdentity] != "AGE-SECRET-KEY-1OLD" {
		t.Error("the age identity was replaced")
	}
	id, _ := age.GenerateX25519Identity()
	if code, _, errOut := run(id.String(), terminalIO{}, "set", "age.identity", "--replace"); code != 0 || secrets[hostsecrets.AgeIdentity] != id.String() {
		t.Errorf("--replace: %s", errOut)
	}
}

// loopNet is a tailnet on loopback whose devices all belong to one owner.
type loopNet struct{ self string }

func (l loopNet) Self(context.Context) (secretxfer.Peer, error) {
	return secretxfer.Peer{Login: "you@example.com", Node: l.self}, nil
}
func (loopNet) Listen(ctx context.Context) (net.Listener, string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	return ln, ln.Addr().String(), nil
}
func (loopNet) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}
func (l loopNet) WhoIs(context.Context, string) (secretxfer.Peer, error) {
	return secretxfer.Peer{Login: "you@example.com", Node: "peer.example.ts.net"}, nil
}

// syncBuffer is a bytes.Buffer two goroutines may use.
type syncBuffer struct {
	ch chan string
	bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.ch <- string(p)
	return len(p), nil
}

func TestCoreSendReceive(t *testing.T) {
	src := mapSecrets{
		hostsecrets.AgeIdentity:  "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ",
		hostsecrets.B2VaultKeyID: "0012345", hostsecrets.B2VaultKey: "K001secret",
		hostsecrets.TailscaleAuthKey: "tskey-auth-x",
	}
	dst := mapSecrets{}
	sd, _ := hostFixture(t, src)
	sd.tailnet = loopNet{"mini.example.ts.net"}
	rd, _ := hostFixture(t, dst)
	rd.tailnet = loopNet{"winbox.example.ts.net"}
	out := &syncBuffer{ch: make(chan string, 16)}
	done := make(chan int, 1)
	go func() {
		done <- coreTransfer(context.Background(), "receive", []string{"--timeout", "20s"}, coreEnv{stdout: out, stderr: io.Discard}, rd)
	}()
	var printed string
	for !strings.Contains(printed, "--code ") {
		printed += <-out.ch
	}
	var addr, code string
	for line := range strings.SplitSeq(printed, "\n") {
		if f := strings.Fields(line); len(f) == 7 && f[2] == "send" {
			addr, code = f[4], f[6]
		}
	}
	go func() {
		for s := range out.ch {
			printed += s
		}
	}()
	var words string
	term := terminalIO{readLine: func(p string) (string, error) {
		if strings.Contains(p, "same four words") {
			words = p
			return "yes", nil
		}
		return "", errors.New("unexpected prompt " + p)
	}}
	var sendOut, sendErr bytes.Buffer
	if c := coreTransfer(context.Background(), "send", []string{"--to", addr, "--code", code}, coreEnv{stdout: &sendOut, stderr: &sendErr, term: term}, sd); c != 0 {
		t.Fatalf("send: exit %d: %s %s", c, sendOut.String(), sendErr.String())
	}
	if c := <-done; c != 0 {
		t.Fatalf("receive: exit %d", c)
	}
	if words == "" || !strings.Contains(sendErr.String(), "Not in") {
		t.Errorf("prompt %q; stderr %s", words, sendErr.String())
	}
	for _, n := range []string{hostsecrets.AgeIdentity, hostsecrets.B2VaultKeyID, hostsecrets.B2VaultKey} {
		if dst[n] != src[n] {
			t.Errorf("%s = %q", n, dst[n])
		}
	}
	if _, ok := dst[hostsecrets.TailscaleAuthKey]; ok {
		t.Error("the Tailscale auth key moved without --with-tailscale-key")
	}
	for _, v := range src {
		if strings.Contains(sendOut.String()+sendErr.String(), v) {
			t.Errorf("a value was printed: %s", v)
		}
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(rd.composePath), "secrets-transfer.log"))
	if err != nil || !strings.Contains(string(log), `"direction":"received"`) || strings.Contains(string(log), "K001secret") {
		t.Errorf("the receiver's log: %s %v", log, err)
	}
	// send needs a terminal.
	var e bytes.Buffer
	if c := coreTransfer(context.Background(), "send", []string{"--to", addr}, coreEnv{stdout: io.Discard, stderr: &e}, sd); c != 2 || !strings.Contains(e.String(), "terminal") {
		t.Errorf("send without a terminal: %d %s", c, e.String())
	}
}

// doctorEnvFor is a DoctorEnv whose dial reaches only reachable, and
// whose docker answers from docker.
func doctorEnvFor(reachable string, docker map[string]string) *app.DoctorEnv {
	return &app.DoctorEnv{
		GOOS: "linux", Getenv: func(string) string { return "" },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if strings.HasSuffix(name, "tailscale") {
				return []byte(`{"BackendState":"Running"}`), nil
			}
			key := strings.Join(args, " ")
			for k, v := range docker {
				if strings.HasPrefix(key, k) {
					return []byte(v), nil
				}
			}
			return nil, errors.New("exit status 1")
		},
		Dial: func(_ context.Context, addr string) error {
			if addr == reachable {
				return nil
			}
			return fmt.Errorf("dial %s: refused", addr)
		},
	}
}

func TestNodeChecks(t *testing.T) {
	dir := t.TempDir()
	running := func() (string, error) { return "running", nil }
	rep := app.RunDoctor(context.Background(), doctorEnvFor("", nil), nodeChecks(dir, nodeService{status: running}))
	if rep.OK || rep.Checks[1].ID != "node-joined" || rep.Checks[1].Status != app.DoctorFail || !strings.Contains(rep.Checks[1].Fix, "join-code") {
		t.Fatalf("not joined: %+v", rep)
	}
	if _, err := nodeagent.NewKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveConfig(dir, nodeagent.Config{Core: "ws://fridolin-core.example.ts.net:8766", Name: "winbox", Capabilities: nodeproto.Capabilities{Host: true}}); err != nil {
		t.Fatal(err)
	}
	rep = app.RunDoctor(context.Background(), linuxNodeEnv(doctorEnvFor("fridolin-core.example.ts.net:8766", nil), "default.target", "yes"), nodeChecks(dir, nodeService{status: running}))
	if !rep.OK {
		t.Errorf("joined, reachable, running: %+v", rep)
	}
	stopped := func() (string, error) { return "installed, stopped", nil }
	rep = app.RunDoctor(context.Background(), linuxNodeEnv(doctorEnvFor("", nil), "default.target", "yes"), nodeChecks(dir, nodeService{status: stopped}))
	var failed []string
	for _, c := range rep.Checks {
		if c.Status == app.DoctorFail {
			failed = append(failed, c.ID)
		}
	}
	if rep.OK || !slices.Equal(failed, []string{"node-core", "node-service"}) {
		t.Errorf("unreachable and stopped: %q", failed)
	}
	var out bytes.Buffer
	if code := writeMachineReport(&out, rep, dir, false); code != 1 || !strings.Contains(out.String(), "Next: tailscale status") {
		t.Errorf("next step: %d %s", code, out.String())
	}
}

// linuxNodeEnv makes env a Linux node's whose systemd user unit is
// enabled into target ("" for not enabled) and whose user lingers when
// linger is "yes".
func linuxNodeEnv(env *app.DoctorEnv, target, linger string) *app.DoctorEnv {
	env.GOOS, env.Home = "linux", "/home/you"
	env.Getenv = func(name string) string {
		if name == "USER" {
			return "you"
		}
		return ""
	}
	stat, run := env.Stat, env.Run
	env.Stat = func(p string) (fs.FileInfo, error) {
		if p == nodeagent.LinuxUnitPath(env.Home) || (target != "" && p == nodeagent.LinuxWantsLink(env.Home, target)) {
			return nil, nil
		}
		return stat(p)
	}
	env.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "loginctl" && strings.Join(args, " ") == "show-user you -p Linger" {
			return []byte("Linger=" + linger + "\n"), nil
		}
		return run(ctx, name, args...)
	}
	return env
}

// TestNodeBootChecks: on Linux the node's user unit must be enabled into
// default.target, which a user's systemd starts at boot, not into
// multi-user.target, which it never reaches (issue #171); without
// lingering doctor warns.
func TestNodeBootChecks(t *testing.T) {
	dir := t.TempDir()
	if _, err := nodeagent.NewKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveConfig(dir, nodeagent.Config{Core: "ws://fridolin-core.example.ts.net:8766", Name: "box"}); err != nil {
		t.Fatal(err)
	}
	running := func() (string, error) { return "running", nil }
	check := func(env *app.DoctorEnv, id string) app.DoctorCheckReport {
		t.Helper()
		for _, c := range app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: running})).Checks {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no check %s", id)
		return app.DoctorCheckReport{}
	}
	reachable := "fridolin-core.example.ts.net:8766"
	if c := check(linuxNodeEnv(doctorEnvFor(reachable, nil), "default.target", "yes"), "node-boot"); c.Status != app.DoctorOK {
		t.Errorf("default.target: %+v", c)
	}
	if c := check(linuxNodeEnv(doctorEnvFor(reachable, nil), "multi-user.target", "yes"), "node-boot"); c.Status != app.DoctorFail || !strings.Contains(c.Fix, "fridolin node start") {
		t.Errorf("multi-user.target: %+v", c)
	}
	if c := check(linuxNodeEnv(doctorEnvFor(reachable, nil), "", "yes"), "node-boot"); c.Status != app.DoctorFail || !strings.Contains(c.Fix, "systemctl --user enable fridolin-node") {
		t.Errorf("not enabled: %+v", c)
	}
	env := linuxNodeEnv(doctorEnvFor(reachable, nil), "default.target", "no")
	if c := check(env, "node-linger"); c.Status != app.DoctorWarn || c.Fix != "sudo loginctl enable-linger you" {
		t.Errorf("no linger: %+v", c)
	}
	if rep := app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: running})); !rep.OK {
		t.Errorf("linger is only a warning: %+v", rep)
	}
	env = doctorEnvFor(reachable, nil)
	env.GOOS = "darwin"
	for _, c := range app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: running})).Checks {
		if c.ID == "node-boot" || c.ID == "node-linger" {
			t.Errorf("%s checked on macOS", c.ID)
		}
	}
}

// TestNodeChecksInABox: a node in a box has no host Tailscale, service or
// systemd unit, and a dial from its container misses the tailnet device
// its agent reaches the core with, so doctor --node goes by the agent's
// own word on its connection there instead of failing those checks.
func TestNodeChecksInABox(t *testing.T) {
	dir := t.TempDir()
	if _, err := nodeagent.NewKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := nodeagent.SaveConfig(dir, nodeagent.Config{Core: "fridolin-core.example.ts.net:8766", Name: "box", Capabilities: nodeproto.Capabilities{Host: true},
		Box: &nodeagent.BoxConfig{Hostname: "fridolin-box-box", Fride: nodeagent.BoxFride}}); err != nil {
		t.Fatal(err)
	}
	noService := func() (string, error) { return "", errors.New("exit status 1") }
	now := time.Now()
	run := func(at time.Time) (rep app.DoctorReport, ids, failed []string) {
		t.Helper()
		env := linuxNodeEnv(doctorEnvFor("", nil), "", "no")
		env.LookPath = func(string) (string, error) { return "", errors.New("not found") }
		env.Now = func() time.Time { return at }
		rep = app.RunDoctor(context.Background(), env, nodeChecks(dir, nodeService{status: noService}))
		for _, c := range rep.Checks {
			ids = append(ids, c.ID)
			if c.Status == app.DoctorFail {
				failed = append(failed, c.ID)
			}
		}
		return rep, ids, failed
	}
	rep, ids, failed := run(now)
	if want := []string{"node-joined", "node-key", "node-box-core", "node-core-leftovers"}; !slices.Equal(ids, want) {
		t.Errorf("checks in a box: %q, want %q", ids, want)
	}
	if rep.OK || !slices.Equal(failed, []string{"node-box-core"}) {
		t.Errorf("never connected: %q", failed)
	}
	status := fmt.Sprintf(`{"connected":true,"version":"v1","at":%q}`, now.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, nodeagent.BoxStatusFile), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	if rep, _, _ := run(now.Add(time.Second)); !rep.OK {
		t.Errorf("connected: %+v", rep)
	}
	rep, _, failed = run(now.Add(5 * time.Minute))
	var out bytes.Buffer
	if code := writeMachineReport(&out, rep, dir, false); code != 1 || !slices.Equal(failed, []string{"node-box-core"}) || !strings.Contains(out.String(), "Next: on the host, in the box's folder: docker compose logs node") {
		t.Errorf("stale status: %q %d %s", failed, code, out.String())
	}
}

func TestCoreMachineChecks(t *testing.T) {
	d, _ := hostFixture(t, mapSecrets{hostsecrets.AgeIdentityPQ: "a", hostsecrets.ExportKey: "k", hostsecrets.B2VaultKeyID: "b", hostsecrets.B2VaultKey: "c"})
	docker := map[string]string{"info": "29.0.0"}
	rep := app.RunDoctor(context.Background(), doctorEnvFor("", docker), coreMachineChecks(d))
	var out bytes.Buffer
	if code := writeMachineReport(&out, rep, "x", false); code != 1 || !strings.Contains(out.String(), "Next: in the repository's folder: fridolin core build") {
		t.Errorf("no image: %s", out.String())
	}
	docker["image inspect"], docker["volume inspect"], docker["ps"] = "sha256:x", "fridolin-data", "Up 3 minutes (healthy)"
	rep = app.RunDoctor(context.Background(), doctorEnvFor("", docker), coreMachineChecks(d))
	out.Reset()
	if code := writeMachineReport(&out, rep, "x", false); code != 0 || !strings.Contains(out.String(), "Next: fridolin core receive") {
		t.Errorf("all there but the optional secrets: %d %s", code, out.String())
	}
}

// The node doctor flags a former core host that still holds what moves
// brought it, or its own data dir from before them, and says how to
// remove it (#266).
func TestCoreLeftoversResult(t *testing.T) {
	cases := []struct {
		name   string
		l      nodeagent.CoreLeftovers
		status string
		want   string
	}{
		{"no moves", nodeagent.CoreLeftovers{}, app.DoctorOK, "no core moved away"},
		{"runs the core", nodeagent.CoreLeftovers{Secrets: []string{"fridolin.claude.oauth.token"}, DataMove: "m-20261024-1200-0001", LastMove: "m-20261024-1200-0001"}, app.DoctorOK, "for the core that runs or may run here"},
		{"former, holds", nodeagent.CoreLeftovers{Former: true, LastMove: "m-20261024-1300-0002", Files: []string{"~/.creds/dashboard-users.json"}, DataMove: "m-20261024-1200-0001"}, app.DoctorWarn, "the core's data dir that move m-20261024-1200-0001 brought"},
		{"former, unrecorded", nodeagent.CoreLeftovers{Former: true, LastMove: "m-20261024-1300-0002", Unrecorded: true}, app.DoctorWarn, "before fridolin recorded"},
		{"former, forgotten", nodeagent.CoreLeftovers{Former: true, LastMove: "m-20261024-1300-0002", DataForgotten: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, app.DoctorOK, "forgotten (2026-10-24)"},
		{"former, original home", nodeagent.CoreLeftovers{Former: true, LastMove: "m-20261024-1300-0002"}, app.DoctorWarn, "ran the core before any move"},
		{"former, own data", nodeagent.CoreLeftovers{Former: true, LastMove: "m-20261024-1300-0002", OwnDataMove: "m-20261024-1200-0001"}, app.DoctorWarn, "before move m-20261024-1200-0001 imported"},
	}
	for _, c := range cases {
		r := coreLeftoversResult(c.l)
		if r.Status != c.status || !strings.Contains(r.Detail, c.want) {
			t.Errorf("%s: %s %q, want %s %q", c.name, r.Status, r.Detail, c.status, c.want)
		}
		if r.Status == app.DoctorWarn && !strings.Contains(r.Fix, "Forgetting a former core host") {
			t.Errorf("%s: the fix %q", c.name, r.Fix)
		}
	}
}
