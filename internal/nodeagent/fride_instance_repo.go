package nodeagent

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Adding a repository to an instance (fride.repo_clone and
// fride.repo_new with an instance): the instance's base folder is in its
// VM, so the node clones or makes the repository there, with the
// instance's git, which reaches only the hosts its rules allow and
// authenticates with placeholders. FrIDE then reads its base folder
// again, as for the node's own FrIDE (fride_repo.go).

// instanceRepo serves fride.repo_clone and fride.repo_new for the
// instance name, whose FrIDE fc reaches.
func (a *Agent) instanceRepo(ctx context.Context, method, name string, fc *frideConn, p nodeproto.FrideRepoParams) (any, *nodeproto.Error) {
	p.Name, p.URL = strings.TrimSpace(p.Name), strings.TrimSpace(p.URL)
	if err := checkRepoName(p.Name); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	clone := method == nodeproto.MethodFrideRepoClone
	if clone {
		if _, err := cloneInstanceURL(p.URL); err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
		}
	} else if p.URL != "" {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a new repository takes no URL")
	}
	st, err := loadFrideInstance(a.Dir, name)
	if err != nil {
		return nil, instanceErr(name, err)
	}
	o := a.instanceOps()
	out := nodeproto.FrideRepo{Name: p.Name}
	cctx, cancel := context.WithTimeout(ctx, repoCloneTimeout)
	defer cancel()
	var args []string
	if clone {
		a.logf("node: cloning a repository into the FrIDE instance %s as %s", name, p.Name)
		args = frideInstanceScript(frideInstanceClone, p.URL, p.Name)
	} else {
		a.logf("node: making the repository %s in the FrIDE instance %s", p.Name, name)
		args = frideInstanceScript(frideInstanceRepoNew, p.Name)
	}
	res, stderr, err := o.d.run(cctx, nil, nil, append([]string{"exec", st.Sandbox}, args...)...)
	kv := parseKeyValues(res)
	switch {
	case err != nil && exitCode(err) == 3, err == nil && kv["clone"] == "have":
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s already exists in the instance's base folder", p.Name)
	case err != nil:
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", sbxError(err, lastLineOf(stderr)))
	}
	out.Path = kv["path"]
	if !clone && p.GitHub {
		vis := "--private"
		if p.Public {
			vis = "--public"
		}
		gout, gerr, err := o.d.run(cctx, nil, nil, append([]string{"exec", st.Sandbox}, frideInstanceScript(frideInstanceGitHubCreate, p.Name, vis)...)...)
		if err != nil {
			out.Warning = p.Name + " was made, but not on GitHub: " + sbxError(err, lastLineOf(gerr)).Error()
			a.logf("node: %s", out.Warning)
		}
		for f := range strings.FieldsSeq(string(gout) + " " + string(gerr)) {
			if strings.HasPrefix(f, "https://github.com/") {
				out.GitHubURL = strings.TrimRight(f, ".,")
				break
			}
		}
	}
	out.Repos, out.Rescanned = a.instanceRescan(ctx, fc)
	return out, nil
}

// instanceRescan has the instance's FrIDE read its base folder again
// and declares the instance's repositories anew. It reports the
// repositories FrIDE knows then, and whether it rescanned.
func (a *Agent) instanceRescan(ctx context.Context, fc *frideConn) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, frideCallTimeout)
	defer cancel()
	if err := fc.do(ctx, http.MethodPost, "/api/config", nil, nil); err != nil {
		a.logf("node: asking a FrIDE instance to read its base folder again: %v", err)
		return nil, false
	}
	info, err := fc.probe(ctx)
	a.frideHostKick()
	if err != nil {
		return nil, true
	}
	return info.Repos, true
}

// exitCode is the exit status of the program err is about, -1 for none.
func exitCode(err error) int {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode()
	}
	return -1
}
