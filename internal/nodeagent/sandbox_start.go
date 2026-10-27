package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// sandboxRunsDir holds the runs' token files on the host, 0600 each.
const sandboxRunsDir = "runs"

// codexSandboxConfig points Codex at sbx's proxy for its ChatGPT login,
// as the Codex kit's config.toml does: the bridge runs Codex with
// --ignore-user-config, which skips that file.
var codexSandboxConfig = []string{
	`model_provider="sandboxd"`,
	`model_providers.sandboxd.name="sandboxd"`,
	`model_providers.sandboxd.base_url="https://chatgpt.com/backend-api/codex"`,
	`model_providers.sandboxd.wire_api="responses"`,
	`model_providers.sandboxd.requires_openai_auth=true`,
}

// withCodexConfig is argv with codexSandboxConfig as -c options after
// `codex exec` (or after the program when it has no exec).
func withCodexConfig(argv []string) []string {
	at := 1
	if len(argv) > 1 && argv[1] == "exec" {
		at = 2
	}
	var opts []string
	for _, c := range codexSandboxConfig {
		opts = append(opts, "-c", c)
	}
	return slices.Concat(argv[:at], opts, argv[at:])
}

// sandboxRunShell runs argv in a new session inside the VM and writes
// its process group id first, so that sandbox.stop finds it: killing
// `sbx exec` on the host does not stop the command inside.
const sandboxRunShell = `echo $$ > "$0"; exec "$@"`

// execArgs is the `sbx exec` of a run. Credentials are named here only
// (-e NAME); their values are in sbx's own environment.
func execArgs(p nodeproto.SandboxRunParams, secretNames []string) []string {
	args := []string{"exec", "-i", "-w", p.Folder}
	for _, n := range secretNames {
		args = append(args, "-e", n)
	}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+p.Env[k])
	}
	args = append(args,
		"-e", fmt.Sprintf("FRIDOLIN_API=http://host.docker.internal:%d", p.APIPort),
		"-e", "FRIDOLIN_BRAIN_TOKEN_FILE="+nodeproto.SandboxTokenFile(p.Run),
		"-e", "FRIDOLIN_BIN="+sandboxBinPath,
		"-e", "FRIDOLIN_TOOLS_DIR="+sandboxToolsPath,
	)
	argv := p.Argv
	if p.Provider == nodeproto.SandboxProviderCodex {
		argv = withCodexConfig(argv)
	}
	args = append(args, nodeproto.SandboxName(p.Brain), "setsid", "-w", "sh", "-c", sandboxRunShell, nodeproto.SandboxPidFile(p.Run))
	return append(args, argv...)
}

// runCredentials is the provider's credential for sbx's environment:
// the Claude token from the host's store; nothing for Codex, whose login
// sbx's proxy holds.
func (a *Agent) runCredentials(provider string) (env, names []string, perr *nodeproto.Error) {
	if provider != nodeproto.SandboxProviderClaude {
		return nil, nil, nil
	}
	tok := a.claudeToken()
	if tok == "" {
		return nil, nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "the Claude token is not in this node's secret store")
	}
	return []string{"CLAUDE_CODE_OAUTH_TOKEN=" + tok}, []string{"CLAUDE_CODE_OAUTH_TOKEN"}, nil
}

// placeholder is a random stand-in for the run token in the VM.
func placeholder() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sbx-run-" + hex.EncodeToString(b), nil
}

// runIDRe is a run id: one name, never a path.
var runIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// tokenPath is the host file of run's token, which must be a run id:
// one name in the state dir's runs folder, never a path.
func (a *Agent) tokenPath(run string) (string, error) {
	if a.Dir == "" {
		return "", errors.New("this agent has no state dir")
	}
	if !runIDRe.MatchString(run) {
		return "", fmt.Errorf("%q is not a run id", run)
	}
	name := filepath.Base("run-" + run + ".token")
	if name != "run-"+run+".token" || strings.Contains(name, "..") {
		return "", fmt.Errorf("%q is not a run id", run)
	}
	return filepath.Join(a.Dir, sandboxDir, sandboxRunsDir, name), nil
}

// tokenFile writes the run token to a 0600 file in the state dir, which
// sbx's custom secret reads on the host.
func (a *Agent) tokenFile(run, token string) (string, error) {
	path, err := a.tokenPath(run)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(token)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// exitCode is how cmd ended: its exit code, or -1 with why when it did
// not exit by itself.
func exitCode(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return ee.ExitCode(), ""
	}
	return -1, strings.TrimSpace(err.Error())
}

