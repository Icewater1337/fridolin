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
nodes the core has not heard from for five minutes. It asks the running
bridge which nodes are connected; when the bridge does not answer it
reads `nodes/nodes.json`, where the core writes each node's `last_seen`
and resources at least once a minute and when the node disconnects.

## Joining a machine

This is the runbook a coding agent follows when the owner clones the
repository on a new machine and says "set this machine up as a node of my
fridolin core" (AGENTS.md, path B). The agent does every step itself
except the three only the owner can do: signing in to Tailscale,
signing in to the core's dashboard (or using the Mac), and approving the
node. Each of those is opened for the owner in the browser, then the agent
waits and checks. `fridolin setup-machine --node` says at any point what
is done and what to do next; it only looks and may be run again and again.

The core must have `nodes.enabled` and `core.tailnet.enabled` on (the
owner's container core on the Mac mini has both): the node reaches it on
the tailnet at `fridolin-core.<tailnet>.ts.net:8766`.

### 1. Tools

Ask once, naming them, then install what is missing.

Windows (PowerShell; winget comes with Windows 11 and current Windows 10):

```powershell
winget install --id Git.Git -e --accept-source-agreements --accept-package-agreements
winget install --id GoLang.Go -e --accept-source-agreements --accept-package-agreements
winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements
# the new PATH, without opening a new shell:
$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')
go version; git --version; tailscale version
```

Where winget is missing, Chocolatey works too, from an administrator
PowerShell: `choco install -y git golang tailscale`. Each installer may
show a UAC prompt: tell the owner to accept it.

macOS: `brew install go git` and `brew install --cask tailscale` (install
Homebrew first if `brew` is missing:
`/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`).
Linux: `sudo apt install -y golang git` (or `sudo dnf install -y golang
git`; Go must be 1.26 or newer, else the tarball from go.dev/dl) and
`curl -fsSL https://tailscale.com/install.sh | sh`.

### 2. Tailscale

The machine must be on the owner's tailnet, logged in as the same account
as the core. Run `tailscale up` (Linux: `sudo tailscale up`); it prints a
login link and waits. Open the link for the owner (`Start-Process URL` on
Windows, `open URL` on macOS, `xdg-open URL` on Linux; the Windows and
macOS apps may open it themselves), ask them to sign in with the account
the core's tailnet belongs to, and wait. Then:

```sh
tailscale status            # lists fridolin-core among the devices
tailscale status --json     # MagicDNSSuffix is <tailnet>.ts.net
```

In PowerShell, the core's address:

```powershell
$tailnet = (tailscale status --json | ConvertFrom-Json).MagicDNSSuffix
$core = "fridolin-core.$tailnet"
Test-NetConnection $core -Port 8766   # TcpTestSucceeded: True
```

### 3. Build fridolin

In the clone (no C compiler needed):

```powershell
$env:CGO_ENABLED = "0"
go build -o bin\fridolin.exe .\cmd\fridolin
```

```sh
CGO_ENABLED=0 go build -o bin/fridolin ./cmd/fridolin
```

On Windows the node service runs as LocalSystem, so its binary must lie
where only administrators can change it. Copy it to Program Files, in one
elevated step (the owner accepts the UAC prompt):

```powershell
Start-Process powershell -Verb RunAs -Wait -ArgumentList '-NoProfile','-Command',"New-Item -ItemType Directory -Force '$env:ProgramFiles\fridolin' | Out-Null; Copy-Item '$PWD\bin\fridolin.exe' '$env:ProgramFiles\fridolin\fridolin.exe' -Force"
$fridolin = "$env:ProgramFiles\fridolin\fridolin.exe"
& $fridolin version
```

On macOS and Linux, `make install` copies it to `~/.local/bin` (on PATH),
which the user service runs.

### 4. A join code from the core

The default needs nothing of the Mac: open the core's dashboard on the
tailnet for the owner, at its Nodes page:

```powershell
Start-Process "https://$core/#/nodes"     # http:// when the tailnet has no HTTPS certificates
```

(`open` / `xdg-open` elsewhere.) A browser that never signed in there
shows the sign-in page: the owner pastes the dashboard token from their
password manager into it, never into the chat. Ask them to click **New
join code** and paste the Windows line it shows into the chat or this
terminal. The code is single-use, expires in 10 minutes and grants
nothing until the owner approves the node, so it may go through the chat.

The alternative, on the Mac mini (or by an agent there):

```sh
docker exec fridolin-core fridolin nodes join-code
```

A native core: `fridolin nodes join-code`. Both print the line for sh and
for PowerShell, with the core's tailnet address.

### 5. Join

Ask the owner which folder the core may use on this machine (default
`Fridolin` in their home folder), create it, and decide the commands:
on a Windows PC none (`--no-exec`), unless the owner names programs to
allow (`--allow PROG=PATTERN,…`; never a shell). Host facts and
notifications stay on.

Windows, in the owner's normal PowerShell (the state dir is then theirs,
`%LOCALAPPDATA%\fridolin-node`), joining without the service first:

