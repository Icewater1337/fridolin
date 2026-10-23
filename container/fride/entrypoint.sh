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
#   set-claude-token     store the token `claude setup-token` printed, read
#                        from stdin, in the home volume (0600)
#   anything else        run as a command (fride doctor, claude, git, …)
set -eu

: "${FRIDE_HOME:=$HOME/fride}"
: "${FRIDE_PORT:=17840}"
: "${FRIDE_WEBHOOK_PORT:=17841}"
# The node service's name on the box network.
: "${FRIDOLIN_NODE_HOST:=node}"
token_file="$HOME/.config/fridolin-box/claude-token"

case "${1:-serve}" in
set-claude-token)
	umask 077
	mkdir -p "$(dirname "$token_file")"
	token=""
	IFS= read -r token || true
	if [ -z "$token" ]; then
		echo "fride set-claude-token: no token on stdin (run claude setup-token and paste what it prints)" >&2
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
[ -e "$FRIDE_HOME/config.json" ] || cp /opt/fride/config.json "$FRIDE_HOME/config.json"
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
	CLAUDE_CODE_OAUTH_TOKEN=$(cat "$token_file")
	export CLAUDE_CODE_OAUTH_TOKEN
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
