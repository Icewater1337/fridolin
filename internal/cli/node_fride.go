package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

const nodeFrideUsage = `fridolin node fride — FrIDE on this machine, run by this node (docs/NODES.md, FrIDE)

Usage:
  fridolin node fride pair --url http://127.0.0.1:PORT [--code CODE|-]
                       pair this node with FrIDE running in companion mode
                       (fride serve --under-fridolin): it runs
                       fride pair-companion --json for the code unless --code
                       gives it (--code - reads it, or pair-companion's JSON,
                       from stdin: a box pipes it over from FrIDE's container), keeps FrIDE's token in the state dir (0600) and
                       gives FrIDE a loopback webhook for its news
  fridolin node fride status
                       what is paired, and whether FrIDE answers
  fridolin node fride host setup|status
                       set this machine up to run FrIDE instances, each a FrIDE in
                       a Docker Sandbox of its own, which the core starts;
                       fridolin node fride host help for more
  fridolin node fride instance ls|stop NAME|rm NAME|logs NAME
                       the FrIDE instances on this machine;
                       fridolin node fride instance help for more

Flags:
  --fride PATH         the fride program (default: fride on the PATH)
  --fride-home DIR     FrIDE's home, FRIDE_HOME, for pair-companion (default: FrIDE's)
  --webhook HOST:PORT  the loopback address to hear FrIDE's webhook on
                       (default: the one paired before, else a free port)
  --base DIR           the folder the core's fride_repo_add clones into or
                       makes new repositories in: one of FrIDE's baseFolders
                       (default: the one set before, else FrIDE's only one)
  --state-dir DIR      the node's state folder

The node offers fride to the core while FrIDE answers; the owner approves
it once on the core (fridolin nodes approve ID --commands).
`

// nodeFrideCmd runs `fridolin node fride`.
func nodeFrideCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, nodeFrideUsage)
		return 0
	}
	sub, args := args[0], args[1:]
	if sub == "host" || sub == "instance" {
		return nodeFrideHostCmd(sub == "instance", args, stdout, stderr)
	}
	fs := flag.NewFlagSet("node fride "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, nodeFrideUsage) }
	stateDir := fs.String("state-dir", "", "the node's state folder")
	url := fs.String("url", "", "FrIDE's loopback address")
	code := fs.String("code", "", "the code of fride pair-companion")
	bin := fs.String("fride", "fride", "the fride program")
	home := fs.String("fride-home", "", "FrIDE's home")
	webhook := fs.String("webhook", "", "the loopback host:port for FrIDE's webhook")
	base := fs.String("base", "", "the folder new repositories go to")
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintf(stderr, "fridolin node fride %s takes no arguments\n", sub)
		return 2
	}
	dir := *stateDir
	if dir == "" {
		if dir, err = platform.NodeStateDir(); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride: %v; pass --state-dir\n", err)
			return 1
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch sub {
	case "pair":
		if *url == "" {
			fmt.Fprintf(stderr, "fridolin node fride pair: --url is missing: FrIDE's address on this machine, such as http://127.0.0.1:7777\n")
			return 2
		}
		if err := thisAppPackage().writeError(dir); err != nil {
			fmt.Fprintf(stderr, "fridolin node fride pair: %v\n", err)
			return 1
		}
		c := *code
		if c == "-" {
			if c, err = readPairCode(os.Stdin); err != nil {
				fmt.Fprintf(stderr, "fridolin node fride pair: %v\n", err)
				return 2
			}
		}
		if c == "" {
			if c, err = frideCode(ctx, *bin, *home); err != nil {
				fmt.Fprintf(stderr, "fridolin node fride pair: %v\nRun fride pair-companion yourself and pass its code with --code.\n", err)
				return 1
			}
		}
		info, err := nodeagent.PairFride(ctx, nodeagent.FridePairOptions{Dir: dir, URL: *url, Code: c, Webhook: *webhook, Base: *base})
		if err != nil {
			fmt.Fprintf(stderr, "fridolin node fride pair: %v\n", err)
			return 1
		}
		cfg, _ := nodeagent.LoadConfig(dir)
		hook, newBase := "", "FrIDE's only base folder"
		if cfg.Fride != nil {
			hook = cfg.Fride.Webhook
			if cfg.Fride.Base != "" {
				newBase = cfg.Fride.Base
			}
		}
		if cfg.Box != nil {
			newBase = "nowhere from the core: in a box, clone them in FrIDE's container (docker compose exec fride git -C /var/lib/fride/repos clone URL)"
		}
		fmt.Fprintf(stdout, "Paired with FrIDE at %s (repositories: %s).\nIts news comes to %s; the token is in %s.\nRepositories the core adds go to %s.\nThe node offers fride to the core within half a minute, while FrIDE runs. On the core, approve it once:\n\n  fridolin nodes approve %s --commands\n\n",
			*url, orNone(info.Repos), hook, filepath.Join(dir, nodeagent.FrideTokenFile), newBase, nodeID(dir))
	case "status":
		cfg, err := nodeagent.LoadConfig(dir)
		if err != nil {
			fmt.Fprintf(stdout, "Not joined: %v\n", err)
			return 1
		}
		if cfg.Fride == nil {
			fmt.Fprintln(stdout, "FrIDE: not paired (fridolin node fride pair --url http://127.0.0.1:PORT)")
			return 0
		}
		fmt.Fprintf(stdout, "FrIDE: %s, webhook %s\n", cfg.Fride.URL, cfg.Fride.Webhook)
		if cfg.Fride.Base != "" {
			fmt.Fprintf(stdout, "New repositories: %s\n", cfg.Fride.Base)
		}
		tok, err := os.ReadFile(filepath.Join(dir, nodeagent.FrideTokenFile))
		if err != nil {
			fmt.Fprintf(stdout, "Token: missing (%v): pair again\n", err)
			return 1
		}
		info, err := nodeagent.ProbeFride(ctx, cfg.Fride.URL, strings.TrimSpace(string(tok)))
		if err != nil {
			fmt.Fprintf(stdout, "Answers: no: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Answers: yes, companion mode, repositories %s\n", orNone(info.Repos))
	default:
		fmt.Fprintf(stderr, "unknown node fride command %q\n\n%s", sub, nodeFrideUsage)
		return 2
	}
	return 0
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func nodeID(dir string) string {
	if key, err := nodeagent.LoadKey(dir); err == nil {
		return nodeagent.DeviceID(key)
	}
	return "<id>"
}

// readPairCode reads a pairing code from r: the code alone, or the JSON
// fride pair-companion --json prints.
func readPairCode(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "{") {
		var code struct {
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(text), &code) == nil && code.Code != "" {
			return code.Code, nil
		}
		return "", errors.New("stdin holds JSON without a code (pipe fride pair-companion --json)")
	}
	if text == "" || strings.ContainsAny(text, " \t\r\n") {
		return "", errors.New("stdin holds no pairing code")
	}
	return text, nil
}

// frideCode asks the fride program for a pairing code, with FRIDE_HOME set
// to home when given.
func frideCode(ctx context.Context, bin, home string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "pair-companion", "--json")
	if home != "" {
		cmd.Env = append(os.Environ(), "FRIDE_HOME="+home)
	}
	var errOut strings.Builder
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s pair-companion --json: %v %s", bin, err, strings.TrimSpace(errOut.String()))
	}
	var code struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(out, &code); err != nil || code.Code == "" {
		return "", fmt.Errorf("%s pair-companion --json printed no code: %q", bin, strings.TrimSpace(string(out)))
	}
	return code.Code, nil
}