```powershell
New-Item -ItemType Directory -Force "$env:USERPROFILE\Fridolin" | Out-Null
& $fridolin node join fj-… --core "${core}:8766" --no-service --no-exec --root "$env:USERPROFILE\Fridolin:rw"
```

then the service, in one elevated step, with the state dir named (an
administrator account may have another profile):

```powershell
$state = "$env:LOCALAPPDATA\fridolin-node"
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','install','--state-dir',"`"$state`""
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','start','--state-dir',"`"$state`""
```

macOS and Linux, in one line (it installs and starts the user service):

```sh
mkdir -p ~/Fridolin
fridolin node join fj-… --core fridolin-core.<tailnet>.ts.net:8766 --no-exec --root ~/Fridolin:rw
```

`join` prints the node's id.

### 6. Approve on the core

On the Nodes page the owner already has open: **Approve device**, then
**Approve commands** (the second approves what the node offers: the
folder, host facts, notifications). Or on the Mac:

```sh
docker exec fridolin-core fridolin nodes approve <id>
docker exec fridolin-core fridolin nodes approve <id> --commands
```

### 7. Verify

```powershell
& $fridolin node status --state-dir $state     # Service fridolin-node: running
& $fridolin doctor --node --state-dir $state   # Tailscale, joined, core reachable, service
& $fridolin setup-machine --node --state-dir $state
```

On the core, `fridolin nodes` (or the Nodes page) shows the node online,
device and commands approved. In a direct chat with the bridge, `!fleet`
lists it.

### Rollback

On the core: `fridolin nodes revoke <id>` (or **Revoke**). On the machine:
`fridolin node stop`, `fridolin node uninstall` (Windows: elevated, with
`--state-dir`), then delete the state dir; uninstall Tailscale if the
owner wants. Joining again later makes a new device with a new code.

### Never

- print `node.key`, the dashboard token or a Tailscale auth key, or ask
  the owner to type a secret into the chat;
- allow a shell or `powershell`/`cmd` with `--allow` (the node refuses
  it anyway) or allow programs the owner did not name;
- run a second core, or touch the core's `bridge.db`: joining a node
  changes nothing on the core but its node registry.

## Join a machine, in short

1. On the core: `fridolin nodes join-code` (or **Nodes → New join code**
   in the dashboard). It prints a single-use code, valid for 10 minutes,
   and the lines to paste (sh and PowerShell):

   ```
   fridolin node join fj-abcd… --core fridolin-core.example.ts.net:8766
   ```

   On the tailnet the address is the core's MagicDNS name once it joined,
   so it resolves on every device of the tailnet. The core keeps only the
   code's SHA-256 in `nodes/nodes.json`.
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
| `fride` | `fride.*` | FrIDE on the node, while it answers: see [FrIDE](#fride) below |
| `android-build` (`android_build`) | `android.build`, `android.read`, `android.drop` | builds an Android app with Gradle, while the node has a JDK 17+ and the Android SDK: see [android-build](#android-build) below |

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

## android-build

A node with a JDK 17 or newer and the Android SDK builds the Plaud Sync
phone app for a core that has neither, such as the fridolin-core
container ([PLAUD.md](PLAUD.md#through-your-site)). The agent looks for
them as the bridge does: `JAVA_HOME` and `ANDROID_HOME` (or
`ANDROID_SDK_ROOT`) of its environment, else where macOS, Homebrew,
Android Studio and Linux distributions put them, taking the newest JDK
the app's Gradle runs on (17 to 23 before newer ones). It declares
`android_build` (with the JDK's version and the SDK's platforms) while it
finds them, looks again every five minutes and before each build, and
sends the core a `declare` when that changes, as it does for FrIDE. The
owner approves it once with `fridolin nodes approve <id> --commands`;
the approval covers it every time it comes back. `"android_build": false`
in `node.json` keeps a node from offering it; `capabilities.android_build`
is refused, since it is never declared by hand. Nodes on Windows do not
offer it: the app's source has no `gradlew.bat`.

**What approving it means.** `android.build` takes a gzip'd tar of an
Android project (at most 16 MiB, 64 MiB and 4096 files unpacked; regular
files and folders only, none outside the project) and up to 32 build
properties (`name=value`, no control characters, backslashes or quotes).
The node unpacks it into a new temp folder, writes the properties to a
file there (0600) and runs `gradlew --no-daemon assembleDebug` with
`-PfridolinConfig` naming that file, the build directory and Gradle's
project cache in the same folder, and no Kotlin daemon, with a timeout of
20 minutes. Gradle runs the project's build scripts, which are code: the
core can have the node run what it sends, unsandboxed, as the agent's
user, with the agent's Gradle home (`~/.gradle`) for downloads. Approve
it on nodes of a core you trust as much as the node itself.

When the build ends the node copies `app/outputs/apk/debug/app-debug.apk`
into `android-transfers/` in its state dir (0600) and deletes the temp
folder, the properties with it: the Plaud developer keys and the pairing
code never stay in a build on the node, and are never logged. The core
reads the APK with `android.read` in 8 MiB chunks, checks its size and
SHA-256, and removes it with `android.drop`; an APK nobody fetched is
removed after an hour, and all of them when the agent starts. A build
that fails answers with Gradle's error and the last 40 lines of its
output. One build runs at a time on a node.

**Placement.** The Plaud card builds on this machine when it has the
toolchain. Else it takes the online nodes with `android-build` approved
and offered: those tagged `android` or `android-build` first
(`fridolin nodes tags <id> android`), then the one with the most free
memory. A lockdown refuses the build, as it refuses `exec`.

## FrIDE

FrIDE is the coding IDE; under fridolin it is where every change to code
goes (PLAN.md, Workstream E, milestone M6). FrIDE runs on the node in
companion mode, and the node agent is its companion: it holds FrIDE's
token, calls FrIDE's loopback API for the core, and relays FrIDE's thread
news. FrIDE's own docs/COMPANION.md describes its side.

On the node, after `fridolin node join`:

```bash
fride serve --under-fridolin            # FrIDE's API stays on 127.0.0.1
fridolin node fride pair --url http://127.0.0.1:7777
```

`pair` runs `fride pair-companion --json` for a code (`--code` gives one
instead; `--fride PATH` and `--fride-home DIR` say which FrIDE), trades it
for FrIDE's token, which it keeps in the state dir as `fride.token`
(0600), and gives FrIDE a webhook on a loopback port of the agent's own,
which `node.json` keeps with FrIDE's address:

```json
"fride": {"url": "http://127.0.0.1:7777", "webhook": "127.0.0.1:53817", "base": "/Users/you/repos"}
```

`base` (`pair --base DIR`) is the folder the core's `fride_repo_add`
clones into or makes new repositories in. It must be one of FrIDE's
`baseFolders`; without it the node takes FrIDE's only base folder, read
from FrIDE's settings at each call, and refuses while FrIDE has several.

A running agent reads both again every 30 seconds; no restart is needed.
`fridolin node fride status` says whether FrIDE answers.

The node declares the capability `fride` only while FrIDE answers
`/api/companion/info` with the token: it asks every 30 seconds and sends
the core a `declare` when that changes, so `fride` appears in the
registry when `fride serve` starts and goes when it stops. The owner
approves it once (`fridolin nodes approve <id> --commands`); the
approval covers it every time it comes back, whatever port or
repositories FrIDE reports. `capabilities.fride` in `node.json` is
refused: FrIDE is never declared by hand.

| Method | FrIDE's API | |
| --- | --- | --- |
| `fride.task` | `POST /api/companion/tasks` | hand FrIDE's inbox a message |
| `fride.status` | `GET /api/companion/threads/<n>`, and the task's | a thread, with its task |
| `fride.reply` | `POST /api/companion/threads/<n>/reply` | answer a thread |
| `fride.task.info`, `fride.close` | `GET /api/companion/tasks/<t>`, `POST …/close` | a task; close it (rationale, agents and stack stopped) |
| `fride.mem` | `GET /api/companion/mem` | may a stack start now |
| `fride.export`, `fride.read` | `GET /api/companion/tasks/<t>/export` | stage the task's bundle on the node, then read it in 8 MiB chunks |
| `fride.write`, `fride.import` | `POST /api/companion/tasks/import` | write a bundle to the node in chunks, then import it |
| `fride.drop` | | remove a staged bundle |
| `fride.delete` | `DELETE /api/tasks/<t>` | remove a task's workspace |
| `fride.repo_clone` | `git clone`, then `POST /api/config` | clone a repository into the base folder |
| `fride.repo_new` | `git init`, a commit, `gh repo create`, then `POST /api/config` | make a new repository in the base folder |

**Repositories.** `fride.repo_clone` and `fride.repo_new` stay inside
the base folder (`fride.base`, see above): the name is one folder name
(letters, digits, `.`, `_`, `-`, not starting with `.` or `-`), the
folder must not exist yet, and one that a failed clone or `git init`
leaves is removed again. A clone takes an `https://` or `ssh://` URL or
`user@host:path`, never a local path, another transport or a URL with
credentials; the node's own git credentials are used. A new repository
gets `git init --initial-branch=main` and a README as its initial
commit and, when asked, `gh repo create <name> --private` (or
`--public`) `--source . --remote origin --push`; if gh fails, the
repository stays on the node and the answer says why. git and gh are
found by absolute path (the PATH, else `/opt/homebrew/bin`,
`/usr/local/bin`, `/usr/bin`), run without a shell and without
prompts, each step with a timeout (a clone 10 minutes, gh 3, the rest
one) and its output cut at 16 KiB. Then the node asks FrIDE to read its
config and base folders again (`POST /api/config`, what **Re-check**
in its Settings does), so FrIDE knows the repository at once, and
sends the core its new declaration.

