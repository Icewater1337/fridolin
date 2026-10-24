package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/seal"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
	"golang.org/x/term"
)

// terminalIO is the terminal of `fridolin core secrets` and send: nil
// fields when stdin is not one.
type terminalIO struct {
	// readHidden prompts on stderr and reads a line without echo.
	readHidden func(prompt string) (string, error)
	// readLine prompts on stderr and reads a line.
	readLine func(prompt string) (string, error)
}

// stdinTerminal is the process's terminal, when stdin is one.
func stdinTerminal() terminalIO {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return terminalIO{}
	}
	in := bufio.NewReader(os.Stdin)
	return terminalIO{
		readHidden: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
		readLine: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			s, err := in.ReadString('\n')
			if err != nil && s == "" {
				return "", err
			}
			return strings.TrimSpace(s), nil
		},
	}
}

// transferSecrets are the host secrets a move carries to the next
// machine (secretxfer.MoveNames; --with-tailscale-key adds that key).
var transferSecrets = secretxfer.MoveNames

// secretFullName takes a secret's name with or without "fridolin.", and
// refuses one the core does not use.
func secretFullName(name string) (string, error) {
	full := name
	if !strings.HasPrefix(full, "fridolin.") {
		full = "fridolin." + full
	}
	for _, s := range containerSecrets {
		if s.secret == full {
			return full, nil
		}
	}
	var names []string
	for _, s := range containerSecrets {
		names = append(names, strings.TrimPrefix(s.secret, "fridolin."))
	}
	return "", fmt.Errorf("%q is not a secret the core uses; one of: %s", name, strings.Join(names, ", "))
}

// inContainer refuses the host commands inside the core's container,
// which has no host secrets.
func inContainer(env coreEnv, sub string) bool {
	if env.getenv("FRIDOLIN_CONTAINER") == "1" {
		fmt.Fprintf(env.stderr, "fridolin core %s: run this on the host that runs Docker, not in the container: the host's secrets are there\n", sub)
		return true
	}
	return false
}

// coreSecrets runs `fridolin core secrets [list] | set NAME [--replace]`.
func coreSecrets(args []string, env coreEnv, d hostDeps) int {
	if inContainer(env, "secrets") {
		return 1
	}
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("core secrets "+sub, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	replace := fs.Bool("replace", false, "replace a secret that is already stored")
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	switch {
	case sub == "list" && len(pos) == 0:
		fmt.Fprintf(env.stdout, "Host secrets of the core, in %s:\n", d.place())
		for _, s := range containerSecrets {
			state := "stored"
			switch ok, err := d.secrets.Has(s.secret); {
			case err != nil:
				state = "error: " + err.Error()
			case !ok:
				state = "missing"
			}
			fmt.Fprintf(env.stdout, "  %-8s %-34s %s\n", state, s.secret, s.env)
		}
		fmt.Fprintln(env.stdout, "Store one with: fridolin core secrets set NAME (the value is typed without echo)")
		return 0
	case sub == "set" && len(pos) == 1:
	default:
		fmt.Fprintln(env.stderr, "usage: fridolin core secrets [list] | fridolin core secrets set NAME [--replace]")
		return 2
	}
	name, err := secretFullName(pos[0])
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core secrets set: %v\n", err)
		return 2
	}
	have, err := d.secrets.Has(name)
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core secrets set: %v\n", err)
		return 1
	}
	if have && !*replace {
		fmt.Fprintf(env.stderr, "fridolin core secrets set: %s is already stored in %s; pass --replace to replace it\n", name, d.place())
		return 1
	}
	var value string
	if env.term.readHidden != nil {
		value, err = env.term.readHidden("Value for " + name + " (typing is not shown): ")
	} else {
		var b []byte
		b, err = io.ReadAll(io.LimitReader(env.stdin, 64<<10))
		value = string(b)
	}
	value = strings.TrimSpace(value)
	switch {
	case err != nil:
		fmt.Fprintf(env.stderr, "fridolin core secrets set: reading the value: %v\n", err)
		return 1
	case value == "":
		fmt.Fprintln(env.stderr, "fridolin core secrets set: the value is empty; nothing was stored")
		return 1
	case strings.ContainsAny(value, " \t\r\n"):
		fmt.Fprintln(env.stderr, "fridolin core secrets set: the value has spaces or line breaks; paste the one line from the password manager")
		return 1
	}
	if msg := checkSecretValue(name, value); msg != "" {
		fmt.Fprintf(env.stderr, "fridolin core secrets set: %s; nothing was stored\n", msg)
		return 1
	}
	if err := d.secrets.Set(name, value); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core secrets set: %v\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Stored %s in %s.\n", name, d.place())
	return 0
}

