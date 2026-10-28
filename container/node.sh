#!/bin/sh
# node.sh — make this machine a node of a fridolin core, natively
# (docs/NODES.md, "Joining a machine"): one agent per machine, which can
# run FrIDE instances, keep backups and become the core.
#
# The core's !join reply has the line to paste, with this script of the
# core's own version:
#
#   curl -fsSL <source>/container/node.sh | sh -s -- \
#     --core fridolin-core.TAILNET.ts.net:8766 --code CODE
#
# It says what it will do and asks before it changes anything. Then it
# puts the machine on the tailnet (installing Tailscale, and opening its
# sign-in link), waits until the core answers, fetches the core's fridolin
# for this machine from the core with the join code, checks it against
# the SHA-256 the core sends with it, installs it as
# ~/.local/lib/fridolin/fridolin (and ~/.local/bin/fridolin), and joins:
# fridolin node join CODE --core HOST:PORT, which installs and starts the
# node service (a launchd agent on macOS, a systemd user unit on Linux).
# It asks for no secret; the code works once and grants nothing until the
# owner approves the node.
#
# Options:
#   --core HOST:PORT   the core's node address (required)
#   --code CODE        the join code (else it asks, once the core answers)
#   --dir DIR          where fridolin goes (default ~/.local/lib/fridolin)
#   --yes              do not ask before starting
# Everything else goes to fridolin node join as it is, such as
# --root ~/Fridolin:rw, --no-exec, --allow PROG, --workstation,
# --name NAME or --no-service (fridolin node help lists them).
set -eu

core="" code="" yes=0 answer="" ts="" tmp=""
dir="${FRIDOLIN_INSTALL_DIR:-$HOME/.local/lib/fridolin}"

die() {
	printf 'node.sh: %s\n' "$*" >&2
	exit 1
}
say() { printf '\n==> %s\n' "$*"; }
ask() {
	# ask PROMPT VAR: a line from the terminal, also when the script came
	# through a pipe.
	printf '%s' "$1" >/dev/tty
	IFS= read -r "$2" </dev/tty || true
}
yes_to() {
	# yes_to PROMPT: whether the answer is yes; --yes answers it.
	[ "$yes" = 1 ] && return 0
	ask "$1 [y/N] " answer
	case "$answer" in y | Y | yes | YES) return 0 ;; esac
	return 1
}

# The script's own options; the rest stays in "$@" for node join, in
# order.
n=$#
while [ "$n" -gt 0 ]; do
	a=$1
	shift
	n=$((n - 1))
	case "$a" in
	--core | --code | --dir)
		[ "$n" -gt 0 ] || die "$a needs a value"
		v=$1
		shift
		n=$((n - 1))
		case "$a" in
		--core) core=$v ;;
		--code) code=$v ;;
		*) dir=$v ;;
		esac
		;;
	--yes) yes=1 ;;
	--box | --box-*) die "$a is for a node in a box: box.sh makes one (the core's !join reply has its line)" ;;
	-h | --help)
		sed -n '2,30p' "$0" 2>/dev/null || true
		exit 0
		;;
	*) set -- "$@" "$a" ;;
	esac
done
[ -n "$core" ] || die "--core is missing (the core's !join reply has the whole line)"
case "$core" in
*[!A-Za-z0-9._:-]* | :* | *:) die "--core $core is not HOST:PORT" ;;
*:*) ;;
*) die "--core $core has no port (HOST:8766)" ;;
esac
check_code() {
	case "$1" in
	fj-*) case "$1" in *[!a-z0-9-]*) return 1 ;; esac ;;
	*) return 1 ;;
	esac
}
[ -z "$code" ] || check_code "$code" || die "--code $code is not a join code (fj-…)"

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "this script is for macOS and Linux; on Windows, node.ps1 (the core's !join reply has its line)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "the core has fridolin for amd64 and arm64, not $(uname -m)" ;;
esac
# A shell under Rosetta says x86_64 on an Apple silicon Mac.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
	arch=arm64
fi
name=$(hostname 2>/dev/null || uname -n)
name=${name%%.*}
bin="$dir/fridolin"
service=""
noservice=0
for a in "$@"; do
	case "$a" in
	--no-service) noservice=1 ;;
	esac
done
if [ "$noservice" = 0 ]; then
	if [ "$os" = darwin ]; then
		service="the launchd agent ai.fridolin.node"
	else
		service="the systemd user unit fridolin-node"
	fi
fi

