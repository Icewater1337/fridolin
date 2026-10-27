package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// Fake tokens, never real ones.
const (
	testGitHubToken = "test-github-token-0001"
	testGitLabToken = "test-gitlab-token-0001"
)

func testIdentities(expires time.Time) []nodeproto.GitIdentity {
	return []nodeproto.GitIdentity{
		{ID: "github", Label: "GitHub", Kind: "github", Host: "github.com", Token: testGitHubToken, Expires: expires},
		{ID: "gitlab", Label: "GitLab", Kind: "gitlab", Host: "gitlab.example.com", Name: "Jane Doe", Email: "jane@example.com", Token: testGitLabToken},
	}
}

// identitiesCall serves fride.identities as the core sends it.
func identitiesCall(t *testing.T, a *Agent, ids []nodeproto.GitIdentity) (nodeproto.FrideIdentitiesResult, *nodeproto.Error) {
	t.Helper()
	raw, err := json.Marshal(nodeproto.FrideIdentities{Identities: ids})
	if err != nil {
		t.Fatal(err)
	}
	out, perr := a.frideIdentitiesCall(context.Background(), raw)
	if perr != nil {
		return nodeproto.FrideIdentitiesResult{}, perr
	}
	return out.(nodeproto.FrideIdentitiesResult), nil
}

// countCalls counts FrIDE's calls of "METHOD path" prefix.
func countCalls(ff *testutil.FakeFride, prefix string) int {
	n := 0
	for _, c := range ff.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestFrideIdentities: the node puts the identities the core gives into
// FrIDE once, removes those it no longer gets (a 404 counts as removed),
// and never writes a token into its state dir or its log.
func TestFrideIdentities(t *testing.T) {
	ff := testutil.StartFakeFride(t)
	a := pairedAgent(t, ff)
	var logs syncLog
	a.Logf = logs.logf
	ids := testIdentities(time.Now().Add(time.Hour))

	res, perr := identitiesCall(t, a, ids)
	if perr != nil || !res.Supported || !slices.Equal(res.Pushed, []string{"github", "gitlab"}) || res.Error != "" {
		t.Fatalf("first push: %+v %v", res, perr)
	}
	got := ff.GitIdentities()
	if got["github"].Token != testGitHubToken || got["gitlab"].Token != testGitLabToken || got["gitlab"].Host != "gitlab.example.com" || got["gitlab"].Email != "jane@example.com" {
		t.Fatalf("FrIDE has %+v", got)
	}
	// The same again: FrIDE has them from this pairing.
	puts := countCalls(ff, "PUT /api/companion/git/identities/")
	if res, _ := identitiesCall(t, a, ids); !slices.Equal(res.Pushed, []string{"github", "gitlab"}) || countCalls(ff, "PUT /api/companion/git/identities/") != puts {
		t.Errorf("the same identities went again: %+v", res)
	}

	// GitLab removed on the core: FrIDE loses it.
	res, _ = identitiesCall(t, a, ids[:1])
	if !slices.Equal(res.Removed, []string{"gitlab"}) || len(ff.GitIdentities()) != 1 {
		t.Fatalf("removing gitlab: %+v, FrIDE has %v", res, ff.GitIdentities())
	}
	// One the node put there that FrIDE no longer has is removed too.
	if err := a.savePushedIdentities([]string{"github", "work"}); err != nil {
		t.Fatal(err)
	}
	refreshed := nodeproto.GitIdentity{ID: "github", Label: "GitHub", Kind: "github", Host: "github.com", Token: "test-github-token-0002"}
	res, _ = identitiesCall(t, a, []nodeproto.GitIdentity{refreshed})
	if !slices.Equal(res.Removed, []string{"work"}) || res.Error != "" || ff.GitIdentities()["github"].Token != "test-github-token-0002" {
		t.Errorf("a 404: %+v", res)
	}

	// None: FrIDE has none of Fridolin's, and the state dir no file.
	if res, _ := identitiesCall(t, a, nil); !slices.Equal(res.Removed, []string{"github"}) || len(ff.GitIdentities()) != 0 {
		t.Errorf("none: %+v, FrIDE has %v", res, ff.GitIdentities())
	}
	if _, err := os.Stat(filepath.Join(a.Dir, frideIdentitiesFile)); !os.IsNotExist(err) {
		t.Errorf("the record stays: %v", err)
	}
	identitiesCall(t, a, ids)
	data, err := os.ReadFile(filepath.Join(a.Dir, frideIdentitiesFile))
	if err != nil || string(data) != "{\"pushed\":[\"github\",\"gitlab\"]}\n" {
		t.Errorf("the record: %s %v", data, err)
	}
	for _, tok := range []string{testGitHubToken, testGitLabToken, "test-github-token-0002"} {
		if strings.Contains(logs.String(), tok) {
			t.Errorf("the log shows a token: %s", logs.String())
		}
	}

	// What the core sends is checked first.
	for _, bad := range [][]nodeproto.GitIdentity{
		{{ID: "none", Kind: "github", Host: "github.com", Token: "x"}},
		{{ID: "github", Kind: "github", Host: "github.com"}},
		{{ID: "github", Kind: "svn", Host: "github.com", Token: "x"}},
		{{ID: "github", Kind: "github", Host: "GitHub.com", Token: "x"}},
		{{ID: "github", Kind: "github", Host: "github.com", Token: "x"}, {ID: "github", Kind: "github", Host: "github.com", Token: "y"}},
	} {
		if _, perr := identitiesCall(t, a, bad); perr == nil || perr.Code != nodeproto.CodeBadRequest {
			t.Errorf("%+v: %v", bad, perr)
		}
	}
}

// A FrIDE from before git identities gets nothing, and one that does not
// answer gets them once it does.
func TestFrideIdentitiesWaitForFrIDE(t *testing.T) {
	ff := testutil.StartFakeFride(t)
	ff.SetGitIdentitiesSupported(false)
	a := pairedAgent(t, ff)
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Approved: nodeproto.Capabilities{Fride: &nodeproto.FrideCap{URL: ff.URL}}})
	ids := testIdentities(time.Time{})
	res, _ := identitiesCall(t, a, ids)
	if res.Supported || len(res.Pushed) != 0 || !strings.Contains(res.Error, "update it") || countCalls(ff, "PUT ") != 0 {
		t.Fatalf("an old FrIDE: %+v", res)
	}
	// FrIDE is updated: the next check passes them on.
	ff.SetGitIdentitiesSupported(true)
	a.frideCheck(context.Background())
	a.identitiesTick(context.Background())
	if len(ff.GitIdentities()) != 2 {
		t.Fatalf("after the update FrIDE has %v", ff.GitIdentities())
	}
	// Paired again (a new FrIDE): the next check passes them on again.
	ff2 := testutil.StartFakeFride(t)
	if _, err := PairFride(context.Background(), FridePairOptions{Dir: a.Dir, URL: ff2.URL, Code: ff2.NewCode()}); err != nil {
		t.Fatal(err)
	}
	a.frideCheck(context.Background())
	a.identitiesTick(context.Background())
	if len(ff2.GitIdentities()) != 2 {
		t.Errorf("the FrIDE paired anew has %v", ff2.GitIdentities())
	}
}

