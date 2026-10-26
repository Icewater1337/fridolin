# Docker Sandboxes spike (#149)

Can a brain run happen in a Docker Sandbox, the microVM of Docker's `sbx`,
the way the bridge runs it today under bubblewrap? This spike tried it
hands-on, with scratch folders and a dev-mode bridge, never the live core.
It ends with a recommendation and a design: sbx as the runtime brains run in
normally, bubblewrap kept beside it as the second runtime and the fallback,
and FrIDE run whole inside one sandbox.

Tested on 2026-10-26 on a Mac mini (Apple M4, macOS 26.4, 24 GB), with
`sbx` v0.50.0 (Homebrew), signed in to Docker, `SBX_NO_TELEMETRY=1` set
for every call. Docker Desktop ran beside it, untouched; `sbx` does not
use it. Everything the spike made (sandboxes, secrets, rules, MCP
registrations, templates, pulled images, scratch folders) was removed
afterwards, the global network policy was reset to what it was before
(not initialized), and the sbx daemon stopped again.

Timings are wall-clock seconds around the `sbx` command, each the middle
of three runs where three were made. "RSS" is the host process that holds
the VM (`containerd-shim-nerdbox-v1`).

## In short

- **Runs work as the bridge makes them.** `sbx exec -i -w <brain folder>
  <sandbox> claude …` with the bridge's exact arguments takes the prompt on
  stdin and streams stream-json on stdout as it happens, and passes the
  exit code through. A dev-mode bridge whose `claude_path` was a small shim
  answered five DMs this way, end to end. `codex exec --json` streams and
  exits the same way.
- **The run token can stay out of the VM.** The brain's MCP server
  (`fridolin mcp --run <id>`) ran inside the VM and reached the bridge on
  the host through sbx's proxy, which swapped a placeholder for the real
  run token. The run saw exactly fridolin's tools, and `read_memory`
  answered. It needs one line changed in fridolin (the brain client must
  use the proxy from the environment).
- **sbx's MCP gateway is not safe for run tokens.** Its own tools
  (`mcp-find`, `mcp-add`) let any sandbox add any MCP server registered on
  the host, another sandbox's included, and call it.
- **Claude's setup-token cannot be injected.** sbx injects an Anthropic
  API key, and has a subscription login of its own (a `/login` inside a
  sandbox, held on the host), but a `claude setup-token` token cannot go
  through the proxy. Until the `/login` way is tested, the token goes into
  the run's environment, as with bubblewrap today.
- **Codex's ChatGPT login is injected by design** (`sbx secret set openai
  --oauth`, from the kit; not run), one login for the whole machine. The image's Codex CLI is too
  old for fridolin.
- **Egress is deny by default, per sandbox.** With the global policy at
  `deny-all`, the Claude kit opens only 7 Anthropic hosts; a sandbox-scoped
  rule adds a host or takes one away for that sandbox alone.
- **Cheap to keep, costly to run.** A sandbox per brain: 4.2 s to create
  (image cached), 0.15 s per `exec` while it runs, 1.3 s to start again
  after it stopped. It stops by itself 30 s after its last `exec`. A
  running sandbox holds 1.0 to 1.4 GB on the host, a stopped one nothing.
- **FrIDE runs whole in a sandbox.** Built inside, served on a published
  port through a forwarder as in the box, paired, and its signed webhook
  reached a listener on the host. Its agents need their Claude login in
  `~/.claude`, not the environment, because FrIDE drops `CLAUDE_CODE_*`
  variables for them.
- **Defaults to change.** sbx forwards the Mac's SSH agent into every
  sandbox (one key was usable from inside), lets sandboxes open URLs in the
  host's browser, and writes an 18 KB `CLAUDE.md` above the workspace that
  Claude reads unless fridolin's excludes keep it out (they do).

## 1. Runs like the bridge's

The bridge starts a run as `claude --output-format stream-json --verbose
--setting-sources project,local --strict-mcp-config --settings <json>
[--dangerously-skip-permissions] [--model …] [--mcp-config <file>
--allowed-tools mcp__fridolin] -p`, with the prompt on stdin and the brain
folder as cwd (`claudecli.ArgList`, `runner.go`). The same in a sandbox:

```sh
sbx create --name spike-claude claude ~/sbx-spike/brain-test \
  ~/sbx-spike/ro-share:ro ~/sbx-spike/media-inbox
printf '%s' "$PROMPT" | sbx exec -i -w ~/sbx-spike/brain-test spike-claude claude \
  --output-format stream-json --verbose --setting-sources project,local \
  --strict-mcp-config --dangerously-skip-permissions --model haiku -p
