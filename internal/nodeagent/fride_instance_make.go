package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// instanceOps works the FrIDE instances of the node whose state dir is
// dir, with sbx: what the agent's fride.instance.* methods and the CLI
// share.
type instanceOps struct {
	dir string
	d   sbxDriver
	// out gets what is done, a line at a time: the person who runs the
	// CLI, or the agent's log.
	out io.Writer
	// secrets holds the Claude token the instances' agents log in with;
	// nil for none.
	secrets hostsecrets.Store
	// url is an instance's FrIDE's address (frideInstanceURL of its port
	// when nil); wait is how long a start waits for FrIDE to answer
	// (frideInstanceWait when 0); portBase is the first port of the pairs
	// (FrideInstancePortBase when 0). Tests set them.
	url      func(name string, port int) string
	wait     time.Duration
	portBase int
}

// frideInstanceWait is how long a start waits for FrIDE to answer.
const frideInstanceWait = 3 * time.Minute

// say writes a line of what is done.
func (o instanceOps) say(format string, args ...any) {
	if o.out != nil {
		fmt.Fprintf(o.out, format+"\n", args...)
	}
}

// urlOf is FrIDE's address of the instance st.
func (o instanceOps) urlOf(st *FrideInstanceState) string {
	if o.url != nil {
		return o.url(st.Name, st.Port)
	}
	return frideInstanceURL(st.Port)
}

// tokensDir is the folder of the instance's git hosts' tokens.
func (o instanceOps) tokensDir(name string) string {
	return filepath.Join(frideInstanceDir(o.dir, name), frideInstanceTokens)
}