A bundle travels in chunks inside protocol messages, staged under
`fride-transfers/` in the state dir and checked by size and SHA-256 on
the core; staged bundles older than a day are removed.

FrIDE's webhook reaches the agent at `http://<webhook>/fride`. The agent
takes a POST from this machine without an `Origin`, whose
`X-Fride-Signature` is `sha256=` and the HMAC-SHA256 of the body keyed
with the SHA-256 of the token; anything else is refused. It sends the
event to the core as an `event` message (kind `fride`), up to 256 waiting
while the core is away (FrIDE retries what is refused while they wait).
The core takes a node's events only while its `fride` is approved.

### On the core

With `nodes.enabled`, the core brain (`role: core`) gets five tools, and
its prompt one rule: anything that changes code goes to FrIDE through
`fride_task`; it never edits a repository itself, and adds a repository
FrIDE does not have with `fride_repo_add` first.

| Tool | |
| --- | --- |
| `fride_task(message, repos?, node?)` | hands the work to FrIDE and returns fridolin's thread number (FrIDE #3) |
| `fride_status(thread?)` | a thread as FrIDE has it now; without a thread, every thread |
| `fride_reply(thread, text)` | passes the owner's answer on |
| `fride_move(thread, node)` | as `!move` |
| `fride_close(thread)` | closes the task and archives its workspace |
| `fride_repo_add(name, url?, new?, github?, private?, node?)` | clones `url`, or with `new` makes the repository (on GitHub, private, unless `github` or `private` is false), on a node with FrIDE |