```

- **stdout streams.** A run with two `sleep 3` tool calls printed its 12
  events at 0.77, 1.94, 4.99, 5.01, 5.88, 8.89, … 9.75 s, as they
  happened.
- **The exit code passes through**: 1 for "Not logged in", 0 for a reply.
  By its help, `sbx exec` itself exits 125 when it cannot run the command.
- **A short run** ("pong", haiku) took 3.35 s from `exec` to result, its
  `init` event after 0.88 s.
- **Codex**: `codex exec --json … -` with the arguments `CodexArgs` builds
  (outer sandbox on, so Codex's own is bypassed) streamed its JSONL events
  over 15 s of retries and ended with `turn.failed` and exit 1, as it
  should without a login.
- **No TTY needed** for `create`, `exec -i`, `stop`, `rm`, `ls`, `policy`,
  `secret` and `mcp`. `sbx run`, the interactive agent session, fails
  without one ("inspect exec: context deadline exceeded"), so fridolin uses
  `create` and `exec`.
- **Killing `sbx exec` does not stop the command in the VM.** After the
  client was killed with SIGINT, SIGTERM or SIGKILL, the command still ran
  inside. A stop or a timeout must end the run inside the VM itself (see
  the design).

### The bridge, end to end

A dev-mode bridge from the worktree (`bin/fridolin --no-whatsapp --config
…`, its own port and data dir) with `claude_path` set to
[scripts/spike/sbx-claude-shim.sh](../scripts/spike/sbx-claude-shim.sh)
ran a scratch brain in the sandbox. `fridolin check` found the CLI through
the shim (`2.1.306 (Claude Code)`, from inside the VM). DMs through
`POST /api/dev/inbound` got their replies after 6 to 10 s, and the run's
token files were gone afterwards, as for any run. The run asked to read the
brain's note with fridolin's tool answered "The spike note remembers a blue
teapot.", and the audit log has:

```json
{"kind":"tool_call","brain":"sbxtest","run":"r5N9IX3dZ5Vdu","detail":"read_memory","outcome":"ok"}
{"kind":"run_end","brain":"sbxtest","run":"r5N9IX3dZ5Vdu","outcome":"ok","duration_ms":7907}
```

## 2. Credentials

### How sbx injects

All outbound traffic of a sandbox goes through a proxy on the host
(`HTTP(S)_PROXY=http://gateway.docker.internal:3128`, with its own CA in
the VM's trust store). `sbx policy log` shows per host whether the proxy
only forwarded it (`forward-bypass`) or opened it (`forward`) to put a
credential in. Two kinds of secrets:

