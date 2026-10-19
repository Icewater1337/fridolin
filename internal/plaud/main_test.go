package plaud

import (
	"github.com/Icewater1337/fridolin/internal/testutil"
	"testing"
)

var repoDir, pkgDir string

func TestMain(m *testing.M) {
	testutil.Main(m, testutil.MainOptions{Ready: func(p testutil.Paths) {
		repoDir, pkgDir = p.RepoDir, p.PackageDir
	}})
}
