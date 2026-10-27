package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// forgetCore is a CoreRunner whose Status says what the test sets, and
// whose ForgetFiles and ForgetData record what they were asked to remove.
type forgetCore struct {
	CoreRunner // the steps of a move, which these tests never run
	status     nodeproto.CoreRuntime
	files      map[string][]string
	data       bool
}

func (f *forgetCore) Status(context.Context) (nodeproto.CoreRuntime, error) { return f.status, nil }

func (f *forgetCore) ForgetFiles(_ context.Context, dir string, names []string) ([]string, error) {
	if f.files == nil {
		f.files = map[string][]string{}
	}
	f.files[dir] = append(f.files[dir], names...)
	var out []string
	for _, n := range names {
		out = append(out, dir+"/"+n)
	}
	return out, nil
}

func (f *forgetCore) ForgetData(context.Context) ([]string, error) {
	if f.data {
		return nil, errors.New("removed twice")
	}
	f.data = true
	return []string{"bridge.db", "config.json"}, nil
}

const (
	thisDevice  = "0123456789abcdef0123456789abcdef"
	otherDevice = "fedcba9876543210fedcba9876543210"
)

// TestCoreForgetRemovesWhatMovesBrought (#266): forget removes the host
// secrets, the secret files and the data dir the record names, once
// another device's core holds the lease, and nothing the machine held
// before: a secret typed there, a file the move kept, a data dir that held
// data when a move imported into it.
func TestCoreForgetRemovesWhatMovesBrought(t *testing.T) {
	a, st := coreHostAgent(t, "icebase")
	core := &forgetCore{status: nodeproto.CoreRuntime{Device: thisDevice, Lease: &nodeproto.CoreLease{Device: thisDevice, State: nodeproto.LeaseLive}}}
	a.CoreRunner = core
	for _, n := range []string{hostsecrets.ClaudeOAuthToken, hostsecrets.B2VaultKey} {
		if err := st.Set(n, "test-value"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.noteReceived([]string{hostsecrets.ClaudeOAuthToken}); err != nil {
		t.Fatal(err)
	}
	// The local GitLab connection goes into the core's data dir.
	if err := a.noteFilesReceived("~/.credentials", []string{"dashboard-users.json", "local:gitlab.json"}); err != nil {
		t.Fatal(err)
	}
	if err := a.noteDataImport(testMove, nodeproto.DataDirEmpty); err != nil {
		t.Fatal(err)
	}
	// A later move finds the data dir held: it stays the moves'.
	if err := a.noteDataImport("m-20261024-1000-0001", nodeproto.DataDirHeld); err != nil {
		t.Fatal(err)
	}

	// The core runs here: the data dir and the files stay, and it says so.
	res, perr := callCore[nodeproto.CoreSecretsForgotten](t, a, nodeproto.MethodCoreSecretsForget, struct{}{})
	if perr != nil || len(res.Data) != 0 || len(res.Files) != 0 || core.data || len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "the core runs here") {
		t.Fatalf("forget where the core runs: %+v %v", res, perr)
	}
	if l := ReadCoreLeftovers(a.Dir); l.DataMove != testMove || !slices.Equal(l.Files, []string{"core/local-secrets/gitlab.json", "~/.credentials/dashboard-users.json"}) {
		t.Errorf("the record after a refused forget: %+v", l)
	}

	// Nobody holds the lease: still refused, the copy may be the only one.
	core.status.Lease = &nodeproto.CoreLease{Device: thisDevice, State: nodeproto.LeaseReleased}
	if res, _ := callCore[nodeproto.CoreSecretsForgotten](t, a, nodeproto.MethodCoreSecretsForget, struct{}{}); len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "may be the core's only one") || core.data {
		t.Fatalf("forget with a released lease: %+v", res)
	}

	// Another core holds it: everything the moves brought goes.
	core.status.Lease = &nodeproto.CoreLease{Device: otherDevice, State: nodeproto.LeaseLive}
	res, perr = callCore[nodeproto.CoreSecretsForgotten](t, a, nodeproto.MethodCoreSecretsForget, struct{}{})
	if perr != nil || len(res.Problems) != 0 || !slices.Equal(res.Data, []string{"bridge.db", "config.json"}) || !slices.Equal(res.Files, []string{"~/.credentials/dashboard-users.json", "/local:gitlab.json"}) {
		t.Fatalf("forget: %+v %v", res, perr)
	}
	if !slices.Equal(core.files["~/.credentials"], []string{"dashboard-users.json"}) || !slices.Equal(core.files[""], []string{"local:gitlab.json"}) {
		t.Errorf("files asked to forget: %+v", core.files)
	}
	// The token went when the earlier forgets ran; the B2 key was there
	// before and stays.
	if ok, _ := st.Has(hostsecrets.ClaudeOAuthToken); ok {
		t.Error("the received secret stays")
	}
	if ok, _ := st.Has(hostsecrets.B2VaultKey); !ok {
		t.Error("forget removed a secret the machine held before")
	}
	l := ReadCoreLeftovers(a.Dir)
	if l.Holds() || l.DataForgotten.IsZero() {
		t.Errorf("the record after forgetting: %+v", l)
	}
	// Again: nothing is left, and the data dir is not removed twice.
	if res, perr := callCore[nodeproto.CoreSecretsForgotten](t, a, nodeproto.MethodCoreSecretsForget, struct{}{}); perr != nil || len(res.Problems)+len(res.Data)+len(res.Files)+len(res.Forgotten) != 0 {
		t.Errorf("a second forget: %+v %v", res, perr)
	}
}

