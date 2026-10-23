package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// On a Linux host with AppArmor, Docker runs a container under its
// docker-default profile, which forbids the mounts bubblewrap needs for a
// brain's sandbox, and Ubuntu (23.10 on) also confines the user namespaces
// an unconfined process makes (kernel.apparmor_restrict_unprivileged_userns),
// so apparmor=unconfined is not enough either. The core runs under a
// profile of its own instead, fridolin-core: unconfined like
// apparmor=unconfined, but allowed to make user namespaces, without
// lifting the restriction for the rest of the machine. `fridolin core
// apparmor`, run once with sudo, installs it; core start, update and run
// then use it (docs/CORE.md, "The brains' sandbox in the container").

// apparmorProfileName is the core container's AppArmor profile.
const apparmorProfileName = "fridolin-core"

// apparmorProfile is /etc/apparmor.d/fridolin-core.
const apparmorProfile = `abi <abi/4.0>,
include <tunables/global>

# The fridolin-core container (docs/CORE.md, "The brains' sandbox in the
# container"): bubblewrap gives each brain run its own user, mount and PID
# namespaces. Unconfined, as Docker's apparmor=unconfined, but allowed the
# user namespaces Ubuntu restricts (kernel.apparmor_restrict_unprivileged_userns).
# Written by fridolin core apparmor.
profile fridolin-core flags=(unconfined) {
  userns,
}
`

// apparmorOverrideName is the compose override beside compose.yaml that
// puts the core under an AppArmor profile, written by the host commands
// on a host with AppArmor.
const apparmorOverrideName = "compose.apparmor.yaml"

// apparmorState is what the host's AppArmor is like.
type apparmorState struct {
	// Enabled: the kernel runs AppArmor (Docker then confines containers).
	Enabled bool
	// Restricted: kernel.apparmor_restrict_unprivileged_userns is on.
	Restricted bool
	// Loaded: the fridolin-core profile is loaded.
	Loaded bool
}

// hostAppArmor reads the host's AppArmor state from /sys and /proc.
func hostAppArmor() apparmorState {
	var st apparmorState
	if b, err := os.ReadFile("/sys/module/apparmor/parameters/enabled"); err == nil {
		st.Enabled = strings.TrimSpace(string(b)) == "Y"
	}
	if b, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil {
		st.Restricted = strings.TrimSpace(string(b)) == "1"
	}
	// Reading the list of loaded profiles takes root on some systems; the
	// profile's own folder in apparmorfs does not.
	if _, err := os.Stat("/sys/kernel/security/apparmor/policy/profiles"); err == nil {
		entries, _ := os.ReadDir("/sys/kernel/security/apparmor/policy/profiles")
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), apparmorProfileName+".") {
				st.Loaded = true
			}
		}
	}
	if !st.Loaded {
		if b, err := os.ReadFile("/sys/kernel/security/apparmor/profiles"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, apparmorProfileName+" ") {
					st.Loaded = true
				}
			}
		}
	}
	return st
}

// apparmorOverride writes the AppArmor override beside the compose file
// at path when the host needs one, and returns its path ("" when it does
// not, and then removes a stale one). The profile when it is loaded, else
// apparmor=unconfined, with a warning when that will not do.
func apparmorOverride(goos, path string, st apparmorState, warn func(string)) (string, error) {
	p := filepath.Join(filepath.Dir(path), apparmorOverrideName)
	if goos != "linux" || !st.Enabled {
		_ = os.Remove(p)
		return "", nil
	}
	profile := "unconfined"
	switch {
	case st.Loaded:
		profile = apparmorProfileName
	case st.Restricted:
		exe, _ := os.Executable()
		if exe == "" {
			exe = "fridolin"
		}
		warn(fmt.Sprintf("this host restricts user namespaces (kernel.apparmor_restrict_unprivileged_userns=1), so the brains' sandbox cannot start and brains refuse every message: run `sudo %s core apparmor` once, then start the core again", exe))
	}
	body := "# Written by fridolin core: the core's AppArmor profile on this host\n" +
		"# (docs/CORE.md, \"The brains' sandbox in the container\").\n" +
		"services:\n  core:\n    security_opt:\n      - apparmor=" + profile + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", p, err)
	}
	return p, nil
}

// coreAppArmor is `fridolin core apparmor`: install and load the core's
// profile, as root.
func coreAppArmor(ctx context.Context, args []string, env coreEnv, d hostDeps) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(env.stdout, "fridolin core apparmor — install the core container's AppArmor profile (Linux, as root; docs/CORE.md)")
		return 0
	}
	if d.goos != "linux" {
		fmt.Fprintln(env.stderr, "fridolin core apparmor: only Linux hosts run AppArmor")
		return 2
	}
	if !d.apparmor().Enabled {
		fmt.Fprintln(env.stdout, "AppArmor is off on this host: the core needs no profile.")
		return 0
	}
	if d.uid != 0 {
		exe, _ := os.Executable()
		fmt.Fprintf(env.stderr, "fridolin core apparmor writes /etc/apparmor.d and loads the profile: run it as root, sudo %s core apparmor\n", exe)
		return 1
	}
	dst := filepath.Join(d.apparmorDir, apparmorProfileName)
	if err := os.WriteFile(dst, []byte(apparmorProfile), 0o644); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core apparmor: %v\n", err)
		return 1
	}
	if err := d.run(ctx, "apparmor_parser", []string{"-r", dst}, d.baseEnv, env.stdout, env.stderr); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core apparmor: loading %s: %v\n", dst, err)
		return 1
	}
	fmt.Fprintf(env.stdout, "Installed and loaded the AppArmor profile %s (%s); it is loaded again at every boot. Start the core again for it to apply.\n", apparmorProfileName, dst)
	return 0
}
