package nodeagent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fakeFrideScript is a script of fride_sandbox_scripts.go in the fake
// sbx's VM, by the name on its first line. Its files are under
// dir/fride: config.json (prepare), git (the identity), built and
// builds.log (build), serves.log and running (serve, which runs until
// stopped or crash appears), stops.log, token (login); code is the
// pairing code pair-code prints, fail-<name> fails a script.
func fakeFrideScript(dir, script string, args []string) int {
	name, _, _ := strings.Cut(strings.TrimPrefix(script, "# fride-sandbox: "), "\n")
	vm := filepath.Join(dir, "fride")
	_ = os.MkdirAll(vm, 0o700)
	at := func(f string) string { return filepath.Join(vm, f) }
	exists := func(f string) bool { _, err := os.Stat(at(f)); return err == nil }
	if b, err := os.ReadFile(at("fail-" + name)); err == nil {
		fmt.Fprint(os.Stderr, string(b))
		return 1
	}
	switch name {
	case "prepare":
		b, _ := io.ReadAll(os.Stdin)
		if !exists("config.json") {
			_ = os.WriteFile(at("config.json"), b, 0o600)
		}
	case "git":
		_ = os.WriteFile(at("git"), []byte(strings.Join(args, "|")), 0o600)
	case "build":
		_ = os.WriteFile(at("built"), []byte(args[1]), 0o600)
		appendFile(at("builds.log"), strings.Join(args, " ")+"\n")
		fmt.Printf("Built FrIDE %s.\n", args[1])
	case "serve":
		_ = os.Remove(at("stopped"))
		appendFile(at("serves.log"), strings.Join(args, " ")+"\n")
		_ = os.WriteFile(at("running"), nil, 0o600)
		fmt.Println("Started FrIDE (42) on 10.0.0.2:" + args[0] + ".")
		defer os.Remove(at("running"))
		for range 3000 {
			if exists("crash") {
				_ = os.Remove(at("crash"))
				break
			}
			if exists("stopped") {
				break
			}
			sleepMS(10)
		}
		fmt.Fprintln(os.Stderr, "FrIDE (42) ended.")
		return 1
	case "stop":
		appendFile(at("stops.log"), "stop\n")
		_ = os.WriteFile(at("stopped"), nil, 0o600)
	case "pair-code":
		code, _ := os.ReadFile(at("code"))
		fmt.Printf(`{"code":%q}`+"\n", strings.TrimSpace(string(code)))
	case "login":
		b, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(at("token"), b, 0o600)
	case "status":
		yes := func(f string) string {
			if exists(f) {
				return "yes"
			}
			return "no"
		}
		built, _ := os.ReadFile(at("built"))
		fmt.Printf("login=%s\ngit=%s\nrunning=%s\ncommit=%s\n", yes("token"), yes("git"), yes("running"), built)
	default:
		fmt.Fprintf(os.Stderr, "the fake runs no fride-sandbox script %q\n", name)
		return 127
	}
	return 0
}