# lent_roots list|make ARGS...: the folders the --root flags among ARGS
# name that do not exist yet: list prints them, make makes them.
lent_roots() {
	act=$1
	shift
	prev=""
	for a in "$@"; do
		r=""
		case "$prev" in --root | -root) r=$a ;; esac
		case "$a" in --root=* | -root=*) r=${a#*=} ;; esac
		prev=$a
		[ -n "$r" ] || continue
		r=${r%:rw}
		r=${r%:ro}
		[ ! -d "$r" ] || continue
		if [ "$act" = make ]; then
			mkdir -p "$r" || die "could not make $r"
		else
			printf '%s\n' "$r"
		fi
	done
}
missing=$(lent_roots list "$@" | tr '\n' ' ')

find_tailscale() {
	if command -v tailscale >/dev/null 2>&1; then
		ts=tailscale
		return 0
	fi
	for c in /Applications/Tailscale.app/Contents/MacOS/Tailscale /opt/homebrew/bin/tailscale /usr/local/bin/tailscale; do
		if [ -x "$c" ]; then
			ts=$c
			return 0
		fi
	done
	return 1
}
tailscale_up() {
	"$ts" status >/dev/null 2>&1
}

if ! find_tailscale; then
	if [ "$os" = darwin ]; then
		if command -v brew >/dev/null 2>&1; then
			ts_plan="install it (brew install --cask tailscale), then sign this machine in (a link to open)"
		else
			ts_plan="install it from https://tailscale.com/download/mac (it waits for you), then sign this machine in (a link to open)"
		fi
	else
		ts_plan="install it with Tailscale's install script (curl -fsSL https://tailscale.com/install.sh | sh, which uses sudo), then sign this machine in (a link to open)"
	fi
elif tailscale_up; then
	ts_plan="installed and signed in"
else
	ts_plan="sign this machine in to your tailnet (a link to open)"
fi
existing=""
if [ -e "$bin" ]; then
	existing=$("$bin" version 2>/dev/null | head -n 1 || true)
	existing="it replaces the fridolin there (${existing:-of an unknown version}), kept as fridolin.prev"
fi

cat <<EOF
This makes $name a node of the fridolin core at $core, natively: one
agent on this machine, which can run FrIDE instances, keep backups and
become the core.

  1. Tailscale: $ts_plan
  2. fetch the core's fridolin for $os/$arch from the core with the join
     code, check its SHA-256 and install it as $bin
     (and ~/.local/bin/fridolin)${existing:+;
     $existing}
  3. join the core: fridolin node join CODE --core $core${*:+ $*}${service:+,
     which installs and starts the node service ($service)}${missing:+
  It makes the folders it lends first: $missing}
EOF
if [ -f "$HOME/Library/LaunchAgents/ai.fridolin.bridge.plist" ] || [ -f "$HOME/.config/systemd/user/fridolin.service" ]; then
	printf '\nnote: a fridolin bridge is installed on this machine (setup.sh). If it runs from %s, it runs the core'"'"'s version after its next restart; --dir puts the node elsewhere.\n' "$bin"
fi
printf '\nThe node serves nothing until you approve it on the core, and the code works once.\n'
yes_to "Continue?" || die "stopped; nothing was changed"
lent_roots make "$@"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

open_url() {
	case "$os" in
	darwin) open "$1" >/dev/null 2>&1 || true ;;
	*)
		if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] && command -v xdg-open >/dev/null 2>&1; then
			xdg-open "$1" >/dev/null 2>&1 &
		fi
		;;
	esac
}

say "Tailscale"
if ! find_tailscale; then
	if [ "$os" = darwin ]; then
		# The cask is tailscale-app in newer Homebrew, tailscale before.
		if ! { command -v brew >/dev/null 2>&1 && { brew install --cask tailscale || brew install --cask tailscale-app; }; }; then
			echo "Install Tailscale from https://tailscale.com/download/mac and open it once."
			open_url "https://tailscale.com/download/mac"
			ask "Press Enter once Tailscale is installed. " answer
		fi
		open -a Tailscale >/dev/null 2>&1 || true
		sleep 3
	else
		curl -fsSL https://tailscale.com/install.sh | sh || die "Tailscale's install script failed: see https://tailscale.com/download/linux"
	fi
	find_tailscale || die "tailscale is not found after installing it: open a new terminal and run the line again"
fi
if [ "$os" = darwin ] && ! tailscale_up; then
	# The app's command line answers only while the app runs.
	open -a Tailscale >/dev/null 2>&1 || true
	sleep 3
fi
if tailscale_up; then
	echo "This machine is on the tailnet."