// TestCoreForgetKeepsTheOriginalHome: a machine whose data dir held the
// core's data when a move imported into it was the core's home before:
// forget keeps it and says why, and never asks the core to remove it.
func TestCoreForgetKeepsTheOriginalHome(t *testing.T) {
	a, _ := coreHostAgent(t, "mini")
	core := &forgetCore{status: nodeproto.CoreRuntime{Device: thisDevice, Lease: &nodeproto.CoreLease{Device: otherDevice, State: nodeproto.LeaseLive}}}
	a.CoreRunner = core
	if err := a.noteDataImport(testMove, nodeproto.DataDirHeld); err != nil {
		t.Fatal(err)
	}
	// An unknown state (an older core) counts as held, too.
	if err := a.noteDataImport("m-20261024-1000-0001", ""); err != nil {
		t.Fatal(err)
	}
	res, perr := callCore[nodeproto.CoreSecretsForgotten](t, a, nodeproto.MethodCoreSecretsForget, struct{}{})
	if perr != nil || core.data || len(res.Data) != 0 || !strings.Contains(res.DataKept, "before move "+testMove) {
		t.Fatalf("forget on the original home: %+v %v", res, perr)
	}
	if l := ReadCoreLeftovers(a.Dir); l.OwnDataMove != testMove || l.DataMove != "" {
		t.Errorf("the record: %+v", l)
	}
}

// TestCoreReceivedRecordIsChecked: a record that names more than a move
// carries (a written file) cannot make forget remove more: names, folders
// and file names outside what a move brings are dropped.
func TestCoreReceivedRecordIsChecked(t *testing.T) {
	a, _ := coreHostAgent(t, "icebase")
	raw, _ := json.Marshal(map[string]any{
		"names": []string{hostsecrets.ClaudeOAuthToken, "login.keychain"},
		"files": map[string][]string{"~/.credentials": {"dashboard-users.json", "../../.ssh/id_ed25519"}, "/etc": {"dashboard-users.json"}},
		"local": []string{"local:gitlab.json", "local:../bridge.db"},
		"data":  map[string]any{"move": "../../x", "at": time.Now()},
	})
	if err := os.WriteFile(filepath.Join(a.Dir, coreReceivedFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := a.readReceived()
	if !slices.Equal(s.Names, []string{hostsecrets.ClaudeOAuthToken}) || len(s.Files) != 1 || !slices.Equal(s.Files["~/.credentials"], []string{"dashboard-users.json"}) || s.Data != nil || !slices.Equal(s.Local, []string{"local:gitlab.json"}) {
		t.Errorf("the checked record: %+v", s)
	}
}

// TestReadCoreLeftovers: a machine is a former core host when its latest
// move took the core away from it (the source of a move that is done, or
// the target of one that did not end there); an import of a version
// before #266 is said to be unrecorded.
func TestReadCoreLeftovers(t *testing.T) {
	a, _ := coreHostAgent(t, "icebase")
	save := func(id, role, phase string, updated time.Time, steps ...string) {
		t.Helper()
		m := &moveRun{State: nodeproto.CoreMoveState{Move: id, Role: role, Phase: phase, Updated: updated}}
		for _, s := range steps {
			m.State.Steps = append(m.State.Steps, nodeproto.CoreMoveStep{Step: s})
		}
		a.saveMove(m)
	}
	if l := ReadCoreLeftovers(a.Dir); l.Former || l.LastMove != "" {
		t.Errorf("no moves: %+v", l)
	}
	now := time.Now().UTC()
	save("m-20261024-0900-0001", nodeproto.MoveTarget, nodeproto.MoveDone, now.Add(-time.Hour), "import")
	if l := ReadCoreLeftovers(a.Dir); l.Former || !l.Unrecorded {
		t.Errorf("the core came here: %+v", l)
	}
	save("m-20261024-1000-0002", nodeproto.MoveSource, nodeproto.MoveDone, now)
	if l := ReadCoreLeftovers(a.Dir); !l.Former || l.LastMove != "m-20261024-1000-0002" || !l.Unrecorded {
		t.Errorf("the core went away: %+v", l)
	}
	save("m-20261024-1100-0003", nodeproto.MoveTarget, nodeproto.MoveRolledBack, now.Add(time.Hour), "import")
	if l := ReadCoreLeftovers(a.Dir); !l.Former || l.LastMove != "m-20261024-1100-0003" {
		t.Errorf("a move here that rolled back: %+v", l)
	}
}
