package site

import (
	"context"

	"fmt"

	"github.com/Icewater1337/fridolin/internal/fsx"

	"github.com/Icewater1337/fridolin/internal/sandbox"

	"github.com/Icewater1337/fridolin/internal/testutil"

	"net"

	"os"
	"os/exec"
	"path/filepath"

	"runtime"

	"strings"
	"testing"
	"time"
)

// newTestSite is a SiteManager in a temp data dir whose npm installs only
// copy the site package's own node_modules (no network in tests). A copy, not
// a link: the live site's sandbox allows the deploy's real path, not the
// checkout a link would lead to.
func newTestSite(t *testing.T) *SiteManager {
	t.Helper()
	return NewSiteManager(testSiteOptions(t))
}

func testSiteOptions(t *testing.T) SiteOptions {
	t.Helper()
	opts := SiteOptions{DataDir: t.TempDir(), GoogleClient: func() string { return "" }}
	opts.NPMInstall = func(ctx context.Context, dir string, prod bool) error {
		src := filepath.Join(repoDir, "site", "node_modules")
		if !fsx.Exists(src) {
			// What an install leaves even then: a deploy without it is built again.
			return os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700)
		}
		if out, err := exec.CommandContext(ctx, "cp", "-R", src, filepath.Join(dir, "node_modules")).CombinedOutput(); err != nil {
			return fmt.Errorf("copying node_modules: %v %s", err, out)
		}
		return nil
	}
	return opts
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newBareRepo is an empty "remote" for the site repository.
func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site.git")
	gitT(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", dir)
	return dir
}

func waitDeploy(t *testing.T, m *SiteManager) *DeploySnapshot {
	t.Helper()
	var j *DeploySnapshot
	testutil.WaitFor(t, 2*time.Minute, "the deploy to finish", func() bool {
		j = m.DeployStatus()
		return j != nil && j.State != "running"
	})
	if j.State != "done" {
		t.Fatalf("deploy %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	return j
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// newTestBuilderSite is a test site whose one brain, "web", has a folder
// and runs without a sandbox (tests cannot nest sandbox-exec).
func newTestBuilderSite(t *testing.T) (*SiteManager, string) {
	t.Helper()
	opts := testSiteOptions(t)
	brainDir := filepath.Join(t.TempDir(), "web-brain")
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts.Builder = SiteBuilderHooks{
		BrainDir: func(id string) string {
			if id == "web" {
				return brainDir
			}
			return ""
		},
		Sandbox:    func(_, bin string, args []string) (string, []string, []string, error) { return bin, args, nil, nil },
		BridgeURL:  func() string { return "http://127.0.0.1:1" },
		Executable: func() (string, error) { return "/usr/bin/true", nil },
	}
	opts.WorkspaceInstall = func(ctx context.Context, brainID, dir string) error { return opts.NPMInstall(ctx, dir, false) }
	return NewSiteManager(opts), brainDir
}

// requireSiteRuntime skips a test that runs the live site without what it
// needs: node, the site package's node_modules, and on macOS a sandbox-exec
// that can apply a profile (not inside another sandbox), since the live site
// runs only under one there.
func requireSiteRuntime(t *testing.T) {
	t.Helper()
	if _, err := findNode(); err != nil {
		t.Skip("node not installed")
	}
	if !fsx.Exists(filepath.Join(repoDir, "site", "node_modules")) {
		t.Skip("run npm install in site/ first")
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command(sandbox.ExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			t.Skipf("%s cannot apply a profile here: %v: %s", sandbox.ExecPath, err, out)
		}
	}
}

// permOf returns the permission bits of path.
func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}
