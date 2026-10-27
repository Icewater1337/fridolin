package nodeagent

// The scripts the node runs in the sandboxed FrIDE's VM, each as
// `sbx exec fride-sandbox sh -c SCRIPT fride-sandbox ARG…`, so that $1 is
// the first argument. Each starts with a line naming it, which the
// tests' fake sbx goes by. FrIDE lives in ~/fride-companion in the VM, as
// the native companion does on a node: src/fride, bin/fride, home
// (FRIDE_HOME: its config, serve.log, fride.pid) and workspaces.

// frideSandboxPrepare makes FrIDE's folders, writes its config from
// stdin unless one is there, marks ponytail's statusline offer as made
// and the workspaces as trusted by Claude Code.
const frideSandboxPrepare = `# fride-sandbox: prepare
set -eu
c="$HOME/fride-companion"
h="$c/home"
mkdir -p "$h" "$c/workspaces" "$c/bin" "$c/src" "$HOME/.claude"
umask 077
if [ -s "$h/config.json" ]; then cat >/dev/null; else cat >"$h/config.json"; fi
touch "$HOME/.claude/.ponytail-statusline-nudged"
s="$HOME/.claude.json"
[ -s "$s" ] || echo '{}' >"$s"
jq --arg w "$c/workspaces" '.projects[$w].hasTrustDialogAccepted = true' "$s" >"$s.tmp"
mv "$s.tmp" "$s"
`

// frideSandboxGit sets the git identity FrIDE's agents commit with:
// $1 the name, $2 the email, each only when given.
const frideSandboxGit = `# fride-sandbox: git
set -eu
[ -z "$1" ] || git config --global user.name "$1"
[ -z "$2" ] || git config --global user.email "$2"
`

// frideSandboxBuild clones FrIDE from $1 once, checks out commit $2 and
// builds it into bin/fride; a FrIDE that runs keeps the old binary until
// it starts again.
const frideSandboxBuild = `# fride-sandbox: build
set -eu
c="$HOME/fride-companion"
src="$c/src/fride"
mkdir -p "$c/bin" "$c/src"
if [ ! -d "$src/.git" ]; then rm -rf "$src"; git clone -q "$1" "$src"; fi
git -C "$src" remote set-url origin "$1"
git -C "$src" fetch -q origin
git -C "$src" cat-file -e "$2^{commit}" 2>/dev/null || git -C "$src" fetch -q origin "$2"
git -C "$src" checkout -q --detach "$2"
cd "$src"
CGO_ENABLED=0 go build -trimpath -o "$c/bin/fride.new" .
mv "$c/bin/fride.new" "$c/bin/fride"
echo "Built FrIDE $(git -C "$src" rev-parse HEAD)."
`

// frideSandboxServe starts FrIDE unless it runs, and returns only when
// it ends: the node holds this exec open, which keeps the VM from
// stopping. FrIDE runs in a session of its own, with two forwarders:
// the VM's port $1 (where sbx publishes it) to FrIDE on the loopback,
// and the loopback's $2 to the node's webhook on the host. A run left
// over (its group in fride.pid) is ended first.
const frideSandboxServe = `# fride-sandbox: serve
set -u
c="$HOME/fride-companion"
h="$c/home"
pidf="$h/fride.pid"
pid=$(cat "$pidf" 2>/dev/null || true)
case "$pid" in '' | *[!0-9]*) pid= ;; esac
if [ -n "$pid" ] && [ "$(cat "/proc/$pid/comm" 2>/dev/null)" = fride ]; then
	echo "FrIDE runs ($pid)."
else
	[ -z "$pid" ] || { kill -TERM -"$pid" 2>/dev/null; sleep 1; }
	[ -x "$c/bin/fride" ] || { echo "FrIDE is not built here: fridolin node fride sandbox update" >&2; exit 2; }
	ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
	[ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	[ -n "$ip" ] || { echo "the sandbox has no network address" >&2; exit 2; }
	cd "$h"
	FRIDE_HOME="$h" setsid sh -c 'socat "TCP-LISTEN:$1,bind=$2,fork,reuseaddr" "TCP:127.0.0.1:$1" &
socat "TCP-LISTEN:$3,bind=127.0.0.1,fork,reuseaddr" "TCP:host.docker.internal:$3" &
exec "$0" serve --under-fridolin --port "$1"' "$c/bin/fride" "$1" "$ip" "$2" >>"$h/serve.log" 2>&1 </dev/null &
	pid=$!
	echo "$pid" >"$pidf"
	echo "Started FrIDE ($pid) on $ip:$1."
fi
while kill -0 "$pid" 2>/dev/null; do sleep 2; done
echo "FrIDE ($pid) ended; its log is $h/serve.log in the sandbox." >&2
exit 1
`

