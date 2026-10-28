#!/bin/bash
# Spike for #149 (docs/SANDBOXES-SPIKE.md), not production code.
#
# The rejected variant: the run's MCP server goes through sbx's host-side
# MCP gateway. The run's --mcp-config file (run-<id>.mcp.json) is read on
# the host, its command (`fridolin mcp --run <id>` with FRIDOLIN_API and
# FRIDOLIN_BRAIN_TOKEN_FILE) registered with `sbx mcp add` and loaded into
# the sandbox, so the stdio server runs on the host, where the token file
# is. In the arguments, the file is replaced by an inline config that
# points at the gateway inside the VM.
#
# It works, but the gateway's dynamic tools (mcp-find, mcp-add) let any
# sandbox add any server registered on the host, another run's included,
# with that run's token. See the doc.
set -u
export SBX_NO_TELEMETRY=1
SBX=${SBX:-sbx}
SANDBOX=${SBX_SANDBOX:-spike-claude}
LOG=${SHIM_LOG:-/tmp/sbx-claude-shim.log}

for a in "$@"; do
	if [ "$a" = "--version" ]; then exec "$SBX" exec "$SANDBOX" claude --version; fi
done

gateway='{"mcpServers":{"fridolin":{"type":"http","url":"http://mcp-gateway.docker.internal/mcp","headers":{"Authorization":"Bearer proxy-managed"}}}}'
args=() server="" prev=""
for a in "$@"; do
	if [ "$prev" = "--mcp-config" ] && [[ "$(basename "$a")" == run-*.mcp.json ]]; then
		run=$(basename "$a" .mcp.json)
		run=${run#run-}
		server="fr-$(echo "$run" | tr 'A-Z_' 'a-z-')"
		cmd=$(jq -r '.mcpServers.fridolin.command' "$a")
		margs=$(jq -r '.mcpServers.fridolin.args | join(",")' "$a")
		menv=$(jq -r '.mcpServers.fridolin.env | to_entries | map("\(.key)=\(.value)") | join(",")' "$a")
		"$SBX" mcp add "$server" --command /usr/bin/env --args "$menv,$cmd,$margs" >>"$LOG" 2>&1
		"$SBX" mcp load "$server" --sandbox "$SANDBOX" >>"$LOG" 2>&1
		args+=("$gateway")
	else
		args+=("$a")
	fi
	prev=$a
done

CLAUDE_CODE_OAUTH_TOKEN="$(/usr/bin/security find-generic-password -a fridolin -s fridolin.claude.oauth.token -w)"
export CLAUDE_CODE_OAUTH_TOKEN
"$SBX" exec -i -e CLAUDE_CODE_OAUTH_TOKEN -w "$PWD" "$SANDBOX" claude "${args[@]}"
rc=$?
if [ -n "$server" ]; then
	"$SBX" mcp unload "$server" --sandbox "$SANDBOX" >>"$LOG" 2>&1
	"$SBX" mcp rm -f "$server" >>"$LOG" 2>&1 </dev/null
fi
exit $rc
