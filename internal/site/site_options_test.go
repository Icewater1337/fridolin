package site

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// Instances can prepare their embedded process concurrently with independent
// install implementations; construction itself never installs anything.
func TestSiteOptionsKeepInstallersLocal(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			want := errors.New(name)
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := NewSiteManager(SiteOptions{
				DataDir: root,
				NPMInstall: func(got context.Context, dir string, prod bool) error {
					calls++
					if got != ctx || dir != filepath.Join(root, "site", "shell") || !prod {
						t.Errorf("install got context=%v dir=%q prod=%v", got == ctx, dir, prod)
					}
					if _, err := os.Stat(filepath.Join(dir, "server", "main.ts")); err != nil {
						t.Errorf("the embedded source was not ready before install: %v", err)
					}
					return want
				},
			})
			if calls != 0 {
				t.Fatal("construction installed dependencies")
			}
			if _, err := m.ensureShell(ctx); !errors.Is(err, want) || calls != 1 {
				t.Fatalf("prepare got %v (%d calls), want this instance's install error", err, calls)
			}
		})
	}
}

// Paths supplied by live callbacks are resolved at the original read points.
// The site's own client file still takes precedence over the shared callback.
func TestSiteOptionsKeepLivePaths(t *testing.T) {
	root := t.TempDir()
	shared, folder := "first-client", "first-brain"
	clientReads, brainReads := 0, 0
	m := NewSiteManager(SiteOptions{
		DataDir: root,
		GoogleClient: func() string {
			clientReads++
			return shared
		},
		Builder: SiteBuilderHooks{BrainDir: func(id string) string {
			brainReads++
			if id != "web" {
				t.Fatalf("brain ID = %q", id)
			}
			return folder
		}},
	})
	if clientReads != 0 || brainReads != 0 {
		t.Fatal("construction read a live path")
	}
	for _, next := range []string{"second", "third"} {
		shared, folder = next+"-client", next+"-brain"
		if got := m.googleClientFile(); got != shared {
			t.Errorf("shared client = %q, want %q", got, shared)
		}
		if got := m.builderBase("web"); got != filepath.Join(folder, ".fridolin-site") {
			t.Errorf("builder path = %q", got)
		}
	}
	own := filepath.Join(root, "site", "google_client.json")
	if err := os.MkdirAll(filepath.Dir(own), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.googleClientFile(); got != own || clientReads != 2 || brainReads != 2 {
		t.Fatalf("own client=%q reads=%d/%d", got, clientReads, brainReads)
	}
}

func TestSiteHostCapabilitiesStayLiveAndLocal(t *testing.T) {
	home := t.TempDir()
	nodeErr := errors.New("instance node unavailable")
	clientErr := errors.New("instance client rejected")
	m := NewSiteManager(SiteOptions{DataDir: t.TempDir(), Host: SiteHost{
		Home:             func() string { return home },
		Node:             func() (string, error) { return "", nodeErr },
		GoogleClientType: func([]byte) (string, error) { return "", clientErr },
	}})
	for i := 0; i < 2; i++ {
		home = t.TempDir()
		if got := m.sandboxInput("node", "deploy", "root", "runtime", nil).Home; got != home {
			t.Fatalf("profile home=%q, want current supplied home %q", got, home)
		}
	}
	if err := m.runNpmInstall(t.Context(), t.TempDir(), false); !errors.Is(err, nodeErr) {
		t.Fatalf("installer node lookup=%v", err)
	}
	if err := m.SetGoogleClient([]byte("synthetic")); !errors.Is(err, clientErr) {
		t.Fatalf("client validation=%v", err)
	}
	if _, err := os.Stat(m.dir("google_client.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected client was written: %v", err)
	}
}

// In the fridolin-core container the live site is told to bind its port on
// the container's interfaces (Docker forwards the published port there);
// outside it, and in a preview, it is not.
func TestSiteRuntimeContainer(t *testing.T) {
	for _, container := range []bool{false, true} {
		root := t.TempDir()
		m := NewSiteManager(SiteOptions{
			DataDir:    root,
			Container:  container,
			NPMInstall: func(context.Context, string, bool) error { return nil },
			Host: SiteHost{
				Node:    func() (string, error) { return "node", nil },
				Profile: func(sandbox.SitePolicy) (string, error) { return "(version 1)", nil },
			},
		})
		m.settings.Approved = "0123456789abcdef"
		read := func(name, label string) bool {
			t.Helper()
			if _, err := m.processCommand(t.Context(), name, defaultSiteListen, filepath.Join(root, "modules"), label); err != nil {
				t.Fatal(err)
			}
			var rt siteRuntime
			data, err := os.ReadFile(m.dir(name + ".runtime.json"))
			if err == nil {
				err = json.Unmarshal(data, &rt)
			}
			if err != nil {
				t.Fatal(err)
			}
			if rt.Listen != defaultSiteListen {
				t.Errorf("listen = %q, want %q", rt.Listen, defaultSiteListen)
			}
			return rt.Container
		}
		if got := read("site", ""); got != container {
			t.Errorf("Container %v: the live site's runtime says container=%v", container, got)
		}
		if read("preview", "branch") {
			t.Errorf("Container %v: a preview is told it runs in the container", container)
		}
	}
}
