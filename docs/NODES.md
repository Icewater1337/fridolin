# Nodes

A node is another machine of yours — a Windows laptop, a Linux box, a
second Mac — that lends the core its commands, files and devices. It runs
the same `fridolin` binary as a small agent (`fridolin node`), opens one
WebSocket to the core, and serves only what you approved. Nodes never
listen on a port; the core listens on one, `nodes.listen`
([CONFIGURATION.md](CONFIGURATION.md#nodes)).

This is milestone M2 of [PLAN.md](PLAN.md) (Workstream A). The tailnet is
the trust boundary: run the node port on the tailnet (the core's `tsnet`
listener, `core.tailnet.enabled`) or on a network you trust. The
connection itself is plain WebSocket; WireGuard encrypts it on the
tailnet.

## Turn it on

On the core, in `config.json`:

```json
"nodes": { "enabled": true, "listen": ":8766" }
```

Both settings wait for a restart. `fridolin doctor` then checks that the
node port answers (required while nodes are on) and warns about approved
nodes the core has not heard from for five minutes.

## Join a machine

1. On the core: `fridolin nodes join-code` (or **Nodes → New join code**
   in the dashboard). It prints a single-use code, valid for 10 minutes,
   and the line to paste:

   ```
   fridolin node join fj-abcd… --core fridolin-core:8766
   ```

   The core keeps only the code's SHA-256 in `nodes/nodes.json`.
2. On the new machine, with fridolin installed, paste that line. It makes
   the node's device key, writes `node.json`, connects once with the code,
   and installs and starts the node service. `--no-service` only joins;
   `fridolin node run` then runs the agent in the foreground.
3. On the core, approve the device:
   `fridolin nodes approve <id>` (or **Approve device** on the Nodes page).
4. Then approve what it offers, separately:
   `fridolin nodes approve <id> --commands`.

Until step 3 a node serves nothing; until step 4 it serves nothing either.
If the node later offers more (you edited its `node.json`, or joined with
other flags), the new part waits for `approve --commands` again, and the
node keeps serving what was approved before. Offering less needs no
approval.

`fridolin nodes revoke <id>` (or **Revoke**) closes the node's connection
at once and refuses it from then on; the agent stops reconnecting. To bring
the machine back, it joins again with a new code, as a new device.

`fridolin nodes` lists the nodes; `fridolin nodes tags <id> docker,gpu`
replaces a node's tags (`nodes.json` may also be edited by hand; the core
reads it again when it changes). In a direct chat, `!fleet` lists the
nodes, whether each is online, and what each may run.

## What a node offers

The node's state dir holds `node.json` (its settings) and `node.key` (its
ed25519 device key, which never leaves it). The state dir is
`~/Library/Application Support/fridolin-node` on macOS,
`$XDG_STATE_HOME/fridolin-node` (default `~/.local/state/fridolin-node`) on
Linux and `%LOCALAPPDATA%\fridolin-node` on Windows; `--state-dir` picks
another. `fridolin node status` shows it.

```json
{
  "core": "fridolin-core:8766",
  "name": "laptop",
  "capabilities": {
    "exec": {
      "allow": [
        {"name": "hostname", "args": []},
        {"name": "git", "args": ["status", "log", "--oneline"]},
        {"name": "C:\\Tools\\backup.exe", "args": null}
      ],
      "timeout_seconds": 60,
      "max_output_bytes": 1048576
    },
    "fs": {"roots": [{"path": "C:\\Users\\you\\Shared", "write": true}]},
    "host": true,
    "notify": true
  }
}
```

| Capability | Methods | What it does |
| --- | --- | --- |
| `exec` | `exec` | runs an allowlisted program with arguments, no shell, with a timeout (default 60 s, at most 3600) and stdout and stderr each cut at `max_output_bytes` (default 1 MiB) |
| `fs` | `fs.list`, `fs.read`, `fs.write` | lists, reads (up to 16 MiB) and, under a root with `write`, writes files below the granted roots |
| `host` | `host.info` | host name, OS, architecture, CPUs, memory, disk free and, on Linux, the battery |
| `notify` | `notify` | a desktop notification: `osascript` on macOS, `notify-send` on Linux; on Windows it is only logged for now |

