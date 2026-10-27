package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
)

// What moves brought this machine, and forgetting it (#266; docs/CORE.md,
// "Forgetting a former core host"). A core host that no longer runs the
// core must not keep the WhatsApp session or the credentials a move
// brought it, once the owner says forget (the keep/forget poll, or
// !revoke). It must never remove what it held before a move either: the
// core's original home keeps its own data dir, and a machine its own
// secrets. So every removal is keyed off a record the agent writes as
// the move brings each thing (core-secrets-received.json): the host
// secrets it received and did not hold, the secret files it wrote and
// that were not there, and the core's data dir when a move's import put
// it into an empty one.

// CoreForgetter is a CoreRunner that also removes what moves brought.
type CoreForgetter interface {
	// ForgetFiles removes the named secret files from dir (a folder
	// under ~) in the core's home (fridolin core files forget), and
	// returns those it removed as ~/dir/name; one not there is no error.
	// Local files ("local:gitlab.json") go from the core's data dir,
	// with dir "".
	ForgetFiles(ctx context.Context, dir string, names []string) ([]string, error)
	// ForgetData empties the core's data dir (fridolin core forget-data),
	// which refuses while a core runs on it or no other device's core
	// holds the lease, and returns the entries it removed.
	ForgetData(ctx context.Context) ([]string, error)
}

// coreReceivedState is core-secrets-received.json.
type coreReceivedState struct {
	// Names are the host secrets received over the node channel that the
	// machine did not hold before.
	Names []string `json:"names"`
	// Files are the secret files moves wrote into the core's credentials
	// folder, by that folder under ~; never one that was there before,
	// which a move keeps as it is.
	Files map[string][]string `json:"files,omitempty"`
	// Local are the local files moves wrote into the core's data dir
	// (secretxfer.LocalFileName, "local:gitlab.json") that were not there.
	Local []string `json:"local,omitempty"`
	// Data is set when a move's import put the core's data dir into an
	// empty one: it, and whatever a core on it wrote since, came with the
	// moves, and forgetting removes it.
	Data *coreDataRecord `json:"data,omitempty"`
	// OwnData is set when a move's import found the data dir holding a
	// core's data and Data was unset: that data predates the moves (the
	// machine was the core's home before), and forgetting keeps it.
	OwnData *coreDataRecord `json:"own_data,omitempty"`
	// DataForgotten is when forgetting last removed the data dir a move
	// brought, until a move brings one again.
	DataForgotten time.Time `json:"data_forgotten,omitzero"`
}

// coreDataRecord is the move that found the data dir as Data or OwnData
// says, and when.
type coreDataRecord struct {
	Move string    `json:"move"`
	At   time.Time `json:"at"`
}

func (s coreReceivedState) empty() bool {
	return len(s.Names) == 0 && len(s.Files) == 0 && len(s.Local) == 0 && s.Data == nil && s.OwnData == nil && s.DataForgotten.IsZero()
}

// readReceived reads the record, keeping only what a move carries: a name
// it does not, a folder outside ~ or a file name it never writes is
// dropped, so that a written file cannot make forgetting remove more.
func (a *Agent) readReceived() coreReceivedState {
	var s coreReceivedState
	if a.Dir == "" {
		return s
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, coreReceivedFile))
	if err != nil || json.Unmarshal(data, &s) != nil {
		return coreReceivedState{}
	}
	s.Names = slices.DeleteFunc(s.Names, func(n string) bool { return !slices.Contains(secretxfer.MoveNames, n) })
	for dir, names := range s.Files {
		names = slices.DeleteFunc(names, func(n string) bool { return !secretxfer.FileName(n) })
		if secretxfer.CheckFilesDir(dir) != nil || len(names) == 0 {
			delete(s.Files, dir)
			continue
		}
		s.Files[dir] = names
	}
	s.Local = slices.DeleteFunc(s.Local, func(n string) bool { return !secretxfer.LocalFileName(n) })
	for _, r := range []**coreDataRecord{&s.Data, &s.OwnData} {
		if *r != nil && nodeproto.CheckMoveID((*r).Move) != nil {
			*r = nil
		}
	}
	return s
}

