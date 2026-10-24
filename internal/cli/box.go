package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/container"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/provenance"
	"github.com/Icewater1337/fridolin/internal/version"
)

const boxUsage = `fridolin box — a node in a box (docs/NODES.md, "Node in a box")

Usage:
  fridolin box compose   print the box's compose file of this version, which
                         box.sh and box.ps1 write beside their .env
  fridolin box updater sh|ps1
                         print the box's updater for the host (box-update.sh,
                         box-update.ps1), which box.sh and box.ps1 install
  fridolin box pending [--state-dir /node]
                         in a throwaway container of the image the box runs:
                         check the core's pending update (signature, registry,
                         build provenance), mark it taken and print VERSION=,
                         NODE_IMAGE= and FRIDE_IMAGE=; exit 3 when none is pending
  fridolin box result [--state-dir /node] ok|failed [MESSAGE]
                         record how the taken update ended, for the agent to report
`

// boxDeps are what box pending uses, so tests check without the network.
type boxDeps struct {
	getenv func(string) string
	verify func(ctx context.Context, image, repo string) error
}

func defaultBoxDeps() boxDeps {
	return boxDeps{getenv: os.Getenv, verify: provenance.Verifier{}.Verify}
}

// boxCmd runs `fridolin box` and returns its exit status.
func boxCmd(args []string, stdout, stderr io.Writer) int {
	return runBox(args, stdout, stderr, defaultBoxDeps())
}

func runBox(args []string, stdout, stderr io.Writer, d boxDeps) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, boxUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "compose":
		if len(args) != 0 {
			fmt.Fprintln(stderr, "fridolin box compose takes no arguments")
			return 2
		}
		_, _ = stdout.Write(container.Box)
		return 0
	case "updater":
		switch {
		case len(args) == 1 && args[0] == "sh":
			_, _ = stdout.Write(container.BoxUpdaterSh)
		case len(args) == 1 && args[0] == "ps1":
			_, _ = stdout.Write(container.BoxUpdaterPs1)
		default:
			fmt.Fprintln(stderr, "fridolin box updater takes sh or ps1")
			return 2
		}
		return 0
	case "pending", "result":
	default:
		fmt.Fprintf(stderr, "fridolin box: unknown command %q\n\n%s", sub, boxUsage)
		return 2
	}
	fs := flag.NewFlagSet("box "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("state-dir", "/node", "the node's state folder")
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if sub == "pending" {
		if len(pos) != 0 {
			fmt.Fprintln(stderr, "fridolin box pending takes no arguments")
			return 2
		}
		return boxPending(*dir, stdout, stderr, d)
	}
	if len(pos) < 1 || len(pos) > 2 || pos[0] != "ok" && pos[0] != "failed" {
		fmt.Fprintln(stderr, "fridolin box result takes ok or failed, and a message")
		return 2
	}
	msg := ""
	if len(pos) == 2 {
		msg = pos[1]
	}
	return boxResult(*dir, pos[0] == "ok", msg, stdout, stderr)
}

// boxPending checks the core's request in dir and, when it holds, marks
// it taken and prints what the updater pulls. A request that does not
// hold is dropped, and the agent reports why.
func boxPending(dir string, stdout, stderr io.Writer, d boxDeps) int {
	r, err := nodeagent.ReadBoxUpdate(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fridolin box pending: %v\n", err)
		return 1
	}
	if r == nil {
		return 3
	}
	refuse := func(err error) int {
		fmt.Fprintf(stderr, "fridolin box pending: refused the update to %s: %v\n", r.Version, err)
		_ = nodeagent.FinishBoxUpdate(dir, nodeproto.UpdateReport{From: version.String(), To: r.Version, Error: "the box refused the update: " + err.Error()})
		return 1
	}
	if err := nodeagent.VerifyBoxUpdate(dir, r); err != nil {
		return refuse(err)
	}
	src := httpapi.BoxSourceFromEnv(d.getenv, version.String())
	if src.Registry == "" {
		return refuse(errors.New("this box's image does not know its registry (FRIDOLIN_BOX_REGISTRY), so it takes no image from the core"))
	}
	for ref, name := range map[string]string{r.NodeImage: "fridolin-core", r.FrideImage: "fridolin-fride"} {
		if !strings.HasPrefix(ref, src.Registry+"/"+name+"@sha256:") {
			return refuse(fmt.Errorf("%s is not %s/%s", ref, src.Registry, name))
		}
	}
	repo := provenance.Repo(src.Source, src.Registry)
	if repo == "" {
		return refuse(errors.New("this box's image does not know its repository"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, ref := range []string{r.NodeImage, r.FrideImage} {
		if err := d.verify(ctx, ref, repo); err != nil {
			return refuse(err)
		}
	}
	if err := nodeagent.TakeBoxUpdate(dir); err != nil {
		fmt.Fprintf(stderr, "fridolin box pending: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "VERSION=%s\nNODE_IMAGE=%s\nFRIDE_IMAGE=%s\n", r.Version, r.NodeImage, r.FrideImage)
	return 0
}

// boxResult records how the taken update ended.
func boxResult(dir string, ok bool, msg string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(filepath.Join(dir, nodeagent.BoxUpdateTakenFile))
	if err != nil {
		fmt.Fprintf(stderr, "fridolin box result: no update is being applied: %v\n", err)
		return 1
	}
	var r nodeagent.BoxUpdateRequest
	if err := json.Unmarshal(data, &r); err != nil {
		fmt.Fprintf(stderr, "fridolin box result: %v\n", err)
		return 1
	}
	rep := nodeproto.UpdateReport{From: r.From, To: r.Version, OK: ok}
	if !ok {
		rep.Error = msg
		if rep.Error == "" {
			rep.Error = "the box's updater went back to the images of before"
		}
	}
	if err := nodeagent.FinishBoxUpdate(dir, rep); err != nil {
		fmt.Fprintf(stderr, "fridolin box result: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Recorded: the update from %s to %s %s.\n", rep.From, rep.To, map[bool]string{true: "holds", false: "failed"}[ok])
	return 0
}