// exec runs script in sandbox, its output to out.
func (o instanceOps) exec(ctx context.Context, sandbox string, stdin io.Reader, script string, args ...string) error {
	flags := []string{"exec"}
	if stdin != nil {
		flags = append(flags, "-i")
	}
	out, stderr, err := o.d.run(ctx, stdin, nil, slices.Concat(flags, []string{sandbox}, frideInstanceScript(script, args...))...)
	if o.out != nil {
		for _, b := range [][]byte{out, stderr} {
			if len(b) > 0 {
				o.out.Write(b)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("in the sandbox %s: %w", sandbox, sbxError(err, lastLineOf(stderr)))
	}
	return nil
}

// lastLineOf is the last line of b, which says why a script failed.
func lastLineOf(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return []byte(s)
}

// status is sbx's status of the instance's sandbox: "" for none.
func (o instanceOps) status(ctx context.Context, st *FrideInstanceState) (string, error) {
	return sbxSandboxStatus(ctx, o.d, st.Sandbox)
}

// sandboxClaudeDenied are the hosts a Claude sandbox may not reach
// although the kit opens them: updates (the template pins Claude Code)
// and remote control.
var sandboxClaudeDenied = []string{"downloads.claude.ai", "bridge.claudeusercontent.com"}

// newSandbox makes st's sandbox with sbx create: its template (with
// sbx's pull policy pull), memory and port, the instance's caches
// read-write at their own path with their environment, and its Docker
// disk. No folder of the host's but the caches goes in.
func (o instanceOps) newSandbox(ctx context.Context, st *FrideInstanceState, pull string) error {
	o.say("Creating the sandbox %s from %s (memory %d MB, Docker disk %s, FrIDE on 127.0.0.1:%d)…", st.Sandbox, st.Template, st.MemoryMB, st.DockerDisk, st.Port)
	if pull == sbxPullMissing {
		o.say("sbx pulls the template unless it has it: a few GB the first time.")
	}
	args := []string{"create", "--name", st.Sandbox, "--memory", strconv.Itoa(st.MemoryMB) + "m", "-t", st.Template, "--pull", pull,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", st.Port, st.Port)}
	for _, h := range sandboxClaudeDenied {
		args = append(args, "--deny-network", h)
	}
	for _, kv := range frideInstanceCacheEnv(st.Caches) {
		args = append(args, "-e", kv)
	}
	args = append(args, "claude")
	if st.Caches != "" {
		args = append(args, st.Caches)
	}
	var env []string
	if st.DockerDisk != "" {
		env = []string{frideInstanceDiskEnv + "=" + st.DockerDisk}
	}
	if _, stderr, err := o.d.run(ctx, nil, env, args...); err != nil {
		return fmt.Errorf("sbx could not create %s: %w", st.Sandbox, sbxError(err, stderr))
	}
	st.Rules, st.Identities, st.Pushed, st.Made = nil, nil, "", time.Now().UTC().Truncate(time.Second)
	return nil
}

// makeInside readies a new sandbox: it puts back keep from store and
// prepares FrIDE's folders and config. The network rules, tokens, login
// and identities come after, with every start.
func (o instanceOps) makeInside(ctx context.Context, st *FrideInstanceState, host *FrideHostState, store string, keep []string) error {
	if n, err := o.restoreVM(ctx, st.Sandbox, store, keep); err != nil {
		return err
	} else if n > 0 {
		o.say("Put back %d saved folders and files (the agents' sessions, FrIDE's tasks) from %s.", n, store)
	}
	var companion io.Reader
	if c := host.companion(); c != nil {
		companion = bytes.NewReader(c)
	}
	return o.exec(ctx, st.Sandbox, companion, frideInstancePrepare)
}

// make makes the instance's sandbox, which sbx does not have: from the
// carry an unfinished remake left, else with the history a removal
// saved.
func (o instanceOps) make(ctx context.Context, st *FrideInstanceState, host *FrideHostState) error {
	store, keep := o.historyDir(st.Name), frideInstanceHistory
	if o.carrying(st.Name) {
		store, keep = o.carryDir(st.Name), frideInstanceCarry
		o.say("An earlier remake of %s left its things in %s; making the sandbox and putting them back…", st.Name, store)
	}
	if err := o.newSandbox(ctx, st, host.pullFor(st.Template)); err != nil {
		return err
	}
	if err := o.makeInside(ctx, st, host, store, keep); err != nil {
		_, _, _ = o.d.run(context.WithoutCancel(ctx), nil, nil, "rm", "-f", st.Sandbox)
		return fmt.Errorf("%w; the sandbox is removed again", err)
	}
	if store == o.carryDir(st.Name) {
		if err := os.RemoveAll(store); err != nil {
			o.say("Note: %s stays (%v); remove it.", store, err)
		}
	}
	return saveFrideInstance(o.dir, st)
}

// remake makes the instance's sandbox anew, at st's memory from st's
// template, and carries over what lives only on the old VM's disk: the
// history, the workspaces, the repositories, the Claude login, Claude
// Code's and git's settings (frideInstanceCarry). The old sandbox goes
// only once its things are on the host; should the new one fail, they
// wait in the carry folder for the next start. status is sbx's status of
// the old sandbox.
func (o instanceOps) remake(ctx context.Context, st *FrideInstanceState, host *FrideHostState, status string) error {
	carry := o.carryDir(st.Name)
	if status == "running" {
		if err := o.exec(ctx, st.Sandbox, nil, frideInstanceStop); err != nil {
			return err
		}
	}
	n, err := o.saveVM(ctx, st.Sandbox, carry, frideInstanceCarry)
	if err != nil {
		return fmt.Errorf("saving the sandbox's things: %w; the sandbox stays as it is", err)
	}
	o.say("Saved %d folders and files of %s (workspaces, repositories, sessions, FrIDE's tasks, the login) to %s.", n, st.Sandbox, carry)
	if _, stderr, err := o.d.run(ctx, nil, nil, "rm", "-f", st.Sandbox); err != nil {
		return fmt.Errorf("sbx could not remove %s: %w", st.Sandbox, sbxError(err, stderr))
	}
	if err := o.make(ctx, st, host); err != nil {
		return fmt.Errorf("%w; what the old sandbox held is in %s, and the next start puts it back", err, carry)
	}
	return nil
}

// claudeToken is the Claude token FrIDE's agents log in with, from the
// node's secret store (FrideSecretStore), or why there is none, with what
// fixes it.
func (o instanceOps) claudeToken() (tok, why string) {
	if o.secrets == nil {
		return "", "the node keeps no secrets: it was started without a secret store"
	}
	tok, err := o.secrets.Get(hostsecrets.ClaudeOAuthToken)
	switch {
	case errors.Is(err, hostsecrets.ErrNotFound):
		return "", "no Claude token is stored for FrIDE's agents; " + claudeTokenFix(runtime.GOOS, o.dir)
	case err == nil:
		tok, err = CheckClaudeToken(tok)
	}
	if err != nil {
		return "", fmt.Sprintf("the Claude token for FrIDE's agents does not read (%v); %s", err, claudeTokenFix(runtime.GOOS, o.dir))
	}
	return tok, ""
}

// login stores the Claude token for FrIDE's agents in the sandbox, from
// the node's secret store: on stdin, never as an argument. It says why
// they have none; noLogin is that reason when the node has no token.
func (o instanceOps) login(ctx context.Context, sandbox string) (problem, noLogin string) {
	tok, why := o.claudeToken()
	if why != "" {
		return "no Claude login for FrIDE's agents: " + why, why
	}
	if _, stderr, err := o.d.run(ctx, strings.NewReader(tok+"\n"), nil,
		append([]string{"exec", "-i", sandbox}, frideInstanceScript(frideInstanceLogin)...)...); err != nil {
		why := "storing the Claude login in the sandbox: " + strings.ReplaceAll(sbxError(err, lastLineOf(stderr)).Error(), tok, "…")
		return why, why
	}
	return "", ""
}

// CheckClaudeToken is token as `claude setup-token` prints it, without
// what a copy or Windows PowerShell adds (a byte order mark, carriage
// returns, spaces), or why it is not one.
func CheckClaudeToken(token string) (string, error) {
	tok := strings.TrimPrefix(token, string(rune(0xfeff)))
	tok = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, tok)
	switch {
	case tok == "":
		return "", errors.New("no token: run claude setup-token and paste the token it prints")
	case strings.Contains(tok, "#"):
		return "", errors.New("that is the sign-in code from the browser, not the token claude setup-token prints after it")
	case !strings.HasPrefix(tok, "sk-ant-oat") || len(tok) == len("sk-ant-oat"):
		return "", errors.New("that does not start with sk-ant-oat, as a token from claude setup-token does")
	case strings.IndexFunc(tok, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0:
		return "", errors.New("that has characters a token never has (copied with other text?)")
	}
	return tok, nil
}