// coreTransfer runs `fridolin core receive` and `fridolin core send`.
func coreTransfer(ctx context.Context, sub string, args []string, env coreEnv, d hostDeps) int {
	if inContainer(env, sub) {
		return 1
	}
	fs := flag.NewFlagSet("core "+sub, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	timeout := fs.Duration("timeout", secretxfer.DefaultTimeout, "how long to wait in all")
	var replace, withTS *bool
	var to, code *string
	if sub == "receive" {
		replace = fs.Bool("replace", false, "let received values replace different ones already stored here")
	} else {
		to = fs.String("to", "", "the address core receive printed (TAILSCALE-IP:PORT)")
		code = fs.String("code", "", "the code core receive printed (asked for when left out)")
		withTS = fs.Bool("with-tailscale-key", false, "send "+hostsecrets.TailscaleAuthKey+" too")
	}
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	case fs.NArg() > 0:
		fmt.Fprintf(env.stderr, "fridolin core %s takes no arguments, got %q\n", sub, fs.Args())
		return 2
	case sub == "send" && *to == "":
		fmt.Fprintln(env.stderr, "fridolin core send: --to ADDRESS is missing (core receive on the other machine prints it)")
		return 2
	}
	if d.tailnet == nil {
		d.tailnet = secretxfer.Local{}
	}
	if sub == "receive" {
		return coreReceive(ctx, env, d, *replace, *timeout)
	}
	return coreSend(ctx, env, d, *to, *code, *withTS, *timeout)
}

func coreReceive(ctx context.Context, env coreEnv, d hostDeps, replace bool, timeout time.Duration) int {
	allowed := make([]string, 0, len(containerSecrets))
	for _, s := range containerSecrets {
		allowed = append(allowed, s.secret)
	}
	res, err := secretxfer.Receive(ctx, secretxfer.ReceiveOptions{
		Net: d.tailnet, Store: d.secrets, Allowed: allowed, Replace: replace, Timeout: timeout,
		Ready: func(addr, code string) {
			fmt.Fprintf(env.stdout, "Waiting %s for the secrets, on the tailnet at %s. On the machine that runs the core now, in a terminal:\n\n  fridolin core send --to %s --code %s\n\nWhen it connects, this machine and that one each show four words. They must be the same.\n", timeout, addr, addr, code)
		},
		Connected: func(p secretxfer.Peer, words string) {
			fmt.Fprintf(env.stdout, "\n%s connected. The words here:\n\n    %s\n\nConfirm on that machine only if it shows the same four words.\n", p, strings.ToUpper(words))
		},
	})
	transferAudit(d, "received", res, err)
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core receive: %v\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Stored in %s, from %s: %s\n", d.place(), res.From, listOrNone(res.Stored))
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(env.stdout, "Already here with the same value: %s\n", strings.Join(res.Unchanged, ", "))
	}
	return 0
}

