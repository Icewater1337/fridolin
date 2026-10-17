package main

import (
	"os"
	"slices"
	"strings"
)

// childScope says which of the programs the bridge starts get a variable.
type childScope uint8

const (
	// anyChild is any program the bridge starts with childEnv.
	anyChild childScope = iota
	// claudeChild is a Claude CLI run: a brain, and the scripts and tools it
	// runs, which inherit the CLI's environment.
	claudeChild
)

// childEnvVar names one variable of the bridge's environment that a program
// of scope gets, or, ending in "*", every variable with that prefix.
type childEnvVar struct {
	name  string
	scope childScope
}

// childEnvAllowlist is what the programs the bridge starts with childEnv
// see of its own environment, along with the names in
// child_env_passthrough. The rest stays with the bridge: whatever the
// service file or the shell that started it holds would otherwise reach
// every brain, where anyone who may message one can ask it to run `env`.
// That keeps a variable from being handed over unasked, no more: a brain
// that goes looking can still read the bridge's environment (see "What
// reaches brains" in docs/CONFIGURATION.md). The bridge adds its own
// variables on top (see childEnv).
var childEnvAllowlist = []childEnvVar{
	// What any program expects to find.
	{"HOME", anyChild},    // where programs keep their settings; the CLI's login lives under it
	{"USER", anyChild},    // who runs the program, for the tools that ask
	{"LOGNAME", anyChild}, // the same, under the name older tools ask for
	{"PATH", anyChild},    // finds what a program runs by name; the sandbox profile reasons about this PATH
	{"SHELL", anyChild},   // the shell the CLI's Bash tool starts
	{"TMPDIR", anyChild},  // the per-user temp folder on macOS, which the sandbox lets brains write
	{"LANG", anyChild},    // language and character set, so non-ASCII text and file names survive
	{"LC_*", anyChild},    // the same per category (LC_ALL, LC_CTYPE, ...)
	{"TERM", anyChild},    // the terminal type, set only when the bridge was started in one
	{"TZ", anyChild},      // a timezone other than the system's

	// Programs behind a proxy, or a firewall that inspects TLS.
	{"HTTPS_PROXY", anyChild},         // the proxy for https URLs
	{"https_proxy", anyChild},         // the same in lower case, which curl and Python prefer
	{"HTTP_PROXY", anyChild},          // the proxy for http URLs
	{"http_proxy", anyChild},          // the same in lower case
	{"NO_PROXY", anyChild},            // hosts to reach without the proxy
	{"no_proxy", anyChild},            // the same in lower case
	{"NODE_EXTRA_CA_CERTS", anyChild}, // extra CA certificates for Node programs, the Claude CLI among them
	{"SSL_CERT_FILE", anyChild},       // a CA bundle for Go, Python and OpenSSL programs
	{"SSL_CERT_DIR", anyChild},        // a folder of CA certificates for the same

	{"DO_NOT_TRACK", anyChild}, // the telemetry opt-out many tools honour

	// The Claude CLI's own settings.
	//
	// CLAUDE_CODE_OAUTH_TOKEN logs it in; CLAUDE_CODE_ENTRYPOINT,
	// CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST and CLAUDE_CODE_USE_BEDROCK say
	// how it runs and which provider it uses.
	{"CLAUDE_CODE_*", claudeChild},
	// ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN log it in; ANTHROPIC_BASE_URL
	// points it at a gateway.
	{"ANTHROPIC_*", claudeChild},
	{"CLAUDE_CONFIG_DIR", claudeChild},       // its login and state, when they are not in ~/.claude
	{"DISABLE_AUTOUPDATER", claudeChild},     // its update check; sandboxed runs set it to 1 whatever this says (see invoke)
	{"DISABLE_TELEMETRY", claudeChild},       // its usage telemetry, if you turned it off
	{"DISABLE_ERROR_REPORTING", claudeChild}, // its error reports, the same

	// Settings of the tools in tools/ that brains run (docs/CONFIGURATION.md).
	{"BRIDGE_DATA_DIR", claudeChild},     // the data dir, when it is not ~/.fridolin
	{"BRIDGE_DB_PATH", claudeChild},      // the database, when it is not in the data dir
	{"CALENDAR_TZ", claudeChild},         // calendar_tool.py's timezone
	{"TRANSCRIBE_MODEL", claudeChild},    // transcribe.py's model
	{"TRANSCRIBE_LANGUAGE", claudeChild}, // transcribe.py's language
}

// childEnv is the environment for a program of scope: the variables of the
// bridge's environment that childEnvAllowlist gives it or passthrough
// (child_env_passthrough) names, then extra (NAME=value), which the bridge
// sets on purpose and which replace a variable of the same name.
func childEnv(scope childScope, passthrough []string, extra ...string) []string {
	set := make(map[string]bool, len(extra))
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !set[name] && (childGets(scope, name) || slices.Contains(passthrough, name)) {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// childGets reports whether childEnvAllowlist gives a program of scope the
// variable name.
func childGets(scope childScope, name string) bool {
	for _, v := range childEnvAllowlist {
		if v.scope != anyChild && v.scope != scope {
			continue
		}
		if prefix, ok := strings.CutSuffix(v.name, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == v.name {
			return true
		}
	}
	return false
}

// validEnvName reports whether name can be an environment variable's name
// in child_env_passthrough: letters, digits and underscores, not starting
// with a digit. "=" or a NUL byte would make the entry another variable or
// none at all.
func validEnvName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, c := range name {
		if c != '_' && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z') && !('0' <= c && c <= '9') {
			return false
		}
	}
	return true
}
