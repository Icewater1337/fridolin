package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Programs from the core (nodeproto/exec_programs.go, docs/NODES.md). The
// owner opts folders in with exec.accept_from_core in node.json; the core
// sends the list of programs its approved command agents run here, signed
// with its update key. The node keeps a program only when it resolves,
// symlinks and all, to an executable file inside one of those folders and
// is no shell or interpreter, keeps it in exec-from-core.json, and
// declares it as an exact rule. It checks the program again before every
// run, and runs the file it resolved.

// ExecProgramsFile holds the programs the core added, and the seq of the
// list they came in.
const ExecProgramsFile = "exec-from-core.json"

// execPrograms is the agent's programs from the core.
type execPrograms struct {
	mu       sync.Mutex
	loaded   bool
	Seq      int64                   `json:"seq"`
	Programs []nodeproto.ExecProgram `json:"programs"`
}

// acceptFolders are the folders node.json opts in, nil for none.
func (a *Agent) acceptFolders() []string {
	if e := a.Config.Capabilities.Exec; e != nil {
		return e.AcceptFromCore
	}
	return nil
}

// loadProgramsLocked reads exec-from-core.json once, and drops what is no
// longer inside a folder node.json opts in.
func (a *Agent) loadProgramsLocked() {
	p := &a.progs
	if p.loaded {
		return
	}
	p.loaded = true
	if a.Dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(a.Dir, ExecProgramsFile))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var f struct {
		Seq      int64                   `json:"seq"`
		Programs []nodeproto.ExecProgram `json:"programs"`
	}
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		a.logf("node: reading %s: %v; the programs from the core wait for the core's next list", ExecProgramsFile, err)
		return
	}
	p.Seq = f.Seq
	for _, pr := range f.Programs {
		if _, err := a.checkCoreProgram(pr); err != nil {
			a.logf("node: no longer allowing %s from the core: %v", pr.Name, err)
			continue
		}
		p.Programs = append(p.Programs, pr)
	}
}

// corePrograms are the programs from the core the node declares.
func (a *Agent) corePrograms() []nodeproto.ExecProgram {
	if len(a.acceptFolders()) == 0 {
		return nil
	}
	a.progs.mu.Lock()
	defer a.progs.mu.Unlock()
	a.loadProgramsLocked()
	return slices.Clone(a.progs.Programs)
}

// declareCorePrograms adds the programs from the core to c's exec rules.
func (a *Agent) declareCorePrograms(c *nodeproto.Capabilities) {
	progs := a.corePrograms()
	if len(progs) == 0 || c.Exec == nil {
		return
	}
	e := *c.Exec
	e.Allow = slices.Clone(e.Allow)
	for _, pr := range progs {
		if len(e.Allow) >= nodeproto.MaxExecRules {
			break
		}
		e.Allow = append(e.Allow, pr.Rule())
	}
	c.Exec = &e
}

// checkCoreProgram reports why the node will not run pr for the core,
// and the file it runs when it will: an absolute, clean path that
// resolves, symlinks and all, to an executable file inside a folder
// node.json opts in, neither of them a shell or an interpreter.
func (a *Agent) checkCoreProgram(pr nodeproto.ExecProgram) (string, error) {
	folders := a.acceptFolders()
	if len(folders) == 0 {
		return "", errors.New("this node accepts no programs from the core (exec.accept_from_core in node.json)")
	}
	if err := pr.Rule().Check(); err != nil {
		return "", err
	}
	name := pr.Name
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return "", fmt.Errorf("%s is not a clean absolute path on this node", name)
	}
	real, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", fmt.Errorf("%s is not there: %v", name, errors.Unwrap(err))
	}
	if nodeproto.IsInterpreter(real) {
		return "", fmt.Errorf("%s is %s, a shell or an interpreter, which the core never adds", name, real)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable (chmod +x)", name)
	}
	for _, f := range folders {
		if CheckWorkRoot(f, false, a.Dir) != nil {
			continue
		}
		rf, err := filepath.EvalSymlinks(f)
		if err != nil {
			continue
		}
		if inside(rf, real) && !sameFile(rf, real) {
			return real, nil
		}
	}
	if real != name {
		return "", fmt.Errorf("%s is %s, which is in no folder this node accepts programs from (%s)", name, real, strings.Join(folders, ", "))
	}
	return "", fmt.Errorf("%s is in no folder this node accepts programs from (%s)", name, strings.Join(folders, ", "))
}

