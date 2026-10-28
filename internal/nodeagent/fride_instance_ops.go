package nodeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// What the core and the owner do with an instance besides starting it:
// stop it (the VM stops, its disk stays), remove it (its history saved
// first), list them, and read FrIDE's log.

// sbxStatuses is sbx's status of each sandbox, by name.
func sbxStatuses(ctx context.Context, d sbxDriver) (map[string]string, error) {
	out, stderr, err := d.run(ctx, nil, nil, "ls", "--json")
	list, ok := parseSbxList(out)
	if err != nil || !ok {
		return nil, fmt.Errorf("sbx does not list its sandboxes: %s", orUnknownErr(firstLine(stderr), err))
	}
	m := make(map[string]string, len(list))
	for _, s := range list {
		m[s.Name] = orUnknown(s.Status)
	}
	return m, nil
}

// probe asks the instance's FrIDE whether it answers, with the token the
// node paired with.
func (o instanceOps) probe(ctx context.Context, st *FrideInstanceState) (FrideInfo, bool) {
	tok := readInstanceToken(o.dir, st.Name)
	if tok == "" {
		return FrideInfo{}, false
	}
	pctx, cancel := context.WithTimeout(ctx, frideProbeTimeout)
	defer cancel()
	info, err := ProbeFride(pctx, o.urlOf(st), tok)
	return info, err == nil
}

// waitOr is how long a start waits for FrIDE.
func (o instanceOps) waitOr() time.Duration {
	if o.wait > 0 {
		return o.wait
	}
	return frideInstanceWait
}

// view is what the node reports of the instance st: status is sbx's
// status of its sandbox, starting says a start is under way, and
// serveProblem why the agent's last run of FrIDE ended ("" when it did
// not, or outside the agent).
func (o instanceOps) view(ctx context.Context, st *FrideInstanceState, status string, starting bool, serveProblem string) nodeproto.FrideInstance {
	v := nodeproto.FrideInstance{Name: st.Name, State: nodeproto.FrideInstanceStopped, MemoryMB: st.MemoryMB, Problem: st.Problem}
	info, answers := FrideInfo{}, false
	if st.Run && status != "" {
		info, answers = o.probe(ctx, st)
	}
	switch {
	case answers:
		// Also while a start brings it up to date: it serves meanwhile.
		v.State, v.URL, v.Repos = nodeproto.FrideInstanceRunning, o.urlOf(st), info.Repos
	case starting:
		v.State = nodeproto.FrideInstanceStarting
	case st.Run && status == "":
		v.State, v.Problem = nodeproto.FrideInstanceFailed, fmt.Sprintf("sbx has no sandbox %s any more: the next start makes it again", st.Sandbox)
	case st.Run && serveProblem != "" && time.Since(st.Started) > o.waitOr():
		v.State, v.Problem = nodeproto.FrideInstanceFailed, "its FrIDE does not answer: "+serveProblem
	case st.Run:
		v.State = nodeproto.FrideInstanceStarting
	case st.Problem != "":
		v.State = nodeproto.FrideInstanceFailed
	}
	v.Problem = problemText(v.Problem)
	if (&nodeproto.FrideHostCap{Instances: []nodeproto.FrideInstance{v}}).Check() != nil {
		v.Repos = nil
	}
	return v
}

// FrideInstanceReport is one instance as `fridolin node fride instance
// ls` shows it.
type FrideInstanceReport struct {
	nodeproto.FrideInstance
	Sandbox     string
	Port        int
	WebhookPort int
	// Sbx is sbx's status of the sandbox, "" for none.
	Sbx string
	// Hosts are the git hosts it has tokens for.
	Hosts []string
}

// list reports every instance, by name; starting and serveProblem are
// the agent's (view).
func (o instanceOps) list(ctx context.Context, starting func(string) bool, serveProblem func(string) string) ([]FrideInstanceReport, error) {
	states := listFrideInstances(o.dir)
	if len(states) == 0 {
		return nil, nil
	}
	statuses, err := sbxStatuses(ctx, o.d)
	if err != nil {
		return nil, err
	}
	out := make([]FrideInstanceReport, 0, len(states))
	for _, st := range states {
		s, p := false, ""
		if starting != nil {
			s = starting(st.Name)
		}
		if serveProblem != nil {
			p = serveProblem(st.Name)
		}
		out = append(out, FrideInstanceReport{FrideInstance: o.view(ctx, st, statuses[st.Sandbox], s, p),
			Sandbox: st.Sandbox, Port: st.Port, WebhookPort: st.WebhookPort, Sbx: statuses[st.Sandbox], Hosts: hostTokenHosts(o.tokensDir(st.Name))})
	}
	return out, nil
}

