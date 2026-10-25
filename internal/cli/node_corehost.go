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
                                     key and fingerprint, and the targets it pins
  fridolin node core-host off        never let the core move to this machine
  fridolin node core-host on         let it again (the default)
  fridolin node core-host trust ID --key KEY [--name NAME]
                                     pin node ID's device key, as fridolin node
                                     core-host show prints it there (optional)
  fridolin node core-host untrust ID remove that pin

Every node is a core host unless core-host is off there: the owner's vote
on the first !movecore to it, in WhatsApp, is what lets it hold the secrets,
and a source then sends them to the device key the core approved at its
join. A pin is only needed to move without the core's word (a pinned key
wins over the one the core names). The settings live in core-host.json in
the state dir, which only this machine writes.
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
		fmt.Fprintln(stdout, "core-host is off on this machine: the core cannot move here, and it neither reports for a move nor takes or sends host secrets. The agent tells the core within a minute.")
	default:
		fmt.Fprintf(stdout, "core-host is on (%s): the core may move here once the owner votes for it (!movecore %s in WhatsApp). The agent tells the core within a minute.\n", id, cfg.Name)
	}
	return 0
}

// writeCoreHostShow prints this machine's core-host settings and device
// key: what a source machine pins with trust.
func writeCoreHostShow(w io.Writer, name string, key ed25519.PrivateKey, ch nodeagent.CoreHostConfig) {
	pub := key.Public().(ed25519.PublicKey)
	state := "off (fridolin node core-host on turns it on again)"
	if ch.Enabled {
		state = "on"
	}
	fmt.Fprintf(w, "Node: %s (%s)\ncore-host: %s\nDevice key: %s\nFingerprint: %s\n", name, nodeagent.DeviceID(key), state, hex.EncodeToString(pub), nodeproto.Fingerprint(pub))
	if len(ch.SendTo) == 0 {
		fmt.Fprintln(w, "Pins no target: it sends host secrets to the device key the core names for a target the owner confirmed.")
	} else {
		fmt.Fprintln(w, "Pinned targets (their pin wins over the key the core names):")
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
	fmt.Fprintf(w, "\nNo pin is needed. To pin this machine on another core host anyway, run there:\n  fridolin node core-host trust %s --key %s%s\n", nodeagent.DeviceID(key), hex.EncodeToString(pub), nameFlag)
}