// execProgramsCall serves exec.programs: the core's whole list of the
// programs it added.
func (a *Agent) execProgramsCall(raw json.RawMessage) (any, *nodeproto.Error) {
	var p nodeproto.ExecProgramsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "exec.programs params: %v", err)
	}
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if p.Node != a.ID() {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the list is for node %s, not this one", p.Node)
	}
	key, e := a.trustedCoreKey()
	if e != nil {
		return nil, e
	}
	if !nodeproto.VerifyExecPrograms(key, p) {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the list's signature does not verify with the core's key %s", nodeproto.KeyFingerprint(key))
	}
	a.progs.mu.Lock()
	defer a.progs.mu.Unlock()
	a.loadProgramsLocked()
	if p.Seq <= a.progs.Seq {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "the list's seq %d is not newer than %d, the last one taken", p.Seq, a.progs.Seq)
	}
	room := nodeproto.MaxExecRules
	if e := a.Config.Capabilities.Exec; e != nil {
		room -= len(e.Allow)
	}
	res := nodeproto.ExecProgramsResult{Accepted: []nodeproto.ExecProgram{}, Refused: []nodeproto.RefusedProgram{}}
	for _, pr := range p.Programs {
		if pr.Args == nil {
			pr.Args = []string{}
		}
		if slices.ContainsFunc(res.Accepted, func(x nodeproto.ExecProgram) bool { return x.Name == pr.Name && slices.Equal(x.Args, pr.Args) }) {
			continue
		}
		if _, err := a.checkCoreProgram(pr); err != nil {
			res.Refused = append(res.Refused, nodeproto.RefusedProgram{ExecProgram: pr, Why: err.Error()})
			continue
		}
		if len(res.Accepted) >= room {
			res.Refused = append(res.Refused, nodeproto.RefusedProgram{ExecProgram: pr, Why: fmt.Sprintf("this node allows at most %d programs", nodeproto.MaxExecRules)})
			continue
		}
		res.Accepted = append(res.Accepted, pr)
	}
	if a.Dir != "" {
		data, err := json.MarshalIndent(struct {
			Seq      int64                   `json:"seq"`
			Programs []nodeproto.ExecProgram `json:"programs"`
		}{p.Seq, res.Accepted}, "", "  ")
		if err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
		}
		if err := writeFile(filepath.Join(a.Dir, ExecProgramsFile), append(data, '\n'), 0o600); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "keeping the programs: %v", err)
		}
	}
	changed := !slices.EqualFunc(a.progs.Programs, res.Accepted, func(x, y nodeproto.ExecProgram) bool {
		return x.Name == y.Name && slices.Equal(x.Args, y.Args) && slices.Equal(x.Agents, y.Agents)
	})
	a.progs.Seq, a.progs.Programs = p.Seq, slices.Clone(res.Accepted)
	for _, r := range res.Refused {
		a.logf("node: refused %s from the core: %s", r.Name, r.Why)
	}
	if changed {
		a.logf("node: the core's programs are now %d (%d refused)", len(res.Accepted), len(res.Refused))
		a.declChanged()
	}
	return res, nil
}

// corePath is the file to run for argv when only a program from the core
// allows it, checked again now; "" when a rule of node.json allows argv.
func (a *Agent) corePath(eff nodeproto.Capabilities, argv []string) (string, *nodeproto.Error) {
	var core *nodeproto.ExecRule
	for _, r := range eff.Exec.Allow {
		if !r.Matches(argv) {
			continue
		}
		if !r.Core {
			return "", nil
		}
		core = &r
	}
	if core == nil {
		return "", nil
	}
	real, err := a.checkCoreProgram(nodeproto.ExecProgram{Name: core.Name, Args: core.Args})
	if err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
	}
	return real, nil
}