func (a *Agent) writeReceived(s coreReceivedState) error {
	if a.Dir == "" {
		return nil
	}
	p := filepath.Join(a.Dir, coreReceivedFile)
	if s.empty() {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if s.Names == nil {
		s.Names = []string{}
	}
	data, _ := json.Marshal(s)
	return writeFile(p, append(data, '\n'), 0o600)
}

// coreReceived are the host secrets core.secrets.forget would remove.
func (a *Agent) coreReceived() []string { return a.readReceived().Names }

func (a *Agent) noteReceived(names []string) error {
	if len(names) == 0 {
		return nil
	}
	s := a.readReceived()
	s.Names = append(s.Names, names...)
	sort.Strings(s.Names)
	s.Names = slices.Compact(s.Names)
	return a.writeReceived(s)
}

// noteFilesReceived records the secret files a move wrote into dir, and
// the local files it wrote into the core's data dir.
func (a *Agent) noteFilesReceived(dir string, names []string) error {
	var creds, local []string
	for _, n := range names {
		if secretxfer.LocalFileName(n) {
			local = append(local, n)
		} else {
			creds = append(creds, n)
		}
	}
	if secretxfer.CheckFilesDir(dir) != nil {
		creds = nil
	}
	if len(creds) == 0 && len(local) == 0 {
		return nil
	}
	s := a.readReceived()
	if len(creds) > 0 {
		if s.Files == nil {
			s.Files = map[string][]string{}
		}
		all := append(s.Files[dir], creds...)
		sort.Strings(all)
		s.Files[dir] = slices.Compact(all)
	}
	s.Local = append(s.Local, local...)
	sort.Strings(s.Local)
	s.Local = slices.Compact(s.Local)
	return a.writeReceived(s)
}

// noteDataImport records, before move imports the core's data dir here,
// whether the move brings it: the data dir is empty (state
// nodeproto.DataDirEmpty), so all of it comes with the moves; or it holds
// data of before, which stays the machine's own. A data dir a move
// brought earlier stays the moves' whatever a later import finds, and a
// state that is not known counts as held.
func (a *Agent) noteDataImport(move, state string) error {
	s := a.readReceived()
	r := &coreDataRecord{Move: move, At: time.Now().UTC().Truncate(time.Second)}
	switch {
	case s.Data != nil:
		return nil
	case state == nodeproto.DataDirEmpty:
		s.Data, s.DataForgotten = r, time.Time{}
		a.logf("node: core move %s: the core's data dir here is empty: forgetting this machine later removes what the move brings", move)
	case s.OwnData != nil:
		return nil
	default:
		s.OwnData = r
		a.logf("node: core move %s: the core's data dir here holds data from before the move: forgetting this machine keeps it", move)
	}
	return a.writeReceived(s)
}

// coreForget is core.secrets.forget: it removes what moves brought this
// machine, as the record says, and nothing else: the core's data dir and
// the secret files first, while the host secrets that read the lease are
// still here, and only once another device's core holds the lease; then
// the host secrets. What it could not remove stays in the record, for the
// next forget.
func (a *Agent) coreForget(ctx context.Context) (any, *nodeproto.Error) {
	rec := a.readReceived()
	res := nodeproto.CoreSecretsForgotten{Forgotten: []string{}}
	if rec.Data != nil || len(rec.Files) > 0 || len(rec.Local) > 0 {
		f, why := a.coreForgetter(ctx)
		if why != "" {
			res.Problems = append(res.Problems, "kept the core's data dir and the secret files a move brought: "+why)
		} else {
			if rec.Data != nil {
				removed, err := f.ForgetData(ctx)
				if err != nil {
					res.Problems = append(res.Problems, "removing the core's data dir: "+err.Error())
				} else {
					res.Data, rec.Data, rec.DataForgotten = orEmpty(removed), nil, time.Now().UTC().Truncate(time.Second)
				}
			}
			dirs := make([]string, 0, len(rec.Files))
			for dir := range rec.Files {
				dirs = append(dirs, dir)
			}
			sort.Strings(dirs)
			for _, dir := range dirs {
				removed, err := f.ForgetFiles(ctx, dir, rec.Files[dir])
				if err != nil {
					res.Problems = append(res.Problems, "removing the secret files in "+dir+": "+err.Error())
					continue
				}
				res.Files = append(res.Files, removed...)
				delete(rec.Files, dir)
			}
			// The local files (the GitLab connection) a move wrote into
			// the data dir; gone already with it when it went whole.
			if len(rec.Local) > 0 {
				removed, err := f.ForgetFiles(ctx, "", rec.Local)
				if err != nil {
					res.Problems = append(res.Problems, "removing the local files in the core's data dir: "+err.Error())
				} else {
					res.Files, rec.Local = append(res.Files, removed...), nil
				}
			}
		}
	}
	if rec.Data == nil && res.Data == nil {
		res.DataKept = a.dataKept(rec)
	}
	if len(rec.Names) > 0 {
		st, _, perr := a.coreStore()
		d, ok := st.(hostsecrets.Deleter)
		switch {
		case perr != nil:
			res.Problems = append(res.Problems, perr.Message)
		case !ok:
			res.Problems = append(res.Problems, "this store cannot remove a secret")
		default:
			var failed []string
			for _, n := range rec.Names {
				if err := d.Delete(n); err != nil {
					failed = append(failed, n)
					continue
				}
				res.Forgotten = append(res.Forgotten, n)
			}
			if len(failed) > 0 {
				res.Problems = append(res.Problems, "could not remove the host secrets "+strings.Join(failed, ", "))
			}
			rec.Names = failed
		}
	}
	if err := a.writeReceived(rec); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	result := "ok"
	if len(res.Problems) > 0 {
		result = strings.Join(res.Problems, "; ")
	}
	a.transferLog(map[string]any{"direction": "forgot", "names": res.Forgotten, "files": res.Files, "data": res.Data, "result": result})
	a.logf("node: core-host: forgot %d host secrets, %d secret files and %d entries of the core's data dir", len(res.Forgotten), len(res.Files), len(res.Data))
	return res, nil
}

// coreForgetter is the runner that removes what a move brought, once
// that is safe: no move runs here, and another device's core holds the
// lease, live, so that this machine's copy is no core's only one and no
// rollback needs it. why says otherwise.
func (a *Agent) coreForgetter(ctx context.Context) (CoreForgetter, string) {
	r, perr := a.coreRunner()
	if perr != nil {
		return nil, perr.Message
	}
	f, ok := r.(CoreForgetter)
	if !ok {
		return nil, "this machine's core cannot remove them"
	}
	if !a.waitMovesEnd(ctx) {
		return nil, "a core move runs on this machine"
	}
	st, err := r.Status(ctx)
	switch {
	case err != nil:
		return nil, "its core cannot be read: " + err.Error()
	case st.HeldBy():
		return nil, "the core runs here"
	case !st.LiveElsewhere():
		return nil, "no other machine's core holds the lease, so this copy may be the core's only one"
	}
	return f, ""
}

// waitMovesEnd waits until no move runs on this machine, a few polls of
// the move at most: the owner is asked to forget once the core runs on
// the target, a moment before the source's watch sees it and ends.
func (a *Agent) waitMovesEnd(ctx context.Context) bool {
	deadline := time.Now().Add(3 * a.movePoll())
	for {
		a.moves.mu.Lock()
		busy := slices.ContainsFunc(mapValues(a.moves.runs), func(o *moveRun) bool { return !nodeproto.MoveFinal(o.State.Phase) })
		a.moves.mu.Unlock()
		if !busy {
			return true
		}
		if time.Now().After(deadline) || !wait(ctx, min(a.movePoll(), 2*time.Second)) {
			return false
		}
	}
}

// dataKept says why forgetting leaves the core's data dir here when no
// move brought it: "" when no move imported one.
func (a *Agent) dataKept(rec coreReceivedState) string {
	switch {
	case rec.OwnData != nil:
		return fmt.Sprintf("Its own data dir stays: it held the core's data before move %s imported into it", rec.OwnData.Move)
	case rec.DataForgotten.IsZero() && a.importedUnrecorded():
		return "A move imported the core's data dir there before fridolin recorded what a move brings, so it stays: remove it by hand (docs/CORE.md, \"Forgetting a former core host\")"
	}
	return ""
}

// importedUnrecorded reports whether a move imported here as the target
// although the record has no word on the data dir: a version before #266.
func (a *Agent) importedUnrecorded() bool {
	for _, m := range a.moveRecords() {
		if m.State.Role == nodeproto.MoveTarget && slices.ContainsFunc(m.State.Steps, func(s nodeproto.CoreMoveStep) bool { return s.Step == "import" }) {
			return true
		}
	}
	return false
}

// moveRecords are the moves in the state dir, oldest first.
func (a *Agent) moveRecords() []*moveRun {
	if a.Dir == "" {
		return nil
	}
	entries, _ := os.ReadDir(filepath.Join(a.Dir, coreMovesDir))
	var out []*moveRun
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || nodeproto.CheckMoveID(id) != nil {
			continue
		}
		if m, err := a.loadMove(id); err == nil {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].State.Updated.Before(out[j].State.Updated) })
	return out
}