**Placement.** `node` is a node's id, name or one of its tags; without it
any node will do. The core takes the online nodes whose `fride` is
approved and offered, that match, and whose FrIDE knows the repositories
asked for, asks each FrIDE's memory check (`fride.mem`) and hands the
work to one that may start a stack, the one with the most free memory
first. When none can, the work waits, the chat is told why, and it is
offered again every 30 seconds and whenever a node's FrIDE comes up; the
chat hears when it found a node.

**News.** `bridge.db` (`fride_threads`) keeps each thread: the chat and
brain that started it, the node, FrIDE's thread there, the task and its
branch. FrIDE's news arrives in that chat: a question with the question
(the owner answers there, and the core brain passes the answer on with
`fride_reply`), `started` and `done` with the task and branch, `done`
with the rationale's path, `failed`, and everything else as an update.
The lines go into the core brain's history as FrIDE's, so its next run
sees what FrIDE asked. FrIDE says `done` at its next look at a closed
task, every 15 seconds.

**Moving.** `!move <thread> <node>` in a direct chat, or `fride_move`:
the task is exported from the node it is on with its agents stopped,
kept in the vault as `vault/workspaces/moves/<task>.bundle`, imported on the
target (as `<task>-moved` when the id is taken there; its agent is
reopened if it was running) and removed from the source. The thread
keeps its number. A moved task has no FrIDE thread on its new node
until the next `fride_reply`, which starts one there naming the task;
news of the source's old thread is dropped.