// frideSandboxStop ends FrIDE and its forwarders: TERM to its group,
// KILL ten seconds later.
const frideSandboxStop = `# fride-sandbox: stop
h="$HOME/fride-companion/home"
pid=$(cat "$h/fride.pid" 2>/dev/null || true)
case "$pid" in '' | *[!0-9]*) exit 0 ;; esac
kill -TERM -"$pid" 2>/dev/null
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.5; i=$((i + 1)); done
kill -KILL -"$pid" 2>/dev/null
rm -f "$h/fride.pid"
`

// frideSandboxPairCode prints FrIDE's pairing code as JSON.
const frideSandboxPairCode = `# fride-sandbox: pair-code
FRIDE_HOME="$HOME/fride-companion/home" exec "$HOME/fride-companion/bin/fride" pair-companion --json
`

// frideSandboxLogin puts the Claude token on stdin into Claude Code's
// settings (env.CLAUDE_CODE_OAUTH_TOKEN, 0600): FrIDE starts its agents
// without CLAUDE_CODE_* variables, and Claude Code reads its settings'
// env itself. The token never is an argument.
const frideSandboxLogin = `# fride-sandbox: login
set -eu
umask 077
mkdir -p "$HOME/.claude"
f="$HOME/.claude/settings.json"
[ -s "$f" ] || echo '{}' >"$f"
jq --rawfile t /dev/stdin '.env.CLAUDE_CODE_OAUTH_TOKEN = ($t | rtrimstr("\n"))' "$f" >"$f.tmp"
mv "$f.tmp" "$f"
`

// frideSandboxStatus says, as key=value lines, whether the Claude login
// and the git identity are there, whether FrIDE runs, and its commit;
// it never prints the token.
const frideSandboxStatus = `# fride-sandbox: status
c="$HOME/fride-companion"
if jq -e '.env.CLAUDE_CODE_OAUTH_TOKEN | type == "string" and length > 0' "$HOME/.claude/settings.json" >/dev/null 2>&1; then echo login=yes; else echo login=no; fi
if [ -n "$(git config --global user.email 2>/dev/null)" ]; then echo git=yes; else echo git=no; fi
h="$c/home"
pid=$(cat "$h/fride.pid" 2>/dev/null || true)
if [ -n "$pid" ] && [ "$(cat "/proc/$pid/comm" 2>/dev/null)" = fride ]; then echo running=yes; else echo running=no; fi
echo "commit=$(git -C "$c/src/fride" rev-parse HEAD 2>/dev/null)"
`

// frideSandboxCredential sets git's credential helper for the URL $1 in
// the VM: it answers user $2 and the placeholder $3, which sbx's proxy
// swaps for the token on the way out. Neither is a secret.
const frideSandboxCredential = `# fride-sandbox: credential
set -eu
git config --global --replace-all "credential.$1.helper" "!f() { test \"\$1\" = get || exit 0; echo username=$2; echo password=$3; }; f"
`

// frideSandboxUncredential removes the helper for the URL $1.
const frideSandboxUncredential = `# fride-sandbox: uncredential
git config --global --unset-all "credential.$1.helper" || true
`

// frideSandboxScript is the argv of `sbx exec` for script with args.
func frideSandboxScript(script string, args ...string) []string {
	return append([]string{"sh", "-c", script, FrideSandboxName}, args...)
}