// CoreLeftovers is what moves left in a node's state dir, as `fridolin
// doctor --node` reports it: read from the record alone, without asking
// the core.
type CoreLeftovers struct {
	// Secrets, Files (~/dir/name) and DataMove (the move whose import
	// put the data dir into an empty one, "" for none) are what
	// forgetting would remove.
	Secrets  []string
	Files    []string
	DataMove string
	// OwnDataMove is the move that found the data dir holding the
	// machine's own data, which stays; Unrecorded is a move that
	// imported here before the record said whether it brought the data
	// dir (a version before #266).
	OwnDataMove string
	Unrecorded  bool
	// DataForgotten is when forgetting removed the data dir a move had
	// brought.
	DataForgotten time.Time
	// LastMove is the latest move on this machine, and Former whether the
	// core went away in it: this machine was its source and it is done,
	// or its target and the core did not stay.
	LastMove string
	Former   bool
}

// Holds reports whether forgetting would remove anything.
func (l CoreLeftovers) Holds() bool {
	return len(l.Secrets) > 0 || len(l.Files) > 0 || l.DataMove != ""
}

// ReadCoreLeftovers reads what moves left in state dir dir.
func ReadCoreLeftovers(dir string) CoreLeftovers {
	a := &Agent{Dir: dir}
	rec := a.readReceived()
	l := CoreLeftovers{Secrets: rec.Names, DataForgotten: rec.DataForgotten,
		Unrecorded: rec.Data == nil && rec.OwnData == nil && rec.DataForgotten.IsZero() && a.importedUnrecorded()}
	for d, names := range rec.Files {
		for _, n := range names {
			l.Files = append(l.Files, d+"/"+n)
		}
	}
	for _, n := range rec.Local {
		l.Files = append(l.Files, secretxfer.LocalDir+"/"+strings.TrimPrefix(n, secretxfer.LocalPrefix))
	}
	sort.Strings(l.Files)
	if rec.Data != nil {
		l.DataMove = rec.Data.Move
	}
	if rec.OwnData != nil {
		l.OwnDataMove = rec.OwnData.Move
	}
	if ms := a.moveRecords(); len(ms) > 0 {
		m := ms[len(ms)-1]
		l.LastMove = m.State.Move
		switch m.State.Role {
		case nodeproto.MoveSource:
			l.Former = m.State.Phase == nodeproto.MoveDone
		case nodeproto.MoveTarget:
			l.Former = nodeproto.MoveFinal(m.State.Phase) && m.State.Phase != nodeproto.MoveDone
		}
	}
	return l
}
