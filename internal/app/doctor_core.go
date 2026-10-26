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

	"github.com/Icewater1337/fridolin/internal/cloud"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// M1's gate checks: the core's tailnet node, its lease, and the brains'
// bubblewrap sandbox in the container. The first two read what the
// running core writes to <data>/core (core_runtime.go), so doctor, run in
// the container with docker exec, reaches no network itself.
var coreDoctorChecks = []doctor.Check{
	{ID: "tailnet", Title: "tailnet node and MagicDNS name", Required: true, Needed: "M1",
		Applies: func(env *doctor.Env) bool { return env.Config != nil && env.Config.Core.Tailnet.Enabled },
		Run:     checkTailnetUp},
	{ID: "lease", Title: "core lease renewed", Required: true, Needed: "M1",
		Applies: func(env *doctor.Env) bool { return env.Config != nil && env.Config.Core.Lease.Enabled },
		Run:     checkLeaseFresh},
	{ID: "container-sandbox", Title: "bubblewrap for brain runs", Required: true, Needed: "M1",
		Applies: func(env *doctor.Env) bool {
			return env.ConfigGOOS == config.ContainerGOOS && env.Config != nil && env.Config.Sandbox.Enabled
		},
		Run: checkContainerSandbox},
}

// readCoreState reads <data>/core/name into v; false when the core has
// not written it.
func readCoreState(env *doctor.Env, name string, v any) (bool, error) {
	data, err := env.ReadFile(filepath.Join(env.Config.Layout().Core(), name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

func checkTailnetUp(_ context.Context, env *doctor.Env) doctor.Result {
	var st tailnetStateRecord
	ok, err := readCoreState(env, tailnetStateFile, &st)
	switch {
	case err != nil:
		return doctor.Result{Status: doctor.Fail, Detail: "reading the core's tailnet state: " + err.Error()}
	case !ok:
		return doctor.Result{Status: doctor.Missing, Detail: "the core has not started its tailnet node",
			Fix: "start the core (fridolin core start) with fridolin.tailscale.authkey in the keychain"}
	}
	when := " (as the core said " + env.Time().Sub(st.UpdatedAt).Round(time.Second).String() + " ago)"
	switch st.State {
	case "up":
		if !st.MagicDNS {
			return doctor.Result{Status: doctor.Warn, Detail: "up as " + st.DNSName + ", but MagicDNS is off" + when,
				Fix: "turn MagicDNS on in the Tailscale admin console (DNS page)"}
		}
		return doctor.Result{Status: doctor.OK, Detail: "up as " + st.DNSName + ", dashboard at " + st.URL + when}
	case "starting":
		return doctor.Result{Status: doctor.Warn, Detail: "joining the tailnet" + when + strings.TrimSuffix(": "+st.Error, ": "),
			Fix: "check the core's log for a sign-in link; store a reusable auth key as fridolin.tailscale.authkey"}
	}
	return doctor.Result{Status: doctor.Fail, Detail: "the tailnet node failed: " + st.Error + when,
		Fix: "check fridolin.tailscale.authkey (a reusable key tagged tag:fridolin-core) and the core's log"}
}

func checkLeaseFresh(_ context.Context, env *doctor.Env) doctor.Result {
	var st leaseStateRecord
	ok, err := readCoreState(env, leaseStateFile, &st)
	switch {
	case err != nil:
		return doctor.Result{Status: doctor.Fail, Detail: "reading the core's lease state: " + err.Error()}
	case !ok:
		return doctor.Result{Status: doctor.Missing, Detail: "the core has not taken the lease on this data dir",
			Fix: "fridolin core promote, then start the core"}
	}
	switch st.State {
	case "lost":
		detail := "lost: " + st.Error
		if st.Holder != "" {
			detail += "; device " + st.Holder + " holds it"
		}
		return doctor.Result{Status: doctor.Fail, Detail: detail + ". This copy's channels are stopped",
			Fix: "stop this copy (fridolin core stop); to move the core back, export there and import and promote here"}
	case "released":
		return doctor.Result{Status: doctor.Warn, Detail: "released by a clean stop; the core is not running here",
			Fix: "fridolin core start"}
	case lease.StateUnverified:
		detail := "unverified: the lease store did not answer (" + st.Error + "). The channels keep running, because this device held the lease last"
		if st.RenewedAt != nil {
			detail += fmt.Sprintf("; last renewed %s ago", env.Time().Sub(*st.RenewedAt).Round(time.Second))
		}
		if cloud.IsCapExceeded(errors.New(st.Error)) {
			return doctor.Result{Status: doctor.Fail, Detail: detail, Fix: cloud.CapFix}
		}
		return doctor.Result{Status: doctor.Warn, Detail: detail, Fix: "check the core's log and its connection to the vault bucket; it keeps retrying"}
	}
	if st.RenewedAt == nil {
		return doctor.Result{Status: doctor.Fail, Detail: "the lease state has no renewal time"}
	}
	age := env.Time().Sub(*st.RenewedAt)
	renew := time.Duration(st.RenewSeconds) * time.Second
	stale := time.Duration(st.StaleSeconds) * time.Second
	detail := fmt.Sprintf("held by device %s (%s), renewed %s ago", st.DeviceID, st.Hostname, age.Round(time.Second))
	switch {
	case stale > 0 && age >= stale:
		return doctor.Result{Status: doctor.Fail, Detail: detail + ": stale, another device may take it",
			Fix: "is the core running? fridolin core status shows who holds the lease now"}
	case renew > 0 && age > 2*renew:
		return doctor.Result{Status: doctor.Warn, Detail: detail + ": renewals are late (the bucket unreachable, or the core stopped?)",
			Fix: "check the core's log and its connection to the vault bucket"}
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}

// checkContainerSandbox runs bubblewrap with a brain run's namespaces, as
// the bridge does before the first sandboxed run.
func checkContainerSandbox(ctx context.Context, env *doctor.Env) doctor.Result {
	if _, err := env.Stat(sandbox.BwrapPath); err != nil {
		return doctor.Result{Status: doctor.Fail, Detail: sandbox.BwrapPath + " is missing, so brains refuse every message",
			Fix: "use the fridolin-core image, which has bubblewrap"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := env.Run(ctx, sandbox.BwrapPath, "--unshare-user", "--unshare-pid", "--unshare-ipc",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--die-with-parent", "--", "/bin/true"); err != nil {
		return doctor.Result{Status: doctor.Fail, Detail: "bubblewrap cannot make a brain's namespaces here: " + err.Error(),
			Fix: "run the core with compose.yaml's security_opt (seccomp profile, systempaths=unconfined); on an AppArmor host also apparmor=unconfined (docs/CORE.md)"}
	}
	return doctor.Result{Status: doctor.OK, Detail: "brain runs get their own user, mount and PID namespaces"}
}
