package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// NativeCore runs the core of a native core host: the agent's own binary
// with a fixed `fridolin core` subcommand for each step, whose arguments
// come from typed, checked values only (a move id, an image by digest),
// never from the call. The agent runs as the owner (a launchd agent, a
// systemd user unit), so these reach the owner's Docker and secret store
// as `fridolin core` in a terminal would.
type NativeCore struct {
	Exe string
	// ReadyURL is the core's /ready; http://127.0.0.1:8765/ready when "".
	ReadyURL string
}

// run runs `Exe args...` and returns its standard output; the error
// carries the end of its standard error, which names no secret (the
// core commands never print one).
func (n NativeCore) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout bytes.Buffer
	stderr := &limitedBuffer{max: 64 << 10}
	cmd := exec.CommandContext(ctx, n.Exe, args...)
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Run(); err != nil {
		what := "fridolin " + strings.Join(args[:min(len(args), 4)], " ")
		if tail := lastLine(stderr.String()); tail != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %v: %s", what, err, tail)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %v", what, err)
	}
	return stdout.Bytes(), nil
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300]
	}
	return l
}

func decodeLast(out []byte, v any) error {
	// The one-off container's output is the command's; take the last
	// line that holds a JSON object.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") {
			return json.Unmarshal([]byte(l), v)
		}
	}
	return fmt.Errorf("no answer in %q", lastLine(string(out)))
}

func (n NativeCore) Status(ctx context.Context) (nodeproto.CoreRuntime, error) {
	var st nodeproto.CoreRuntime
	out, err := n.run(ctx, 3*time.Minute, "core", "run", "core", "status", "--json")
	if err != nil {
		return st, err
	}
	return st, decodeLast(out, &st)
}

func (n NativeCore) Stop(ctx context.Context) error {
	_, err := n.run(ctx, 2*time.Minute, "core", "stop")
	return err
}

func (n NativeCore) Start(ctx context.Context) error {
	_, err := n.run(ctx, 5*time.Minute, "core", "start")
	return err
}

func (n NativeCore) Export(ctx context.Context, move string) (nodeproto.CoreExportInfo, error) {
	var info nodeproto.CoreExportInfo
	if err := nodeproto.CheckMoveID(move); err != nil {
		return info, err
	}
	out, err := n.run(ctx, time.Hour, "core", "run", "core", "export", "--move", move, "--json")
	if err != nil {
		return info, err
	}
	return info, decodeLast(out, &info)
}

func (n NativeCore) Import(ctx context.Context) error {
	_, err := n.run(ctx, time.Hour, "core", "run", "core", "import", "--latest", "--force")
	return err
}

func (n NativeCore) Promote(ctx context.Context) error {
	_, err := n.run(ctx, 3*time.Minute, "core", "run", "core", "promote")
	return err
}

func (n NativeCore) Release(ctx context.Context) error {
	_, err := n.run(ctx, 3*time.Minute, "core", "run", "core", "release")
	return err
}

func (n NativeCore) Ready(ctx context.Context) (bool, string) {
	url := n.ReadyURL
	if url == "" {
		url = "http://127.0.0.1:8765/ready"
	}
	return ReadyAt(ctx, http.DefaultClient, url)
}

// ReadyAt asks a core's /ready whether it is ready for a move: WhatsApp
// connected and, with the lease on, the lease held.
func ReadyAt(ctx context.Context, c *http.Client, url string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := c.Do(req)
	if err != nil {
		return false, "the core does not answer"
	}
	defer resp.Body.Close()
	var r struct {
		Ready    bool   `json:"ready"`
		WhatsApp string `json:"whatsapp"`
		Version  string `json:"version"`
		Lease    *struct {
			State string `json:"state"`
		} `json:"lease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return false, "its /ready does not read"
	}
	lease := "lease off"
	if r.Lease != nil {
		lease = "lease " + r.Lease.State
	}
	detail := fmt.Sprintf("version %s, WhatsApp %s, %s", r.Version, r.WhatsApp, lease)
	return r.Ready && (r.Lease == nil || r.Lease.State == "held"), detail
}

func (n NativeCore) Rehearse(ctx context.Context, move string) (nodeproto.CoreRehearsal, error) {
	var res nodeproto.CoreRehearsal
	if err := nodeproto.CheckMoveID(move); err != nil {
		return res, err
	}
	out, err := n.run(ctx, time.Hour, "core", "rehearse", "--json")
	if err != nil {
		return res, err
	}
	return res, decodeLast(out, &res)
}

func (n NativeCore) Image(ctx context.Context, version, image string, checkOnly bool) (nodeproto.CoreImageResult, error) {
	var res nodeproto.CoreImageResult
	args := []string{"core", "pull", "--json"}
	if image != "" {
		if err := nodeproto.CheckImageDigest(image); err != nil {
			return res, err
		}
		args = append(args, "--from", image)
	}
	if checkOnly {
		args = append(args, "--check")
	}
	out, err := n.run(ctx, time.Hour, args...)
	if err != nil {
		return res, err
	}
	// core pull tags the image with its own version, the agent's, which
	// the plan's version equals.
	_ = version
	return res, decodeLast(out, &res)
}