- **Service secrets** (`anthropic`, `openai`, `github`, …), global or
  `--sandbox`-scoped, read from stdin. The agent's kit says where they go:
  Anthropic's as `x-api-key` on `api.anthropic.com`,
  `console.anthropic.com`, `claude.ai` and `mcp-proxy.anthropic.com`.
  Adding or removing one applied to the running sandbox at once ("Applied
  secret to sandbox").
- **Custom secrets** (`sbx secret set-custom`, experimental): a
  placeholder the VM holds is swapped for the value in any request header
  to the hosts named. Tested against a header echo: `Authorization` and
  `X-Api-Key` were swapped, a trailing newline from a `--command` source
  was dropped. The value can come from `--command`, which sandboxd runs on
  the host each time it needs the value, so it never passes through a
  command line or the VM.

Only clients that use the proxy get the swap. A direct connection that a
rule allows goes through without it.

### Claude

- **An API key works.** The kit's "apikey" mode writes `apiKeyHelper:
  "echo proxy-managed"` into the VM's `~/.claude/settings.json`, and the
  proxy puts the key in. The mode is decided when the sandbox is created
  (`SBX_CRED_ANTHROPIC_MODE`), so the secret must be stored before
  `sbx create`, or the sandbox recreated. No API key was at hand to run
  one; the injection itself showed (next point).
- **A setup-token does not.** Stored as the `anthropic` secret (piped from
  the Keychain into `sbx secret set anthropic --sandbox …`), it went out as
  `x-api-key` and Anthropic answered "invalid x-api-key". As a custom
  secret behind a Bearer placeholder it did not arrive at all: once any
  secret names `api.anthropic.com`, the proxy drops the client's
  `Authorization` header there. The same fake Bearer token got "OAuth
  access token is invalid" from the host and "x-api-key header is
  required" from inside the sandbox.
- **A subscription login is built in, not tested.** The Claude kit has an
  OAuth mode: the VM's `~/.claude/.credentials.json` holds sentinels,
  `sk-ant-oat01-proxy-managed` and `sk-ant-ort01-proxy-managed` <!-- sbx's fixed placeholders, not tokens: personal-data:ignore -->
  for the access and refresh token, and the proxy swaps them and refreshes the real tokens at
  `platform.claude.com/v1/oauth/token`. sbx's docs have the owner run
  `/login` inside a sandbox for it. `sbx secret set anthropic --oauth` is
  cloud-only in v0.50.0. This needs the owner's browser once; whether the
  login then serves every sandbox is what the test must show.
- **Nothing leaked.** With the token stored as a secret, a scan as root in
  the VM found no `sk-ant-…` value but the sentinels in the environment and
  command line of every process (`/proc/*/environ`, `/proc/*/cmdline`) or
  in any file under `/home`, `/root`, `/etc`, `/run`, `/tmp`, `/var/tmp`
  and `/opt`. The same scan found the token at once when it was passed in
  the environment instead.
- **Today's way works.** With no Anthropic secret stored, the proxy only
  forwards, and the token in the run's environment logs Claude in
  (`sbx exec -e CLAUDE_CODE_OAUTH_TOKEN`, the value taken from the sbx
  command's own environment, never its command line). That is what
  bubblewrap does now, and what the spike's runs used.
- `sbx secret rm` prints the stored value's first 6 and last 4 characters.
  Its output must not go into a log.

### Codex

The Codex kit injects an API key (`OPENAI_API_KEY`, as Bearer on
`api.openai.com`) or a ChatGPT login: the proxy holds the tokens,
refreshes them at `auth.openai.com/oauth/token` and swaps the sentinel
`oai-oat01-proxy-managed` on `chatgpt.com`, where the kit points Codex
(`model_provider = "sandboxd"`, `base_url =
"https://chatgpt.com/backend-api/codex"` in `~/.codex/config.toml`). The
login is stored with `sbx secret set openai --oauth`, global only: one
ChatGPT account for every sandbox on the machine. A `codex login` inside a
sandbox stays in that sandbox, which sbx warns about. Three catches:

- fridolin's `--ignore-user-config` skips that `config.toml`, so the
  provider must come as `-c` settings;
- the image's Codex CLI is 0.149.1, older than the 0.163.0 fridolin
  needs, so the template must bring a newer one;
- today each brain signs in on its own (`CODEX_HOME` per brain); with sbx
  there is one login per machine, or an API key per sandbox.

## 3. The brain's folder and the host

- **Same path, read-write.** The brain folder appeared at its host path
  (virtiofs). A file written inside showed up on the host, owned by the
  host's user. A folder given as `PATH:ro` was read-only inside; the media
  inbox, mounted read-write, took a file.
- **Nothing else of the host.** `/Users` showed only the path down to the
  mounts. `~/.ssh`, `~/.fridolin`, the owner's secrets folder,
  `~/Library/Keychains`, the real brain folders and an unmounted sibling
  folder did not exist inside. A symlink to `/` in the brain folder led to
  the VM's own root.
- **Extra read-only mounts work**, as more paths at `create` (the media
  inbox, `~/fridolin-private`, the tools). Mounts are fixed at creation:
  changing them means recreating the sandbox.
- **No masks inside a mount.** sbx can make one file inside a writable
  workspace read-only, but not hide a folder there. `sandbox.deny_paths`
  that lie inside a brain folder cannot be enforced as they are with
  bubblewrap.
- **Root in the VM.** The agent user is in `sudo` and `docker` groups, and
  the VM has its own Docker daemon. The VM is the wall, not the user.
- **sbx writes instructions above the workspace.** Inside the VM, the
  workspace's parent folder gets an 18 KB `CLAUDE.md` about the sandbox
  (not on the host). Asked whether its instructions mention
  `sandbox-persistent.sh`, a run said "Yes." without excludes and "No."
  with fridolin's `claudeMdExcludes` for every folder above the brain
  (`excludeInstructionsAbove`), so the bridge's own setting already keeps
  it out.
- **The Mac's SSH agent is forwarded by default**
  (`ssh.agentForwardingEnabled`): `ssh-add -l` inside listed one key, also
  when the daemon was started with no `SSH_AUTH_SOCK` (it falls back to
  launchd's). A brain could sign with the owner's key.
- **Sandboxes may open URLs in the host's browser** (`browser.enabled`),
  and a shared skills store is mounted read-only at
  `/run/sbx/skills-shared` (`--skills off` turns it off).

## 4. Fridolin's MCP tools

A run reaches fridolin's tools through `fridolin mcp --run <id>`, a stdio
server that calls the bridge's `/brain/mcp/*` with the run's token from
`FRIDOLIN_BRAIN_TOKEN_FILE`. Both ways were proved end to end with the
dev bridge.

### (a) sbx's MCP gateway: works, rejected

`sbx mcp add NAME --command … --args …` registers a stdio server that runs
on the host; `sbx mcp load NAME --sandbox SB` gives it to a sandbox, where
Claude reaches it at `http://mcp-gateway.docker.internal/mcp` (Bearer
`proxy-managed`, swapped by the proxy).
[scripts/spike/sbx-claude-shim-gateway.sh](../scripts/spike/sbx-claude-shim-gateway.sh)
registered each run's own command (with `/usr/bin/env` setting
`FRIDOLIN_API` and `FRIDOLIN_BRAIN_TOKEN_FILE`, as `sbx mcp add` has no
`--env`), loaded it, replaced the run's `--mcp-config` with the gateway,
and removed it after the run. The token file never left the host, and
`read_memory` answered.

But the gateway adds tools of its own: `code-mode` (JavaScript run "in an
isolated container"), `mcp-exec`, `mcp-config-set`, `mcp-find` and
`mcp-add`. From inside a sandbox, `mcp-find` listed a dummy server that was
registered on the host but loaded into no sandbox, `mcp-add` added it
("Added "spike-other" with 1 tools.") and its tool answered. A per-run
server with its run's token would be open to every other sandbox on the
machine the same way. Created with `--static-mcp`, a sandbox had no
`mcp-find` or `mcp-add` (only `code-mode`, `mcp-exec` and the static
tools), and `sbx mcp load` from the host still worked; but the static set
is fixed at creation, and the gateway stays a larger surface than
fridolin needs.

### (b) The server inside the VM, the token in the proxy: recommended

[scripts/spike/sbx-claude-shim.sh](../scripts/spike/sbx-claude-shim.sh):

1. One sandbox-scoped rule lets the VM reach the bridge's port on the host:
   `sbx policy allow network --sandbox SB localhost:18790`. The VM reaches
   it as `host.docker.internal:18790`; every other host port stays refused
   (403, "Approval required").
2. Per run, a sandbox-scoped custom secret with a random placeholder,
   whose value sandboxd reads from the run's token file on the host:
   `sbx secret set-custom --sandbox SB --host localhost --placeholder
   sbx-run-<random> --command "/bin/cat <run token file>"`.
3. The VM gets a token file holding only the placeholder, and the run's
   `--mcp-config` names a linux/arm64 `fridolin` on a read-only mount:
   `fridolin mcp --run <id>` with `FRIDOLIN_API=http://host.docker.internal:18790`.
4. After the run, the secret and the placeholder file go.

The run listed exactly `ask_user`, `check_approval`, `read_memory` and
`request_approval`, and `read_memory` answered (the audit lines above).
The proxy rewrites the `Host` header to `localhost:PORT` on the way.

It needed one change: `BrainClientFor` (`internal/toolcli/toolcmd.go`)
builds its HTTP client with `Proxy: nil`, so inside the VM it dialled
directly, got through without the swap, and the bridge refused the
placeholder (no tools). With `Proxy: http.ProxyFromEnvironment`, built
into a scratch binary for the test, it worked. Go never sends a request
for `localhost` or a loopback address through a proxy, so natively and
under bubblewrap nothing changes.

The same token file serves everything else that calls the brain API from
a run (`$FRIDOLIN_BIN calendar`, the poll helpers), unchanged.

## 5. Egress

- **Defaults.** Before the spike the global policy was not initialized;
  sbx asks for one before the first sandbox. `balanced` has 195 network allow rules in
  six groups (AI services, package managers, code and containers, OS
  packages, cloud infrastructure, certificate checks) and allows every
  file path.
- **Scopes.** A rule is global or `--sandbox`-scoped. Sandbox rules sit on
  top of the global policy, and a deny always wins, so a per-sandbox
  allowlist needs a global `deny-all`. Under `deny-all` the agent's kit
  adds a sandbox-scoped allow of its own: for Claude
  `api.anthropic.com`, `platform.claude.com`, `claude.com`,
  `code.claude.com`, `downloads.claude.ai`, `mcp-proxy.anthropic.com` and
  `bridge.claudeusercontent.com` (all on 443), for Codex 15 hosts with
  GitHub, npm and Ubuntu's mirrors.
- **What a Claude run needs** is that list, minus what may go:
  `downloads.claude.ai` (updates, pointless with a pinned image) and
  `bridge.claudeusercontent.com` (remote control); the run then reached
  Anthropic, and Claude's own telemetry (Datadog) was refused.

From inside, under `deny-all` and the kit's rules:

```text
https://api.anthropic.com/         404
https://platform.claude.com/       200
https://example.com/               403 Approval required for example.com
https://github.com/                403 Approval required for github.com
https://pypi.org/simple/           403 Approval required for pypi.org
```

then `sbx policy allow network --sandbox spike-brain example.com:443` and
`sbx policy deny network --sandbox spike-brain downloads.claude.ai`:

```text
https://downloads.claude.ai/       403 Blocked by local rule for downloads.claude.ai
https://example.com/               200
https://github.com/                403 Approval required for github.com
```

A refused host is queued as an approval (`sbx policy approval ls`), which
whoever uses sbx on the Mac could grant. `sbx policy reset` deletes every
rule and stops the daemon, running sandboxes with it.

## 6. Cost and lifecycle

| | seconds |
| --- | --- |
| `create`, first (pulls `claude-code-docker`) | 41.6 |
| `create`, first for Codex (pulls `codex-docker`) | 37.4 |
| `create`, image cached, headless | 4.2 |
| `create` from a saved template | 2.7 |
| `exec true` into a running sandbox | 0.15 |
| `stop` | 0.23 |
| `exec` into a stopped sandbox (boots it) | 1.3 |
| `rm` | 0.40 |
| `sbx daemon restart` | 2.3 |

| Memory on the host | |
| --- | --- |
| running, idle after a boot | 1,310 MB RSS (the guest uses 450 MB of 12 GB) |
| after one Claude run | 950 to 1,400 MB RSS |
| FrIDE served, idle | 1,100 MB RSS, 0.6 % CPU (FrIDE itself 24 MB) |
| stopped | none |

A sandbox gets half the host's memory (12 GB here) and every CPU unless
`--memory` and `--cpus` say otherwise; the host gives it only what it
touches, and does not seem to take it back until it stops. Swap stayed at
2.65 of 4 GB used throughout, with Docker Desktop's VM and the live core
running.

- **It stops by itself** 30 s after its last `exec` session ends
  (sandboxd: "auto-stopped runtime after last session disconnected"), and
  the next `exec` boots it in 1.3 s. A published port does not count as a
  session: FrIDE, left running with no session open, went with the VM.
- **One sandbox per run** would cost 3 to 5 s more per run (create, rm)
  and start every run with cold caches. **One sandbox per brain** costs
  0.15 s per run while it runs, 1.3 s after a pause, and nothing while
  stopped.
- **What survives.** A stop keeps the VM's whole disk: files in the home
  folder, `/tmp` and `/etc`, installed packages. A daemon restart stops
  every sandbox (a run in progress dies) and keeps sandboxes, their disks,
  secrets, policy rules and MCP registrations. `rm` deletes the sandbox
  and its sandbox-scoped secrets and rules; the host folders stay.
- **launchd.** Run as a launchd agent would be (a new session with no
  terminal, stdin from `/dev/null`, an environment of `HOME`, `USER` and a
  plain `PATH`), `sbx` started its daemon itself (its parent then
  launchd), and `ls`, `exec` and `exec -i` with a run worked. What needs a
  terminal: `sbx run`, and `template save`, which asks for confirmation and
  has no `--force` (the spike answered it through a pseudo-terminal).
  `rm` and `template rm` take `--force`.

## 7. Images and templates

- The Claude agent's image is `docker/sandbox-templates:claude-code-docker`:
  Ubuntu 26.04 on arm64, Claude Code 2.1.306, Node 22, Python 3.14, Go
  1.27.3, git, gh, ripgrep, jq, socat and Docker; no ffmpeg. Codex's is
  `codex-docker`, with Codex CLI 0.149.1.
- **A template with fridolin's tools works**: ffmpeg installed in a
  sandbox (18 s, with `ports.ubuntu.com:80` allowed for that sandbox),
  `sbx template save` (12 s), then `sbx create --pull never -t
  fridolin-brain-spike:v1 claude …` (2.7 s) had ffmpeg and the same Claude
  Code. The kit's start-up `apt-get update` runs in the background at
  every boot and holds apt's lock for a while.
- **Better, for fridolin**: build the template as an image in CI, from
  `claude-code-docker` with ffmpeg, the Python deps of `tools/`, pinned
  Claude Code and Codex CLI, and pull it by digest with `-t`, as the
  `fridolin-fride` image is built today. `-t` with a registry image was
  not tried.

## FrIDE in a Docker Sandbox, built and run by fridolin

The idea: as the box runs FrIDE in a container of its own, the node agent
on the Mac runs the whole FrIDE in one sandbox, so FrIDE's coding agents
run inside that microVM with it. No change to FrIDE. Proved with a scratch
clone of FrIDE, never the owner's FrIDEs (the native companion on this Mac
holds `127.0.0.1:17840`, so the scratch one was published on 27840):

```sh
sbx create --name spike-fride --memory 6g -p 127.0.0.1:27840:17840 claude \
  ~/sbx-spike/fride-repos ~/sbx-spike/fride:ro ~/sbx-spike/vmscripts:ro
sbx policy allow network --sandbox spike-fride \
  "proxy.golang.org:443,sum.golang.org:443,storage.googleapis.com:443"
sbx exec spike-fride sh build-fride.sh      # copy the source in, go build: 76 s cold
sbx exec spike-fride sh serve-fride.sh      # held open: fride serve --under-fridolin --port 17840
```

- **(a) The published port needs the box's forwarder.** `sbx ports` lands
  on the VM's network interface, not its loopback, where FrIDE listens:
  nothing answered until `socat TCP-LISTEN:17840,bind=<VM address>
  TCP:127.0.0.1:17840` ran inside. Then FrIDE refused "unexpected Host
  header", as the request named the host's port 27840: the host port must
  be FrIDE's own port, as both sides have 17840 in the box. With FrIDE's
  Host, the UI answered 200 and `/api/companion/info` 401 without a token;
  paired (a code from `fride pair-companion --json` run inside, traded on
  the host at `/api/companion/pair`), it answered 200:
  `{"configured":true,"mode":"companion","orchestrator":true,…,"webhook_url":"http://127.0.0.1:17841/fride"}`.
- **(b) The webhook reaches the host.** A second forwarder inside,
  `socat TCP-LISTEN:17841,bind=127.0.0.1 TCP:host.docker.internal:27841`,
  and the sandbox-scoped rule `localhost:27841` carried FrIDE's news to a
  listener on the host's `127.0.0.1:27841`: three signed POSTs to `/fride`
  from `127.0.0.1` (`update`, `update`, `failed`). No polling fallback is
  needed.
- **(c) FrIDE's agents need their login in files.** A message to FrIDE
  failed with "the orchestrator could not think: Not logged in", although
  `fride serve` had `CLAUDE_CODE_OAUTH_TOKEN`: FrIDE starts its agents with
  `agent.CleanEnv()`, which drops every `CLAUDE_CODE_*` variable. So the
  login must be in `~/.claude`: sbx's proxy-managed API key
  (`apiKeyHelper`) or its `/login` capture (`.credentials.json` with
  sentinels), which keep the token out of the VM; or the owner's own
  `claude /login` inside, which keeps it in the VM's disk, as the box keeps
  it in its volume. The box hands the token to FrIDE by the same variable,
  at a FrIDE commit that drops it too: worth checking that the box's agents
  are logged in.
- **(d) Workspaces in the VM, repositories on the host.** With the base
  folder mounted read-write at its host path, a repository made on the
  host as `fride.repo_new` does (`git init`, a commit), then `POST
  /api/config`, was in FrIDE's repos at once (`{"repos":["demo"]}`). So the
  node's `fride_repo_add` works unchanged. Task workspaces and worktrees
  stay on the VM's own disk, which lasts until `sbx rm`.
- **(e) Updates** rebuild FrIDE inside: 76 s the first time (modules
  downloaded), 0.4 s when nothing changed (Go's cache is on the VM's
  disk). Then the held `exec` restarts it. A new template is the other way.
- **(f) Cost**: 1.1 GB RSS and 0.6 % CPU idle, under a 6 GB cap.
- **It must be held open.** FrIDE kept running inside when the `exec` that
  started it was killed, but the VM stopped 30 s later and took FrIDE with
  it. The node agent keeps `sbx exec … fride serve` running as its child,
  and starts it again after a restart.
- **Which FrIDEs.** The companion on the Mac: yes. The box's FrIDE: no. A
  local sandbox needs the host's hypervisor (macOS 14+ on Apple silicon,
  Windows 11 with the Windows Hypervisor Platform, Ubuntu 24.04+ with KVM;
  inside a VM only with nested virtualization), which a container on
  icebase does not get. A native node on such a host could run one.

## Recommendation and design

Build it: brains run in Docker Sandboxes normally, bubblewrap stays the
second runtime and the fallback, and FrIDE moves into a sandbox the node
agent runs. sbx gives brains what bubblewrap cannot: a VM wall instead of
namespaces, an egress allowlist per brain, and run tokens (later model
tokens) that never enter the run. Its cost is memory, a second moving part
on the Mac, and a young tool (v0.x) to pin and watch.

### Where the choice lives

- `config.json`: `sandbox.runtime`, `"docker-sandbox"` or `"bubblewrap"`,
  the default for every brain, and `sandbox.node`, the node whose
  `sandbox` capability runs them (default: the node on the machine that
  holds the brain folders).
- `brain.json`: `sandbox.runtime` to override it, `sandbox.fallback`
  (below) and `sandbox.egress`, the hosts the brain may reach beyond its
  provider and the bridge. These are brain.json fields like any other: set
  from the dashboard by the owner, and a change the brain makes itself
  waits for the owner's approval, whose diff names a weaker runtime or a
  wider egress.

### When the Mac or sbx is unavailable

Fall back to bubblewrap by itself, and tell the owner, by default. A brain
must not go silent because the Mac sleeps, updates sbx or restarts its
daemon, and bubblewrap is a sandbox fridolin fully supports: no run ever
goes unsandboxed. What the run loses is what bubblewrap lacks today (the
token in its environment, the container's open network), and the owner
sees it: one message per outage in their DM ("Docker Sandboxes are off on
the Mac mini: brains run under bubblewrap until it is back"), the
dashboard's brain card, and the run's audit line (`runtime`). A brain for
which the allowlist is the point, one that reads strangers' groups say,
sets `"fallback": "refuse"` and refuses until the node is back, saying why.
Only being unavailable falls back (node offline, sbx missing or its daemon
down, the VM not starting); a sandbox that starts but whose secret, rule or
mount cannot be set up refuses the run, as a sandbox that cannot be set up
does today. A run is never moved to the other runtime halfway.

### One plumbing, two runtimes

Everything up to the process stays shared: `claudeArgs` and `codexArgs`,
the prompt on stdin, `ParseProvider` and the stream-json and JSONL
parsing, the run token and its revocation, the run's MCP config, notices,
deadlines, the audit. The runtime is a small strategy behind one interface:

```go
type runtime interface {
	Name() string                                        // "bubblewrap", "sandbox-exec", "docker-sandbox"
	Ready(ctx context.Context, brain string) error       // for the fallback, the dashboard and doctor
	Prepare(spec brain.RunSpec, access runAccess) (plan, error) // paths, env, MCP config for where it runs
	Start(ctx context.Context, s claudecli.Spawn) (claudecli.Proc, error)
}
```

`claudecli.Process` becomes an interface (`Wait`, `StderrTail`, `Stopped`,
an id instead of a PID); bubblewrap and sandbox-exec keep today's
`exec.Cmd` behind it, and docker-sandbox returns a process whose stdout is
the node's stream. `Prepare` is where they differ: for docker-sandbox the
MCP command is the VM's `fridolin` on the tools mount,
`FRIDOLIN_API` the relay below, `FRIDOLIN_BRAIN_TOKEN_FILE` a placeholder
file, `FRIDOLIN_BIN` and `FRIDOLIN_TOOLS_DIR` paths in the VM, and no
provider token in the environment once the proxy holds it.

### The node capability `sandbox`

On a node with sbx (macOS first), approved like `fride`: it runs agents on
that machine, inside microVMs.

| Method | What it does |
| --- | --- |
| `sandbox.info` | sbx version (against a tested one, as `TestedClaudeVersion`), signed in, daemon, the checks below, templates, free memory |
| `sandbox.ensure` | creates `fridolin-<brain>` from the template with the brain's mounts, memory cap and sandbox-scoped egress rules, or recreates it when they changed; idempotent |
| `sandbox.run` | one run: token in, stdout streamed back as events, then the exit code and stderr's tail |
| `sandbox.stop` | ends a run inside the VM (TERM to its process group, KILL after the grace) |
| `sandbox.remove`, `sandbox.list` | |

`sandbox.run` writes the run's token to a 0600 file in the node's state
dir, sets the sandbox-scoped custom secret that reads it, puts the
placeholder file in the VM, and runs `sbx exec -i -w <folder>
fridolin-<brain> setsid sh -c 'echo $$ > /tmp/run-<id>.pid; exec claude
…'`, so `sandbox.stop` can find the run: killing the `exec` client does
not. Afterwards it removes the secret and both files; the core revokes the
token as it does now.

The **brain API relay**: the node listens on one loopback port, which every
brain sandbox may reach (`localhost:<port>`), and passes `/brain/*`
requests to the core over its WebSocket, as a new request/response pair.
The core's API stays off the network, and it does not matter whether the
core runs in the container on the Mac or elsewhere.

### Credentials, the run token and MCP

- Run token: as in section 4 (b). The MCP server runs inside the VM; the
  gateway is not used. Fridolin's change: `ProxyFromEnvironment` in
  `BrainClientFor`, and a linux binary of fridolin for the VM (the node
  binaries CI builds already include linux/arm64 and amd64).
- Claude: the goal is the proxy-held subscription login (the `/login`
  capture), once the owner has tested it; an API key, from the node's
  Keychain through stdin into a sandbox-scoped `anthropic` secret, works
  now. Until then the setup-token goes in the run's environment, as today.
  The secret must exist before `create`, which sets the kit's mode.
- Codex: `sbx secret set openai --oauth` (one ChatGPT login for the
  machine) or an API key per sandbox; the provider settings come as `-c`.
- GitHub: a run's GitHub token (`withGitHubToken`) as a sandbox-scoped
  `github` secret; the VM already has `GH_TOKEN` as a sentinel. Not tested.

### Egress

The node owns sbx's global policy on that machine: `deny-all`. Each brain
sandbox gets the kit's provider hosts, minus `downloads.claude.ai` and
`bridge.claudeusercontent.com`, plus the relay port and the brain's
`sandbox.egress`. A brain that uses WebFetch needs the hosts it fetches; one
that must browse freely gets `"**:443"`, and the owner sees that on the
brain's card. The same check sets `ssh.agentForwardingEnabled` and
`browser.enabled` to false.

### Lifecycle

One sandbox per brain, `fridolin-<brain>`, made by `sandbox.ensure` at the
brain's first run and remade when its mounts or the template change. It
stops 30 s after a run and boots in 1.3 s for the next. Mounts: the brain
folder and its media inbox read-write, `sandbox.allow_paths`, the tools
folder (with the VM's `fridolin`) and `~/fridolin-private` read-only, all
at their host paths. `--memory 4g`; with `max_concurrent_runs` at 2, about
3 GB at the peak.

### A core move

The sandbox runtime runs where the brain folders are. With the core in its
container on the Mac, the Mac's folders are the ones the core uses, bound
in at the same paths, and the Mac's node mounts them. When the core moves
to icebase, the brain folders move with the export, and the Mac's copies
are stale: the core then sees that its brain folders are not the node's,
and runs brains under bubblewrap in icebase's core, with the fallback's
notice; on the way back it uses the Mac's sandboxes again. To make sure a
node never runs a brain in a stale copy, `sandbox.ensure` checks a marker
the core writes into the folder (an id that changes with each move).

### Codex vs Claude

Both run. Codex is easier for credentials (the ChatGPT login is injected
today), harder for setup (one login per machine, the provider as `-c`
settings, a newer CLI in the template). Claude needs the `/login` capture
tested before its token can stay out of the VM.

### The dashboard and doctor

- Brains page: each brain's runtime (Docker Sandbox on the Mac mini,
  bubblewrap), the fallback and egress settings, and now: "in a Docker
  Sandbox", "under bubblewrap: the Mac mini has been offline since 14:02".
- Nodes page: the `sandbox` capability with what `sandbox.info` says.
- `fridolin doctor`: a check per brain (the runtime chosen, whether it is
  ready now, what the fallback would do) and, on the node (`--node`), sbx's
  version, sign-in, daemon, the global policy, the SSH agent and browser
  settings, the template and the relay. `fridolin check --print-sandbox
  <id>` prints the sbx commands for a docker-sandbox brain.

### FrIDE, on fridolin's side

`fridolin node fride sandbox create | start | stop | update | status | rm`,
which the node agent also runs by itself:

1. **Create** `fridolin-fride` from a template with FrIDE's toolchain (the
   Claude kit's image has Go, Node, git and Claude Code; add the pinned
   ponytail and gh), the base folder mounted read-write at its host path,
   `--memory 6g`, the port published as `127.0.0.1:P:P`, and an egress
   list for coding work (Go's proxy, GitHub, npm).
2. **Build** FrIDE inside at the commit fridolin pins (as the
   `fridolin-fride` image does), write the companion config, set the git
   identity.
3. **Run** `fride serve --under-fridolin --port P` as an `exec` the agent
   keeps open, with the two forwarders of the box: the VM's P to FrIDE's
   loopback, and the loopback's W (the agent's webhook port) to
   `host.docker.internal:W`, with the rule `localhost:W`. With the same
   port numbers inside and out, FrIDE's Host check and the webhook URL
   work unchanged.
4. **Pair** with `fridolin node fride pair --url http://127.0.0.1:P --code
   <code from fride pair-companion inside> --base <base folder>`.
5. **Update** by rebuilding inside at the new commit and restarting the
   held `exec`.

The Claude login for FrIDE's agents goes into the VM's `~/.claude`: the
proxy-held login or API key once available, else a `claude /login` the
owner does inside. P cannot be 17840 while the native companion runs on
this Mac: the sandboxed FrIDE replaces it or takes another port.

### What to change in fridolin

1. `internal/toolcli/toolcmd.go`: the brain client uses the proxy from the
   environment.
2. `internal/claudecli`: `Process` as an interface.
3. `internal/app`: the runtime strategy, its choice per brain and the
   fallback with its notice; `brain.json` and `config.json` fields with
   their schemas and golden files.
4. The node: the `sandbox` capability, the relay, the sbx driver (the CLI,
   no terminal), and `node fride sandbox`.
5. CI: the brain template image, built and published like
   `fridolin-fride`.
6. The dashboard and doctor, as above.

### Risks

- **sbx is young.** v0.50.0, flags and kit formats marked experimental
  (custom secrets, kits). Pin the version, test against it, and have
  doctor warn on another.
- **Shared settings.** The global policy, the SSH agent and browser
  settings are the machine's, also for the owner's own sbx use.
- **Memory.** 1 to 1.4 GB per running sandbox on a 24 GB Mac that also
  runs Docker Desktop's VM and the core.
- **The proxy sees everything** in clear, on the host, with its own CA in
  every VM.
- **Subscription tokens.** Until the `/login` capture is tested, the
  Claude token stays in the run's environment, as today.
- **Stops.** A run must be ended inside the VM; a daemon restart ends
  every run at once.
- **Docker's account.** sbx needs `sbx login` and pulls from Docker Hub.
- **Approvals.** Refused hosts queue as approvals someone could grant on
  the Mac.
- **No masks inside a brain folder**: `deny_paths` there cannot be hidden.

## What the owner must decide or do

1. Let fridolin's node own sbx's settings on the Mac: the global policy at
   `deny-all`, `ssh.agentForwardingEnabled` and `browser.enabled` off.
   They apply to the owner's own sandboxes too.
2. Test sbx's subscription login once: `/login` in a scratch Claude
   sandbox, in the browser, then whether a second sandbox is logged in and
   no token is inside. Or decide for an API key, or keep the setup-token
   in the environment.
3. For Codex in sandboxes: `sbx secret set openai --oauth`, one ChatGPT
   login for the Mac.
4. FrIDE's port: the sandboxed FrIDE replaces the native companion on
   17840, or runs beside it on another port.
5. Check that the box's FrIDE agents are logged in (FrIDE drops
   `CLAUDE_CODE_OAUTH_TOKEN` for its agents).
6. Confirm the fallback default (automatic, with a notice; `refuse` per
   brain).

## Not tested

A run with an Anthropic API key (none at hand), sbx's `/login` capture,
GitHub token injection, `-t` with an image from a registry, a FrIDE task
with a logged-in agent, ponytail in the sandbox, and hosts other than the
Mac.

## Commands used

```sh
export SBX_NO_TELEMETRY=1
sbx policy ls                                  # before: "not initialized"
sbx policy init balanced; sbx policy ls --wide # the defaults
sbx create --name NAME [--memory 4g] [-p 127.0.0.1:H:G] [--static-mcp S] [-t TEMPLATE --pull never] \
  claude|codex FOLDER [FOLDER:ro …]
sbx exec -i [-e NAME] [-u root] -w FOLDER NAME claude … -p     # prompt on stdin
security find-generic-password -a fridolin -s fridolin.claude.oauth.token -w \
  | sbx secret set anthropic --sandbox NAME                    # never echoed or in argv
sbx secret set-custom --sandbox NAME --host HOST --placeholder P --command "/bin/cat FILE"
sbx secret rm --placeholder P --sandbox NAME -f
sbx mcp add NAME --command /usr/bin/env --args "K=V,…,CMD,ARG,…"; sbx mcp load NAME --sandbox SB
sbx policy allow network --sandbox NAME HOST:PORT; sbx policy deny network --sandbox NAME HOST
sbx policy log NAME; sbx policy reset --force; sbx policy init deny-all
sbx ports NAME; sbx stop NAME; sbx rm -f NAME; sbx daemon restart
sbx template save NAME TAG      # asks for confirmation on a terminal
sbx template rm --force TAG
```
