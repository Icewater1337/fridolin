package nodeproto

import (
	"fmt"
	"strings"
)

// Plain says what c lets the core do on the node, one short sentence per
// family, in words the owner reads in an approval alert and on the Nodes
// page: what approving it means, not how it is spelled in the protocol.
// None of them is empty; nothing offered is no sentence at all.
func (c Capabilities) Plain() []string {
	var out []string
	if c.Exec != nil && len(c.Exec.Allow) > 0 {
		names := make([]string, len(c.Exec.Allow))
		for i, r := range c.Exec.Allow {
			names[i] = r.Name
		}
		out = append(out, "run these programs there when asked: "+strings.Join(names, ", "))
	}
	if c.FS != nil && len(c.FS.Roots) > 0 {
		var read, write []string
		for _, r := range c.FS.Roots {
			if r.Write {
				write = append(write, r.Path)
			} else {
				read = append(read, r.Path)
			}
		}
		if len(read) > 0 {
			out = append(out, "read the files in "+strings.Join(read, ", "))
		}
		if len(write) > 0 {
			out = append(out, "read and change the files in "+strings.Join(write, ", "))
		}
	}
	if c.Host {
		out = append(out, "report its system details (OS, disk, memory)")
	}
	if c.Notify {
		out = append(out, "show notifications on its screen")
	}
	if c.Fride != nil {
		out = append(out, "run FrIDE coding tasks on this machine — that means it can run any command there")
	}
	if c.AndroidBuild != nil {
		out = append(out, "build Android apps there")
	}
	if c.Backup != nil {
		out = append(out, "keep Fridolin's encrypted backups in folders there")
	}
	if c.Update != nil {
		out = append(out, "update Fridolin there to the core's version")
	}
	if c.Agent != nil {
		out = append(out, "run Claude Code or Codex sessions in "+folders(c.Agent.Roots)+" — they can run any command there")
	}
	if c.Terminal != nil {
		out = append(out, "open a terminal in "+folders(c.Terminal.Roots)+" — anything you could type there")
	}
	if c.CoreHost != nil {
		out = append(out, CoreHostPlain)
	}
	return out
}

// CoreHostPlain is the sentence of Plain for core-host.
const CoreHostPlain = "hold all of Fridolin's secrets, so the core can move there"

func folders(roots []string) string {
	switch len(roots) {
	case 0:
		return "no folder yet"
	case 1, 2, 3:
		return strings.Join(roots, ", ")
	}
	return fmt.Sprintf("%s and %d more folders", strings.Join(roots[:2], ", "), len(roots)-2)
}
