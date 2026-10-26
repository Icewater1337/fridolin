#!/bin/bash
# Spike for #149 (docs/SANDBOXES-SPIKE.md), not production code.
#
# A claude_path shim that moves one brain run into a Docker Sandbox (sbx).
# A dev-mode bridge runs it exactly as it runs `claude`: the same
# arguments, the prompt on stdin, stream-json on stdout, cwd = the brain
# folder, which the sandbox mounts at the same path.
#
# The run's MCP server (`fridolin mcp --run <id>`) runs INSIDE the VM and
# calls the bridge's HTTP API through sbx's proxy. The run token never
# enters the VM:
#
# - the VM gets a token file that holds only a random placeholder;
# - a sandbox-scoped custom secret tells the proxy to swap the placeholder
#   for the real token, which sandboxd reads on the host from the run's
#   token file (`--command "cat FILE"`), in requests to the host;
# - a sandbox-scoped policy rule (sbx policy allow network --sandbox SB
#   localhost:PORT) lets the VM reach the bridge's port on the host, as
#   host.docker.internal, and nothing else there.
#
# Needs: an existing sandbox (SBX_SANDBOX) created with the brain folder
# and FRIDOLIN_IN_VM's folder mounted; a linux/arm64 fridolin built with
# the brain client's HTTP transport using the proxy from the environment
# (internal/toolcli/toolcmd.go has Proxy: nil, so the proxy cannot swap
# the placeholder); and the Claude login. The spike's login was the
# Keychain's setup-token in the run's environment, as bubblewrap hands it
# over today: sbx v0.50.0 cannot inject a setup-token (see the doc).
set -u
export SBX_NO_TELEMETRY=1
SBX=${SBX:-sbx}
SANDBOX=${SBX_SANDBOX:-spike-claude}
LOG=${SHIM_LOG:-/tmp/sbx-claude-shim.log}
FRIDOLIN_IN_VM=${FRIDOLIN_IN_VM:-/Users/you/sbx-spike/ro-share/fridolin-linux} # on a read-only mount

for a in "$@"; do
	if [ "$a" = "--version" ]; then exec "$SBX" exec "$SANDBOX" claude --version; fi
done

args=() placeholder="" run="" prev=""
for a in "$@"; do
	if [ "$prev" = "--mcp-config" ] && [[ "$(basename "$a")" == run-*.mcp.json ]]; then
		run=$(basename "$a" .mcp.json)
		run=${run#run-}
		tokfile=$(jq -r '.mcpServers.fridolin.env.FRIDOLIN_BRAIN_TOKEN_FILE' "$a")
		api=$(jq -r '.mcpServers.fridolin.env.FRIDOLIN_API' "$a") # http://127.0.0.1:PORT
		port=${api##*:}
		placeholder="sbx-run-$(openssl rand -hex 12)"
		"$SBX" secret set-custom --sandbox "$SANDBOX" --host localhost --placeholder "$placeholder" \
			--command "/bin/cat $tokfile" >>"$LOG" 2>&1
		printf '%s' "$placeholder" | "$SBX" exec -i "$SANDBOX" sh -c "umask 077; cat > /tmp/run-$run.token"
		cfg=$(jq -nc --arg cmd "$FRIDOLIN_IN_VM" --arg run "$run" --arg api "http://host.docker.internal:$port" \
			--arg tf "/tmp/run-$run.token" \
			'{mcpServers:{fridolin:{type:"stdio",command:$cmd,args:["mcp","--run",$run],env:{FRIDOLIN_API:$api,FRIDOLIN_BRAIN_TOKEN_FILE:$tf}}}}')
		args+=("$cfg")
	else
		args+=("$a")
	fi
	prev=$a
done

CLAUDE_CODE_OAUTH_TOKEN="$(/usr/bin/security find-generic-password -a fridolin -s fridolin.claude.oauth.token -w)"
export CLAUDE_CODE_OAUTH_TOKEN
"$SBX" exec -i -e CLAUDE_CODE_OAUTH_TOKEN -w "$PWD" "$SANDBOX" claude "${args[@]}"
rc=$?
if [ -n "$placeholder" ]; then
	"$SBX" secret rm --placeholder "$placeholder" --sandbox "$SANDBOX" -f >>"$LOG" 2>&1
	"$SBX" exec "$SANDBOX" rm -f "/tmp/run-$run.token"
fi
exit $rc
