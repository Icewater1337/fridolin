package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// frideSandboxJoin joins the core as the sandboxed FrIDE's node: a
// second node on this machine that offers nothing but fride (no
// commands, host facts, notifications or android-build), runs from its
// own copy of this fridolin, and as a service of its own.
func frideSandboxJoin(pos []string, dir string, f frideSandboxFlags, stdout, stderr io.Writer) int {
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "fridolin node fride sandbox join takes the join code\n\n%s", nodeFrideSandboxUsage)
		return 2
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "fridolin node fride sandbox join: "+format+"\n", args...)
		return 1
	}
	if def, err := platform.NodeStateDir(); err == nil && samePath(def, dir) {
		return fail("%s is this machine's node: the sandboxed FrIDE's node needs a state dir of its own (leave --state-dir out)", dir)
	}
	if _, err := nodeagent.LoadConfig(dir); err == nil {
		return fail("%s holds a node already (fridolin node status --state-dir %q)", dir, dir)
	}
	name := f.name
	if name == "" {
		host, _ := os.Hostname()
		if i := strings.IndexByte(host, '.'); i > 0 {
			host = host[:i]
		}
		name = host + "-sbx"
	}
	if err := copyOwnBinary(dir); err != nil {
		return fail("copying fridolin into %s: %v", dir, err)
	}
	o := joinFlags{core: f.core, name: name, noExec: true, noHost: true, noNotify: true, noSvc: f.noService,
		service: nodeagent.FrideSandboxService, noAndroid: true}
	if rc := nodeJoin(pos[0], dir, o, thisAppPackage(), stdout, stderr); rc != 0 {
		return rc
	}
	fmt.Fprintf(stdout, "Next, once the core approved this node: fridolin node fride sandbox create --repo <fridolin checkout> --git-name NAME --git-email EMAIL\n(its state dir, %s, is the default of these commands).\n", dir)
	return 0
}

// copyOwnBinary copies the running fridolin into the state dir, where
// the second node's service runs it from and the core's updates replace
// it without touching the machine's node's.
func copyOwnBinary(dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dst := nodeagent.OwnBinary(dir)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".fridolin-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
