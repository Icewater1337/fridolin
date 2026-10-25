#!/bin/sh
# Entry point of the fridolin-fride image (container/fride/Dockerfile), run
# by tini: FrIDE's companion for the node of the same box (docs/NODES.md,
# "Node in a box").
#
# FrIDE answers on 127.0.0.1 only and checks that requests are addressed
# there, and it posts its news to a loopback webhook; the node's agent
# insists on loopback addresses for both too. So each side forwards: here
# the box network's FRIDE_PORT reaches FrIDE on this container's
# loopback, and the loopback webhook port reaches the node service's; the
# agent does the same in the node container. Both see 127.0.0.1, and the
# two containers share no network namespace, so FrIDE never reaches what
# listens on the node's loopback (the core's ports, once the box is the
# core).
#
#   serve (default)      start FrIDE
#   set-claude-token     store the token `claude setup-token` printed
#                        (sk-ant-oat…), read from stdin, in the home volume
#                        (0600); anything else, such as the browser's
#                        sign-in code, is refused
#   anything else        run as a command (fride doctor, claude, git, …)
set -eu

: "${FRIDE_HOME:=$HOME/fride}"
: "${FRIDE_PORT:=17840}"
: "${FRIDE_WEBHOOK_PORT:=17841}"
# The node service's name on the box network.
: "${FRIDOLIN_NODE_HOST:=node}"
token_file="$HOME/.config/fridolin-box/claude-token"
# UTF-8's byte order mark, which Windows PowerShell can put before what it
# pipes.
bom=$(printf '\357\273\277')

# token_problem TOKEN says why TOKEN is not one `claude setup-token`
# prints (sk-ant-oat, then letters, digits, - and _); nothing when it is.
token_problem() {
	case "$1" in
	*'#'*) echo "it is the sign-in code from the browser, not the token claude setup-token prints after it (under \"Your OAuth token\")" ;;
	sk-ant-oat*[!A-Za-z0-9_-]*) echo "it has characters a token never has (copied with other text?)" ;;
	sk-ant-oat?*) ;;
	*) echo "it does not start with sk-ant-oat, as a token from claude setup-token does" ;;
	esac
}

case "${1:-serve}" in
set-claude-token)
	umask 077
	mkdir -p "$(dirname "$token_file")"
	token=""
	IFS= read -r token || true
	# Windows PowerShell can start what it pipes with a byte order mark and
	# ends it with a carriage return, and a copy from a terminal can bring
	# spaces: none of them is part of a token.
	token=$(printf '%s' "${token#"$bom"}" | tr -d '\r\t ')
	if [ -z "$token" ]; then
		echo "fride set-claude-token: no token on stdin (run claude setup-token and paste the token it prints)" >&2
		exit 2
	fi
	problem=$(token_problem "$token")
	if [ -n "$problem" ]; then
		echo "fride set-claude-token: nothing stored: $problem" >&2
		exit 2
	fi
	printf '%s\n' "$token" >"$token_file.tmp"
	mv "$token_file.tmp" "$token_file"
	echo "Stored the Claude token. It counts from the service's next start: docker compose restart fride"
	exit 0
	;;
serve) ;;
*) exec "$@" ;;
esac

mkdir -p "$FRIDE_HOME" "$HOME/repos" "$HOME/.claude"
[ -e "$FRIDE_HOME/config.json" ] || cp /opt/fride/companion.json "$FRIDE_HOME/config.json"
# ponytail's offer to change the statusline is taken as made (docs/NODES.md,
# "Ponytail for every agent"); /workspaces is trusted for Claude Code, as
# the owner confirms it once on a native node.
touch "$HOME/.claude/.ponytail-statusline-nudged"
settings="$HOME/.claude.json"
[ -s "$settings" ] || echo '{}' >"$settings"
jq '.projects["/workspaces"].hasTrustDialogAccepted = true' "$settings" >"$settings.tmp"
mv "$settings.tmp" "$settings"

# The starter repositories (FRIDE_STARTER_REPOS, git URLs the image was
# built with), once; FrIDE starts without them when they cannot be cloned.
for url in ${FRIDE_STARTER_REPOS:-}; do
	repo=$(basename "$url" .git)
	[ -d "$HOME/repos/$repo" ] && continue
	git clone -q "$url" "$HOME/repos/$repo" ||
		echo "note: cloning $repo failed; FrIDE starts without it" >&2
done

if [ -s "$token_file" ]; then
	# A token stored before set-claude-token dropped what Windows
	# PowerShell adds may still carry a byte order mark or carriage return.
	CLAUDE_CODE_OAUTH_TOKEN=$(tr -d '\r' <"$token_file")
	CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN#"$bom"}
	export CLAUDE_CODE_OAUTH_TOKEN
	problem=$(token_problem "$CLAUDE_CODE_OAUTH_TOKEN")
	if [ -n "$problem" ]; then
		echo "note: the stored Claude token will not sign in: $problem. Run claude setup-token, then pipe the token into: docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token" >&2
	fi
else
	echo "note: no Claude token yet: run claude setup-token, then pipe it into: docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token" >&2
fi
if [ -z "$(git config --global user.email || true)" ]; then
	echo "note: no git identity yet, so an agent's first commit stops at a question: docker compose exec fride git config --global user.email you@example.com (and user.name)" >&2
fi

ip=$(hostname -i | awk '{print $1}')
socat "TCP-LISTEN:$FRIDE_PORT,bind=$ip,fork,reuseaddr" "TCP:127.0.0.1:$FRIDE_PORT" &
socat "TCP-LISTEN:$FRIDE_WEBHOOK_PORT,bind=127.0.0.1,fork,reuseaddr" "TCP:$FRIDOLIN_NODE_HOST:$FRIDE_WEBHOOK_PORT" &
exec fride serve --under-fridolin --port "$FRIDE_PORT"
