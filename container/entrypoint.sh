#!/bin/sh
# Entry point of the fridolin-core image (container/Dockerfile), run by tini.
#
# The volume at $FRIDOLIN_HOME (/data) is the data dir, and the config is
# $FRIDOLIN_CONFIG (/data/config.json) in it. Before the bridge runs on a
# new, empty volume, it gets a starter config whose data_dir is the volume,
# so the bridge never falls back to ~/.fridolin inside the image, which is
# not kept. Other commands find the volume as it is: `core import` wants
# it empty. Then the arguments run as a fridolin command: `run
# --container` by default, or whatever `docker run IMAGE ...` passes
# (check, version, core status).
set -eu

: "${FRIDOLIN_HOME:=/data}"
: "${FRIDOLIN_CONFIG:=$FRIDOLIN_HOME/config.json}"
export FRIDOLIN_HOME FRIDOLIN_CONFIG

# A native install moved into the container (docs/CORE.md) has HOME at
# the Mac's home folder, an empty tmpfs under the bound folders. Scripts
# from the Mac that name ~/.local/bin/<tool> find the image's tool there:
# a link for each of FRIDOLIN_HOME_BIN that is on PATH. Brains' runs see
# the folder read-only.
if [ -n "${FRIDOLIN_HOST_HOME:-}" ] && [ "$FRIDOLIN_HOST_HOME" = "${HOME:-}" ] && [ -w "$HOME" ]; then
	mkdir -p "$HOME/.local/bin"
	for tool in ${FRIDOLIN_HOME_BIN-claude cloudflared gh node npm npx}; do
		case "$tool" in */* | .*) continue ;; esac
		if target=$(command -v "$tool" 2>/dev/null) && [ "${target#/}" != "$target" ]; then
			ln -sfn "$target" "$HOME/.local/bin/$tool"
		fi
	done
fi

# The owner's zone (config's timezone) is the process's too, whatever
# host started the container: core start passes the host's zone as TZ,
# which on a server is often UTC (#250). Brains' runs get it already.
if [ -r "$FRIDOLIN_CONFIG" ]; then
	zone=$(jq -r '.timezone // empty' "$FRIDOLIN_CONFIG" 2>/dev/null || true)
	case "$zone" in
	"" | *[!A-Za-z0-9_+/-]* | /* | *..*) ;;
	*) [ -f "/usr/share/zoneinfo/$zone" ] && TZ=$zone && export TZ ;;
	esac
fi

if [ "${1:-run}" = run ] && [ ! -e "$FRIDOLIN_CONFIG" ]; then
	/opt/fridolin/fridolin init --config "$FRIDOLIN_CONFIG" --data-dir "$FRIDOLIN_HOME" \
		--claude-path /usr/local/bin/claude
fi

exec /opt/fridolin/fridolin "$@"