**exec rules.** `name` is `argv[0]` exactly: a bare program name, which
the node looks up in its `PATH`, or an absolute path. `args` `null` allows
any arguments, `[]` none, and a list of regular expressions allows
arguments that each match one of them whole. No rule may name a shell or a
program that runs others (`sh`, `bash`, `zsh`, `fish`, `cmd`,
`powershell`, `pwsh`, `wsl`, `env`, `sudo`, `osascript`, `xargs`, …): the
node refuses to load such a `node.json`, and both sides refuse such a call
whatever a rule says. An interpreter you allow (`python`, `node`) can still
run anything; allow one only with argument patterns that pin what it runs.
Without `--allow` or `--no-exec`, a node offers its OS's report-only
commands: `uname`, `uptime`, `df` (and `sw_vers` on macOS), or `hostname`,
`whoami`, `systeminfo` on Windows.

**fs roots.** A path in a call is absolute. It must lie under a granted
root, as written or as the root's symlinks resolve, and is then opened
through Go's `os.Root`, which refuses every way out of the root, `..` and
symlinks included.

`fridolin node join` flags set these: `--allow PROG`, `--allow PROG=`,
`--allow PROG=RE,RE`, `--no-exec`, `--root PATH`, `--root PATH:rw`,
`--no-host`, `--no-notify`, `--name`.

Both sides check every call: the core against the approvals before it
sends anything, the node against its own `node.json` and the approvals the
core sent it, so a core that approved more than the node declares cannot
make it serve more.

## The service

`fridolin node install | start | stop | status | uninstall` manage the
node service through [kardianos/service](https://github.com/kardianos/service):

| OS | Service |
| --- | --- |
| macOS | launchd user agent `ai.fridolin.node` (the bridge's is `ai.fridolin.bridge`), logs in the state dir |
| Linux | systemd user unit `fridolin-node`, restarted always |
| Windows | Windows Service `fridolin-node`, started at boot, restarted after a failure |

The service runs `fridolin node run --state-dir <dir>` with the binary that
installed it, so install from where the binary stays. A revoked node's
agent stays up without reconnecting until it is stopped, so the service
manager does not restart it into the same refusal.

## The protocol

Package `internal/nodeproto`. JSON text messages over a WebSocket at
`ws://<core>/v1/node`, subprotocol `fridolin-node.v1`; each message is an
envelope `{"v": 1, "type": …, "id": …, "body": …}`, and a message of
another version is refused.

1. The node sends `hello`: device ID, ed25519 public key, name, OS,
   architecture, version, the capabilities it declares and, the first
   time, the join code. The device ID is the first 12 hex digits of the
   key's SHA-256, so no node can claim another's.
2. The core answers `challenge` with a 32-byte nonce; the node answers
   `auth`, its signature over `fridolin-node-auth/v1`, the device ID and
   the nonce. A signature that does not verify closes the connection
   (`bad-auth`).
3. The core records the node (an unknown device needs a valid code, else
   `unknown-device`; a revoked one gets `revoked`) and sends `welcome`
   with the node's state: `device` (`pending`, `approved`, `revoked`),
   `commands` (`none`, `pending`, `approved`) and `approved`, the part of
   its declaration it may serve. Each approval later sends `state`.
4. The node sends `heartbeat` every 20 seconds with its free resources;
   the core closes a connection it heard nothing from for 90 seconds.
5. The core sends `request` (`id`, `method`, `params`) and the node answers
   `response` with the same `id`: a `result`, or an `error` with a code
   (`denied`, `outside-root`, `not-found`, `bad-request`, `timeout`,
   `failed`, `unavailable`, `unsupported`).
6. Either side may send `close` with a reason (`revoked`,
   `unknown-device`, `bad-auth`, `replaced`, `shutdown`, `protocol`,
   `idle`) before closing. After `revoked` or `unknown-device` the agent
   stops; after anything else it reconnects with exponential backoff (1 s
   up to a minute, with jitter).

Before the signature checks out a message may be 256 KiB; after it,
48 MiB. The decoder has a fuzz target (`FuzzDecode`).

## The core's API

The dashboard API (`/api/nodes…`, see [API.md](API.md#nodes)) lists the
nodes, makes join codes, approves, revokes, edits tags and calls a
capability; `fridolin nodes` uses it with the dashboard token. Inside the
bridge, `nodes.Service` has `Call(ctx, id, method, params, out)` and typed
helpers (`Exec`, `List`, `ReadFile`, `WriteFile`, `HostInfo`, `Notify`)
for the parts that will place work on nodes.
