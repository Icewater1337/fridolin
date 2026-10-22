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
getting a join code from the core (`!join` in their WhatsApp chat with
the bridge), and approving the node (`!approve <id>` there, or the Nodes
page). None of them needs the dashboard token on the new machine. The
agent opens what needs a browser for the owner, then waits and checks. `fridolin setup-machine --node` says at any point what
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

The default needs nothing of the core machine and no token: ask the
owner to send `!join` to Fridolin in their own WhatsApp chat with it, and
to paste the code it replies with (or the whole line for this OS) here.
The reply has the code, the lines to paste for sh and for PowerShell,
and the core's node address. The code is single-use, expires in 10
minutes and grants nothing until the owner approves the node, so it may
go through the chat.

`!join` (like `!dashboard`, `!approve` and `!revoke`) answers only the
owner — a number in `allowed_numbers` — and only in their direct chat,
never in a group; each of `!join` and `!dashboard` answers 5 times an
hour, and every request, refused or not, goes into the audit log (kinds
`node_join_code`, `dashboard_link`, `node_approved`, `node_revoked`)
without the code.

Second choice, the dashboard from this machine: the owner sends
`!dashboard`, and opens the tailnet link it replies with here (or the
agent opens it for them):

```powershell
Start-Process "http://fridolin-core.<tailnet>.ts.net/#login=…"   # the link from !dashboard
```

(`open` / `xdg-open` elsewhere.) The link works once, within 10 minutes,
and signs this browser in for 30 days; then **Nodes → New join code**.
On the core machine, `fridolin open --print --tailnet` prints the same
kind of link (`docker exec fridolin-core fridolin open --print
--tailnet` for the container core). The core serves its dashboard on the
tailnet with HTTPS on 443 when the tailnet issues certificates, else plain
HTTP on 80 (inside WireGuard), and the link says which; the session
cookie is HttpOnly and SameSite=Strict, and Secure only over HTTPS, so
that a browser keeps it over plain HTTP. A browser that opens the
dashboard without a link gets the sign-in page, which asks for the
token: don't go there, send `!dashboard` instead.

The last choice, on the Mac mini (or by an agent there):

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

When the node joins, the core tells the owner in their WhatsApp chat:
"*laptop* (Windows) wants to join as a node — approve it on the Nodes
page or reply `!approve <id>`". `!approve <id>` approves the device and
what it offers (the folder, host facts, notifications) in one go, and is
audited; `!revoke <id>` shuts it out instead. On the Nodes page the same
is **Approve device**, then **Approve commands**. Or on the Mac:

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

