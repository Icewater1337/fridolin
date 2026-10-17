package main

import (
	"context"
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
	// desktopChild is a helper that opens your browser for a sign-in:
	// rclone connecting Google Drive, `plaud login` (see withDesktop).
	desktopChild
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
	{"TMUX_TMPDIR", anyChild},  // where tmux keeps its socket: the viewer must start the server `tmux attach` finds

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

	// Your desktop session, for a sign-in that opens your browser: on Linux
	// rclone and the Plaud CLI call xdg-open, which finds the session
	// through these. macOS's open needs none of them.
	{"DISPLAY", desktopChild},                  // the X11 display
	{"WAYLAND_DISPLAY", desktopChild},          // the Wayland display
	{"XAUTHORITY", desktopChild},               // the cookie that lets a program use DISPLAY
	{"XDG_RUNTIME_DIR", desktopChild},          // where the Wayland and D-Bus sockets live
	{"DBUS_SESSION_BUS_ADDRESS", desktopChild}, // the session bus, through which the desktop opens a URL
	{"XDG_CURRENT_DESKTOP", desktopChild},      // which desktop's opener xdg-open asks
	{"KDE_SESSION_VERSION", desktopChild},      // which opener xdg-open runs on KDE (kde-open or kde-open5)
	{"XDG_DATA_DIRS", desktopChild},            // where the desktop finds installed apps, snap and Flatpak browsers among them
	{"XDG_CONFIG_DIRS", desktopChild},          // where it finds the system-wide default apps (mimeapps.list)
	{"BROWSER", desktopChild},                  // a browser you chose over the desktop's

	// Settings of the tools in tools/ that brains run (docs/CONFIGURATION.md).
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

// desktopKey marks a context whose helper runs open your browser.
type desktopKey struct{}

// withDesktop marks ctx as a sign-in that opens your browser: the helper
// programs run on it get desktopChild's variables (see helperScope).
func withDesktop(ctx context.Context) context.Context {
	return context.WithValue(ctx, desktopKey{}, true)
}

// helperScope is the scope of a helper program, such as rclone, run on ctx.
func helperScope(ctx context.Context) childScope {
	if ctx.Value(desktopKey{}) != nil {
		return desktopChild
	}
	return anyChild
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
