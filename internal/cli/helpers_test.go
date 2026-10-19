package cli

import (
	"flag"
	"github.com/Icewater1337/fridolin/examples"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"path/filepath"
	"testing"
)

var testHome, pkgDir, repoDir string
var update = flag.Bool("update", false, "rewrite golden fixtures")
var brainTemplates = registry.TemplatesFrom(examples.FS, prompt.IdentityMapPath)

func TestMain(m *testing.M) {
	testutil.Main(m, testutil.MainOptions{Version: claudecli.TestedClaudeVersion, RunMain: Main, Ready: func(p testutil.Paths) { testHome, pkgDir, repoDir = p.Home, p.PackageDir, p.RepoDir }})
}

// CLI check tests only probe the provider's version; their stand-in does not
// depend on environment variables permitted to application child processes.
func fakeClaudeEnv(t *testing.T, _ int) string {
	return versionCLI(t, "", claudecli.TestedClaudeVersion+" (Claude Code)")
}
func assertGolden(t testing.TB, name string, got []byte) {
	t.Helper()
	testutil.AssertGolden(t, filepath.Join(repoDir, "cmd", "fridolin", "testdata", "golden"), name, got, *update)
}