else
	echo "Sign in with the account of the core's tailnet; tailscale up prints the link, and it opens here where it can."
	sudo=""
	if [ "$os" = linux ] && [ "$(id -u)" != 0 ]; then
		sudo=sudo
		sudo -v || die "tailscale up needs sudo on Linux"
	fi
	# tailscale up waits for the sign-in; meanwhile, open its link.
	$sudo "$ts" up >"$tmp/up" 2>&1 &
	pid=$!
	shown=""
	while ps -p "$pid" >/dev/null 2>&1; do
		if [ -z "$shown" ]; then
			shown=$(grep -Eo 'https://[^[:space:]]+' "$tmp/up" 2>/dev/null | head -n 1 || true)
			if [ -n "$shown" ]; then
				printf '\n  %s\n\n' "$shown"
				open_url "$shown"
			fi
		fi
		sleep 1
	done
	if ! wait "$pid"; then
		cat "$tmp/up" >&2
		die "tailscale up failed"
	fi
	tailscale_up || die "this machine is not on the tailnet yet: tailscale status says why"
	echo "Signed in."
fi

say "Waiting for the core at $core"
i=0
until [ "$(curl -s -o /dev/null --connect-timeout 5 -m 10 -w '%{http_code}' "http://$core/" 2>/dev/null || true)" != 000 ]; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || die "the core at $core does not answer. Is this machine on the core's tailnet, signed in with the same account (tailscale status lists fridolin-core)?"
	sleep 2
done
echo "The core answers."

if [ -z "$code" ]; then
	ask "Send !join to fridolin in your WhatsApp chat and type the code it sends: " code
	check_code "$code" || die "$code is not a join code (fj-…)"
fi

say "Fetching fridolin from the core"
# fetch URL FILE: the core's answer into FILE; stop with what the core
# said when it is not 200.
fetch() {
	status=$(curl -sS -o "$2" --connect-timeout 10 -w '%{http_code}' "$1") || die "fetching $1 failed"
	if [ "$status" != 200 ]; then
		die "the core answered $status: $(head -n 3 "$2" 2>/dev/null | cut -c 1-300)"
	fi
}
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		openssl dgst -sha256 -r "$1" | cut -d ' ' -f 1
	fi
}
query="os=$os&arch=$arch"
fetch "http://$core/join/$code/fridolin.sha256?$query" "$tmp/sha256"
want_sum="" want_size="" version=""
read -r want_sum want_size version <"$tmp/sha256" || true
case "$want_sum" in
*[!0-9a-f]* | "") die "the core sent no SHA-256" ;;
esac
[ "${#want_sum}" = 64 ] || die "the core sent no SHA-256"
fetch "http://$core/join/$code/fridolin?$query" "$tmp/fridolin"
got_size=$(wc -c <"$tmp/fridolin" | tr -d ' ')
got_sum=$(sha256 "$tmp/fridolin")
if [ "$got_size" != "$want_size" ] || [ "$got_sum" != "$want_sum" ]; then
	die "the fridolin from the core does not match the SHA-256 it sent; nothing was installed"
fi
echo "fridolin $version for $os/$arch, SHA-256 $got_sum"

say "Installing fridolin in $dir"
mkdir -p "$dir" "$HOME/.local/bin"
cp "$tmp/fridolin" "$bin.new"
chmod 755 "$bin.new"
if [ -f "$bin" ] && ! cmp -s "$bin.new" "$bin"; then
	cp -p "$bin" "$bin.prev"
	echo "The fridolin it replaces is $bin.prev."
fi
mv -f "$bin.new" "$bin"
link="$HOME/.local/bin/fridolin"
if [ "$(readlink "$link" 2>/dev/null || true)" != "$bin" ]; then
	if [ -f "$link" ] && [ ! -L "$link" ]; then
		mv -f "$link" "$link.prev"
		echo "$link was a fridolin of its own; it is $link.prev now."
	fi
	ln -sf "$bin" "$link"
fi
echo "$bin, and $link"
case ":$PATH:" in
*":$HOME/.local/bin:"*) ;;
*) echo "note: ~/.local/bin is not on your PATH; add it, or run $bin" ;;
esac

say "Joining the core"
"$bin" node join "$code" --core "$core" "$@" ||
	die "joining failed. A code works once, within 10 minutes: send !join again, then run the line again with the new code"

if [ "$os" = linux ] && [ "$noservice" = 0 ] && command -v loginctl >/dev/null 2>&1 &&
	[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || true)" != yes ]; then
	if [ "$yes" != 1 ] && yes_to "Keep the node running while you are logged out and after a reboot (sudo loginctl enable-linger $(id -un))?"; then
		sudo loginctl enable-linger "$(id -un)" || echo "note: lingering is still off: sudo loginctl enable-linger $(id -un)"
	else
		echo "note: the node runs only while you are logged in; sudo loginctl enable-linger $(id -un) keeps it running"
	fi
fi

say "Done"
cat <<EOF
The core tells you in WhatsApp that $name joined; reply !approve there
(or approve it on the dashboard's Nodes page).

Here: fridolin node status, and fridolin doctor --node. To remove the
node: fridolin node stop, fridolin node uninstall, then its state dir
(fridolin node status names it); !revoke on the core shuts it out.
EOF