// stop clears run, ends FrIDE and stops the sandbox's VM; its disk
// stays. A stopped sandbox stays as it is: an exec would boot it.
func (o instanceOps) stop(ctx context.Context, name string) (*FrideInstanceState, error) {
	st, err := loadFrideInstance(o.dir, name)
	if err != nil {
		return nil, err
	}
	st.Run, st.Problem = false, ""
	if err := saveFrideInstance(o.dir, st); err != nil {
		return st, err
	}
	status, err := o.status(ctx, st)
	if err != nil || status != "running" {
		return st, err
	}
	if err := o.exec(ctx, st.Sandbox, nil, frideInstanceStop); err != nil {
		o.say("Note: ending FrIDE in %s: %v", st.Sandbox, err)
	}
	if _, stderr, err := o.d.run(ctx, nil, nil, "stop", st.Sandbox); err != nil {
		return st, fmt.Errorf("sbx could not stop %s: %w", st.Sandbox, sbxError(err, stderr))
	}
	o.say("Stopped %s; its disk stays.", st.Sandbox)
	return st, nil
}

// remove saves the instance's history (unless skipHistory), then
// removes its sandbox, with its workspaces and repositories, and the
// instance's state, pairing and tokens; the history stays, for the next
// sandbox of that name.
func (o instanceOps) remove(ctx context.Context, name string, skipHistory bool) error {
	st, err := loadFrideInstance(o.dir, name)
	if err != nil {
		return err
	}
	if st.Run {
		st.Run = false
		if err := saveFrideInstance(o.dir, st); err != nil {
			return err
		}
	}
	status, err := o.status(ctx, st)
	if err != nil {
		return err
	}
	if status != "" && !skipHistory {
		if err := o.saveHistory(ctx, st, status); err != nil {
			return fmt.Errorf("%w; nothing removed", err)
		}
	}
	if status != "" {
		if _, stderr, err := o.d.run(ctx, nil, nil, "rm", "-f", st.Sandbox); err != nil {
			return fmt.Errorf("sbx could not remove %s: %w", st.Sandbox, sbxError(err, stderr))
		}
		o.say("Removed the sandbox %s.", st.Sandbox)
	}
	return removeInstanceFiles(o.dir, name)
}

// logs is the last lines of the instance's FrIDE log, read while its
// sandbox runs.
func (o instanceOps) logs(ctx context.Context, name string, lines int) ([]byte, error) {
	st, err := loadFrideInstance(o.dir, name)
	if err != nil {
		return nil, err
	}
	status, err := o.status(ctx, st)
	if err != nil {
		return nil, err
	}
	if status != "running" {
		return nil, fmt.Errorf("the sandbox %s is %s: FrIDE's log waits in its VM until the instance runs again", st.Sandbox, orUnknown(status))
	}
	if lines <= 0 {
		lines = 200
	}
	out, stderr, err := o.d.run(ctx, nil, nil, append([]string{"exec", st.Sandbox}, frideInstanceScript(frideInstanceLogs, strconv.Itoa(lines))...)...)
	if err != nil {
		return nil, fmt.Errorf("in the sandbox %s: %w", st.Sandbox, sbxError(err, lastLineOf(stderr)))
	}
	return out, nil
}

// FrideInstanceOptions are what `fridolin node fride instance` works
// with.
type FrideInstanceOptions struct {
	// Dir is the node's state dir, Sbx the sbx program.
	Dir, Sbx string
	// Out gets what is done.
	Out io.Writer
	// env is added to sbx's environment (tests: the fake's).
	env []string
}

func (o FrideInstanceOptions) ops() instanceOps {
	return instanceOps{dir: o.Dir, d: sbxCLI{path: o.Sbx, env: o.env}, out: o.Out}
}

// ListFrideInstances reports the node's instances.
func ListFrideInstances(ctx context.Context, o FrideInstanceOptions) ([]FrideInstanceReport, error) {
	return o.ops().list(ctx, nil, nil)
}

// StopFrideInstance stops the instance name: the agent no longer keeps
// its FrIDE running, and its VM stops.
func StopFrideInstance(ctx context.Context, o FrideInstanceOptions, name string) error {
	_, err := o.ops().stop(ctx, name)
	return err
}

// RemoveFrideInstance removes the instance name, saving its history
// first unless skipHistory.
func RemoveFrideInstance(ctx context.Context, o FrideInstanceOptions, name string, skipHistory bool) error {
	return o.ops().remove(ctx, name, skipHistory)
}

// FrideInstanceLogs is the last lines of the instance's FrIDE log.
func FrideInstanceLogs(ctx context.Context, o FrideInstanceOptions, name string, lines int) ([]byte, error) {
	return o.ops().logs(ctx, name, lines)
}