On the core: `!revoke <id>` in the owner's chat, `fridolin nodes revoke
<id>` or **Revoke**. On the machine:
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

1. Send `!join` to the bridge in your WhatsApp chat, or on the core run
   `fridolin nodes join-code` (or **Nodes → New join code** in the
   dashboard). It gives a single-use code, valid for 10 minutes,
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

   Or both at once: reply `!approve <id>` to the message the bridge sends
   you when the node joins.

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
| `notify` | `notify` | a desktop notification: `osascript` on macOS, `notify-send` on Linux, a toast on Windows (see below) |
| `fride` | `fride.*` | FrIDE on the node, while it answers: see [FrIDE](#fride) below |
| `android-build` (`android_build`) | `android.build`, `android.read`, `android.drop` | builds an Android app with Gradle, while the node has a JDK 17+ and the Android SDK: see [android-build](#android-build) below |
| `backup` | `fs.roots`, `fs.browse`, `backup.*` | keeps the core's restic repositories in folders the owner picks: see [Backups](#backups) below |
| `agent`, `terminal` | `agent.*`, `terminal.*` | a Claude Code or Codex session, and a shell, in folders the owner granted: see [Workstation](#workstation) below |

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

**Notifications on Windows.** The node shows a toast (a `ToastGeneric`
toast through Windows PowerShell 5.1's `ToastNotificationManager`, listed
under Windows PowerShell in Windows' notification settings). The title
and text never go into the script as text: they travel as base64 inside
an `-EncodedCommand` and become XML text nodes. The node service runs as
LocalSystem in session 0, which has no desktop, so the agent starts
PowerShell as the signed-in user in their session (the console's session
first, else the first active one, a Remote Desktop session say). When
nobody is signed in it only logs the notification and answers
`{"shown": false, "method": "log", "reason": "nobody is signed in to Windows"}`.
Focus assist (Do not disturb) and turning off PowerShell's notifications
in Settings → System → Notifications hide toasts, without an error.

**fs roots.** A path in a call is absolute. It must lie under a granted
root, as written or as the root's symlinks resolve, and is then opened
through Go's `os.Root`, which refuses every way out of the root, `..` and
symlinks included.

`fridolin node join` flags set these: `--allow PROG`, `--allow PROG=`,
`--allow PROG=RE,RE`, `--no-exec`, `--root PATH`, `--root PATH:rw`,
`--backup`, `--backup-root PATH`, `--workstation`, `--workstation-root
PATH`, `--allow-home`, `--no-host`, `--no-notify`, `--name`.

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

On the node, after `fridolin node join` (the full setup, with a FrIDE of
its own for fridolin, a service and Windows through WSL2, is
[Setting up FrIDE on a node](#setting-up-fride-on-a-node)):

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

## Setting up FrIDE on a node

This is the runbook a coding agent follows on a node machine when the
owner says "set up FrIDE on this node" (AGENTS.md, path D). The machine
is a node already (path B: `fridolin node status` says the service runs,
and the core approved it). The agent does every step itself except the
ones only the owner can do: creating the Linux user in WSL, signing in
to Claude Code (and, optionally, GitHub), trusting the workspace folder
in Claude Code, and approving `fride` on the core. The agent opens each
of those for the owner, waits, and then checks.

The layout is the same on every OS, and the same as on the Mac mini:
one companion FrIDE for fridolin, separate from any FrIDE the owner runs
himself (`~/.fride`, port 7777, fleet mode). That one stays untouched.

| | |
| --- | --- |
| `~/fride-companion/src/fride` | FrIDE's source, which it is built from |
| `~/fride-companion/bin/fride` | the FrIDE binary |
| `~/fride-companion/home` | its `FRIDE_HOME`: `config.json`, the hash of the companion token, `serve.log` |
| `~/fride-companion/repos` | its only base folder; it starts with `fridolin` and `fride` |
| `~/fride-companion/workspaces` | its `workspaceRoot` |
| `http://127.0.0.1:17840` | its API and UI, on loopback only |

Below, `<fridolin-url>` is the clone URL of this repository
(`git remote get-url origin` in this checkout) and `<fride-url>` is
FrIDE's. Take FrIDE's from the owner, or from `git remote get-url origin`
in a FrIDE checkout they already have.

### Windows: FrIDE runs in WSL2

FrIDE does not build for Windows today: it locks files with
`unix.Flock` and runs agents in a Unix PTY. The backlog issue "FrIDE:
native Windows support" lists what is missing. So on Windows FrIDE runs
in WSL2 (Ubuntu), and the node agent stays the native Windows service it
already is. They meet on `127.0.0.1`. Windows reaches FrIDE in WSL
through WSL's localhost forwarding. FrIDE's webhook reaches the agent on
Windows only with WSL's *mirrored* networking (Windows 11 22H2 or
newer). Without mirrored networking everything works except the news: a
`fride_task` starts and `fride_status` reads it, but FrIDE's questions
and its `done` never reach the chat by themselves.