func coreSend(ctx context.Context, env coreEnv, d hostDeps, to, code string, withTS bool, timeout time.Duration) int {
	if env.term.readLine == nil {
		fmt.Fprintln(env.stderr, "fridolin core send: run it in a terminal: it asks you to compare four words with the other machine")
		return 2
	}
	names := slices.Clone(transferSecrets)
	if withTS {
		names = append(names, hostsecrets.TailscaleAuthKey)
	}
	secrets := map[string]string{}
	var missing []string
	for _, n := range names {
		v, err := d.secrets.Get(n)
		switch {
		case errors.Is(err, hostsecrets.ErrNotFound):
			missing = append(missing, n)
		case err != nil:
			fmt.Fprintf(env.stderr, "fridolin core send: %v\n", err)
			return 1
		default:
			secrets[n] = v
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(env.stderr, "Not in %s, so not sent: %s\n", d.place(), strings.Join(missing, ", "))
	}
	sendDriftCheck(ctx, env, d)
	if code == "" {
		var err error
		if code, err = env.term.readLine("The code core receive shows: "); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core send: %v\n", err)
			return 1
		}
	}
	host, _ := os.Hostname()
	res, err := secretxfer.Send(ctx, secretxfer.SendOptions{
		Net: d.tailnet, To: to, Code: code, Secrets: secrets, From: host, Timeout: timeout,
		Confirm: func(p secretxfer.Peer, words string) (bool, error) {
			fmt.Fprintf(env.stdout, "Connected to %s. The words here:\n\n    %s\n\n", p, strings.ToUpper(words))
			ans, err := env.term.readLine("Does " + p.Node + " show the same four words? Type yes to send " + fmt.Sprint(len(secrets)) + " secrets: ")
			return strings.EqualFold(strings.TrimSpace(ans), "yes"), err
		},
	})
	clear(secrets)
	transferAudit(d, "sent", res, err)
	if err != nil {
		fmt.Fprintf(env.stderr, "fridolin core send: %v\n", err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Sent. %s stored: %s\n", res.Peer.Node, listOrNone(res.Stored))
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(env.stdout, "Already there with the same value: %s\n", strings.Join(res.Unchanged, ", "))
	}
	return 0
}

// sendDriftCheck runs before core send: a move comes next, and its export
// refuses while a brain.json on disk keeps more home than the manifest
// the owner approved (#189). It asks the running core (core export
// --drift) and passes its warnings on; a core that does not run, or
// Docker missing, says nothing here.
func sendDriftCheck(ctx context.Context, env coreEnv, d hostDeps) {
	if d.docker == "" || d.run == nil {
		return
	}
	var out bytes.Buffer
	err := d.run(ctx, d.docker, []string{"exec", coreContainer, "fridolin", "core", "export", "--drift"}, d.baseEnv, &out, &out)
	if !strings.Contains(out.String(), "warning: brain ") {
		return
	}
	fmt.Fprintf(env.stderr, "Before the move, the core says:\n%s", out.String())
	if err != nil {
		fmt.Fprintln(env.stderr, "Its export refuses until you approve those changes in the dashboard, or pass --use-approved to core export. The secrets can go now all the same.")
	}
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// transferAudit appends one line per transfer, names only, to
// secrets-transfer.log beside the compose file.
func transferAudit(d hostDeps, direction string, res secretxfer.Result, err error) {
	if d.composePath == "" {
		return
	}
	line := map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "direction": direction,
		"peer": res.Peer.Node, "peer_login": res.Peer.Login, "from": res.From,
		"stored": res.Stored, "unchanged": res.Unchanged, "result": "ok",
	}
	if err != nil {
		line["result"] = "error: " + err.Error()
	}
	b, jerr := json.Marshal(line)
	if jerr != nil {
		return
	}
	dir := filepath.Dir(d.composePath)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, ferr := os.OpenFile(filepath.Join(dir, "secrets-transfer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// checkSecretValue says what is wrong with value as the secret name, or
// "" when it fits: an age identity of the right kind, a seal key.
func checkSecretValue(name, value string) string {
	for _, s := range containerSecrets {
		if s.secret != name {
			continue
		}
		switch {
		case name == hostsecrets.AgeIdentity:
			if _, err := age.ParseX25519Identity(value); err != nil {
				return "that is not an age identity (AGE-SECRET-KEY-1…)"
			}
		case s.made == madeIdentity:
			if _, err := age.ParseHybridIdentity(value); err != nil {
				return "that is not a post-quantum age identity (AGE-SECRET-KEY-PQ-1…)"
			}
		case s.made == madeKey:
			if _, err := seal.ParseKey(value); err != nil {
				return "that is not a key of 32 bytes in base64"
			}
		}
	}
	return ""
}