// sandboxRunStart is sandbox.run: it sets up the run's token, starts it
// in the brain's sandbox and returns while it runs.
func (a *Agent) sandboxRunStart(ctx context.Context, p nodeproto.SandboxRunParams) (any, *nodeproto.Error) {
	if err := p.Check(); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%v", err)
	}
	if _, perr := a.sandboxReady(ctx, p.Provider); perr != nil {
		return nil, perr
	}
	d := a.sbx()
	if d == nil {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "sbx is not installed on this node")
	}
	if st, err := a.loadSandboxState(); err != nil || st.Sandboxes[p.Brain].Key == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "%s is not made yet: sandbox.ensure first", nodeproto.SandboxName(p.Brain))
	}
	credEnv, credNames, perr := a.runCredentials(p.Provider)
	if perr != nil {
		return nil, perr
	}
	r := newSandboxRun(p.Run, p.Brain)
	if perr := a.addRun(r); perr != nil {
		return nil, perr
	}
	fail := func(code string, format string, args ...any) (any, *nodeproto.Error) {
		a.dropRun(p.Run)
		return nil, nodeproto.Errorf(code, format, args...)
	}
	ph, err := placeholder()
	if err != nil {
		return fail(nodeproto.CodeFailed, "%v", err)
	}
	hostFile, err := a.tokenFile(p.Run, p.Token)
	if err != nil {
		return fail(nodeproto.CodeFailed, "the run's token file: %v", err)
	}
	if _, stderr, err := d.run(ctx, nil, nil, "secret", "set-custom", "--sandbox", r.name, "--host", "localhost",
		"--placeholder", ph, "--command", "/bin/cat "+hostFile); err != nil {
		a.cleanupRun(d, r.name, p.Run, "")
		return fail(nodeproto.CodeFailed, "sbx could not set the run's secret: %v", sbxError(err, stderr))
	}
	if _, stderr, err := d.run(ctx, strings.NewReader(ph), nil, "exec", "-i", r.name, "sh", "-c", `umask 077; cat > "$0"`,
		nodeproto.SandboxTokenFile(p.Run)); err != nil {
		a.cleanupRun(d, r.name, p.Run, ph)
		return fail(nodeproto.CodeFailed, "the run's token file in the VM: %v", sbxError(err, stderr))
	}
	// The run's GitHub (sandbox_github.go), until it ends.
	endGitHub := func() {}
	if p.GitHubToken != "" {
		if perr := a.githubOpen(ctx, d, p.Brain, p.GitHubToken); perr != nil {
			a.cleanupRun(d, r.name, p.Run, ph)
			a.dropRun(p.Run)
			return nil, perr
		}
		endGitHub = func() { a.githubClose(d, p.Brain) }
	}
	stop := func() { a.stopRun(r, nodeproto.DefaultSandboxGraceS*time.Second) }
	cmd, err := d.start(strings.NewReader(p.Stdin), stdoutWriter{r: r, overflowed: stop}, stderrWriter{r: r}, credEnv, execArgs(p, credNames)...)
	if err != nil {
		a.cleanupRun(d, r.name, p.Run, ph)
		endGitHub()
		return fail(nodeproto.CodeFailed, "sbx could not start the run: %v", err)
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()
	var timer *time.Timer
	if p.TimeoutS > 0 {
		timer = time.AfterFunc(time.Duration(p.TimeoutS)*time.Second, func() {
			r.mu.Lock()
			r.timedOut = true
			r.mu.Unlock()
			stop()
		})
	}
	go func() {
		code, msg := exitCode(cmd.Wait())
		if timer != nil {
			timer.Stop()
		}
		a.cleanupRun(d, r.name, p.Run, ph)
		endGitHub()
		r.finish(code, msg)
		a.logf("node: sandbox run %s in %s ended with %d", p.Run, r.name, code)
	}()
	a.logf("node: sandbox run %s started in %s", p.Run, r.name)
	return nodeproto.SandboxRunResult{Run: p.Run}, nil
}

// cleanupRun removes the run's secret and both token files. The output
// of `sbx secret rm` is never logged: it prints part of the value.
func (a *Agent) cleanupRun(d sbxDriver, name, run, ph string) {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	if ph != "" {
		_, _, _ = d.run(ctx, nil, nil, "secret", "rm", "--placeholder", ph, "--sandbox", name, "-f")
	}
	_, _, _ = d.run(ctx, nil, nil, "exec", name, "rm", "-f", nodeproto.SandboxTokenFile(run), nodeproto.SandboxPidFile(run))
	if p, err := a.tokenPath(run); err == nil {
		_ = os.Remove(p)
	}
}
