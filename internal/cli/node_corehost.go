package cli

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// coreHostFlags are `fridolin node core-host trust`'s.
type coreHostFlags struct {
	key, name string
}

const nodeCoreHostUsage = `fridolin node core-host — this machine as a core host (docs/NODES.md, core-host)

Usage:
  fridolin node core-host show       whether core-host is on, this machine's device id,
                                     key and fingerprint, and the targets it trusts
  fridolin node core-host on         offer core-host to the core: a machine the core
                                     may move to, which then holds every host secret
  fridolin node core-host off        stop offering it
  fridolin node core-host trust ID --key KEY [--name NAME]
                                     send host secrets to node ID: pin its device key,
                                     as fridolin node core-host show prints it there
  fridolin node core-host untrust ID stop sending host secrets to node ID

The settings live in core-host.json in the state dir, which only this
machine writes. Every change waits for the owner's approval on the core
(!approve ID, or fridolin nodes approve ID --commands) before it counts.
`

// nodeCoreHost runs `fridolin node core-host`.
func nodeCoreHost(pos []string, o coreHostFlags, dir string, pkg appPackage, stdout, stderr io.Writer) int {
	if len(pos) == 0 || pos[0] == "help" {
		fmt.Fprint(stdout, nodeCoreHostUsage)
		return 0
	}
	sub, args := pos[0], pos[1:]
	want := map[string]int{"show": 0, "on": 0, "off": 0, "trust": 1, "untrust": 1}
	n, ok := want[sub]
	if !ok || len(args) != n {
		fmt.Fprintf(stderr, "fridolin node core-host: unknown command or wrong arguments\n\n%s", nodeCoreHostUsage)
		return 2
	}
	if (o.key != "" || o.name != "") && sub != "trust" {
		fmt.Fprintln(stderr, "--key and --name go with trust")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "fridolin node core-host %s: %v\n", sub, err)
		return 1
	}
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		return fail(err)
	}
	key, err := nodeagent.LoadKey(dir)
	if err != nil {
		return fail(err)
	}
	ch, err := nodeagent.LoadCoreHost(dir)
	if err != nil {
		return fail(err)
	}
	if sub == "show" {
		writeCoreHostShow(stdout, cfg.Name, key, ch)
		return 0
	}
	if err := pkg.writeError(dir); err != nil {
		return fail(err)
	}
	id := nodeagent.DeviceID(key)
	switch sub {
	case "on":
		ch.Enabled = true
	case "off":
		ch.Enabled = false
	case "trust":
		target := args[0]
		if err := nodeproto.CheckDeviceID(target); err != nil {
			return fail(err)
		}
		if target == id {
			return fail(fmt.Errorf("%s is this machine", target))
		}
		pub, err := hex.DecodeString(strings.TrimSpace(o.key))
		if err != nil || len(pub) != ed25519.PublicKeySize {
			fmt.Fprintf(stderr, "fridolin node core-host trust needs --key: the device key in hex that fridolin node core-host show prints on %s\n", target)
			return 2
		}
		if got := nodeproto.DeviceIDFor(pub); got != target {
			return fail(fmt.Errorf("that key is the key of %s, not of %s: nothing was pinned", got, target))
		}
		ch.SendTo = slices.DeleteFunc(ch.SendTo, func(p nodeagent.CoreHostPin) bool { return p.ID == target })
		ch.SendTo = append(ch.SendTo, nodeagent.CoreHostPin{ID: target, PublicKey: hex.EncodeToString(pub), Name: o.name, PinnedAt: time.Now().UTC().Truncate(time.Second)})
		fmt.Fprintf(stdout, "Pinned %s with the device key fingerprint\n\n  %s\n\nCompare it with what fridolin node core-host show prints on that machine; if it differs, run fridolin node core-host untrust %s.\n", target, nodeproto.Fingerprint(pub), target)
	case "untrust":
		before := len(ch.SendTo)
		ch.SendTo = slices.DeleteFunc(ch.SendTo, func(p nodeagent.CoreHostPin) bool { return p.ID == args[0] })
		if len(ch.SendTo) == before {
			fmt.Fprintf(stdout, "This machine did not trust %s.\n", args[0])
			return 0
		}
	}
	if err := nodeagent.SaveCoreHost(dir, ch); err != nil {
		return fail(err)
	}
	switch {
	case !ch.Enabled:
		fmt.Fprintln(stdout, "core-host is off on this machine: it offers the core neither core.check nor host secrets.")
	default:
		fmt.Fprintf(stdout, "core-host is on (settings %s). The agent tells the core within a minute; the change counts once the owner approves it there: !approve %s, or fridolin nodes approve %s --commands.\n", ch.Hash()[:12], id, id)
	}
	return 0
}

// writeCoreHostShow prints this machine's core-host settings and device
// key: what a source machine pins with trust.
func writeCoreHostShow(w io.Writer, name string, key ed25519.PrivateKey, ch nodeagent.CoreHostConfig) {
	pub := key.Public().(ed25519.PublicKey)
	state := "off (fridolin node core-host on offers it)"
	if ch.Enabled {
		state = "on"
	}
	fmt.Fprintf(w, "Node: %s (%s)\ncore-host: %s\nDevice key: %s\nFingerprint: %s\n", name, nodeagent.DeviceID(key), state, hex.EncodeToString(pub), nodeproto.Fingerprint(pub))
	if len(ch.SendTo) == 0 {
		fmt.Fprintln(w, "Trusts no target: it sends host secrets nowhere.")
	} else {
		fmt.Fprintln(w, "Sends host secrets only to:")
		for _, p := range ch.SendTo {
			k, _ := p.Key()
			label := p.ID
			if p.Name != "" {
				label = p.Name + " (" + p.ID + ")"
			}
			fmt.Fprintf(w, "  %s, fingerprint %s, pinned %s\n", label, nodeproto.Fingerprint(k), p.PinnedAt.Format("2006-01-02"))
		}
	}
	nameFlag := ""
	if name != "" && !strings.ContainsAny(name, " \t'\"$`\\") {
		nameFlag = " --name " + name
	}
	fmt.Fprintf(w, "\nTo let another core host send its secrets here, run there:\n  fridolin node core-host trust %s --key %s%s\n", nodeagent.DeviceID(key), hex.EncodeToString(pub), nameFlag)
}