// The node of the FrIDE in a Docker Sandbox holds the identities back: a
// real token must never enter the VM. It says why, and asks for none.
func TestFrideIdentitiesHeldFromTheSandbox(t *testing.T) {
	ff := testutil.StartFakeFride(t)
	a := pairedAgent(t, ff)
	a.Config.Service = FrideSandboxService
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Approved: nodeproto.Capabilities{Fride: &nodeproto.FrideCap{URL: ff.URL}}})
	res, perr := identitiesCall(t, a, testIdentities(time.Now().Add(time.Minute)))
	if perr != nil || res.Held == "" || len(res.Pushed) != 0 || len(ff.GitIdentities()) != 0 || countCalls(ff, "PUT ") != 0 {
		t.Fatalf("the sandboxed FrIDE: %+v %v, FrIDE has %v", res, perr, ff.GitIdentities())
	}
	asks := a.gitIDs.init()
	a.identitiesTick(context.Background())
	select {
	case q := <-asks:
		t.Errorf("the sandbox's node asked: %+v", q)
	default:
	}
	if len(ff.GitIdentities()) != 0 {
		t.Errorf("the tick passed them on: %v", ff.GitIdentities())
	}
}

// The node asks the core for its identities when it has none yet, and
// for a new GitHub token well before the one FrIDE has runs out.
func TestFrideIdentitiesAsk(t *testing.T) {
	ff := testutil.StartFakeFride(t)
	a := pairedAgent(t, ff)
	asks := a.gitIDs.init()
	ask := func() string {
		select {
		case q := <-asks:
			return q.Reason
		default:
			return ""
		}
	}
	// Not approved: no asking.
	a.identitiesTick(context.Background())
	if r := ask(); r != "" {
		t.Fatalf("asked unapproved: %q", r)
	}
	a.setState(nodeproto.State{Device: nodeproto.DeviceApproved, Approved: nodeproto.Capabilities{Fride: &nodeproto.FrideCap{URL: ff.URL}}})
	a.identitiesTick(context.Background())
	if r := ask(); r != "none yet" {
		t.Fatalf("first ask: %q", r)
	}
	a.identitiesTick(context.Background())
	if r := ask(); r != "" {
		t.Errorf("asked again at once: %q", r)
	}
	identitiesCall(t, a, testIdentities(time.Now().Add(time.Hour)))
	a.gitIDs.mu.Lock()
	a.gitIDs.asked = time.Time{}
	a.gitIDs.mu.Unlock()
	a.identitiesTick(context.Background())
	if r := ask(); r != "" {
		t.Errorf("asked with an hour left: %q", r)
	}
	identitiesCall(t, a, testIdentities(time.Now().Add(identitiesRefreshBefore-time.Minute)))
	a.identitiesTick(context.Background())
	if r := ask(); r != "expiring" {
		t.Errorf("a token that runs out soon: %q", r)
	}
	a.identitiesTick(context.Background())
	if r := ask(); r != "" {
		t.Errorf("asked again within %v: %q", identitiesAskEvery, r)
	}
}
