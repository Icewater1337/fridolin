package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
)

// sbxDriver runs Docker Sandboxes' CLI. The real one is sbxCLI; tests
// point it at a fake binary.
type sbxDriver interface {
	// run runs sbx with args to the end. env holds NAME=VALUE pairs added
	// to sbx's own environment (credentials go there, never into args).
	run(ctx context.Context, stdin io.Reader, env []string, args ...string) (stdout, stderr []byte, err error)
	// start starts sbx with args and returns it running.
	start(stdin io.Reader, stdout, stderr io.Writer, env []string, args ...string) (*exec.Cmd, error)
	// unavailable says why sbx cannot run now, before any call: on
	// Windows, when nobody is signed in whose session it runs in. ""
	// when it can.
	unavailable() string
	// hostFolder gives dir, a folder of the node's that sbx reads or
	// writes on the host (a sandbox's tokens, its history), to the user
	// who runs sbx and nobody else: on Windows that user and SYSTEM; on
	// macOS and Linux the agent is that user, and the folder is 0700.
	hostFolder(dir string) error
	// secretCommand is the host command sbx runs to read the token in
	// file (sbx secret set-custom --command): /bin/cat, on Windows the
	// node's own fridolin.exe (sbxSecretCommandFor).
	secretCommand(file string) (string, error)
}

// sbxCLI runs the sbx binary at path: without telemetry, and without a
// terminal (stdin is what the caller gives, else nothing). On Windows,
// from the node's service, it runs in the signed-in user's session as
// that user (sbx_windows.go), and path "" is their sbx.
type sbxCLI struct {
	path string
	// env is added to every call's environment (tests: the fake's dir).
	env []string
	// self is the node's own program, which sbx runs on Windows to read
	// a token (Agent.Executable).
	self string
}

// cmd is the call of sbx with args; done releases what it holds once
// sbx started.
func (s sbxCLI) cmd(ctx context.Context, stdin io.Reader, env []string, args []string) (c *exec.Cmd, done func(), err error) {
	c, base, done, err := sbxCommand(ctx, s.path, args)
	if err != nil {
		return nil, nil, err
	}
	c.Env = slices.Concat(sbxEnv(base), s.env, env)
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	c.Stdin = stdin
	return c, done, nil
}

// sbxDropped are variables of the agent's environment sbx never gets:
// telemetry's (set below) and credentials, which a run passes itself.
var sbxDropped = []string{"SBX_NO_TELEMETRY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY"}

// sbxEnv is the environment sbx runs with: the agent's, without
// sbxDropped, with telemetry off.
func sbxEnv(base []string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(sbxDropped, name) {
			out = append(out, kv)
		}
	}
	return append(out, "SBX_NO_TELEMETRY=1")
}

func (s sbxCLI) run(ctx context.Context, stdin io.Reader, env []string, args ...string) ([]byte, []byte, error) {
	c, done, err := s.cmd(ctx, stdin, env, args)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err = c.Run()
	return out.Bytes(), errb.Bytes(), err
}

func (s sbxCLI) start(stdin io.Reader, stdout, stderr io.Writer, env []string, args ...string) (*exec.Cmd, error) {
	// A run ends by itself or through sandbox.stop, never by a context.
	c, done, err := s.cmd(context.Background(), stdin, env, args)
	if err != nil {
		return nil, err
	}
	defer done()
	c.Stdout, c.Stderr = stdout, stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	return c, nil
}

// firstLine is the first line of b, trimmed and cut short: what an sbx
// error says, for a problem or a log line. Never use it on output that
// may hold a secret.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// sbxError is err with what sbx printed on stderr.
func sbxError(err error, stderr []byte) error {
	if msg := firstLine(stderr); msg != "" {
		return errors.New(msg)
	}
	return err
}
