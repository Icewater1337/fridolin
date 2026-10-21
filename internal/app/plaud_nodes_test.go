package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// noAndroidBuild keeps a test's node agent from offering android-build,
// whatever JDK and Android SDK the machine running the tests has (CI's
// runners have both): a declaration that grows after the join would leave
// the approval the test makes pending.
var noAndroidBuild = new(bool)

func TestAppBuildersPickByTagThenMemory(t *testing.T) {
	ab := &nodeproto.AndroidBuildCap{Java: "21"}
	view := func(id string, online bool, tags []string, free uint64, build bool) nodes.View {
		v := nodes.View{Node: &nodes.Node{ID: id, Name: id, Tags: tags, Resources: &nodeproto.HostStats{MemAvailable: free}}, Online: online}
		if build {
			v.Effective.AndroidBuild = ab
		}
		return v
	}
	got := appBuilders([]nodes.View{
		view("big", true, []string{"macos"}, 64<<30, true),
		view("offline", false, []string{"android"}, 0, true),
		view("tagged", true, []string{"android"}, 1<<30, true),
		view("nobuild", true, []string{"android"}, 128<<30, false),
		view("small", true, nil, 2<<30, true),
	})
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	if strings.Join(names, " ") != "tagged big small" {
		t.Errorf("builders %v, want tagged big small", names)
	}
}

// The Plaud card's build on a node, through the node port and an agent in
// this process, with a fake Gradle: the APK arrives, a lockdown refuses.
func TestPlaudFleetBuildsOnANode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a node offers android-build on macOS and Linux only")
	}
	svc, err := nodes.New(nodes.Options{Dir: t.TempDir(), Logf: t.Logf})
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
	code, _, err := svc.NewJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	j, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: dir, Core: svc.Addr(), Code: code, Name: "mac-mini", Capabilities: nodeproto.Capabilities{Host: true}, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	tc := platform.AndroidToolchain{JavaHome: t.TempDir(), JavaMajor: 21, SDK: t.TempDir(), Platforms: []string{"android-35"}}
	agent.Logf, agent.HeartbeatEvery = t.Logf, 50*time.Millisecond
	agent.AndroidToolchain = func() (platform.AndroidToolchain, error) { return tc, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = agent.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	wait := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	locked := false
	fleet := newPlaudFleet(svc, func() bool { return locked })
	// The agent's own declaration, android-build in it, not the smaller one
	// of Join's connection, which may still count as online.
	wait("the node to declare android-build", func() bool {
		n, err := svc.Registry().Get(j.ID)
		return err == nil && svc.Online(j.ID) && n.Declared.AndroidBuild != nil
	})
	if b := fleet.builders(); len(b) != 0 {
		t.Fatalf("an unapproved node builds: %v", b)
	}
	if _, err := svc.ApproveDevice(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveCommands(j.ID); err != nil {
		t.Fatal(err)
	}
	wait("the approval", func() bool { return agent.State().Approved.AndroidBuild != nil })
	b := fleet.builders()
	if len(b) != 1 || b[0].ID != j.ID || b[0].Name != "mac-mini" {
		t.Fatalf("builders %v", b)
	}

	var src bytes.Buffer
	zw := gzip.NewWriter(&src)
	tw := tar.NewWriter(zw)
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in -PfridolinBuildDir=*) out=\"${a#-PfridolinBuildDir=}\";; esac; done\nmkdir -p \"$out/app/outputs/apk/debug\" && printf 'PK built' > \"$out/app/outputs/apk/debug/app-debug.apk\"\n"
	_ = tw.WriteHeader(&tar.Header{Name: "gradlew", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(script))
	_ = tw.Close()
	_ = zw.Close()
	props := map[string]string{"client_id": "fake-id", "pairing_code": "abc"}

	var apk bytes.Buffer
	if out, err := fleet.build(context.Background(), j.ID, src.Bytes(), props, &apk); err != nil || apk.String() != "PK built" {
		t.Fatalf("build: %q, %v, log %s", apk.String(), err, out)
	}
	locked = true
	if _, err := fleet.build(context.Background(), j.ID, src.Bytes(), props, &apk); err == nil || !strings.Contains(err.Error(), "lockdown") {
		t.Errorf("a build in a lockdown: %v", err)
	}
}

// The doctor check: a way to build the phone app while it uploads
// through the site.
func TestDoctorPlaudAppBuild(t *testing.T) {
	const registry = "/home/you/.fridolin/nodes/nodes.json"
	fresh := `"last_seen":"2026-10-20T11:59:00Z"`
	ab := `"android_build":{"java":"21"}`
	tests := []struct {
		name      string
		local     bool
		nodesOn   bool
		nodesJSON string
		status    string
		detail    string
	}{
		{"local toolchain", true, false, "", DoctorOK, "builds the app here"},
		{"a node builds it", false, true, `{"nodes":[{"id":"a","name":"mac-mini","device":"approved",` + fresh + `,"declared":{` + ab + `},"approved":{` + ab + `}}]}`, DoctorOK, "on a node: mac-mini"},
		{"offered, not approved", false, true, `{"nodes":[{"id":"a","name":"mac-mini","device":"approved",` + fresh + `,"declared":{` + ab + `},"approved":{}}]}`, DoctorWarn, "offers android-build, not approved"},
		{"approved, not heard from", false, true, `{"nodes":[{"id":"a","name":"mac-mini","device":"approved","declared":{` + ab + `},"approved":{` + ab + `}}]}`, DoctorWarn, "not heard from"},
		{"approved, no JDK any more", false, true, `{"nodes":[{"id":"a","name":"mac-mini","device":"approved",` + fresh + `,"declared":{},"approved":{` + ab + `}}]}`, DoctorWarn, "finds no JDK"},
		{"nothing", false, false, "", DoctorWarn, "nodes are off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			if tc.nodesJSON != "" {
				s.contents = map[string]string{registry: tc.nodesJSON}
			}
			cfg := doctorConfig(func(c *config.Settings) {
				c.Plaud.PhoneUpload = true
				c.Nodes.Enabled = tc.nodesOn
			})
			env := s.doctorEnv(cfg, nil)
			env.AndroidToolchain = func() error {
				if tc.local {
					return nil
				}
				return os.ErrNotExist
			}
			got := result(t, RunDoctor(context.Background(), env, DoctorRegistry), "plaud-app-build")
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%s %q, want %s with %q", got.Status, got.Detail, tc.status, tc.detail)
			}
			if got.Status != DoctorOK && got.Fix == "" {
				t.Error("a warning without a fix")
			}
		})
	}
	rep := RunDoctor(context.Background(), healthyMac().doctorEnv(doctorConfig(nil), nil), DoctorRegistry)
	for _, c := range rep.Checks {
		if c.ID == "plaud-app-build" {
			t.Error("the check runs without plaud.phone_upload")
		}
	}
}