**Closing.** `fride_close` closes the task in FrIDE (the rationale is
written, agents and stack stop), exports it into the vault as
`vault/workspaces/<task>.fride.tar.gz` (the vault's path, which `fridolin
vault get` takes), where the vault's policy lets it go cold at once and,
with the vault running, it is uploaded to the cold tier right away; checks
that the archive is a whole gzip'd tar, and only then removes the
workspace on the node with `DELETE /api/tasks/<task>?force=1` (force:
the rationale and any uncommitted work are in the archive). The branch
stays in the repositories.

**Adding a repository.** `fride_repo_add` takes the online nodes with
`fride` approved that match `node`; when one's FrIDE lists the
repository already it says so and does nothing, else it calls
`fride.repo_clone` or `fride.repo_new` on the first.

**Policy.** `fride_task`, `fride_reply`, `fride_move`, `fride_close`
and `fride_repo_add` are write actions of M5's policy (`App.guarded`): a lockdown refuses them,
as it refuses `!move`, and so does a run whose risk budget is read;
`fride_status` only reads. Every call is a `tool_call` line in the audit
log.

**Not yet.** The
dashboard shows the FrIDE address each node reports and links it; FrIDE's
UI is on the node's loopback, so the link works on the node itself until
the node agent publishes it over the tailnet. `fridolin doctor`'s
`nodes-fride` warns about nodes whose approved FrIDE does not answer.

## The service

`fridolin node install | start | stop | status | uninstall` manage the
node service through [kardianos/service](https://github.com/kardianos/service):

| OS | Service |
| --- | --- |
| macOS | launchd user agent `ai.fridolin.node` (the bridge's is `ai.fridolin.bridge`), logs in the state dir |
| Linux | systemd user unit `fridolin-node`, restarted always |
| Windows | Windows Service `fridolin-node`, started at boot, restarted after a failure |

The service runs `fridolin node run --state-dir <dir>` with the binary that
installed it, so install from where the binary stays. On Windows the
service runs as LocalSystem: `install`, `start`, `stop` and `uninstall`
need an elevated PowerShell (`Start-Process … -Verb RunAs`), and the
binary belongs in `%ProgramFiles%\fridolin\`, where only administrators
may replace it ([Joining a machine](#joining-a-machine)). On a machine
that joined a core and has no bridge config, `fridolin doctor` checks the
node: Tailscale, `node.json` and the device key, the core's node port,
the service (`--node` and `--state-dir` say so explicitly). A revoked node's
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
   The node may also send, on its own, `event` (news, such as FrIDE's,
   with a `kind`) and `declare` (what it offers now, when FrIDE comes or
   goes); the core answers a `declare` with `state`.
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
helpers (`Exec`, `List`, `ReadFile`, `WriteFile`, `HostInfo`, `Notify`,
and `Fride…` for FrIDE, with `FrideExport` and `FrideImport` streaming a
bundle in chunks) for the parts that place work on nodes; `Options.OnEvent`
and `OnDeclare` hear what nodes report.
