package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/ghapp/ghapptest"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/sandboxrun/sandboxruntest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// A run in a Docker Sandbox with a GitHub grant: its token goes to the
// node in sandbox.run's github_token, never in the run's environment or
// arguments, and is revoked once the run is over. A run without a grant
// sends none.
func TestDockerSandboxGitHubToken(t *testing.T) {
	t.Parallel()
	fleet := &fakeFleet{node: &sandboxruntest.Node{Info: sandboxruntest.Ready("claude"), Output: vmOutput("hello from the VM")}}
	optionsOf(t).hooks.sandboxFleet = fleet
	optionsOf(t).waits.sandboxLook = 50 * time.Millisecond
	tb, _ := serveTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A", Routing: manifest.Routing{HandlesDMs: true},
		Sandbox: &manifest.Sandbox{Runtime: manifest.RuntimeDockerSandbox}})
	fake := useFakeGitHub(t, tb.github)
	creds, err := tb.github.client.Convert(context.Background(), ghapptest.Code)
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.github.secrets.save(creds); err != nil {
		t.Fatal(err)
	}

	grant := &chat.GitHubGrant{Repos: []string{"you/fridolin"}, Permissions: map[string]string{"issues": "write"}}
	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: apiTestDM, Sender: "15551234567", Text: "file the issue", Source: chat.SourceSchedule, GitHub: grant}})
	testutil.WaitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.sentTo(apiTestDM)) > 0 })
	minted := fake.LastMinted()
	var runs []nodeproto.SandboxRunParams
	fleet.node.Set(func(n *sandboxruntest.Node) { runs = slices.Clone(n.Runs) })
	if len(runs) != 1 {
		t.Fatalf("%d runs on the node", len(runs))
	}
	r := runs[0]
	if minted.Token == "" || r.GitHubToken != minted.Token {
		t.Errorf("the node got GitHub token %q, GitHub minted %q", r.GitHubToken, minted.Token)
	}
	if strings.Contains(strings.Join(r.Argv, " "), minted.Token) || strings.Contains(r.Stdin, minted.Token) {
		t.Error("the GitHub token is in the run's arguments or prompt")
	}
	for k, v := range r.Env {
		if strings.Contains(v, minted.Token) || k == "GH_CONFIG_DIR" || k == "GH_TOKEN" {
			t.Errorf("the run's environment has %s=%s", k, v)
		}
	}
	testutil.WaitFor(t, 5*time.Second, "the run's GitHub token to be revoked", func() bool { return len(fake.Live()) == 0 })

	tb.handleInbound(context.Background(), brain.Inbound{Inbound: chat.Inbound{JID: apiTestDM, Sender: "15551234567", Text: "hello", Source: "whatsapp"}})
	testutil.WaitFor(t, 10*time.Second, "the second reply", func() bool { return len(tb.sentTo(apiTestDM)) > 1 })
	fleet.node.Set(func(n *sandboxruntest.Node) { runs = slices.Clone(n.Runs) })
	if len(runs) != 2 || runs[1].GitHubToken != "" {
		t.Errorf("a run without a grant: %d runs, GitHub token %q", len(runs), runs[len(runs)-1].GitHubToken)
	}
}
