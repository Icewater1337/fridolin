package nodeagent

// The scripts the node runs in a FrIDE instance's VM, each as
// `sbx exec fride-<name> sh -c SCRIPT fride-instance ARG…`, so that $1
// is the first argument. Each starts with a line naming it, which the
// tests' fake sbx goes by. FrIDE itself is the template's
// (/opt/fride/bin/fride); its state lives in ~/fride-companion in the VM,
// as the native companion's does on a node: home (FRIDE_HOME: its
// config, serve.log, fride.pid), workspaces and repos, the base folder
// the instance's repositories are cloned into.

// frideInstanceScriptPrefix starts each script's first line.
const frideInstanceScriptPrefix = "# fride-instance: "

// frideInstancePrepare makes FrIDE's folders, writes its config from
// stdin, else from the template's own (/opt/fride/companion.json, which
// the published template has), named for an instance (of one put back
// from history, only its base folders and workspace root), with the VM's
// own base folder and workspaces, marks ponytail's statusline offer as
// made and the workspaces as trusted by Claude Code.
const frideInstancePrepare = `# fride-instance: prepare
set -eu
c="$HOME/fride-companion"
h="$c/home"
mkdir -p "$h" "$c/workspaces" "$c/repos" "$HOME/.claude"
umask 077
if [ -s "$h/config.json" ]; then
	cp "$h/config.json" "$h/config.json.old"
else
	cat >"$h/config.json.old"
	if [ ! -s "$h/config.json.old" ]; then
		t=/opt/fride/companion.json
		[ -s "$t" ] || { echo "the template has no FrIDE config ($t), and the node gave none: fridolin node fride host setup" >&2; exit 2; }
		jq '.name = "fridolin companion (instance)"' "$t" >"$h/config.json.old"
	fi
fi
jq --arg r "$c/repos" --arg w "$c/workspaces" '.baseFolders = [$r] | .workspaceRoot = $w' "$h/config.json.old" >"$h/config.json.tmp"
mv "$h/config.json.tmp" "$h/config.json"
rm -f "$h/config.json.old"
touch "$HOME/.claude/.ponytail-statusline-nudged"
s="$HOME/.claude.json"
[ -s "$s" ] || echo '{}' >"$s"
jq --arg w "$c/workspaces" '.projects[$w].hasTrustDialogAccepted = true' "$s" >"$s.tmp"
mv "$s.tmp" "$s"
`

// frideInstanceGit sets the git identity FrIDE's agents commit with in
// workspaces of no identity: $1 the name, $2 the email, each only when
// given.
const frideInstanceGit = `# fride-instance: git
set -eu
[ -z "$1" ] || git config --global user.name "$1"
[ -z "$2" ] || git config --global user.email "$2"
`

// frideInstanceServe starts FrIDE unless it runs, and returns only when
// it ends: the node holds this exec open, which keeps the VM from
// stopping. FrIDE runs in a session of its own, with two forwarders:
// the VM's port $1 (where sbx publishes it) to FrIDE on the loopback,
// and the loopback's $2 to the node's webhook on the host. A run left
// over (its group in fride.pid) is ended first.
const frideInstanceServe = `# fride-instance: serve
set -u
c="$HOME/fride-companion"
h="$c/home"
f=/opt/fride/bin/fride
pidf="$h/fride.pid"
pid=$(cat "$pidf" 2>/dev/null || true)
case "$pid" in '' | *[!0-9]*) pid= ;; esac
if [ -n "$pid" ] && [ "$(cat "/proc/$pid/comm" 2>/dev/null)" = fride ]; then
	echo "FrIDE runs ($pid)."
else
	[ -z "$pid" ] || { kill -TERM -"$pid" 2>/dev/null; sleep 1; }
	[ -x "$f" ] || { echo "the template has no FrIDE: fridolin node fride host setup" >&2; exit 2; }
	ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
	[ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	[ -n "$ip" ] || { echo "the sandbox has no network address" >&2; exit 2; }
	cd "$h"
	FRIDE_HOME="$h" setsid sh -c 'socat "TCP-LISTEN:$1,bind=$2,fork,reuseaddr" "TCP:127.0.0.1:$1" &
socat "TCP-LISTEN:$3,bind=127.0.0.1,fork,reuseaddr" "TCP:host.docker.internal:$3" &
exec "$0" serve --under-fridolin --port "$1"' "$f" "$1" "$ip" "$2" >>"$h/serve.log" 2>&1 </dev/null &
	pid=$!
	echo "$pid" >"$pidf"
	echo "Started FrIDE ($pid) on $ip:$1."
fi
while kill -0 "$pid" 2>/dev/null; do sleep 2; done
echo "FrIDE ($pid) ended; its log is $h/serve.log in the sandbox." >&2
exit 1
`

// frideInstanceStop ends FrIDE and its forwarders: TERM to its group,
// KILL ten seconds later.
const frideInstanceStop = `# fride-instance: stop
h="$HOME/fride-companion/home"
pid=$(cat "$h/fride.pid" 2>/dev/null || true)
case "$pid" in '' | *[!0-9]*) exit 0 ;; esac
kill -TERM -"$pid" 2>/dev/null
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.5; i=$((i + 1)); done
kill -KILL -"$pid" 2>/dev/null
rm -f "$h/fride.pid"
`

