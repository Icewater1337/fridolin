package nodeproto

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Programs the core adds to a node's exec allowlist (docs/NODES.md,
// "Programs from the core"). A node opts folders in, locally, with
// exec.accept_from_core. When the owner approves a command agent, the
// core sends the node the whole list of programs its approved agents run
// there (exec.programs), signed with the core's update key, which the node
// pinned. The node keeps each program that resolves, symlinks and all, to
// an executable file inside one of those folders and is no shell or
// interpreter, as a rule of its exact arguments, and declares it; a
// program no longer listed goes. The core approves what the node kept,
// because the owner approved the agents that run it.

// MethodExecPrograms replaces the programs the core added (ExecProgramsParams).
const MethodExecPrograms = "exec.programs"

// MaxCorePrograms bounds the programs the core may add to one node.
const MaxCorePrograms = 32

// ExecProgram is one program the core asks a node to allow: Name with
// exactly Args, for the agents that run it.
type ExecProgram struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
	// Agents are the approved agents that run it, as brain/name.
	Agents []string `json:"agents,omitempty"`
}

// Rule is the exec rule of p.
func (p ExecProgram) Rule() ExecRule {
	args := p.Args
	if args == nil {
		args = []string{}
	}
	return ExecRule{Name: p.Name, Args: args, Exact: true, Core: true}
}

// ExecProgramsParams is the list of programs the core added to the node,
// all of them: what it lists replaces what the core added before. Seq
// grows with every list the core signs; the node refuses one no newer
// than the last it took.
type ExecProgramsParams struct {
	Node      string        `json:"node"`
	Seq       int64         `json:"seq"`
	Programs  []ExecProgram `json:"programs"`
	Signature []byte        `json:"signature"`
}

// Check reports what is wrong with p, its signature aside.
func (p ExecProgramsParams) Check() error {
	switch {
	case p.Node == "":
		return errors.New("exec.programs: no node")
	case p.Seq <= 0:
		return errors.New("exec.programs: no seq")
	case len(p.Programs) > MaxCorePrograms:
		return fmt.Errorf("exec.programs: at most %d programs", MaxCorePrograms)
	case len(p.Signature) != ed25519.SignatureSize:
		return errors.New("exec.programs: no signature")
	}
	// Each program is checked on its own: one the node refuses is
	// refused alone.
	for _, pr := range p.Programs {
		if len(pr.Agents) > 64 {
			return fmt.Errorf("exec.programs: %s: at most 64 agents", pr.Name)
		}
	}
	return nil
}

// ExecProgramsMessage is what the core signs: the node it is for, the
// seq and the list, so that a list for one node or an old one cannot be
// played to another or again.
func ExecProgramsMessage(node string, seq int64, programs []ExecProgram) []byte {
	if programs == nil {
		programs = []ExecProgram{}
	}
	body, _ := json.Marshal(struct {
		Node     string        `json:"node"`
		Seq      int64         `json:"seq"`
		Programs []ExecProgram `json:"programs"`
	}{node, seq, programs})
	return append([]byte("fridolin exec.programs v1\n"), body...)
}

// SignExecPrograms signs a list with the core's update key.
func SignExecPrograms(key ed25519.PrivateKey, node string, seq int64, programs []ExecProgram) []byte {
	return ed25519.Sign(key, ExecProgramsMessage(node, seq, programs))
}

// VerifyExecPrograms checks the signature of p with the core's public key.
func VerifyExecPrograms(pub []byte, p ExecProgramsParams) bool {
	if len(pub) != ed25519.PublicKeySize || len(p.Signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), ExecProgramsMessage(p.Node, p.Seq, p.Programs), p.Signature)
}

// ExecProgramsResult is what the node kept of a list, and what it refused
// and why.
type ExecProgramsResult struct {
	Accepted []ExecProgram    `json:"accepted"`
	Refused  []RefusedProgram `json:"refused"`
}

// RefusedProgram is a program a node would not allow.
type RefusedProgram struct {
	ExecProgram
	Why string `json:"why"`
}

// interpreterRE are the programs that run code or other programs their
// arguments name: the core never adds one, since with arguments of the
// core's choosing it would run anything.
var interpreterRE = regexp.MustCompile(`^(python|pythonw|python\d+(\.\d+)*|pypy\d*|node|nodejs|deno|bun|perl\d*(\.\d+)*|ruby\d*(\.\d+)*|irb|php\d*(\.\d+)*|lua\d*(\.\d+)*|luajit|tclsh\d*(\.\d+)*|wish\d*(\.\d+)*|expect|r|rscript|julia|java|jshell|groovy|scala|kotlin|swift|dotnet|awk|gawk|mawk|nawk|sed|gsed|find|git|make|gmake|docker|podman|kubectl|ssh|scp|rsync|curl|wget|nc|ncat|socat|open|launchctl|systemctl|crontab|at|screen|tmux|vim?|nvim|emacs|less|more|man|tar|zip|unzip|time|timeout|nice|ionice|chroot|strace|ltrace|gdb|lldb|watch|parallel|eval|exec|start|runas|rundll32|regsvr32|msiexec|schtasks|wmic|certutil|bitsadmin|forfiles|conhost)$`)

// IsInterpreter reports whether name, a program name or path in any OS's
// spelling, is a shell or a program that runs other code for its caller:
// the core never adds one to a node's allowlist.
func IsInterpreter(name string) bool {
	if IsShell(name) {
		return true
	}
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	for _, ext := range []string{".bat", ".cmd", ".ps1", ".vbs", ".js", ".msc"} {
		if strings.HasSuffix(base, ext) {
			// Windows runs these through an interpreter.
			return true
		}
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".com")
	return interpreterRE.MatchString(base)
}
