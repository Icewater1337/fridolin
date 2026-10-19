package site

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDeployRejectsNonCommitBeforeEffects(t *testing.T) {
	for _, value := range []string{"", "short", "--upload-pack=evil", "../../../../outside", strings.Repeat("a", 39) + "/", strings.Repeat("a", 40) + "\ncommand"} {
		t.Run(value, func(t *testing.T) {
			m := newTestSite(t)
			// A nil job makes any attempt to proceed into checkout fail immediately.
			if err := m.runDeploy(context.Background(), nil, value); err == nil || err.Error() != "name a commit by its full id" {
				t.Fatalf("worker accepted %q: %v", value, err)
			}
			if err := m.Deploy(context.Background(), value); err == nil || err.Error() != "name a commit by its full id" {
				t.Fatalf("entry accepted %q: %v", value, err)
			}
			if m.DeployStatus() != nil {
				t.Fatal("invalid commit started a job")
			}
		})
	}
}

func TestFetchValidatesPersistedBranchBeforeGit(t *testing.T) {
	for _, branch := range []string{"--exec=evil", "main..other", "main\ncommand", "main;command"} {
		m := newTestSite(t)
		if err := os.MkdirAll(m.repoDir(), 0700); err != nil {
			t.Fatal(err)
		}
		m.settings.Repo.Branch = branch
		if _, err := m.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "not a branch name") {
			t.Fatalf("branch %q: %v", branch, err)
		}
	}
}
