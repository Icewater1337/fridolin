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

if [ "${1:-run}" = run ] && [ ! -e "$FRIDOLIN_CONFIG" ]; then
	/opt/fridolin/fridolin init --config "$FRIDOLIN_CONFIG" --data-dir "$FRIDOLIN_HOME" \
		--claude-path /usr/local/bin/claude
fi

exec /opt/fridolin/fridolin "$@"
