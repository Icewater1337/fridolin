package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/tailnet"
)

// boxTailnetWait is how long node tailnet and node join --box wait for
// the box's tailnet device to come up: long enough for the owner to open
// the sign-in link and sign in.
const boxTailnetWait = 15 * time.Minute

// newBoxTailnet makes a box's tailnet device; tests hand in a fake.
var newBoxTailnet tailnet.Factory = tailnet.New

// boxTailnet is the tailnet device of the box whose node keeps its state
// in dir: tsnet, with its own state in dir's tailnet folder, signed in
// with TS_AUTHKEY the first time when it is set, else with the link it
// prints.
func boxTailnet(dir, hostname string, stderr io.Writer) (tailnet.Node, error) {
	if os.Getenv("FRIDOLIN_BOX_NO_TAILNET") == "1" {
		// For trying a box against a dev core: the box's own network,
		// no tailnet (docs/NODES.md, "Trying a box").
		fmt.Fprintln(stderr, "box: FRIDOLIN_BOX_NO_TAILNET=1: reaching the core over the box's network, not the tailnet")
		return &tailnet.Fake{Status: tailnet.Status{DNSName: hostname}}, nil
	}
	tdir := filepath.Join(dir, "tailnet")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		return nil, err
	}
	return newBoxTailnet(tailnet.Options{
		Hostname: hostname, Dir: tdir, AuthKey: strings.TrimSpace(os.Getenv("TS_AUTHKEY")),
		Logf: func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) },
	})
}

// nodeTailnet is `fridolin node tailnet`: the box joins the tailnet as
// its own device and says how it is known, so that node join --box finds
// it signed in and spends the join code at once.
func nodeTailnet(dir, name, hostname string, stdout, stderr io.Writer) int {
	if hostname == "" {
		if name == "" {
			name, _ = os.Hostname()
			if i := strings.IndexByte(name, '.'); i > 0 {
				name = name[:i]
			}
		}
		hostname = nodeagent.BoxHostname(name)
	}
	if err := nodeagent.CheckTailnetHostname(hostname); err != nil {
		fmt.Fprintf(stderr, "fridolin node tailnet: %v\n", err)
		return 2
	}
	tn, err := boxTailnet(dir, hostname, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node tailnet: %v\n", err)
		return 1
	}
	defer tn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), boxTailnetWait)
	defer cancel()
	st, err := tn.Up(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin node tailnet: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "On the tailnet as %s (%s).\n", st.DNSName, strings.Join(st.IPs, ", "))
	return 0
}
