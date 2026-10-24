package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// containerOpenTimeout bounds asking Docker: a Docker Desktop that is not
// started must not hold up a native fridolin open.
const containerOpenTimeout = 15 * time.Second

// containerDashboardLink is `fridolin open` on a host whose core runs in
// the fridolin-core container (docs/CORE.md): the token is in the
// container's volume, not in a data folder here, so the container's own
// fridolin asks the bridge for the link. ok is false when no such
// container runs here (or Docker is missing), and the native open goes on;
// in the container itself it is false too.
func containerDashboardLink(ctx context.Context, env coreEnv, d hostDeps, tailnet bool) (link string, ok bool, err error) {
	if env.getenv("FRIDOLIN_CONTAINER") == "1" || d.docker == "" || d.run == nil {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, containerOpenTimeout)
	defer cancel()
	var ps bytes.Buffer
	if err := d.run(ctx, d.docker, []string{"ps", "--filter", "name=^" + coreContainer + "$", "--format", "{{.Names}}"}, d.baseEnv, &ps, &bytes.Buffer{}); err != nil || strings.TrimSpace(ps.String()) != coreContainer {
		return "", false, nil
	}
	args := []string{"exec", coreContainer, "fridolin", "open", "--print"}
	if tailnet {
		args = append(args, "--tailnet")
	}
	var out, errOut bytes.Buffer
	if err := d.run(ctx, d.docker, args, d.baseEnv, &out, &errOut); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", true, fmt.Errorf("the core runs in the %s container, and its fridolin open refused: %s", coreContainer, msg)
	}
	// The link is the last line; the bridge's log lines may come first.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	link = strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
		return "", true, errors.New("the core's fridolin open --print printed no link: run docker exec " + coreContainer + " fridolin open --print yourself, or send !dashboard in your WhatsApp chat")
	}
	return link, true, nil
}
