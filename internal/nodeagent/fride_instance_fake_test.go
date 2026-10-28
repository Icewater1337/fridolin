package nodeagent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fakeVM is the folder of sandbox's FrIDE in the fake sbx: what the
// scripts leave there.
func fakeVM(dir, sandbox string) string { return filepath.Join(dir, "fride", sandbox) }

// fakeFrideScript is a script of fride_instance_scripts.go in the fake
// sbx's VM of sandbox, by the name on its first line. Its files are
// under fride/<sandbox>/: config.json (prepare), git (the identity),
// serves.log and running (serve, which runs until stopped, crash or the
// VM's stop appears), stops.log, token (login), credentials (git's
// helpers), owns.log (own); code is the pairing code pair-code prints,
// fail-<name> fails a script. list, mkdirs, clone and repo-new work on
// vmhome/<sandbox>/, the VM's home, which cp copies to and from.
func fakeFrideScript(dir, sandbox, script string, args []string) int {
	name, _, _ := strings.Cut(strings.TrimPrefix(script, frideInstanceScriptPrefix), "\n")
	vm := fakeVM(dir, sandbox)
	home := filepath.Join(dir, "vmhome", sandbox)
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
		if len(b) == 0 {
			// The template's own, as the published template has it.
			b = []byte(`{"name":"fridolin companion (instance)","from":"the template"}`)
		}
		if !exists("config.json") {
			_ = os.WriteFile(at("config.json"), b, 0o600)
		}
		_ = os.MkdirAll(filepath.Join(home, "fride-companion", "repos"), 0o755)
	case "git":
		_ = os.WriteFile(at("git"), []byte(strings.Join(args, "|")), 0o600)
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
	case "list":
		fmt.Println("home=" + fakeVMHome)
		for _, p := range args {
			if _, err := os.Stat(filepath.Join(home, p)); err == nil {
				fmt.Println("have=" + p)
			}
		}
	case "mkdirs":
		for _, p := range args {
			_ = os.MkdirAll(filepath.Join(home, p), 0o755)
		}
	case "own":
		appendFile(at("owns.log"), strings.Join(args, " ")+"\n")
		_ = os.Remove(filepath.Join(home, "fride-companion", "home", "fride.pid"))
	case "credential":
		appendFile(at("credentials"), strings.Join(args, " ")+"\n")
	case "uncredential":
		b, _ := os.ReadFile(at("credentials"))
		var keep []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" && !strings.HasPrefix(l, args[0]+" ") {
				keep = append(keep, l+"\n")
			}
		}
		_ = os.WriteFile(at("credentials"), []byte(strings.Join(keep, "")), 0o600)
	case "clone":
		d := filepath.Join(home, "fride-companion", "repos", args[1])
		fmt.Println("path=" + fakeVMHome + "/fride-companion/repos/" + args[1])
		if _, err := os.Stat(d); err == nil {
			fmt.Println("clone=have")
			return 0
		}
		appendFile(at("clones.log"), args[0]+"\n")
		_ = os.MkdirAll(filepath.Join(d, ".git"), 0o755)
		fmt.Println("clone=done")
	case "repo-new":
		d := filepath.Join(home, "fride-companion", "repos", args[0])
		if _, err := os.Stat(d); err == nil {
			fmt.Fprintf(os.Stderr, "%s already exists in the base folder\n", args[0])
			return 3
		}
		_ = os.MkdirAll(filepath.Join(d, ".git"), 0o755)
		fmt.Println("path=" + fakeVMHome + "/fride-companion/repos/" + args[0])
	case "gh-create":
		fmt.Println("https://github.com/you/" + args[0])
	case "logs":
		fmt.Printf("the last %s lines of FrIDE's log\n", args[0])
	default:
		fmt.Fprintf(os.Stderr, "the fake runs no fride-instance script %q\n", name)
		return 127
	}
	return 0
}