// frideInstancePairCode prints FrIDE's pairing code as JSON.
const frideInstancePairCode = `# fride-instance: pair-code
FRIDE_HOME="$HOME/fride-companion/home" exec /opt/fride/bin/fride pair-companion --json
`

// frideInstanceLogin puts the Claude token on stdin into Claude Code's
// settings (env.CLAUDE_CODE_OAUTH_TOKEN, 0600): FrIDE starts its agents
// without CLAUDE_CODE_* variables, and Claude Code reads its settings'
// env itself. The token never is an argument.
const frideInstanceLogin = `# fride-instance: login
set -eu
umask 077
mkdir -p "$HOME/.claude"
f="$HOME/.claude/settings.json"
[ -s "$f" ] || echo '{}' >"$f"
jq --rawfile t /dev/stdin '.env.CLAUDE_CODE_OAUTH_TOKEN = ($t | rtrimstr("\n"))' "$f" >"$f.tmp"
mv "$f.tmp" "$f"
`

// frideInstanceCredential sets git's credential helper for the URL $1
// in the VM: it answers user $2 and the placeholder $3, which sbx's
// proxy swaps for the token on the way out. Neither is a secret.
const frideInstanceCredential = `# fride-instance: credential
set -eu
git config --global --replace-all "credential.$1.helper" "!f() { test \"\$1\" = get || exit 0; echo username=$2; echo password=$3; }; f"
`

// frideInstanceUncredential removes the helper for the URL $1.
const frideInstanceUncredential = `# fride-instance: uncredential
git config --global --unset-all "credential.$1.helper" || true
`

// frideInstanceClone clones the URL $1 into the base folder as $2,
// unless it is there; the credential helpers hand git the placeholders.
// It prints path=FOLDER and clone=have or clone=done.
const frideInstanceClone = `# fride-instance: clone
set -eu
r="$HOME/fride-companion/repos"
d="$r/$2"
echo "path=$d"
if [ -e "$d" ]; then echo clone=have; exit 0; fi
mkdir -p "$r"
rm -rf "$d.part"
if ! GIT_TERMINAL_PROMPT=0 git clone -q -- "$1" "$d.part"; then rm -rf "$d.part"; exit 1; fi
mv "$d.part" "$d"
echo clone=done
`

// frideInstanceRepoNew makes the repository $1 in the base folder: git
// init on main and a first commit, a README. It exits 3 when $1 is
// there already.
const frideInstanceRepoNew = `# fride-instance: repo-new
set -eu
d="$HOME/fride-companion/repos/$1"
if [ -e "$d" ]; then echo "$1 already exists in the base folder" >&2; exit 3; fi
mkdir -p "$d"
cd "$d"
if ! { git init -q --initial-branch=main && printf '# %s\n' "$1" >README.md && git add -- README.md && git commit -q --no-verify -m "Initial commit"; }; then
	cd /
	rm -rf "$d"
	exit 1
fi
echo "path=$d"
`

// frideInstanceGitHubCreate creates the repository $1 on GitHub from the
// base folder with gh ($2 is --private or --public) and pushes it; gh
// authenticates through sbx's github service.
const frideInstanceGitHubCreate = `# fride-instance: gh-create
set -eu
cd "$HOME/fride-companion/repos/$1"
GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1 gh repo create "$1" "$2" --source . --remote origin --push
`

// frideInstanceLogs prints the last $1 lines of FrIDE's log.
const frideInstanceLogs = `# fride-instance: logs
h="$HOME/fride-companion/home"
tail -n "$1" "$h/serve.log" 2>/dev/null || echo "FrIDE has no log yet."
`

// frideInstanceList prints the VM's home and which of the paths under
// it, the arguments, are there.
const frideInstanceList = `# fride-instance: list
echo "home=$HOME"
for p; do [ ! -e "$HOME/$p" ] || echo "have=$p"; done
`

// frideInstanceMkdirs makes the folders under the VM's home.
const frideInstanceMkdirs = `# fride-instance: mkdirs
set -eu
for p; do mkdir -p "$HOME/$p"; done
`

// frideInstanceOwn, as root, gives the paths under the home $1 to the
// home's owner, and removes the pid file of a FrIDE that ran elsewhere.
const frideInstanceOwn = `# fride-instance: own
set -eu
h=$1
shift
u=$(stat -c %u:%g "$h")
for p; do [ ! -e "$h/$p" ] || chown -R "$u" "$h/$p"; done
rm -f "$h/fride-companion/home"/fride.pid
`

// frideInstanceScripts are all the scripts, for the test that they are
// sh.
var frideInstanceScripts = []string{frideInstancePrepare, frideInstanceGit, frideInstanceServe, frideInstanceStop,
	frideInstancePairCode, frideInstanceLogin, frideInstanceCredential, frideInstanceUncredential, frideInstanceClone,
	frideInstanceRepoNew, frideInstanceGitHubCreate, frideInstanceLogs, frideInstanceList, frideInstanceMkdirs, frideInstanceOwn}

// frideInstanceScript is the argv of `sbx exec` for script with args.
func frideInstanceScript(script string, args ...string) []string {
	return append([]string{"sh", "-c", script, "fride-instance"}, args...)
}
