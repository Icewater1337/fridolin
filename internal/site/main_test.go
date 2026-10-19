package site

import (
	"github.com/Icewater1337/fridolin/internal/testutil"
	"os"
	"testing"
)

var repoDir string

func TestMain(m *testing.M) {
	testutil.Main(m, testutil.MainOptions{
		Ready: func(p testutil.Paths) { repoDir = p.RepoDir },
		Extra: func() (bool, int) {
			if os.Getenv("FRIDOLIN_FAKE_SESSION") == "1" {
				return true, fakeSession()
			}
			return false, 0
		},
	})
}