Every Linux command below runs from the agent's shell as
`wsl -d Ubuntu --exec bash -lc '…'`. Commands that need root add
`-u root`: WSL lets the Windows user be root in their own distribution
without a password, so the owner never types the Linux password for the
agent. Keep double quotes out of these commands, because Windows
PowerShell 5.1 passes them to native programs garbled. If the agent's
shell is Git Bash (Claude Code's shell on Windows), also set
`MSYS_NO_PATHCONV=1` so that Linux paths in arguments stay as written. Write
files (the config, the start script) with your own file tools through
`\\wsl.localhost\Ubuntu` followed by the Linux home folder (which
`wsl -d Ubuntu --exec bash -lc 'echo $HOME'` prints), with LF line
endings, rather than through `wsl` and echo.

1. **WSL and Ubuntu.** If WSL and Ubuntu are installed, `wsl -l -v`
   lists `Ubuntu` with VERSION 2. If not, run this in an elevated
   PowerShell (UAC prompt):
   `Start-Process wsl -Verb RunAs -Wait -ArgumentList '--install','-d','Ubuntu'`.
   If it asks for a restart, tell the owner; they restart and you
   resume afterwards. On its first start, Ubuntu opens a window that
   asks for a UNIX user name and password. **The owner** types them
   there, never in the chat. Wait until `wsl -d Ubuntu --exec whoami`
   prints the user. If `wsl -l -v` shows version 1, run
   `wsl --set-default-version 2`; if WSL says it is out of date, run
   `wsl --update`.

2. **Mirrored networking.** Ask first, because it changes how all of
   the owner's WSL distributions reach the network. On Windows 11
   22H2+, add the following to `%UserProfile%\.wslconfig` (create the
   file if it is missing, and keep what is already there):

   ```ini
   [wsl2]
   networkingMode=mirrored
   ```

   Then run `wsl --shutdown` and start Ubuntu again. On Windows 10,
   skip this step and tell the owner that FrIDE's news will not reach
   the chat (see above).

3. **Tools in Ubuntu.** Install the packages, clone FrIDE, install the
   Go version FrIDE's `go.mod` names from go.dev into `/usr/local/go`
   (Ubuntu's own Go is too old), and install Claude Code for the user
   rather than for root:

   ```powershell
   wsl -d Ubuntu -u root --exec bash -lc 'apt-get update && apt-get install -y git curl ca-certificates build-essential lsof nodejs npm wslu'
   wsl -d Ubuntu --exec bash -lc 'git clone <fride-url> ~/fride-companion/src/fride'
   wsl -d Ubuntu -u root --exec bash -lc 'v=$(sed -n s/^go.//p $(getent passwd 1000 | cut -d: -f6)/fride-companion/src/fride/go.mod); a=$(dpkg --print-architecture); rm -rf /usr/local/go; curl -fsSL https://go.dev/dl/go$v.linux-$a.tar.gz | tar -C /usr/local -xz && ln -sf /usr/local/go/bin/go /usr/local/bin/go && go version'
   wsl -d Ubuntu --exec bash -lc 'npm config set prefix ~/.local && npm i -g @anthropic-ai/claude-code && ~/.local/bin/claude --version'
   ```

   User 1000 is the one created in step 1. `node --version` must be
   18 or newer. If Ubuntu's Node is older, install Node from NodeSource
   or nvm and tell the owner which one you used. Ubuntu's `~/.profile`
   puts `~/.local/bin` on a login shell's PATH.

4. **Claude Code login.** Run `wsl -d Ubuntu` to open an Ubuntu window
   for the owner. Ask them to type `claude` there and follow the login.
   Claude opens the sign-in page in the Windows browser through
   `wslview` (from wslu). If no browser opens, Claude prints a URL: open
   it for the owner with `Start-Process URL`. Wait, then check with
   `wsl -d Ubuntu --exec bash -lc 'claude -p hi'`. Optionally, do the
   same for GitHub so FrIDE's agents can push branches and open pull
   requests: install `gh` with `apt-get install -y gh` as root, then the
   owner runs `gh auth login` in the Ubuntu window.

5. **Build FrIDE**:

   ```powershell
   wsl -d Ubuntu --exec bash -lc 'mkdir -p ~/fride-companion/bin ~/fride-companion/home ~/fride-companion/repos ~/fride-companion/workspaces && cd ~/fride-companion/src/fride && go build -o ~/fride-companion/bin/fride . && ~/fride-companion/bin/fride version'
   ```

6. **Starter repositories.** Clone them into the base folder, in the
   WSL file system. Never put them under `/mnt/c`: Windows drives are
   slow from WSL and lose Unix permissions.

   ```powershell
   wsl -d Ubuntu --exec bash -lc 'cd ~/fride-companion/repos && git clone <fridolin-url> && git clone <fride-url>'
   ```

7. **Config.** Write `config.json` in `~/fride-companion/home` (see "The
   config" below), then check it:

   ```powershell
   wsl -d Ubuntu --exec bash -lc 'export FRIDE_HOME=~/fride-companion/home; ~/fride-companion/bin/fride config check && ~/fride-companion/bin/fride doctor'
   ```

   `config check` must report no errors. `doctor` names anything that
   is missing. Its check *Claude Code trusts the workspaces* fails
   until the next step is done.

8. **Trust the workspace folder.** FrIDE's agents start in
   `~/fride-companion/workspaces`. Claude Code asks once whether it
   trusts a folder, and an agent stopped at that question does nothing.
   Ask the owner to run `cd ~/fride-companion/workspaces && claude` in
   the Ubuntu window, choose **Yes, I trust this folder**, and quit.
   `fride doctor` then passes that check.

9. **Run it at logon.** WSL stops a distribution a few seconds after
   the last `wsl.exe` using it ends, even if systemd services are
   running. A systemd unit alone would therefore not keep FrIDE up. A
   Scheduled Task covers both needs: at the owner's logon it starts
   `wsl.exe`, which runs `fride serve` and keeps Ubuntu up while it
   runs, and it restarts FrIDE when it ends. First write the start
   script `~/fride-companion/bin/fride-serve`, then make it executable
   with `wsl -d Ubuntu --exec bash -lc 'chmod +x ~/fride-companion/bin/fride-serve'`:

   ```sh
   #!/bin/sh
   export FRIDE_HOME="$HOME/fride-companion/home" SHELL=/bin/bash PATH="$HOME/.local/bin:/usr/local/go/bin:$HOME/go/bin:/usr/local/bin:/usr/bin:/bin"
   cd "$FRIDE_HOME" || exit 1
   exec "$HOME/fride-companion/bin/fride" serve --under-fridolin --port 17840 >>"$FRIDE_HOME/serve.log" 2>&1
   ```

   Then register the task as the owner. No elevation is needed for a
   task of their own. `conhost.exe --headless` runs `wsl.exe` without a
   console window:

   ```powershell
   $h = wsl -d Ubuntu --exec bash -lc 'echo $HOME'
   $action = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\conhost.exe" `
     -Argument "--headless $env:SystemRoot\System32\wsl.exe -d Ubuntu --exec $h/fride-companion/bin/fride-serve"
   $trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
   $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 `
     -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
   Register-ScheduledTask -TaskName 'FrIDE companion (WSL)' -Action $action -Trigger $trigger -Settings $settings `
     -Description 'FrIDE in companion mode for the fridolin node, http://127.0.0.1:17840' -Force
   Start-ScheduledTask -TaskName 'FrIDE companion (WSL)'
   ```

   Check that `(Invoke-WebRequest http://127.0.0.1:17840/ -UseBasicParsing).StatusCode`
   is 200 and that no window stayed open. If a console window does
   appear (an older Windows without `conhost --headless`), tell the
   owner it may be minimised but must not be closed. FrIDE runs only
   while the owner is signed in, so the node offers `fride` only then.

10. **Pair the node with FrIDE**, from the owner's normal PowerShell.
    `fride` is not on Windows' PATH, so the code comes from WSL and goes
    straight into `pair` without ever being printed:

    ```powershell
    $code = (wsl -d Ubuntu --exec bash -lc 'FRIDE_HOME=~/fride-companion/home ~/fride-companion/bin/fride pair-companion --json' | ConvertFrom-Json).code
    fridolin node fride pair --url http://127.0.0.1:17840 --code $code
    Remove-Variable code
    ```

    If the service was installed with a state dir other than the default
    (`$env:LOCALAPPDATA\fridolin-node`), pass the same `--state-dir`.
    Do **not** pass `--base`. FrIDE's base folder is a Linux path that
    the agent on Windows cannot reach, so `fride_repo_add` does not
    work on a Windows node yet. Add repositories in Ubuntu instead:
    `git clone` into `~/fride-companion/repos`, then **Re-check** in
    FrIDE's Settings (or restart the task). FrIDE and `fride_task` see
    them at once.

Then continue with "Approve and verify" below.

### macOS and Linux: FrIDE natively

This is the same setup as on the Mac mini, next to the owner's own FrIDE.

1. **Tools**: git, Go (the version FrIDE's `go.mod` names or a newer
   one; an older `go` fetches it), Node 18+ and Claude Code
   (`npm i -g @anthropic-ai/claude-code`), and lsof. On macOS:
   `brew install go git node`. On Linux: the packages from WSL's
   step 3, with `sudo`.
2. **Claude Code login**: the owner runs `claude` once and signs in;
   check with `claude -p hi`.
3. **Build**: run `git clone <fride-url>
   ~/fride-companion/src/fride` and
   `mkdir -p ~/fride-companion/{bin,home,repos,workspaces}`, then
   `go build -o ~/fride-companion/bin/fride .` in the clone.
4. **Starter repositories**: clone `fridolin` and `fride` into
   `~/fride-companion/repos`, as in WSL's step 6.
5. **Config**: write it as below, then run
   `FRIDE_HOME=~/fride-companion/home ~/fride-companion/bin/fride config check`
   and `fride doctor` the same way.
6. **Trust**: the owner runs `claude` in `~/fride-companion/workspaces`
   and chooses **Yes, I trust this folder**.
7. **Service.**
   - macOS: a launchd agent `~/Library/LaunchAgents/ai.fride.companion.plist`
     that runs `~/fride-companion/bin/fride serve --under-fridolin --port 17840`.
     It needs `RunAtLoad` and `KeepAlive`; `WorkingDirectory` and
     `FRIDE_HOME` set to `~/fride-companion/home`; `HOME`, `SHELL`, and a
     `PATH` that has `go`, `node` and `claude` (launchd's own PATH has
     none of them); and its log, `serve.log`, in `~/fride-companion/home`.
     Load it with
     `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ai.fride.companion.plist`.
   - Linux: a systemd user unit
     `~/.config/systemd/user/fride-companion.service` with the same
     command, `Environment=FRIDE_HOME=…` and `PATH=…`, and
     `Restart=always`. Start it with
     `systemctl --user enable --now fride-companion`. For it to run
     without a login, run `loginctl enable-linger` (ask first).
8. **Pair**:

   ```bash
   fridolin node fride pair --url http://127.0.0.1:17840 \
     --fride ~/fride-companion/bin/fride --fride-home ~/fride-companion/home \
     --base ~/fride-companion/repos
   ```

### The config

`config.json` in `~/fride-companion/home` is the same on every OS. It has one
profile, for FrIDE's `stack up`, which builds fridolin from the task's
worktree and runs it in dev mode: no WhatsApp, with a scratch data dir
and `HOME` under the workspace. FrIDE needs at least one profile; add
more with the owner later (see FrIDE's SETUP.md).

```json
{
  "version": 1,
  "name": "fridolin companion",
  "workspaceRoot": "~/fride-companion/workspaces",
  "baseFolders": ["~/fride-companion/repos"],
  "coreRepos": ["fridolin"],
  "maxSlots": 2,
  "requirements": [
    { "name": "go", "command": "go version", "why": "fridolin and fride are Go programs" }
  ],
  "defaultProfile": "dev",
  "profiles": {
    "dev": {
      "description": "fridolin from the task's worktree in dev mode: no WhatsApp, scratch data dir and HOME under the workspace",
      "entry": "bridge",
      "services": {
        "bridge": {
          "repo": "fridolin",
          "run": "mkdir -p {{workspace}}/.dev/bridge/home && printf '{\"data_dir\":\"%s\",\"http_listen\":\"127.0.0.1:%s\",\"allowed_numbers\":[],\"allow_any_dm\":false,\"sandbox\":{\"enabled\":false}}' {{workspace}}/.dev/bridge {{port}} > {{workspace}}/.dev/bridge/config.json && go build -o {{workspace}}/.dev/bin/fridolin ./cmd/fridolin && exec {{workspace}}/.dev/bin/fridolin --no-whatsapp --config {{workspace}}/.dev/bridge/config.json",
          "port": 18810,
          "env": {
            "HOME": "{{workspace}}/.dev/bridge/home",
            "GOPATH": "{{home}}/go",
            "GOCACHE": "{{home}}/.cache/fride-go-build",
            "FRIDOLIN_CONFIG": "{{workspace}}/.dev/bridge/config.json",
            "BROWSER": "none"
          },
          "health": "http://127.0.0.1:{{port}}/health",
          "startTimeout": 600
        }
      }
    }
  },
  "companion": { "enabled": true }
}
```

### Approve and verify

1. **Approve on the core.** Within 30 seconds of FrIDE answering and
   the node being paired, `fride` appears in what the node offers. It
   then waits for the owner, who approves it with **Approve commands**
   on the core's Nodes page (open
   `https://fridolin-core.<tailnet>.ts.net/#/nodes` for them) or with
   `fridolin nodes approve <id> --commands` on the core. Where the core
   has a chat command for approving, such as `!approve`, that works too.
2. **Verify**, one check at a time:
   - `fride doctor` (in WSL, with `FRIDE_HOME=~/fride-companion/home`)
     shows no failed check;
   - `fridolin node fride status` says FrIDE answers, and lists
     `fridolin` and `fride` among its repositories;
   - the core lists the node with `fride` (in `fridolin nodes`, on the
     Nodes page, or with `!fleet` in a direct chat);
   - a tiny task from the core works. The owner writes to fridolin in
     a direct chat, for example: "ask FrIDE on <node> to add a line
     'Set up by FrIDE on <node>' to the fride repository's README on a
     branch, without pushing". FrIDE #n should come back with
     `started`, then with `done` and the branch. On Windows without
     mirrored networking, ask for `fride_status` instead. Then have the
     core close the task (`fride_close`), so the test leaves no
     workspace behind.

**Rollback.**
- Windows: `Unregister-ScheduledTask -TaskName 'FrIDE companion (WSL)'`,
  then `wsl -d Ubuntu --exec pkill -f fride-companion/bin/fride`.
- macOS: `launchctl bootout gui/$(id -u)/ai.fride.companion`.
- Linux: `systemctl --user disable --now fride-companion`.

The node stops offering `fride` within 30 seconds. `fride.token` and
the `fride` entry in `node.json` can stay. Removing `~/fride-companion`
deletes the workspaces and any uncommitted work in them, so ask first.

## Backups

A node with the capability `backup` lends its disks to the core's
[node targets](VAULT.md#node-targets): restic repositories the core keeps
in folders on it. In `node.json`:

```json
"backup": {"roots": ["D:\\"]}
```

`roots` are the drives or folders it lends; none (`"backup": {}`) is every
local drive and volume. `fridolin node backup on [--root PATH]…` and
`fridolin node backup off` edit it (restart the agent after), as `node
join --backup [--backup-root PATH]…` does when joining. The owner
approves it like `exec` and `fs`, with the second approval; lending more
roots later waits for that approval again, and the node keeps serving
what was approved meanwhile.

The folders are not in `node.json`: the owner picks one for each target
on the Vault page, and the core adds it to the node's approval
(`approved.backup.folders`, kept across later approvals, removed with the
target, gone with a revocation). A folder must lie under a lent root.

| Method | |
| --- | --- |
| `fs.roots` | the drives and volumes, with their free space: Windows' drive letters, macOS's `/` and `/Volumes/*`, Linux's `/` and the mounts of disks (`/media`, `/run/media`, `/mnt`, network shares); the home folder first; or the lent roots |
| `fs.browse` | the names of the folders in a folder under a lent root (hidden ones on request), and whether it holds a restic repository; never a file, never content |
| `backup.info` | whether a backup folder exists and holds a repository, its size and the space left |
| `backup.create` | make a backup folder and restic's subfolders |
| `backup.stat`, `backup.list`, `backup.read`, `backup.write`, `backup.delete` | restic's files in an approved folder, by restic's own names only (`config`, or `data`, `keys`, `locks`, `snapshots`, `index` with a 64-digit hex name), in chunks of up to 8 MiB; a write lands in `.fridolin-uploads/` and appears under its name only after its size and SHA-256 check out, never replacing a file with other content; `config` is never deleted |

The node never runs restic and never sees a repository's password: the
core runs restic and sends restic's encrypted files. The repository
methods are the core's backups' own; the dashboard's generic node call
refuses them. Both sides check each call against the approved folders.

## Updates

Once a node is connected, the core updates its fridolin to the core's own
version: from the dashboard (Nodes → **Update to <version>** on the
node's card), with `fridolin nodes update <id>` (or `--all` for every
connected node that runs an older version), or through the core brain's
`node_update(node)` tool (write risk, refused in a lockdown).

**The capability.** A node agent of this version offers `update`
(`"update": false` in `node.json` turns it off). Like every capability it
waits for the owner's approval: Approve what it offers on the card, or
`fridolin nodes approve <id> --commands`. A node whose fridolin predates
updates does not offer it; update it by hand once (build or copy the new
fridolin onto it as under [Build fridolin](#3-build-fridolin), then
`fridolin node stop` and `fridolin node start`), approve `update` when it
connects, and the core updates it from then on.

**The builds.** The core sends the build of its own version for the
node's OS and architecture: darwin, linux and windows on amd64 and
arm64, gzip'd, with a `manifest.json` of their SHA-256 and size. The core
image carries them in `/opt/fridolin/node-binaries/`; a native install
builds them with `make node-binaries` (into `bin/node-binaries`; the
core looks for `node-binaries` beside its own binary, so for the service
`make node-binaries NODE_BINARIES=~/.local/lib/fridolin/node-binaries`,
or name the folder in `$FRIDOLIN_NODE_BINARIES`). Builds of another version than the core's are
not sent.

**What the node checks.** The core streams the binary in chunks of up to
8 MiB and signs `(version, os, arch, sha256)` with its update key
(`nodes/update.key` on the core, an ed25519 key made the first time a
node connects). The node pins the core's public key when it joins
(`core.pub` in its state folder); a node that joined before keeps the key
of the core it is connected to at its first approved update. It refuses a
binary whose signature, platform, size or SHA-256 do not match, and one
signed by another key than the pinned one.

**The swap.** The node unpacks the binary beside its own, moves its
running fridolin to `<binary>.prev` (on Windows it renames the running
exe aside, which Windows allows) and the new one into place, writes
`update.json` and restarts its service. The new version is on trial for
5 minutes: if it does not connect to the core in that time, or dies three
times before it does, the node puts `.prev` back and restarts into it,
and the core records the update as failed. `fridolin node rollback` puts
`.prev` back by hand.

**When not.** The core does not update a node that runs a FrIDE task of
the core's or a backup to a folder on it (`--force`, or the dashboard's
confirmation, does it anyway), one that is offline, or one that runs a
newer version than the core (`--force` goes back).

**Auto updates** are off by default. `fridolin nodes auto-update <id> on`,
or the switch on the node's card, updates the node by itself when the
core runs a newer version than the node's: when it connects, and while it
is connected, never while it is busy. It only moves a node from a version
known to be older (by its number, or one the core itself ran before, in
`nodes/core-versions.json`), and leaves a version that failed alone until
the owner turns auto on again. `fridolin doctor` warns about approved
nodes that run an older version than the core, and about a last update
that failed.

## Workstation

A node in the workstation mode lets the core run a **Claude Code or Codex
session** (capability `agent`) and a **terminal** (capability `terminal`)
in folders its owner granted, so the core brain can say "look into
~/Projects/foo on the laptop and fix the build" and a session does it
there. It is the lightweight path: [FrIDE](#fride) stays the one for real
coding work (tasks, worktrees, branches to review), and the core brain's
prompt says when to use which.

Turn it on, on the node, then approve it on the core:

```bash
fridolin node workstation on --root ~/Projects --root ~/Scratch   # both capabilities
fridolin node workstation on --root ~/Projects --no-terminal      # agent sessions only
fridolin node workstation off
fridolin node stop && fridolin node start                         # the core hears it
fridolin nodes approve <id> --commands                            # on the core
```

or join with it: `fridolin node join … --workstation` (in the `--root`
folders) or `--workstation-root PATH` (repeat it). In `node.json` it is

```json
"agent":    {"roots": ["/Users/you/Projects"]},
"terminal": {"roots": ["/Users/you/Projects"]}
```

**Folders.** A session's folder must lie under a granted root, as written
and with its symlinks resolved; both the core and the node check it. A
root is never the node's home folder or a folder above it, unless
`--allow-home` (`allow_home`), which grants the home folder itself and
never one above. Nor is a root a whole drive, a system folder (`/usr`,
`/etc`, `/System`, `C:\Windows`, …) or a place that holds keys and
credentials (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`,
`~/.git-credentials`, `~/.fridolin`, the node's state dir, every hidden
home folder named for secrets or tokens), or lies in one; a sandboxed
session may not read those either. The approval is two-stage like every other
capability, and a node that adds a folder waits for the owner again.

| Capability | Methods | What it does |
| --- | --- | --- |
| `agent` | `agent.start`, `agent.status`, `agent.reply`, `agent.stop`, `agent.list` | a Claude Code (`claude -p`) or Codex (`codex exec`) session in a folder, its events streamed back, its questions answered, stopped |
| `terminal` | `terminal.open`, `terminal.send`, `terminal.read`, `terminal.resize`, `terminal.close`, `terminal.list` | a shell in a folder on a pseudo-terminal (a PTY on macOS and Linux, ConPTY on Windows), its output kept (1 MiB) to be read from an offset |

At most 8 sessions of each kind run on a node; a terminal nobody used for
30 minutes is closed, and a session that ran for 2 hours is stopped.

### Agent sessions

The core brain starts one with `node_agent_run(prompt, node?, folder?,
provider?, model?)` and follows it with `node_agent_status`,
`node_agent_reply` and `node_agent_stop`; those tools exist only on the
core brain, and only while some node has `agent` approved (the terminal
tools likewise). The core numbers sessions (Agent #2). Each turn is one
run of the CLI; a turn that ends with a question (AskUserQuestion, or a
last line `QUESTION: …`) waits, the question reaches the chat that started
the session, and the person's answer goes back with `node_agent_reply`,
which resumes the provider's own session. When the session ends, the
chat hears its summary and what changed in the folder (`git status` in a
repository, else the files modified since it started).

**Irreversible steps.** A turn that says a step cannot be undone (a last
line `IRREVERSIBLE: …`: a push, a deploy, a delete outside the folder) or,
without a sandbox, a tool Claude Code's permission rules refused, waits
for an M5 approval poll in the chat. Only the owner's vote answers it:
the brain cannot (`node_agent_reply` refuses while the poll is open), and a
yes in a lockdown is a no.

**Sandbox, per OS.** Recorded in DECISIONS.md:

| OS | A session runs | Its permissions |
| --- | --- | --- |
| macOS | under `sandbox-exec` with a profile of its own: writes only in its folder, its temp folder and the provider's own state; the denied places above unreadable | `claude --dangerously-skip-permissions` (Codex: its own sandbox off), since the sandbox holds it |
| Linux | under bubblewrap (`bwrap`), the same confinement, where `bwrap` runs | as on macOS; without a working `bwrap`, as on Windows |
| Windows | without a sandbox | Claude Code's own permission checks (`--permission-mode acceptEdits`: edits in the folder), and what they refuse goes to the owner as an approval poll; Codex with `--sandbox workspace-write` |

The node reports which it uses (`sandbox` on the Nodes page and in each
session); a terminal runs in the same sandbox, or as an ordinary shell of
the node's user on Windows.

**Credentials.** A session uses the node's own Claude or Codex login: the
node owner's machine, logged in as they are. The core sends its own
`CLAUDE_CODE_OAUTH_TOKEN` only when the core brain passes `use_core_login`
for that one session, which it does only when the person asked for it;
the node puts it in that session's environment, never stores or logs it,
and forgets it when the session ends. Terminals never get a login token:
the node strips `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` and every
`FRIDOLIN_` variable from a shell's environment.

### Terminals

The core brain opens one with `node_terminal_open(node?, folder?)`, types
with `node_terminal_send` (Enter added unless `enter` is false), reads
with `node_terminal_read` (escape sequences left out) and closes it with
`node_terminal_close`. The dashboard's Nodes page has a **Terminal**
button on an online node that has `terminal` approved: it opens a shell in
a granted folder in a dialog and closes it with the dialog. The view is
line-oriented (output, a line to type, Ctrl-C, Tab); full-screen programs
such as an editor do not draw there.

### Audit, lockdown, housekeeping

Every session's start and end is a `node_session` line of the audit log
(node, folder, sandbox, who started it, how it ended, how many files
changed), and every command line typed into a terminal a `node_command`
line, with token-like strings masked. A lockdown refuses starting a
session, answering one and typing, and stops every session and terminal
that runs (the owner hears how many); reading goes on. Housekeeping stops
sessions and terminals that run on a node and that the core has no record
of, such as those a restart of the core forgot (`node-session` findings).

## The service

`fridolin node install | start | stop | status | uninstall` manage the
node service through [kardianos/service](https://github.com/kardianos/service):

| OS | Service |
| --- | --- |
| macOS | launchd user agent `ai.fridolin.node` (the bridge's is `ai.fridolin.bridge`), logs in the state dir |
| Linux | systemd user unit `fridolin-node`, wanted by `default.target`, restarted always |
| Windows | Windows Service `fridolin-node`, started at boot, restarted after a failure |

The service runs `fridolin node run --state-dir <dir>` with the binary that
installed it, so install from where the binary stays. On Windows the
service runs as LocalSystem: `install`, `start`, `stop` and `uninstall`
need an elevated PowerShell (`Start-Process … -Verb RunAs`), and the
binary belongs in `%ProgramFiles%\fridolin\`, where only administrators
may replace it ([Joining a machine](#joining-a-machine)). On a machine
that joined a core and has no bridge config, `fridolin doctor` checks the
node: Tailscale, `node.json` and the device key, the core's node port,
the service (`--node` and `--state-dir` say so explicitly), and on Linux
that the unit is enabled into `default.target.wants` and, as a warning
only, that the user lingers. A user's systemd starts `default.target` at
boot when the user lingers (`sudo loginctl enable-linger $USER`), and
otherwise at their first login; it never reaches `multi-user.target`,
the system's target. A unit installed before this was fixed was enabled
there and stayed off after a reboot: `fridolin node install`, `start`,
`join` and the agent itself (`node run`, so an update reaches it) move it
to `default.target`. A revoked node's
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
`Roots`, `Browse`, `BackupInfo`, `ApproveBackupFolder`,
and `Fride…` for FrIDE, with `FrideExport` and `FrideImport` streaming a
bundle in chunks) for the parts that place work on nodes; `Options.OnEvent`
and `OnDeclare` hear what nodes report.
