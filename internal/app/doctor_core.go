package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// M1's gate checks: the core's tailnet node, its lease, and the brains'
// bubblewrap sandbox in the container. The first two read what the
// running core writes to <data>/core (core_runtime.go), so doctor, run in
// the container with docker exec, reaches no network itself.
var coreDoctorChecks = []DoctorCheck{
	{ID: "tailnet", Title: "tailnet node and MagicDNS name", Required: true, Needed: "M1",
		Applies: func(env *DoctorEnv) bool { return env.Config != nil && env.Config.Core.Tailnet.Enabled },
		Run:     checkTailnetUp},
	{ID: "lease", Title: "core lease renewed", Required: true, Needed: "M1",
		Applies: func(env *DoctorEnv) bool { return env.Config != nil && env.Config.Core.Lease.Enabled },
		Run:     checkLeaseFresh},
	{ID: "container-sandbox", Title: "bubblewrap for brain runs", Required: true, Needed: "M1",
		Applies: func(env *DoctorEnv) bool {
			return env.ConfigGOOS == config.ContainerGOOS && env.Config != nil && env.Config.Sandbox.Enabled
		},
		Run: checkContainerSandbox},
}

func init() { DoctorRegistry = append(DoctorRegistry, coreDoctorChecks...) }

// onHost leaves out a check of the host (its Docker, its Tailscale, the
// secrets in its keychain) when doctor runs inside the core's container,
// which has none of them: the container gets the secrets as variables.
func onHost(env *DoctorEnv) bool { return env.ConfigGOOS != config.ContainerGOOS }

// readCoreState reads <data>/core/name into v; false when the core has
// not written it.
func (env *DoctorEnv) readCoreState(name string, v any) (bool, error) {
	data, err := env.ReadFile(filepath.Join(env.Config.Layout().Core(), name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

func checkTailnetUp(_ context.Context, env *DoctorEnv) DoctorResult {
	var st tailnetStateRecord
	ok, err := env.readCoreState(tailnetStateFile, &st)
	switch {
	case err != nil:
		return DoctorResult{Status: DoctorFail, Detail: "reading the core's tailnet state: " + err.Error()}
	case !ok:
		return DoctorResult{Status: DoctorMissing, Detail: "the core has not started its tailnet node",
			Fix: "start the core (fridolin core start) with fridolin.tailscale.authkey in the keychain"}
	}
	when := " (as the core said " + env.now().Sub(st.UpdatedAt).Round(time.Second).String() + " ago)"
	switch st.State {
	case "up":
		if !st.MagicDNS {
			return DoctorResult{Status: DoctorWarn, Detail: "up as " + st.DNSName + ", but MagicDNS is off" + when,
				Fix: "turn MagicDNS on in the Tailscale admin console (DNS page)"}
		}
		return DoctorResult{Status: DoctorOK, Detail: "up as " + st.DNSName + ", dashboard at " + st.URL + when}
	case "starting":
		return DoctorResult{Status: DoctorWarn, Detail: "joining the tailnet" + when + strings.TrimSuffix(": "+st.Error, ": "),
			Fix: "check the core's log for a sign-in link; store a reusable auth key as fridolin.tailscale.authkey"}
	}
	return DoctorResult{Status: DoctorFail, Detail: "the tailnet node failed: " + st.Error + when,
		Fix: "check fridolin.tailscale.authkey (a reusable key tagged tag:fridolin-core) and the core's log"}
}

func checkLeaseFresh(_ context.Context, env *DoctorEnv) DoctorResult {
	var st leaseStateRecord
	ok, err := env.readCoreState(leaseStateFile, &st)
	switch {
	case err != nil:
		return DoctorResult{Status: DoctorFail, Detail: "reading the core's lease state: " + err.Error()}
	case !ok:
		return DoctorResult{Status: DoctorMissing, Detail: "the core has not taken the lease on this data dir",
			Fix: "fridolin core promote, then start the core"}
	}
	switch st.State {
	case "lost":
		detail := "lost: " + st.Error
		if st.Holder != "" {
			detail += "; device " + st.Holder + " holds it"
		}
		return DoctorResult{Status: DoctorFail, Detail: detail + ". This copy's channels are stopped",
			Fix: "stop this copy (fridolin core stop); to move the core back, export there and import and promote here"}
	case "released":
		return DoctorResult{Status: DoctorWarn, Detail: "released by a clean stop; the core is not running here",
			Fix: "fridolin core start"}
	}
	if st.RenewedAt == nil {
		return DoctorResult{Status: DoctorFail, Detail: "the lease state has no renewal time"}
	}
	age := env.now().Sub(*st.RenewedAt)
	renew := time.Duration(st.RenewSeconds) * time.Second
	stale := time.Duration(st.StaleSeconds) * time.Second
	detail := fmt.Sprintf("held by device %s (%s), renewed %s ago", st.DeviceID, st.Hostname, age.Round(time.Second))
	switch {
	case stale > 0 && age >= stale:
		return DoctorResult{Status: DoctorFail, Detail: detail + ": stale, another device may take it",
			Fix: "is the core running? fridolin core status shows who holds the lease now"}
	case renew > 0 && age > 2*renew:
		return DoctorResult{Status: DoctorWarn, Detail: detail + ": renewals are late (the bucket unreachable, or the core stopped?)",
			Fix: "check the core's log and its connection to the vault bucket"}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}

// checkContainerSandbox runs bubblewrap with a brain run's namespaces, as
// the bridge does before the first sandboxed run.
func checkContainerSandbox(ctx context.Context, env *DoctorEnv) DoctorResult {
	if _, err := env.Stat(sandbox.BwrapPath); err != nil {
		return DoctorResult{Status: DoctorFail, Detail: sandbox.BwrapPath + " is missing, so brains refuse every message",
			Fix: "use the fridolin-core image, which has bubblewrap"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := env.Run(ctx, sandbox.BwrapPath, "--unshare-user", "--unshare-pid", "--unshare-ipc",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--die-with-parent", "--", "/bin/true"); err != nil {
		return DoctorResult{Status: DoctorFail, Detail: "bubblewrap cannot make a brain's namespaces here: " + err.Error(),
			Fix: "run the core with compose.yaml's security_opt (seccomp profile, systempaths=unconfined); on an AppArmor host also apparmor=unconfined (docs/CORE.md)"}
	}
	return DoctorResult{Status: DoctorOK, Detail: "brain runs get their own user, mount and PID namespaces"}
}
