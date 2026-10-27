package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// sandboxMinFreeMB is the free memory below which doctor warns: a
// running sandbox holds 1 to 1.4 GB on the host.
const sandboxMinFreeMB = 2048

// sandboxChecks are doctor --node's checks of Docker Sandboxes, from one
// look at sbx (look). They apply where sbx is installed and not turned
// off; before setup only the first two say anything.
func sandboxChecks(look func() nodeagent.SandboxStatus) []doctor.Check {
	if look == nil {
		return nil
	}
	installed := func(*doctor.Env) bool { s := look(); return s.Sbx != "" && !s.Off }
	answers := func(e *doctor.Env) bool { return installed(e) && look().Err == nil }
	setUp := func(e *doctor.Env) bool { return answers(e) && look().SetUp() }
	res := func(ok bool, okDetail, bad, fix string, warn bool) doctor.Result {
		if ok {
			return doctor.Result{Status: doctor.OK, Detail: okDetail}
		}
		st := doctor.Fail
		if warn {
			st = doctor.Warn
		}
		return doctor.Result{Status: st, Detail: bad, Fix: fix}
	}
	check := func(id, title string, applies func(*doctor.Env) bool, run func(s nodeagent.SandboxStatus) doctor.Result) doctor.Check {
		return doctor.Check{ID: id, Title: title, Applies: applies, Run: func(context.Context, *doctor.Env) doctor.Result { return run(look()) }}
	}
	return []doctor.Check{
		check("node-sandbox-sbx", "Docker Sandboxes (sbx)", installed, func(s nodeagent.SandboxStatus) doctor.Result {
			if s.Err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: s.Err.Error(), Fix: "sbx diagnose"}
			}
			v := s.Info.Version
			return res(v == nodeagent.TestedSbxVersion, v+" at "+s.Sbx, fmt.Sprintf("%s at %s; fridolin is tested with %s", v, s.Sbx, nodeagent.TestedSbxVersion), "brew upgrade sbx, or install "+nodeagent.TestedSbxVersion, true)
		}),
		check("node-sandbox-setup", "Docker Sandboxes set up for fridolin", answers, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.SetUp(), "template "+s.Info.Template, nodeagent.ErrSandboxNotSetUp.Error()+" (until then the node does not offer them)", "fridolin node sandbox setup --repo <a fridolin checkout>", true)
		}),
		check("node-sandbox-daemon", "sbx's daemon", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.Info.Daemon, "answers", "does not answer", "sbx daemon start; sbx diagnose", false)
		}),
		check("node-sandbox-login", "sbx signed in to Docker", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.Info.SignedIn, "signed in", "not signed in", "sbx login", false)
		}),
		check("node-sandbox-settings", "sbx's global settings are fridolin's", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.Info.Settings, "network policy deny-all, SSH agent forwarding and the browser off", "not as fridolin sets them", "fridolin node sandbox setup", false)
		}),
		check("node-sandbox-template", "the sandbox template in sbx", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			gone := strings.Contains(s.Info.Problem, "no longer has the template")
			return res(!gone, s.Info.Template, s.Info.Problem, "fridolin node sandbox setup", false)
		}),
		check("node-sandbox-bin", "the linux fridolin for the sandboxes", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.Info.Bin != "", s.Info.Bin+" (the core replaces it when its version differs)", "none yet", "approve the capability on the core, which then sends it; or fridolin node sandbox setup --bin PATH", true)
		}),
		check("node-sandbox-claude", "the Claude token for sandboxed runs", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			return res(s.ClaudeToken, "in the host's secret store", "not in the host's secret store: Claude brains fall back to bubblewrap", "fridolin core secrets set claude.oauth.token (claude setup-token makes one)", true)
		}),
		check("node-sandbox-codex", "the Codex login in sbx", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			has := slices.Contains(s.Info.Providers, nodeproto.SandboxProviderCodex)
			return res(has, "sbx holds the ChatGPT login", "none: Codex brains fall back to bubblewrap", "sbx secret set openai --oauth", true)
		}),
		check("node-sandbox-memory", "free memory for sandboxes", setUp, func(s nodeagent.SandboxStatus) doctor.Result {
			mb := s.Info.FreeMemoryMB
			return res(mb >= sandboxMinFreeMB, fmt.Sprintf("%d MB free", mb), fmt.Sprintf("%d MB free; a running sandbox takes 1 to 1.4 GB", mb), "close what you do not need, or give brains less memory", true)
		}),
	}
}
