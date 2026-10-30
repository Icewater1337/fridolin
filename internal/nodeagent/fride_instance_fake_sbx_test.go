package nodeagent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// fakeFrideSbx stands in for the sbx commands only the FrIDE instances
// use, in a fake dir its fixture marked (fride-fake), before fakeSbx
// gets the rest: create records the Docker disk's size (disk.log) and
// its arguments; stop marks a sandbox stopped (stopped-<name>), which ls
// shows and an exec boots; rm takes the VM's disk; cp copies between the
// host and vmhome/<sandbox>/, the VM's home, as sbx does (a folder into
// an existing one merges, a missing source fails); policy rm removes a
// rule by its resource; secret keeps the github service's value in
// github-secret and each custom secret in custom/<placeholder> (its
// hosts and value). fail-cp fails cp with its text, fail-ls fails ls, and
// fail-ls-after-cp has a failed cp make sbx stop listing, as an expired
// context does.
func fakeFrideSbx(dir string, args []string) (int, bool) {
	if _, err := os.Stat(filepath.Join(dir, "fride-fake")); err != nil || len(args) == 0 {
		return 0, false
	}
	record := func() {
		line, _ := json.Marshal(map[string]any{"args": args, "telemetry_off": os.Getenv("SBX_NO_TELEMETRY") == "1"})
		appendFile(filepath.Join(dir, "calls.log"), string(line)+"\n")
	}
	switch {
	case args[0] == "create":
		appendFile(filepath.Join(dir, "disk.log"), os.Getenv(frideInstanceDiskEnv)+"\n")
		if _, err := os.Stat(filepath.Join(dir, "fail-create")); err == nil {
			return 0, false
		}
		name := args[slices.Index(args, "--name")+1]
		_ = os.MkdirAll(filepath.Join(dir, "vmhome", name), 0o755)
		return 0, false
	case args[0] == "ls":
		record()
		if _, err := os.Stat(filepath.Join(dir, "fail-ls")); err == nil {
			return 1, true
		}
		rows := []map[string]string{}
		for _, n := range fakeSandboxes(dir) {
			status := "running"
			if _, err := os.Stat(filepath.Join(dir, "stopped-"+n)); err == nil {
				status = "stopped"
			}
			rows = append(rows, map[string]string{"name": n, "status": status, "agent": "claude"})
		}
		b, _ := json.Marshal(rows)
		fmt.Println(string(b))
		return 0, true
	case args[0] == "stop":
		record()
		for _, n := range args[1:] {
			_ = os.WriteFile(filepath.Join(dir, "stopped-"+n), nil, 0o600)
			// The VM stops, and every exec in it ends.
			_ = os.WriteFile(filepath.Join(fakeVM(dir, n), "stopped"), nil, 0o600)
		}
		return 0, true
	case args[0] == "exec":
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && slices.Contains(fakeSandboxes(dir), a) {
				_ = os.Remove(filepath.Join(dir, "stopped-"+a))
				break
			}
		}
		return 0, false
	case args[0] == "rm":
		// The VM's disk goes with the sandbox.
		name := args[len(args)-1]
		_ = os.RemoveAll(filepath.Join(dir, "vmhome", name))
		_ = os.RemoveAll(fakeVM(dir, name))
		_ = os.Remove(filepath.Join(dir, "stopped-"+name))
		return 0, false
	case len(args) > 2 && args[0] == "policy" && args[1] == "rm" && slices.Contains(args, "--resource"):
		record()
		i, j := slices.Index(args, "--sandbox"), slices.Index(args, "--resource")
		scope, res := "sandbox:"+args[i+1], args[j+1]
		saveFakeRules(dir, slices.DeleteFunc(fakeRules(dir), func(r fakeRule) bool { return r.Scope == scope && slices.Contains(r.Resources, res) }))
		return 0, true
	case args[0] == "cp":
		record()
		if b, err := os.ReadFile(filepath.Join(dir, "fail-cp")); err == nil {
			fmt.Fprint(os.Stderr, string(b))
			if _, err := os.Stat(filepath.Join(dir, "fail-ls-after-cp")); err == nil {
				_ = os.WriteFile(filepath.Join(dir, "fail-ls"), nil, 0o600)
			}
			return 1, true
		}
		return fakeCp(dir, args[1], args[2]), true
	case args[0] == "secret" && len(args) > 2 && args[1] == "set" && args[2] == "github":
		record()
		b, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(filepath.Join(dir, "github-secret"), b, 0o600)
		return 0, true
	case args[0] == "secret" && len(args) > 2 && args[1] == "rm" && args[2] == "github":
		record()
		_ = os.Remove(filepath.Join(dir, "github-secret"))
		return 0, true
	case args[0] == "secret" && len(args) > 1 && args[1] == "set-custom":
		record()
		return fakeSetCustom(dir, args[2:]), true
	case args[0] == "secret" && len(args) > 2 && args[1] == "rm" && args[2] == "--placeholder":
		record()
		_ = os.Remove(filepath.Join(dir, "custom", args[3]))
		return 0, true
	}
	return 0, false
}

// fakeSetCustom is `secret set-custom --sandbox S --host H… --placeholder
// P --command "/bin/cat 'FILE'"` (on Windows fridolin.exe's sbx-secret):
// it reads FILE as sbx would.
func fakeSetCustom(dir string, args []string) int {
	var hosts []string
	ph, cmd := "", ""
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--host":
			hosts = append(hosts, args[i+1])
		case "--placeholder":
			ph = args[i+1]
		case "--command":
			cmd = args[i+1]
		}
	}
	file := sbxSecretFileOf(cmd)
	if file == "" || ph == "" {
		fmt.Fprintf(os.Stderr, "set-custom: %v\n", args)
		return 2
	}
	b, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.MkdirAll(filepath.Join(dir, "custom"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "custom", ph), []byte(strings.Join(hosts, ",")+"="+string(b)), 0o600)
	fmt.Printf("Saved custom secret %s\n", ph)
	return 0
}

// fakeVMHome is the VM's home in the fake.
const fakeVMHome = "/home/agent" // personal-data:ignore (the home of sbx's VM user)

// fakeCp is `cp SRC DST`, one of them SANDBOX:PATH under the VM's home.
func fakeCp(dir, src, dst string) int {
	resolve := func(p string) (string, bool) {
		name, path, ok := strings.Cut(p, ":")
		if !ok || strings.HasPrefix(p, "/") {
			return p, false
		}
		if !slices.Contains(fakeSandboxes(dir), name) {
			return "", true
		}
		rel, ok := strings.CutPrefix(path, fakeVMHome)
		if !ok {
			return "", true
		}
		return filepath.Join(dir, "vmhome", name, rel), true
	}
	from, inVM := resolve(src)
	to, toVM := resolve(dst)
	if from == "" || to == "" || inVM == toVM {
		fmt.Fprintf(os.Stderr, "error: cp %s %s\n", src, dst)
		return 1
	}
	if _, err := os.Stat(from); err != nil {
		fmt.Fprintf(os.Stderr, "error: path %q not found\n", src)
		return 1
	}
	if fi, err := os.Stat(to); err == nil && fi.IsDir() {
		to = filepath.Join(to, filepath.Base(from))
	} else if _, err := os.Stat(filepath.Dir(to)); err != nil {
		fmt.Fprintf(os.Stderr, "error: tar extract failed: %s: Cannot open\n", filepath.Dir(dst))
		return 1
	}
	if err := copyTree(from, to); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// copyTree copies src to dst, a folder into what is there: files of the
// same name are replaced, others stay.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}