// IsNoFrideInstance reports whether err is about an instance the node
// does not have.
func IsNoFrideInstance(err error) bool { return errors.Is(err, errNoFrideInstance) }

// frideInstanceCall serves the fride.instance.* methods.
func (a *Agent) frideInstanceCall(ctx context.Context, method string, raw json.RawMessage) (any, *nodeproto.Error) {
	decode := func(v any) *nodeproto.Error {
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodFrideInstanceStart:
		var p nodeproto.FrideInstanceStartParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.frideInstanceStart(ctx, p)
	case nodeproto.MethodFrideInstanceStatus:
		if a.Dir == "" || a.sbx() == nil {
			return nodeproto.FrideInstances{Instances: []nodeproto.FrideInstance{}}, nil
		}
		rows, err := a.instanceOps().list(ctx, a.inst.isStarting, a.inst.serveProblem)
		if err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%v", err)
		}
		out := nodeproto.FrideInstances{Instances: []nodeproto.FrideInstance{}}
		for _, r := range rows {
			out.Instances = append(out.Instances, r.FrideInstance)
		}
		return out, nil
	case nodeproto.MethodFrideInstanceStop, nodeproto.MethodFrideInstanceRemove:
		var p nodeproto.FrideInstanceRef
		if e := decode(&p); e != nil {
			return nil, e
		}
		if err := nodeproto.CheckFrideInstanceName(p.Name); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
		o := a.instanceOps()
		if a.Dir == "" || o.d == nil {
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no FrIDE instance %s on this node", p.Name)
		}
		lock := a.inst.lock(p.Name)
		lock.Lock()
		defer lock.Unlock()
		defer a.frideHostKick()
		if method == nodeproto.MethodFrideInstanceRemove {
			if err := a.releaseInstance(ctx, p.Name); err != nil {
				return nil, instanceErr(p.Name, err)
			}
			if err := o.remove(ctx, p.Name, false); err != nil {
				return nil, instanceErr(p.Name, err)
			}
			a.logf("node: removed the FrIDE instance %s; its history stays", p.Name)
			return struct{}{}, nil
		}
		if err := a.releaseInstance(ctx, p.Name); err != nil {
			return nil, instanceErr(p.Name, err)
		}
		st, err := o.stop(ctx, p.Name)
		if err != nil {
			return nil, instanceErr(p.Name, err)
		}
		a.logf("node: stopped the FrIDE instance %s", p.Name)
		return nodeproto.FrideInstance{Name: st.Name, State: nodeproto.FrideInstanceStopped, MemoryMB: st.MemoryMB}, nil
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// releaseInstance clears the instance's run, and waits until the agent
// let go of its FrIDE: then nothing boots the sandbox again behind a
// stop.
func (a *Agent) releaseInstance(ctx context.Context, name string) error {
	st, err := loadFrideInstance(a.Dir, name)
	if err != nil {
		return err
	}
	if st.Run {
		st.Run = false
		if err := saveFrideInstance(a.Dir, st); err != nil {
			return err
		}
	}
	a.inst.syncServe(ctx)
	return nil
}

// instanceErr is err of the instance name as the core gets it.
func instanceErr(name string, err error) *nodeproto.Error {
	switch {
	case errors.Is(err, errNoFrideInstance):
		return nodeproto.Errorf(nodeproto.CodeNotFound, "no FrIDE instance %s on this node", name)
	case errors.Is(err, context.DeadlineExceeded):
		return nodeproto.Errorf(nodeproto.CodeTimeout, "the FrIDE instance %s: %v", name, err)
	}
	return nodeproto.Errorf(nodeproto.CodeFailed, "the FrIDE instance %s: %v", name, err)
}

// waitAnswers waits until something answers HTTP at url, for wait at
// most (frideInstanceWait when 0).
func waitAnswers(ctx context.Context, url string, wait time.Duration) error {
	if wait <= 0 {
		wait = frideInstanceWait
	}
	deadline := time.Now().Add(wait)
	for {
		rctx, cancel := context.WithTimeout(ctx, frideProbeTimeout)
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, url+"/", nil)
		if err != nil {
			cancel()
			return err
		}
		resp, err := frideClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("FrIDE does not answer at %s after %s", url, wait.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// parseKeyValues reads key=value lines.
func parseKeyValues(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			m[k] = v
		}
	}
	return m
}

// problemText is s as a problem the node reports: printable, and short
// enough for nodeproto's bound (1024 bytes).
func problemText(s string) string {
	s = clean(s, 1000)
	for len(s) > 1000 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}
