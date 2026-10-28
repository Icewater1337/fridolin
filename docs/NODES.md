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

A machine joins natively by default: one fridolin agent on it
([VISION.md](VISION.md#the-technology-for-each-role)), which can start
the VMs of FrIDE instances, keep backups and run the core in Docker when
the core moves there, and does what a container cannot (Apple builds,
USB devices, notifications, the workstation mode on host folders). The
[one-liner](#the-one-liner) that `!join` sends installs it. A [node in a
box](#node-in-a-box) is the fallback where Docker Sandboxes cannot run,
such as a machine without virtualization. Building fridolin from source,
steps 1 to 5 below, is the last resort.

This is the runbook a coding agent follows when the owner clones the
repository on a new machine and says "set this machine up as a node of my
fridolin core" (AGENTS.md, path B). The agent runs the one-liner in a
terminal the owner sees, or does every step itself, except the three
only the owner can do: signing in to Tailscale,
getting a join code from the core (`!join` in their WhatsApp chat with
the bridge), and approving the node (`!approve` there, or the Nodes
page). None of them needs the dashboard token on the new machine. The
agent opens what needs a browser for the owner, then waits and checks. `fridolin setup-machine --node` says at any point what
is done and what to do next; it only looks and may be run again and again.

The core must have `nodes.enabled` and `core.tailnet.enabled` on (the
owner's container core on the Mac mini has both): the node reaches it on
the tailnet at `fridolin-core.<tailnet>.ts.net:8766`.

### The one-liner

The owner sends `!join` to fridolin in their WhatsApp chat (or clicks
**Nodes → New join code**, or runs `fridolin nodes join-code` on the
core). A core that knows where its install scripts are published (an
image CI built, `FRIDOLIN_BOX_SOURCE`) answers with the native line
first:

```sh
curl -fsSL https://raw.githubusercontent.com/OWNER/fridolin/COMMIT/container/node.sh | sh -s -- \
  --core fridolin-core.TAILNET.ts.net:8766 --code CODE
```

On Windows, in PowerShell, not as administrator (the script asks for
that once, for the one step that needs it):

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/OWNER/fridolin/COMMIT/container/node.ps1))) `
  -Core fridolin-core.TAILNET.ts.net:8766 -Code CODE
```

The script is the one of the core's own commit. Add the folder to lend
and the commands: `--root ~/Fridolin:rw --no-exec` (PowerShell: `-Root
"$env:USERPROFILE\Fridolin:rw" -NoExec`); the script makes a folder that
does not exist yet. `node.sh` passes every option it does not know on
to `fridolin node join` (`--allow`, `--workstation`, `--name`,
`--no-service`…; `--dir` puts fridolin elsewhere); `node.ps1` takes
`-Root`, `-NoExec`, `-Allow`, `-Backup`, `-BackupRoot`, `-Workstation`,
`-WorkstationRoot`, `-Name` and `-StateDir`, and the rest in
`-JoinArgs`. Run it in a terminal the owner sees: it says what it will
do and asks before it changes anything (`--yes`, `-Yes`: it does not
ask), on the terminal, also when it came through the pipe (over SSH:
`ssh -t`). It never asks for a secret.

1. **Tailscale.** Where it is missing, the script installs it (macOS:
   `brew install --cask tailscale`, or it opens the download page and
   waits; Linux: Tailscale's install script, with sudo; Windows: `winget
   install --id Tailscale.Tailscale -e`, which shows a UAC prompt). Where
   the machine is not signed in, it runs `tailscale up` (Linux: with
   sudo), prints the sign-in link and opens it where there is a desktop;
   the owner signs in with the account of the core's tailnet. Then it
   waits until the core's address answers.
2. **fridolin.** It fetches the core's own build for this OS and
   architecture from the core, on the node port, with the join code:
   `GET /join/CODE/fridolin.sha256?os=OS&arch=ARCH` answers `<sha256>
   <size> <version>`, and `GET /join/CODE/fridolin?os=OS&arch=ARCH` the
   binary, from the builds the core keeps for [updates](#updates). It
   checks the binary's size and SHA-256 and installs nothing that does
   not match: on macOS and Linux as `~/.local/lib/fridolin/fridolin`,
   with `~/.local/bin/fridolin` linked to it and the one it replaces kept
   as `fridolin.prev`; on Windows as `%ProgramFiles%\fridolin\fridolin.exe`,
   where only administrators can change it.
3. **The join.** `fridolin node join CODE --core HOST:8766` with the
   options passed on, which spends the code. On macOS and Linux it
   installs and starts the node service as the owner (a launchd agent;
   a systemd user unit, after which the script offers to turn lingering
   on). On Windows it joins with `--no-service` as the owner; then one
   elevated step, one UAC prompt, copies `fridolin.exe` into Program
   Files (the one it replaces kept as `fridolin.exe.prev`) and runs
   `node install` and `node start` there. The state dir is
   `%LOCALAPPDATA%\fridolin-node`, or `%USERPROFILE%\.fridolin-node` when
   the script runs inside an app package such as the Claude desktop app
   ([Inside an app package](#inside-an-app-package)). Declining the UAC
   prompt leaves the node joined, the binary in
   `%USERPROFILE%\.fridolin-install-…` and the commands to finish in an
   administrator's PowerShell.

Then the core tells the owner that the node joined, and `!approve` lets
it in ([6. Approve on the core](#6-approve-on-the-core), [7.
Verify](#7-verify)). A code that ran out while the owner signed in to
Tailscale: `!join` again, and the same line with the new code.

The node port serves the binary only for a code that is valid and
unused, and the download does not spend it. Nothing there lists codes:
an unknown, used or expired code gets `403`, a platform other than
`darwin`, `linux` or `windows` on `amd64` or `arm64` gets `400`, and a
core without node binaries of its version (a native core without `make
node-binaries`) `503`. A host may ask 20 times in 10 minutes and all
hosts 100 times, and four binaries go out at once; beyond that, `429`.
WireGuard carries it on the tailnet, as it carries the nodes'
WebSocket; everything else on the port is as it was.

The numbered steps below are the native install by hand, building
fridolin from source: for a core that offers no native line (a local
build), or a machine the scripts do not cover.

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
Linux: `sudo apt install -y golang git make` (or `sudo dnf install -y
golang git make`) and `curl -fsSL https://tailscale.com/install.sh | sh`.
Ubuntu 24.04's `golang` is Go 1.22, which is fine: any Go 1.21 or newer
downloads the version `go.mod` names on the first build (it needs the
network; without it, the tarball from go.dev/dl).

A machine that is to be a [core host](#core-host) (the core may move to
it) needs more: Docker (Linux: `sudo apt install -y docker.io
docker-compose-v2`, and the user in the `docker` group), a current GitHub
`gh`, which checks the core's image before it is pulled (Linux: from
GitHub's apt repository, <https://cli.github.com>; Ubuntu's own is too
old), and on Ubuntu `sudo fridolin core apparmor` once after the build
([CORE.md](CORE.md#the-core-moves-itself), "Before"). The first move to
it pulls the core's image, several GB.

### 2. Tailscale

The machine must be on the owner's tailnet, logged in as the same account
as the core. Run `tailscale up` (Linux: `sudo tailscale up`); it prints a
login link and waits. Open the link for the owner (`Start-Process URL` on
Windows, `open URL` on macOS, `xdg-open URL` on Linux; the Windows and
macOS apps may open it themselves), ask them to sign in with the account
the core's tailnet belongs to, and wait. On a server without a desktop
(a cloud VM reached over SSH) open the link on the machine the owner sits
at instead; `sudo tailscale up --hostname NAME` names the device. Then:

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

On macOS and Linux, `make install` copies it to `~/.local/bin`, which
the user service runs. On Linux that folder is on the PATH of a login
shell only, and only once it exists: over a plain `ssh host 'command'`,
run `~/.local/bin/fridolin` or `bash -lc 'fridolin …'`.

### 4. A join code from the core

The default needs nothing of the core machine and no token: ask the
owner to send `!join` to Fridolin in their own WhatsApp chat with it, and
to paste the code it replies with (or the whole line for this OS) here.
The reply has the code, the lines to paste for sh and for PowerShell,
and the core's node address. The code is single-use, expires in 10
minutes and grants nothing until the owner approves the node, so it may
go through the chat.

`!join` (like `!dashboard`, `!approve`, `!deny` and `!revoke`) answers only the
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

Windows, joining without the service first, with the state dir named
in every command (an administrator account may have another profile).
Which one depends on where the commands run:

```powershell
# In the owner's PowerShell, started from the Start menu: the default.
$state = "$env:LOCALAPPDATA\fridolin-node"
# Inside the Claude desktop app (an agent's commands there), or any other
# app installed from the Microsoft Store or as an MSIX package: outside AppData.
$state = "$env:USERPROFILE\.fridolin-node"
```

Inside such an app, Windows keeps what the commands write under AppData
in the app's own copy, which the service never sees ([Inside an app
package](#inside-an-app-package)); `join` refuses a state dir it would
write there, before it spends the code, and names the one to use. The
home folder, like AppData in it, is readable only by the owner, SYSTEM
and the administrators.

```powershell
New-Item -ItemType Directory -Force "$env:USERPROFILE\Fridolin" | Out-Null
& $fridolin node join fj-… --core "${core}:8766" --no-service --no-exec --root "$env:USERPROFILE\Fridolin:rw" --state-dir $state
```

then the service, in one elevated step:

```powershell
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','install','--state-dir',"`"$state`""
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','start','--state-dir',"`"$state`""
```

macOS and Linux, in one line (it installs and starts the user service):

```sh
mkdir -p ~/Fridolin
fridolin node join fj-… --core fridolin-core.<tailnet>.ts.net:8766 --no-exec --root ~/Fridolin:rw
```

`join` prints the node's id.

On Linux the service is a systemd user unit, which stops when the owner
logs out and does not start at boot unless the user lingers. On a
server, or any machine that should serve while nobody is logged in, ask
first, then run `sudo loginctl enable-linger $USER`. `fridolin doctor
--node` warns while it is off.

### 6. Approve on the core

When the node joins, the core tells the owner in their WhatsApp chat, in
plain words: "*laptop* (Windows) wants to join as a node", when, where it
connected from, that it was started with the code from their `!join`, what
approving lets it do (one line each: "read and change the files in
C:\Users\you\Shared", "report its system details"), and "Was this you?
Reply !approve — or !deny if not". `!approve` alone approves the device and
what it offers in one go, and is audited; `!deny` shuts it out instead.
Nobody types the id ([Approving from WhatsApp](#approving-from-whatsapp)).
On the Nodes page the same is **Approve device**, then **Approve
commands**. Or on the Mac:

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
lists it. None of these needs an administrator. When the service does
not start, Windows says only error 1053 ("did not respond to the start
or control request in a timely fashion", event 7009 in the System log);
`doctor --node` says why, from the state dir the service was installed
with.

### Inside an app package

The Claude desktop app is an MSIX package, as are apps from the Microsoft
Store. Windows keeps what a process of such an app creates under the
user's AppData, `%LOCALAPPDATA%` and `%APPDATA%`, in the package's own
copy, `%LOCALAPPDATA%\Packages\<family>\LocalCache\Local` (and
`\Roaming`), and shows the app's processes that copy in place of the real
folder. That holds for every process the app starts, elevated ones too,
and whether or not it carries the package's identity: the shells of the
Claude desktop app's Code tab do not, and are redirected all the same.
Every other process, the node service among them, sees the real folder.

So a `fridolin node join` run there with the default state dir puts
`node.json` and `node.key` into
`%LOCALAPPDATA%\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Local\fridolin-node`,
where `fridolin node status` in the app finds them, while the service,
installed with `--state-dir %LOCALAPPDATA%\fridolin-node`, finds an
empty folder. The agent then exits before it reaches the service
manager, so it can log nothing, and Windows reports error 1053 within a
second.

fridolin checks for it:

- `node join`, `node backup`, `node workstation` and `node fride pair`
  create and remove a file in the state dir to see where it lands, and
  refuse a state dir
  whose writes end up in a package's copy, before anything is written
  (join spends no code);
- `node install` refuses a state dir whose `node.json` is, as it sees
  it, a package's copy;
- `doctor --node`, `setup-machine --node`, `node status` and a failed
  `node start` read the state dir from the service's command line and
  say when it has no `node.json`, and in which package's copy one lies.

Use a state dir outside AppData from inside such an app:
`$env:USERPROFILE\.fridolin-node`, in the home folder, which only the
owner, SYSTEM and the administrators may read, and name it with
`--state-dir` in every `fridolin node` command, the elevated ones too.
Or run the commands in a PowerShell the owner starts from the Start
menu, which keeps the default.

A node that already hit it keeps its device and approval when its two
files move. From a PowerShell started from the Start menu, copy them to
where the service looks, then start it:

```powershell
$fridolin = "$env:ProgramFiles\fridolin\fridolin.exe"
$copy = "$env:LOCALAPPDATA\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Local\fridolin-node"   # doctor --node names it
$state = "$env:LOCALAPPDATA\fridolin-node"
New-Item -ItemType Directory -Force $state | Out-Null
Copy-Item "$copy\node.json", "$copy\node.key" $state
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','start','--state-dir',"`"$state`""
```

Or, from inside the app, copy them out of AppData and install the
service with that state dir:

```powershell
$copy = "$env:LOCALAPPDATA\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Local\fridolin-node"
$state = "$env:USERPROFILE\.fridolin-node"
New-Item -ItemType Directory -Force $state | Out-Null
Copy-Item "$copy\node.json", "$copy\node.key" $state
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','uninstall'
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','install','--state-dir',"`"$state`""
Start-Process $fridolin -Verb RunAs -Wait -ArgumentList 'node','start','--state-dir',"`"$state`""
```

Once the service runs, the package's copy is only a stale view of the
node for the app's processes; the owner may delete it (`Remove-Item
-Recurse $copy`), after which they see the real state dir.

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
   and the lines to paste, each for sh and for PowerShell: the native
   [one-liner](#the-one-liner) first, then a [node in a
   box](#node-in-a-box), then the plain join for a machine that has
   fridolin already:

   ```
   curl -fsSL https://raw.githubusercontent.com/OWNER/fridolin/COMMIT/container/node.sh | sh -s -- --core fridolin-core.example.ts.net:8766 --code fj-abcd…
   fridolin node join fj-abcd… --core fridolin-core.example.ts.net:8766
   ```

   On the tailnet the address is the core's MagicDNS name once it joined,
   so it resolves on every device of the tailnet. The core keeps only the
   code's SHA-256 in `nodes/nodes.json`.
2. On the new machine, paste the native line. It installs Tailscale and
   the core's fridolin, fetched from the core with the code, and joins.
   With fridolin installed already, the plain line does the join: it
   makes the node's device key, writes `node.json`, connects once with
   the code, and installs and starts the node service. `--no-service`
   only joins; `fridolin node run` then runs the agent in the foreground.
3. On the core, approve the device:
   `fridolin nodes approve <id>` (or **Approve device** on the Nodes page).
4. Then approve what it offers, separately:
   `fridolin nodes approve <id> --commands`.

   Or both at once: reply `!approve` to the message the bridge sends you
   when the node joins ([Approving from WhatsApp](#approving-from-whatsapp)).

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
nodes, whether each is online, what each may do in plain words, and what
waits for your approval ("⏳ wants to join — reply !approve"), with each
node's id last, for reference.

Each `fridolin nodes` command that takes a node takes its id or its
name, in any case: `fridolin nodes approve mac-mini` is `fridolin nodes
approve <its id>`. So do `!approve`, `!deny`, `!revoke` and `!movecore`,
and the API's node routes. A name more than one node has is refused,
with each node's id to use instead; a revoked node answers to its name
only when no other node has it.

### Approving from WhatsApp

Everything that waits for the owner is told in their direct chat the same
way, and answered with one word: a node's device, what a node offers
(fride among it, with what approving it means), a
brain's [scheduled agent, changed brain.json or
skill](BRAINS.md#approval) (for an agent that runs a program on a node,
also what approving does to that node's allowlist, or why the program
would not run there yet: "Programs from the core"), a run's request for
approval, and the core move's two polls ([`!movecore`](CORE.md), and
afterwards whether the machine the core left keeps the host secrets).
The alert
says what wants what, when, where it came from and how it was started,
what approving would let it do (one sentence per capability), and "Was
this you?". It ends with "Reply !approve — or !deny if not"; the id comes
last, in small print, for reference only. Several at once come as one
list.

- `!approve` alone approves the one thing that waits. With several
  waiting it sends a multi-select poll, one plain option each, and under
  it the ref of each option. Every option the owner ticks is approved;
  one vote that ticks several approves them all and says so in one
  message, and taking a tick back undoes nothing. Nobody else's vote
  counts. A poll has room for 12; the notice says how many more wait.
  `!deny` alone (and `!revoke` alone) turns them down the same way: a node that waits to join
  is shut out, a node's request for more keeps what was approved before,
  and a brain's file stays unapproved, without being asked about again
  until it changes. For the core move, `!approve` is "Move now" and
  `!deny` "Cancel"; for the poll after it, `!approve` keeps the host
  secrets and `!deny` forgets them. Voting in those polls works as
  before.
- Something the owner did not start is flagged *Unexpected*: a node whose
  join code came neither from `!join` nor from the dashboard (or
  `fridolin nodes join-code`, which goes through it), a node asking for
  more on its own, a file a brain wrote into its own
  folder. `!approve` alone then asks once more: "reply !approve again
  within 2 minutes". So does something that waited longer than a day,
  and something the owner was never shown (the alert comes first). A
  poll vote that ticks several such ones lists them in one message, and
  the second `!approve` approves them all.
- `!approve <ref>` and `!deny <ref>` still name one by the ref the alert
  ends with; `!revoke <ref>` shuts out a node, approved or not. A node's
  name, in any case, stands for its ref.
- Only the owner, in their direct chat. Every `!approve`, `!deny`, vote
  and confirmation goes into the audit log (`node_approved`,
  `node_revoked`, and `owner_approval` for the rest). What the owner was
  told of, and turned down, is kept in `<data_dir>/owner-approvals.json`,
  so a restart does not tell it again.

The Nodes page says the same for a waiting node: how it was started, and
"If you approve, it can:" with the same sentences (`allows` in `GET
/api/nodes`, and `joined_via`: `chat`, `dashboard` or none).

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
| `core-host` (`core_host`) | `core.check`, `core.secrets.*` | a machine the core may move to, which holds host secrets for it; on unless `core-host.json` turns it off (not `node.json`), and needs no approval: see [core-host](#core-host) below |

**exec rules.** `name` is `argv[0]` exactly: a bare program name, which
the node looks up in its `PATH`, or an absolute path. `args` `null` allows
any arguments, `[]` none, and a list of regular expressions allows
arguments that each match one of them whole. No rule may name a shell or a
program that runs others (`sh`, `bash`, `zsh`, `fish`, `cmd`,
`powershell`, `pwsh`, `wsl`, `env`, `sudo`, `osascript`, `xargs`, …): the
node refuses to load such a `node.json`, and both sides refuse such a call
whatever a rule says. An interpreter you allow (`python`, `node`) can still
run anything; allow one only with argument patterns that pin what it runs.
`"exact": true` makes `args` the arguments themselves, in order. A rule
with `"approve": true` asks you before every call it allows, each time
(see [The core brain runs commands](#the-core-brain-runs-commands)); a
call that a rule without it allows too runs without asking. Turning
`approve` on or off changes the rule, so the node's commands wait for your
approval again, and until then that rule allows nothing.
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

### Programs from the core

A [scheduled agent](BRAINS.md#scheduled-agents) of kind `command` runs a
program on a node. Rather than adding each such program to `node.json` by
hand, a node can name folders whose programs the core may add, once, on
the node:

```json
"exec": {
  "allow": [],
  "accept_from_core": ["/Users/you/Personal/projects", "/Users/you/tools"],
  "timeout_seconds": 900
}
```

Restart the node (`fridolin node stop`, `fridolin node start`) and
approve what it offers on the Nodes page: the folders are part of its
declaration, so they wait for that approval like any new rule. From then
on, approving a command agent whose program is in one of those folders
adds that program to the node's allowlist, with exactly the agent's
arguments; saving an agent in the wizard does the same. Deleting the
agent removes the program again, unless another approved agent runs it
with the same arguments.

How it goes: whenever the approved agents change, a node connects or
its commands are approved, and otherwise every half minute, the core
works out each such node's list (the programs its approved command
agents run there that `node.json` does not allow already) and sends a
node whose list changed the whole of it, `exec.programs`. The list names
the node and a sequence number, and is signed with the core's update key
(`core.pub`, pinned when the node joined, or at its first update or list from the core). The
node refuses a list that does not verify with that key, is for another
node, or is no newer than the last it took. It keeps each program that

- is a clean absolute path that resolves, symlinks and all, to an
  executable file inside one of the folders (as they resolve too);
- is no shell and no interpreter, as written or as it resolves: no `sh`,
  `bash`, `python`, `node`, `perl`, `ruby`, `osascript`, `awk`, `find`,
  `git`, `ssh`, `curl`, … (`nodeproto.IsInterpreter`), and on Windows no
  `.bat`, `.cmd`, `.ps1`, `.vbs` or `.js`;

and refuses the rest, each with why; the Nodes page shows both, the
programs with the agents they are for. A folder must pass the same check
as a workstation root: absolute, not the home folder or a whole drive,
and neither in nor holding a system folder (`/usr`, `/bin`,
`/opt/homebrew`, `C:\Windows`, …), the node's state dir or a place of
keys (`~/.ssh`, …); a `node.json` with another does not load.

The node writes what it kept to `exec-from-core.json` in its state dir
and declares it as rules marked `"core": true, "exact": true`, which the
core approves itself, because you approved the agents that run them. It
checks such a program again before every run, and runs the file it
resolved then: a program that became a symlink out of its folder, or a
folder taken out of `accept_from_core`, is refused. A program the core
did not list again is gone from the node's allowlist at once; nothing
else in `node.json` changes. In a lockdown the core sends nothing.

The Schedules page and the wizard say, for each command agent, whether
its node runs its program, whether approving or saving adds it, or what
the node must opt into first; `fridolin doctor` warns about approved
command agents whose node does not allow their program (`nodes-programs`).

### The core brain runs commands

The core brain runs a command on one of your machines with
`node_exec(argv, node?, dir?, timeout_seconds?, approval_id?,
wait_seconds?)`, and lists what each machine allows with
`node_exec_rules(node?)`. Both exist only on the core brain, and only
while a node whose `exec` you approved is online; a sub-brain and a
delegated task never have them. It is for quick things (a status, opening
an app, an update, a backup script): work in a folder goes to an
[agent session](#workstation), code to [FrIDE](#fride), and the core
brain's prompt says when to use which. `node` is the node's name or id,
and may be left out while only one node runs commands. The core checks
the call against what you approved of the node before it sends anything,
and the node against its `node.json` again; a shell never runs, whatever
a rule says. The answer is the exit code, stdout and stderr (each cut at
`max_output_bytes`), whether it timed out, and how long it took. `dir` is
the working directory the brain picks, so allow a program only with
arguments that are safe in any folder.

A wider list for a Mac, in `node.json`: the report-only commands run when
the core brain asks, the rest only with your yes each time.

```json
"exec": {
  "allow": [
    {"name": "uname", "args": ["-[amnrsv]+"]},
    {"name": "sw_vers", "args": []},
    {"name": "uptime", "args": []},
    {"name": "df", "args": ["-[hkP]+"]},
    {"name": "pmset", "args": ["-g", "batt"], "exact": true},
    {"name": "brew", "args": ["outdated", "list", "--version"]},
    {"name": "softwareupdate", "args": ["--list", "-l"]},
    {"name": "open", "args": ["-a", "Music"], "exact": true},
    {"name": "open", "args": ["-a", "Safari"], "exact": true},
    {"name": "brew", "args": ["upgrade", "update"], "approve": true},
    {"name": "softwareupdate", "args": ["--install", "-i", "--all", "-a"], "approve": true},
    {"name": "shutdown", "args": ["-r", "now"], "exact": true, "approve": true},
    {"name": "launchctl", "args": ["kickstart", "-k", "gui/501/com\\.example\\.[a-z]+"], "approve": true},
    {"name": "/Users/you/bin/backup.sh", "args": [], "approve": true}
  ],
  "timeout_seconds": 1800
}
```

Restart the node (`fridolin node stop`, `fridolin node start`) and approve
what it offers on the Nodes page, which marks each rule that asks "asks
you each time". `open` with any argument can open a script or an app that
runs one, so allow it only for the apps you name, exactly. A node that
runs as you cannot run what needs root: `shutdown` and `softwareupdate
--install` answer that they must be run as root there, and fridolin never
runs `sudo`.

**How you approve.** For a command whose rule asks, `node_exec` asks you
itself, as `request_approval` does: a poll in your chat, "🔐 Approve? Run
`brew upgrade` on mac-mini", with Yes, No and Show me the draft (the
whole command, the node's id and the folder), and the same approval on
the dashboard's Approvals page. The brain never asks you for that yes
itself. The call waits for your vote up to `wait_seconds` (default 5
minutes); Yes runs the command, No does not, and no answer within
`policy.approval_timeout_hours` counts as no. Unanswered by then, the call
answers pending with an `approval_id`; the brain waits on with
`check_approval`, or hears your answer as a new message, and calls
`node_exec` again with it. An approval runs exactly the command, node and
folder it named, once: another command, or a second run, asks again. A
brain set to `approvals: auto` still asks for these, since the rule is
the machine's word that the command asks. A lockdown refuses `node_exec`,
and a yes that comes during one runs nothing. A scheduled agent of kind
`command` does not ask at each run: approving the agent was the yes for
its command.

Every call is a `node_exec` line of the audit log: the node and the
command line (token-like strings masked), the approval and who said yes
for a command that asks, and the exit code, `timed out`, `pending`,
`denied` or why it failed or was refused.

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

## Node in a box

A node in a box runs as containers made from fridolin's own images,
instead of a native install (issue #191): Docker and one compose file,
the same on Windows, macOS and Linux. It is the fallback where Docker
Sandboxes cannot run, such as a machine without virtualization: join
natively ([above](#joining-a-machine)) wherever the machine can, since
only a native agent can start the VMs of FrIDE instances, and it does
what a container cannot (Apple builds, USB phones, desktop
notifications, the workstation mode on the host's own folders). A
machine may run a box and a native node side by side, with different
node ids.

| Service | Image | Holds | Volumes |
| --- | --- | --- | --- |
| `node` | `fridolin-core`, as `fridolin node run` | the node's key, `node.json`, the core's update key, its tailnet device; a standby core, off until a core move hands the core to this box ([The core in a box](#the-core-in-a-box)) | `fridolin-node` at `/node`; the backups at `/backups` (the volume `fridolin-backups`, or a host folder); the core's `fridolin-box-data` at `/data` and `fridolin-box-home` at `/var/lib/fridolin` |
| `fride` | `fridolin-fride` | FrIDE's companion, its repositories and settings, the Claude token, the git identity | `fride-home` at `/var/lib/fride`, `fride-workspaces` at `/workspaces` |

- **No Docker socket** in either service: on Docker Desktop it controls
  the VM that shares the owner's whole home (macOS) or `C:\` (Windows),
  which would undo the containers' wall.
- **`fride` never mounts the node's volumes**: a coding agent cannot read
  the node's key, the backups, or the core's data and host secrets once
  the box runs the core. Both services run unprivileged, with
  every capability dropped and `no-new-privileges`.
- **Its own tailnet device.** The node reaches the core over Tailscale
  inside its container (tsnet, as the core does), as the device
  `fridolin-box-<name>`; the host needs no Tailscale. Its device state is
  in the node's volume, so a restart is the same device. With the
  tailnet's default policy every device of the owner reaches every other;
  a policy that restricts it must let `fridolin-box-*` reach
  `fridolin-core` on port 8766.
- **The box network.** The two services talk over the box's own compose
  network only. Nothing is published on the host but, while the box runs
  the core, its dashboard and site on the host's `127.0.0.1`, which
  FrIDE's address on the box network cannot reach. (Docker Desktop lets
  any container reach the host's `127.0.0.1` as `host.docker.internal`,
  FrIDE's too: there the dashboard's sign-in guards it, as it does
  against every program on the machine.)
- **Backups**: the node lends `/backups` to the core's
  [node targets](VAULT.md#node-targets). What lands there is restic's
  encrypted files, never a password.
- **No commands, no notifications** by default: the container has
  neither the host's programs nor a desktop. `--allow` at the join still
  allows programs, which then run inside the container.
- **Updates** come as new images, never as a binary from the core: a box
  offers `update (box)`, and its updater on the host applies them
  ([Box updates](#box-updates)).

### Joining a box

1. **Docker.** Windows and macOS: Docker Desktop (Windows: `winget install
   --id Docker.DockerDesktop -e`, one UAC prompt and its terms on the
   first start; set it to start at sign-in, so the box comes back after
   a restart). Linux: Docker Engine with the compose plugin (Ubuntu:
   `sudo apt-get install -y docker.io docker-compose-v2`; or Docker's
   own packages), and Docker for the user who runs the line (`sudo
   usermod -aG docker $USER`, then log in again; or run it as root).
   OrbStack works on macOS. The host needs no Tailscale.
2. **The line from `!join`.** The owner sends `!join` to fridolin in
   their WhatsApp chat. A core that knows where its box script is
   published (an image CI built, or `fridolin core build` of a clean
   checkout of a GitHub repository) answers with a line for a box second,
   after the [native one-liner](#the-one-liner) (`fridolin nodes
   join-code` on the core prints the same):

   ```bash
   curl -fsSL https://raw.githubusercontent.com/OWNER/fridolin/COMMIT/container/box.sh | sh -s -- \
     --registry ghcr.io/OWNER --version VERSION --core fridolin-core.TAILNET.ts.net:8766 --code CODE
   ```

   On Windows, in PowerShell (no administrator needed):

   ```powershell
   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/OWNER/fridolin/COMMIT/container/box.ps1))) `
     -Registry ghcr.io/OWNER -Version VERSION -Core fridolin-core.TAILNET.ts.net:8766 -Code CODE
   ```

   The script is the one of the core's own commit, and the images are
   of the core's version. Run it in a terminal: it asks its questions
   on the terminal, also when it came through the pipe (over SSH: `ssh
   -t`).
3. **What the script does**, after saying so and asking:
   - pulls both images and pins them by digest in `.env`, beside the
     compose file it writes from the image (`fridolin box compose`), in
     `~/.fridolin-box` (Windows `%LOCALAPPDATA%\fridolin-box`); with `gh`
     signed in on the machine it also checks their build provenance
     ([CORE.md](CORE.md#on-ghcr));
   - signs the box in to the tailnet (`fridolin node tailnet`): it
     prints a Tailscale link the owner opens and signs in to with the
     account of the core's tailnet, or uses `TS_AUTHKEY` when set.
     Export it before the line (`export TS_AUTHKEY=…`, read from a
     password manager, not typed into a shared shell's history): in
     `TS_AUTHKEY=… curl … | sh` it would reach `curl` only. A key with
     tags makes the box a device of those tags, which the tailnet's
     policy must let reach `fridolin-core`;
   - joins the core with the code (`fridolin node join CODE --core …
     --box`). A code works once, within 10 minutes; if it ran out while
     the tailnet sign-in took long, send `!join` again and run the line
     with the new code;
   - starts the box (`docker compose up -d`) and installs its updater
     ([Box updates](#box-updates); `--no-updater` leaves it out). On
     Linux the updater is a systemd user timer, which runs only while
     the user is logged in unless lingering is on: `sudo loginctl
     enable-linger $USER` (the script reminds you);
   - FrIDE: runs `claude setup-token` in the `fride` container (open its
     link and sign in; the browser shows a code with a `#` in it, which
     goes into `setup-token`'s own prompt), takes the token it then
     prints, which starts with `sk-ant-oat`, without echo and stores it
     in FrIDE's volume (anything else, such as that code, is refused, and
     it asks again), asks for the git name and email the agents
     commit with, and pairs FrIDE with the node (`fride pair-companion`
     piped into `fridolin node fride pair --code -`, so the code is never
     shown).

   Options: `--name NAME` (default: the host name), `--backups DIR` (a
   host folder such as `F:\Fridolin`, bound into `node` only), `--no-fride`
   (a box without FrIDE), `--dir DIR`, `--yes`. PowerShell takes the same
   as `-Name`, `-Backups`, `-NoFride`, `-Dir`, `-Yes`.
4. **Approve** it as any node: fridolin tells the owner in WhatsApp that
   the box joined; `!approve ID` lets it in with what it offers (host
   facts, backups, FrIDE once it answers), or **Approve device** and
   **Approve commands** on the Nodes page.
5. **Verify**: `docker compose ps` in the box's folder shows both
   services running; `docker compose exec node fridolin node fride status
   --state-dir /node` says FrIDE answers; the core lists the node online
   with `host`, `backup` and `fride` (`!fleet`, or the Nodes page).
   `docker compose exec node fridolin doctor --node --state-dir /node`
   checks the box from inside: it leaves out the host's Tailscale and a
   node service, which a box does not have, and checks that the agent is
   connected to the core over the box's own tailnet device.

**Updating by hand**: the same line with `--update` and the core's
version, no code (`-Update` in PowerShell). It pulls and pins the new
images, writes the compose file and the updater of that version and
recreates the services; the node keeps its key and approval. A box made
before its image knew `fridolin box pending` (#191 phase 2) needs this
once before the core can update it.

### Box updates

The core updates a box to the images of its own version, pinned by
digest, without ever reaching the box's Docker:

1. The owner clicks **Update** on the Nodes page, or turns on auto
   updates, as for any node (`update` must be approved). The core looks
   up the digests of `fridolin-core:VERSION` and `fridolin-fride:VERSION`
   on its registry (anonymously: the packages are public; a core of a
   local build, which does not know its registry, says so and offers no
   update), signs version and both digests with its update key, and
   calls `update.box`.
2. The agent checks the signature against the core key it pinned at the
   join and keeps the request in its volume (`box-update.json`). That is
   all it does: the `node` service has no Docker socket.
3. The **updater** on the host (`update.sh` beside the compose file, run
   every hour as the owner by the launchd agent
   `ai.fridolin.box-updater`, the systemd user timer
   `fridolin-box-updater` or cron; on Windows `update.ps1`, run by the
   Scheduled Task *fridolin box updater* while the owner is signed in)
   asks a throwaway container **of the image the box runs now** to check
   the request (`fridolin box pending`): the signature again, that both
   images are `ghcr.io/OWNER/fridolin-core@sha256:…` and
   `…/fridolin-fride@sha256:…` of the registry that image was built for,
   and their build provenance (GitHub's attestations for this
   repository's `image.yml` on `main`, checked with `gh attestation
   verify --bundle`, which needs no login). A request that fails is
   dropped and reported.
4. It pulls both images by digest, keeps `.env`, `compose.yaml`,
   `core.yaml` and `seccomp.json` as `.prev`, writes the new digests and
   the new version's compose files (refusing one that names the Docker
   socket; `core.yaml` and `seccomp.json` from an image that has them,
   adding them to `.env`'s `COMPOSE_FILE` once) and updater, and runs
   `docker compose up -d`. A box that runs the core stops it cleanly and
   starts it again on the new image.
5. It waits up to 5 minutes for the `node` service to be healthy, which
   it is while the agent is connected to the core (`fridolin node
   health`). If it is not, the `.prev` files come back and the old
   images start again.
6. The result goes into the node's volume (`fridolin box result`); the
   agent reports it to the core, which shows it on the Nodes page like
   any update. The updater logs to `update.log` beside the compose file.

Removing the updater: `launchctl bootout gui/$(id -u)/ai.fridolin.box-updater`
and its plist in `~/Library/LaunchAgents` (macOS), `systemctl --user
disable --now fridolin-box-updater.timer` (Linux), `crontab -e`, or
`Unregister-ScheduledTask "fridolin box updater"` (Windows).

**Removing**: `docker compose down -v` in the box's folder deletes the
containers, the network and the volumes (the node's key, the backups in
the volume, FrIDE's workspaces), then `!revoke ID` on the core. A backups
folder on the host stays. A box made before its updater waited for the
volume may have a `fridolin-node` volume compose did not make (compose
warns so on every command), which `down -v` leaves: `docker volume rm
fridolin-node`.

**Repositories for FrIDE** live in its own container, which the node
cannot reach, so the core's `fride_repo_add` does not work for a box:
clone into `/var/lib/fride/repos` in the `fride` container
(`docker compose exec fride git -C /var/lib/fride/repos clone URL`), then
**Re-check** in FrIDE's settings.

### The core in a box

A box can take the core from another machine and give it back (#191
phase 4): the core then runs **inside the box's `node` container**, as a
child of the node agent, on the box's own data volume. No second
container, no Docker socket: the box's role is a file in the node's
volume (`core-role.json`, `standby` or `core`), which the agent reads
when it starts, so a box that is the core starts it again after a
restart or a box update, and `docker compose stop` stops it cleanly
(which releases the lease) without standing the box down.

**The core's settings** are a second compose file beside the box's,
`core.yaml` ([container/box-core.yaml](../container/box-core.yaml),
`fridolin box core-compose`), with `seccomp.json` beside it (`fridolin
box seccomp`), both named in `.env`'s `COMPOSE_FILE`. `box.sh` and
`box.ps1` write them; a box made before gets them with its next update
(the updater of the version before writes the box's own compose file
only, so it takes one more update, or `box.sh --update` once). They give
the `node` service:

| What | Why |
| --- | --- |
| `fridolin-box-data` at `/data`, `fridolin-box-home` at `/var/lib/fridolin` | the core's data dir and its home (the credentials folder a move brings, never in an export); named apart from a core container's `fridolin-data`, so a machine that runs both never shares one |
| `127.0.0.1:8765` and `127.0.0.1:8780` on the host (`FRIDOLIN_BOX_DASHBOARD_PORT`, `FRIDOLIN_BOX_SITE_PORT` in `.env`) | the dashboard and the site, as a core container publishes them. The core listens on the container's loopback (`fridolin run --container --box`), and the agent forwards the published ports to it, refusing FrIDE's address on the box network |
| the core container's seccomp profile and `systempaths=unconfined` | bubblewrap gives each brain's run its own user, mount and PID namespaces ([CORE.md](CORE.md#the-brains-sandbox-in-the-container)) |
| `FRIDOLIN_BOX_CORE=1`, `mem_limit` `FRIDOLIN_BOX_CORE_MEMORY` (8g), `stop_grace_period` 45s | the agent may run the core; room for it; a clean stop |

On **Linux with AppArmor** (Ubuntu), which confines the user namespaces
bubblewrap makes, load the core's profile once (`sudo fridolin core
apparmor`, from a fridolin build on that machine, [CORE.md](CORE.md#the-brains-sandbox-in-the-container))
and add `FRIDOLIN_BOX_APPARMOR=fridolin-core` to the box's `.env`, then
`docker compose up -d`. `box.sh` says so when it sees the restriction,
and the move's pre-flight refuses a box whose sandbox does not start.
Settings you add to `.env` (the ports, memory, AppArmor, `FRIDOLIN_TZ`)
stay across `box.sh --update`.

**Making it a core host.** Nothing to do: a box, like every approved
node, is a core host unless core-host is off there, and the owner's vote
on the first `!movecore <box>` confirms it ([core-host](#core-host)).

**A move to the box** is the same move ([CORE.md, "The core moves
itself"](CORE.md#the-core-moves-itself)): `!movecore <box>` or `fridolin
nodes move SRC BOX --yes`. In the box:

- the host secrets go into its sealed store in the node's volume, the
  secret files of the credentials folder into the core's home volume;
- the rehearsal imports the latest export into a scratch folder in the
  node's volume and removes it: never `/data`, never the lease;
- the handoff runs `fridolin core import --latest --force` on `/data`,
  `core promote`, then starts the core (`run --container --box`, through
  the image's entrypoint) with the host secrets of the sealed store in
  its environment, the same names `fridolin core start` passes from a
  Mac's keychain (`hostsecrets.CoreEnv`), and waits for `/ready`;
- as the source, the box stops its core (SIGTERM; a core that does not
  end within 30 seconds is killed, and the move then sees the lease not
  released), exports, and stands down. A rollback starts it again.

The pre-flight says what changes on a box: its own checks (the core's
settings, bubblewrap) block when they fail; the brain folders live in
`fridolin-box-data`, so they move with every export and come home on the
way back, but no folder of that machine shows them; what is bound to the
source (scripts on a Mac, Apple builds) stays there and runs on its
node, called over the node channel while it is online. A new box cannot
read the lease before the move sends it the bucket's keys, which the
pre-flight says without refusing.

`fridolin node status` in the box (`docker compose exec node fridolin
node status --state-dir /node`) says whether it is the core. **Never
`docker compose down -v`** a box that runs the core: `fridolin-box-data`
is the core, as `fridolin-data` is a core container's. Move the core
away first.

### Trying a box

`FRIDOLIN_BOX_NO_TAILNET=1` in the node's environment (for `node
tailnet`, `node join --box` and `node run`) makes the box reach the core
over its own network instead of the tailnet: for trying a box against a
dev-mode core on the same machine (`--core host.docker.internal:PORT`,
with `nodes.listen` on that port), never for a real one.

### The FrIDE image

`ghcr.io/OWNER/fridolin-fride` ([container/fride/Dockerfile](../container/fride/Dockerfile))
holds FrIDE, built from a pinned commit of its repository, with Claude
Code (the version the core's image has), git, Go (the version FrIDE's
`go.mod` asks for; `GOTOOLCHAIN=auto` fetches a newer one a repository
names), Node, GitHub's `gh`, ripgrep, jq, lsof and python3, and the
ponytail plugin at the commit [below](#ponytail-for-every-agent). CI
publishes it beside the core's image, with the same version tags and
build provenance ([CORE.md](CORE.md#on-ghcr)).

It runs as the user `fride` (uid 10002), apart from the node's (10001),
with two volumes: `/var/lib/fride` (FrIDE's home in `~/fride`, its config,
the repositories in `~/repos`, Claude Code's settings, the git identity
and the Go caches) and `/workspaces` (the tasks' workspaces). On its
first start it writes FrIDE's config for the box (the one [below](#the-config)
with the box's paths), clones `fridolin` and `fride` into `~/repos`,
marks `/workspaces` as trusted for Claude Code and marks ponytail's
statusline offer as made. Nothing secret is in the image:

- **The Claude token** (from `claude setup-token`, typed by the owner)
  goes into the home volume through stdin, never a command line:
  `docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token`,
  then `docker compose restart fride`. It takes only a token as
  `setup-token` prints it (`sk-ant-oat`, then letters, digits, `-` and
  `_`) and refuses anything else, such as the browser's sign-in code
  (it has a `#`), with why; it drops what Windows PowerShell adds to
  what it pipes (a carriage return at the end, sometimes a byte order
  mark in front) and spaces around the token. A
  stored token of another shape makes FrIDE's log say so at its start
  (`docker compose logs fride`).
- **The git identity** the agents commit with:
  `docker compose exec fride git config --global user.name NAME` (and
  `user.email`).

FrIDE answers only on its loopback and posts its news to a loopback
webhook, and the node's agent insists on loopback addresses for both. So
the two services forward on the box's network: in the `fride` container,
port 17840 of the network reaches FrIDE on its loopback and the
loopback's 17841 reaches the `node` service; the agent forwards the
other way in the `node` container. Both sides see `127.0.0.1`, and the
containers share no network namespace, so FrIDE never reaches what
listens on the node's loopback.

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
| `fride.identities` | `PUT`/`DELETE /api/companion/git/identities/<id>` | hand FrIDE its git identities ([below](#git-identities-for-fride)) |

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

**What approving `fride` means.** It is arbitrary execution on that
machine, whatever `exec` allows: FrIDE runs Claude Code agents there,
which can run anything the machine's user can, and on Windows reach
PowerShell, `winget`, `docker.exe` and `C:\` through WSL's interop.
Whoever can send `fride.task` (the core, and the core brain through
`fride_task`) can therefore run anything there. Every place that
approves it says so: the Nodes page while it waits, `!approve`'s answer,
and `!fleet`, which marks a node with `fride` approved ("FrIDE's agents
can run anything on it"):

> fride lets fridolin hand coding tasks to FrIDE on this machine. FrIDE
> runs Claude Code agents there, and they can run anything this
> machine's user can (on Windows through WSL, PowerShell and C:\ too),
> whatever exec allows or refuses: approving fride is approving arbitrary
> commands on this machine. Approve it only on a machine you would let
> fridolin do anything on.

A core host that runs FrIDE as the user that holds the host secrets lets
any FrIDE task read them; the move's report says so.

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
branch; `fride_news` keeps the latest 50 pieces of each thread's news
whole, which the dashboard's FrIDE page shows. FrIDE's news arrives in
that chat: a question with the question (the owner answers there, and
the core brain passes the answer on with `fride_reply`), `started` and
`done` with the task and branch, `done` with the rationale's path,
`failed`, and everything else as an update. Each is at most three lines
and 300 characters there, with a pointer to the FrIDE page. By default
(`fride.news: "quiet"`, docs/CONFIGURATION.md) the chat gets only
`done`, `failed`, real questions and updates that say something merged;
"Created task …", FrIDE's echo of the brief ("• → agent: …") and
progress stay on the page, and an identical failure goes out once per
thread. FrIDE's notice that an agent "has not moved for 2h" is progress
however it asks, and one that only repeats the thread's last, but for
its hours, is dropped for six hours, also with `fride.news: "all"`. A question whose text asks nothing (no question mark, none of
"should I", "please confirm" and the like) is taken as an update: FrIDE
marks progress as needing a reply too (fride#16). The relay gathers
news for a few seconds: one text a node sends for several threads goes
to each chat once, as "FrIDE on mini (#9, #10, …)", and the same text
from that node is not sent there again within a minute. Work a scheduled
agent handed over goes to that agent instead
([BRAINS.md](BRAINS.md#scheduled-agents)). The lines go into the
history of the brain that started the work as FrIDE's, so its next run
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
and `fride_repo_add` are write actions of M5's policy (`tools.Guarded`): a lockdown refuses them,
as it refuses `!move`, and so does a run whose risk budget is read;
`fride_status` only reads. Every call is a `tool_call` line in the audit
log.

**Not yet.** The
dashboard shows the FrIDE address each node reports and links it; FrIDE's
UI is on the node's loopback, so the link works on the node itself until
the node agent publishes it over the tailnet. `fridolin doctor`'s
`nodes-fride` warns about nodes whose approved FrIDE does not answer.

### The core and FrIDE instances

A FrIDE instance is a whole FrIDE in a Docker Sandbox of its own on a
node that offers `fride_host`, for one trust domain that
`fride.instances` names ([CONFIGURATION.md](CONFIGURATION.md); the
direction is in [VISION.md](VISION.md#fride-instances)). The core starts
one when work needs it and stops it once it idles; the node side is in
[FrIDE instances](#fride-instances).

**Tools.** With instances configured, the core brain has three more, and
its prompt lists each instance with its description and repositories,
so it picks the instance by what the work is for:

| Tool | |
| --- | --- |
| `fride_task(…, instance?)` | with `instance`, starts it unless it runs (`medium`) and hands it the work; without, a node's own FrIDE as above |
| `fride_instances()` | each instance: description, repositories, identities, where it runs, its state, memory and open threads; and the nodes that run instances, with their free memory |
| `fride_spawn(instance, size?, reason)` | starts one ahead, or remakes a running one with no open thread at another size |
| `fride_stop(instance)` | stops one with no open thread |

`fride_repo_add` takes `instance` too. Work on an instance stays on it:
its thread is (node, instance, FrIDE's thread there), every call for it
names the instance, and `fride_move` refuses it. A reply or a close to
work whose instance stopped starts it again on its node.

**Sizes.** `small` 3072 MB (code and unit tests), `medium` 6144 MB (with
the project's stack), `large` 12288 MB (a heavy stack);
`memory_mb` in the instance's config is a fixed size instead.

**Placement.** The nodes it may run on (its `nodes`, else every
approved node whose `fride_host` is ready), with its open threads' node
only when it has some. A node's room is its available memory less its
reserve (6144 MB unless node.json says otherwise), and the instance's
own memory where it runs already. The core picks the node with the most
room that fits, the one that has the instance's sandbox (its disk)
first; else it stops idle instances of other trust domains (no open
thread, the longest idle first) on the node with the most room where
that is enough; else it tries the next smaller size, down to `small`,
and tells the chat the instance got less; else it refuses, naming what
runs where. An instance with an open thread is never stopped to make
room. One start or stop goes at a time.

**Identities.** A start (`fride.instance.start`) carries the instance's
repositories, egress, and the identities its `identities` names, with
their tokens, from the GitHub and GitLab connections as in [Git
identities for FrIDE](#git-identities-for-fride); the first one with a
commit name and email gives `git`. When one changes, and every five
minutes, a running instance whose identities, repositories or egress
changed is started again at its memory with `update_only`, which the
node takes as an update and which never starts one that stopped (an
idle stop whose declaration has yet to reach the core stands). No
token goes into a log, the audit or `bridge.db`.

**Idle stop.** An instance with no open thread and no news for its
`idle_stop_minutes` (default 30; -1 never) stops; its disk stays. Work
that cannot get an instance waits, its chat told why, and goes once it
can; a node's start that failed is tried again by the queue after five
minutes. Every start and stop is an audit line of kind `fride_instance`
(by `core brain`, `dashboard`, `queue`, `idle`, or `core` for one
stopped to make room).

**Dashboard and doctor.** `GET /api/fride` lists the instances with
their node, state, memory and open threads; `POST
/api/fride/instances/{name}/stop` (`{"force": true}` for one with open
threads) and `/start` (`{"size": …}`) stop and start one. In a lockdown
`/start`, `fride_spawn` and `fride_task` start none. `fridolin doctor`'s
`fride-instances` warns about an instance no node can run and about
identities the core does not hold.

### Git identities for FrIDE

FrIDE can hold several git identities, and a workspace (task) is created
under one of them (FrIDE's docs/GIT.md). The core hands each FrIDE the
ones Fridolin has, with their tokens:

| Id | Kind, host | Token | Commit name and email |
| --- | --- | --- | --- |
| `github` | github, `github.com` | the GitHub app's installation token ([GITHUB.md](GITHUB.md)), or the typed token when no app is connected | the GitHub card's, optional |
| `gitlab` | gitlab, the host of the GitLab address | the GitLab connection's ([CONNECTIONS.md](CONNECTIONS.md#gitlab)) | the GitLab card's, optional |

**Which nodes.** Every online node whose `fride` is approved and offered
gets both, by default. `nodes.git_identities` in config.json narrows it
by node id or name ([CONFIGURATION.md](CONFIGURATION.md#nodes)), for a
work GitLab token that should not be on every machine:

```json
"nodes": {"git_identities": {"mini": ["github"], "laptop-sbx": []}}
```

**How.** The core sends a node all of its identities in one call,
`fride.identities` (at most 8; ids as FrIDE takes them, never `none`,
which FrIDE reserves). The node keeps them in memory only and, once
FrIDE answers `git_identities: true` in `/api/companion/info`, puts each
into FrIDE with `PUT /api/companion/git/identities/<id>`. FrIDE keeps
them apart from its own `config.json`, in
`<FRIDE_HOME>/git-identities.companion.json`, and the token in
`<FRIDE_HOME>/secrets/git/<id>.token` (0600). One the node put there
before that is not in the call it removes (`DELETE`; FrIDE's 404 counts
as removed). The state dir keeps only which ids it put there
(`fride-identities.json`), never a token. A FrIDE that does not take
identities yet gets them once it is updated; a FrIDE paired anew gets
them at the node's next check.

The core sends them when a node connects, when its `fride` is approved
or comes up, when an identity changes on the dashboard, and every five
minutes to a node that did not take them. Nothing goes in a lockdown.

**The GitHub token.** An installation token of the app's installation
on its owner (else its first one), for all its repositories and
permissions, which lasts an hour. 25 minutes before it ends the node
asks the core for a new one (the event `fride.identities`), and the
core mints one when the one it handed out has less than 30 minutes left.
git asks FrIDE's credential helper at each push and so always gets the
current token; `gh` and `glab` read theirs when a session starts. The
app must let it write code (*Contents: Read and write* in the app's
settings on GitHub, accepted on the installation): an app that may only
read code gives FrIDE no GitHub identity, so that the node's own git
login keeps pushing, and the dashboard says why. A typed token goes as
it is.

**Collisions.** An identity of FrIDE's own `config.json` with the same id
wins for its label, host, name and email, but the token Fridolin hands
over replaces its token: there is one token file per id. Keep your own
identities under other ids than `github` and `gitlab`.

**What it shows.** Integrations → GitLab lists each FrIDE node with the
identities its FrIDE took and when, or why not (`GET
/api/git-identities`). No token is in a log, the audit, a GET or the
dashboard.

**FrIDE instances** get theirs with `fride.instance.start`, as
placeholders ([Tokens as placeholders](#tokens-as-placeholders)): the
token stays on the host in a sandbox-scoped sbx secret, which swaps the
placeholder for it on the way to its host, and FrIDE gets the
placeholder as the token. The old sandboxed FrIDE's node (service
`fride-sandbox`) gets none: a real token must never enter its VM, so it
holds them back and says so (`held`), and the dashboard shows why.

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
| `~/fride-companion/plugins/ponytail` | the ponytail plugin its agents load, pinned to one commit |
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

   Then the ponytail plugin for FrIDE's agents (see "Ponytail for every
   agent" below):

   ```powershell
   wsl -d Ubuntu --exec bash -lc 'git clone -q https://github.com/DietrichGebert/ponytail ~/fride-companion/plugins/ponytail && git -C ~/fride-companion/plugins/ponytail checkout -q --detach 16352ac229eb08a9598dcae48fe9f2b342567d86 && mkdir -p ~/.claude && touch ~/.claude/.ponytail-statusline-nudged'
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

   **Restarting it** (a new `fride` binary, a config key the running
   one predates): stopping the task ends `wsl.exe` on Windows, but the
   `fride serve` inside Ubuntu keeps running, on the old binary even
   after a new one was put in its place, and the task's next start
   cannot take the port. End the Linux process too:

   ```powershell
   Stop-ScheduledTask -TaskName 'FrIDE companion (WSL)'
   wsl -d Ubuntu --exec pkill -f 'fride-companion/bin/fride serve'
   Start-ScheduledTask -TaskName 'FrIDE companion (WSL)'
   ```

   `wsl -d Ubuntu --exec bash -lc 'ps -eo pid,lstart,args | grep "[f]ride serve"'`
   then shows one server, started just now. A restart ends the agents of
   running tasks: check that none runs first. An agent of FrIDE's own
   (WSL) cannot run this itself, since it ends with the server; it can
   start it as a detached Windows process that waits a little, through
   `/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe` (WSL's
   `appendWindowsPath` may keep Windows programs off its PATH).

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
    FrIDE's Settings (or restart it as in step 9). FrIDE and `fride_task` see
    them at once.

Then continue with "Approve and verify" below.

### macOS and Linux: FrIDE natively

This is the same setup as on the Mac mini, next to the owner's own FrIDE.

1. **Tools**: git, Go (the version FrIDE's `go.mod` names or a newer
   one; an older `go` fetches it), Node 18+ and Claude Code
   (`npm i -g @anthropic-ai/claude-code`), and lsof. On macOS:
   `brew install go git node`. On Linux (Ubuntu 24.04):
   `sudo apt-get install -y git curl ca-certificates build-essential lsof nodejs npm gh`,
   then Claude Code for the user, not root:
   `npm config set prefix ~/.local && npm i -g @anthropic-ai/claude-code`.
   Ubuntu's Go and Node 18 are fine; `go` fetches the version FrIDE's
   `go.mod` names on the first build.
2. **Claude Code login**: the owner runs `claude` once and signs in;
   check with `claude -p hi`. On a machine reached over SSH, open a
   terminal for the owner with `ssh -t HOST 'cd ~/fride-companion/workspaces && bash -lc claude'`
   once `~/fride-companion/workspaces` exists (step 3): Claude prints a
   sign-in URL to open on their own machine, and asks about trusting the
   folder, which is step 6 done at once.
   **Git identity**: FrIDE's agents commit on the node, and with no
   identity the first commit stops to ask in the agent's terminal,
   which the chat does not show. Ask the owner which name and email
   to use, then
   `git config --global user.name "NAME" && git config --global user.email EMAIL`.
3. **Build**: run `git clone <fride-url>
   ~/fride-companion/src/fride` and
   `mkdir -p ~/fride-companion/{bin,home,repos,workspaces}`, then
   `go build -o ~/fride-companion/bin/fride .` in the clone.
4. **Starter repositories**: clone `fridolin` and `fride` into
   `~/fride-companion/repos`, as in WSL's step 6, and the ponytail
   plugin (see "Ponytail for every agent" below):

   ```bash
   git clone -q https://github.com/DietrichGebert/ponytail ~/fride-companion/plugins/ponytail
   git -C ~/fride-companion/plugins/ponytail checkout -q --detach 16352ac229eb08a9598dcae48fe9f2b342567d86
   mkdir -p ~/.claude && touch ~/.claude/.ponytail-statusline-nudged
   ```
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
     `Restart=always`:

     ```ini
     [Unit]
     Description=FrIDE companion (under fridolin)

     [Service]
     ExecStart=%h/fride-companion/bin/fride serve --under-fridolin --port 17840
     WorkingDirectory=%h/fride-companion/home
     Environment=FRIDE_HOME=%h/fride-companion/home
     Environment=SHELL=/bin/bash
     Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
     Restart=always
     RestartSec=5

     [Install]
     WantedBy=default.target
     ```

     Its log goes to the journal (`journalctl --user -u fride-companion`).
     Start it with
     `systemctl --user daemon-reload && systemctl --user enable --now fride-companion`.
     For it to run without a login, and after a reboot, the user must
     linger (`sudo loginctl enable-linger $USER`, ask first; the node's
     service needs it too, see Join above).
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
  "companion": {
    "enabled": true,
    "claudePlugins": ["~/fride-companion/plugins/ponytail"]
  }
}
```

### Ponytail for every agent

Every Claude Code agent the companion FrIDE starts loads
[ponytail](https://github.com/DietrichGebert/ponytail) (MIT), a plugin
that makes agents write the least code that works, with
`/ponytail-review` and `/ponytail-audit`. FrIDE passes each folder in
`companion.claudePlugins` to Claude Code as `--plugin-dir` (FrIDE's
docs/COMPANION.md, "Plugins for task agents"), for new and resumed
sessions. That loads the plugin for that session only: nothing is
installed into `~/.claude`, no marketplace is added, the owner's own
Claude Code sessions do not get it, and no prompt appears. A
project-level `enabledPlugins` was not used: it installs into the
owner's plugin cache, asks in an interactive session, and would need a
`.claude/settings.json` in every task's worktree.

- **Pinned.** The checkout is detached at one commit, ponytail v5.3.2
  (`16352ac229eb08a9598dcae48fe9f2b342567d86`), so an upstream change
  never reaches the agents by itself. To move it, fetch
  (`git -C ~/fride-companion/plugins/ponytail fetch --tags`), read what
  changed (`git -C ~/fride-companion/plugins/ponytail log --stat
  HEAD..vX.Y.Z`, the `hooks/` folder above all), check it out with
  `git -C ~/fride-companion/plugins/ponytail checkout -q --detach
  vX.Y.Z`, and put its commit in this file. Agents started afterwards
  use it; no restart is needed.
- **What it runs.** Its hooks run `node` as the owner: at session start
  (the ruleset, and a map of the repository from `git ls-files`), when a
  subagent starts, and on each prompt (the `/ponytail` level). They keep
  small state files in `~/.claude` (`.ponytail-active`,
  `ponytail-modes/`). Its "band" is a Claude Code UI module that runs
  `git grep` at session start and, after each turn, writes the task's
  row (cost, tokens, turns) to `~/.config/ponytail/band/<repo>.csv` and
  `board.html`. Its webhook is empty by default, so nothing leaves the
  machine. `node` must be on the PATH FrIDE runs with.
- **The statusline nudge.** In the first session without a `statusLine`
  in `~/.claude/settings.json`, ponytail tells the agent to offer to add
  one there, which an unattended agent should not do to the owner's own
  settings. The empty file `~/.claude/.ponytail-statusline-nudged`
  (made above) marks that offer as made; it changes no setting.
- **Off.** Remove the entry from `companion.claudePlugins`: agents
  started after the next **Re-check** in FrIDE's Settings (or a
  restart) go without it. Deleting the checkout does the same, and
  `fride config check` then warns.

### Approve and verify

1. **Approve on the core.** Within 30 seconds of FrIDE answering and
   the node being paired, `fride` appears in what the node offers. It
   then waits for the owner, who approves it with **Approve commands**
   on the core's Nodes page (open
   `https://fridolin-core.<tailnet>.ts.net/#/nodes` for them, `http://`
   when the tailnet has HTTPS off: `fridolin doctor` names the address) or with
   `fridolin nodes approve <id> --commands` on the core. Where the core
   has a chat command for approving, such as `!approve`, that works too.
   Approving `fride` approves arbitrary commands on that machine
   ([FrIDE](#fride), "What approving `fride` means").
2. **Verify**, one check at a time:
   - `fride doctor` (in WSL, with `FRIDE_HOME=~/fride-companion/home`)
     shows no failed check;
   - `fridolin node fride status` says FrIDE answers, and lists
     `fridolin` and `fride` among its repositories;
   - ponytail loads: in `~/fride-companion/workspaces`,
     `claude -p --plugin-dir ~/fride-companion/plugins/ponytail 'List your skills whose names start with ponytail.'`
     names `ponytail:ponytail`, `ponytail:ponytail-review` and
     `ponytail:ponytail-audit` (in WSL through `wsl -d Ubuntu --exec bash -lc`),
     and while the test task below runs, its agent's command line
     (`ps -ef | grep -- --plugin-dir`) carries
     `--plugin-dir …/fride-companion/plugins/ponytail`;
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

     When FrIDE reports instead that the agent is idle or "stopped at
     its prompt", look at the agent's screen (FrIDE's web UI, on the
     node or through `ssh -L`; the board's **Show hidden** lists the
     companion's tasks). On a node's first task, Claude Code's one-time
     auto-mode notice can swallow the instruction; answering "send it
     again" works
     (FrIDE's issue #4). A
     question the agent asks in its terminal (a missing git identity,
     say) does not reach the chat
     (FrIDE's issue #5).

**Rollback.**
- Windows: `Unregister-ScheduledTask -TaskName 'FrIDE companion (WSL)'`,
  then `wsl -d Ubuntu --exec pkill -f fride-companion/bin/fride`.
- macOS: `launchctl bootout gui/$(id -u)/ai.fride.companion`.
- Linux: `systemctl --user disable --now fride-companion`.

The node stops offering `fride` within 30 seconds. `fride.token` and
the `fride` entry in `node.json` can stay. Removing `~/fride-companion`
deletes the workspaces and any uncommitted work in them, so ask first.

## Docker Sandboxes

FrIDE instances run in Docker Sandboxes (`sbx`) microVMs on a Mac, a
Linux machine with KVM, or Windows 11 on x64 with the Windows Hypervisor
Platform ([FrIDE instances](#fride-instances); Windows:
[FrIDE instances on Windows](#fride-instances-on-windows)). Brains do
not: they run under bubblewrap in the core's container, or sandbox-exec
natively ([VISION.md](VISION.md#why-not-docker-sandboxes-for-brains)).
Fridolin is tested with sbx v0.50.0.

- **Install and sign in**: `brew install --cask sbx` on a Mac,
  `winget install -h Docker.sbx` on Windows, then `sbx login` as the
  user whose sbx it is. sbx needs no Docker. Docker Desktop or OrbStack
  only builds the template on the machine itself; without Docker the
  node takes the template the core publishes
  ([Setting a machine up](#setting-a-machine-up)).
- **sbx's global settings** are fridolin's on this machine: the network
  policy `deny-all`, `ssh.agentForwardingEnabled` and `browser.enabled`
  off. They apply to your own sandboxes too. `fridolin node fride host
  setup` makes them hold: it replaces another policy (`sbx policy
  reset` removes every rule), and restarts sbx's daemon when no sandbox
  runs. Every few minutes the agent of a node that offers `fride_host`
  sets a setting that drifted again and initializes a missing network
  policy; another policy it leaves to `host setup`, since replacing it
  stops every sandbox.

## FrIDE instances

A FrIDE instance is one whole FrIDE, unchanged, with its coding agents,
in a Docker Sandbox of its own on a node, for one trust domain: for
example `fridolin` (GitHub) and `work` (a self-hosted GitLab). What the
agents run stays in the VM, and they reach only the hosts the instance
may reach. Workspaces inside one instance share its VM, so the instance,
not the workspace, is the wall between trust domains
([VISION.md](VISION.md), "FrIDE instances").

The core starts an instance when a task needs it, sizes its memory at
start, hands it work with the `fride_task` flow, and stops it once it
has been idle for a while; the instances are named in its config
([CONFIGURATION.md](CONFIGURATION.md#frideinstances)). The machine's own
node agent runs them: there is no second node per FrIDE. A node can host
several instances, each with its own sandbox, ports, pairing, webhook,
repositories, git identities and network allowlist.

The native companion FrIDE ([Setting up FrIDE on a node](#setting-up-fride-on-a-node))
runs beside them on the same node, as the instance `""`:

| | native companion | an instance, such as `work` |
| --- | --- | --- |
| FrIDE | `http://127.0.0.1:17840` | `http://127.0.0.1:17850`, published from the sandbox `fride-work` |
| webhook | `127.0.0.1:17841` | `127.0.0.1:17851`, forwarded out of the VM |
| repositories | `~/fride-companion/repos` on the host | `~/fride-companion/repos` in the VM, cloned there |
| workspaces | `~/fride-companion/workspaces` | `~/fride-companion/workspaces` in the VM |
| git | the machine's git and gh | placeholders, which sbx's proxy swaps for the tokens; never a token in the VM |
| state on the node | `node.json`'s `fride`, `fride.token` | `fride-instances/work/` in the state dir |

The ports come in pairs from 17850 up, FrIDE's and its webhook's, the
first pair no other instance has and nothing listens on; the node keeps
an instance's pair for as long as it has the instance.

### Setting a machine up

The machine is a node already ([Joining a machine](#joining-a-machine)),
with sbx as in [Docker Sandboxes](#docker-sandboxes) (installed, and
`sbx login`).

**The template.** Every instance's sandbox is made from one image, from
[container/sandbox/fride.Dockerfile](../container/sandbox/fride.Dockerfile):
sbx's Claude kit with FrIDE built at the commit fridolin pins
(`FRIDE_COMMIT`, which a test holds to
[container/fride/Dockerfile](../container/fride/Dockerfile)'s), its
config (`/opt/fride/companion.json`, the box's), the Claude Code
fridolin is tested with, and ponytail at the commit
[Ponytail for every agent](#ponytail-for-every-agent) names. It holds no
secret. The node gets it one of two ways:

- **From the core** (the default where no Docker is found, and the way
  on Windows): CI publishes the template for amd64 and arm64 with the
  core's images, `ghcr.io/OWNER/fridolin-fride-sandbox`
  ([CORE.md](CORE.md#on-ghcr)), and the core names it in each start
  (`fride.instance.start`'s `template`): `<registry>/fridolin-fride-sandbox:t-<hash>`,
  with the tag its image names, which changes only with what goes into
  the template, else its version. sbx pulls it the first time a sandbox
  is made from it (a few GB). Nothing to build, and no checkout:

  ```sh
  fridolin node fride host setup --no-build
  ```

  A core that does not know where its images are published (a local
  build, without `FRIDOLIN_BOX_REGISTRY`) names none, and places no
  instance on such a node: its doctor and `fride_task` say why.
- **Built here**, with Docker, in a fridolin checkout of the core's
  version:

  ```sh
  fridolin node fride host setup --repo .
  ```

  Docker builds the template unless sbx has it (a few minutes the first
  time), sbx loads it as `fridolin-fride-sbx:<hash>`, and Docker's copy
  goes. FrIDE's git URL is the checkout's origin with `fridolin`
  replaced by `fride`, on GitHub (`--fride-repo URL`). The node's own
  template wins over the one the core names.

Either way setup checks sbx (running, signed in), makes sbx's global
settings fridolin's, and records it all in `fride-host.json` in the
node's state dir; from then on the node's agent offers `fride_host`.

Flags: `--no-build` (the template from the core), `--docker-disk 40g`
(each sandbox's Docker disk), `--no-caches` (see [Shared
caches](#shared-caches)), `--reserve-mb N` (below), `--state-dir DIR`.

Then approve `fride_host` on the core once: `!approve`, the Nodes page,
or `fridolin nodes approve ID --commands`. As with `fride`, approving it
approves arbitrary commands, here inside the instances' VMs.

`fridolin node fride host status` shows the template, sbx, whether the
node is ready, the memory and the instances. A newer FrIDE, Claude Code
or ponytail comes with a new template: from the core, once a core
publishes one; built here, by running `host setup` again in an updated
checkout. An instance gets the new template when the node makes its
sandbox anew, at its next start while it is stopped. The old template
stays in sbx (`sbx template rm` frees its few GB).

### FrIDE instances on Windows

Windows 11 on x64 runs Docker Sandboxes with the Windows Hypervisor
Platform; sbx needs no Docker Desktop. The node is native
([Joining a machine](#joining-a-machine)): its service runs as
LocalSystem. Once, as the person who uses the machine:

1. **The Windows Hypervisor Platform**, in an administrator PowerShell
   (then restart, if Windows asks):

   ```powershell
   Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform -All
   ```

2. **sbx**, in their own PowerShell (not elevated): `winget install -h
   Docker.sbx`, then `sbx login` (the browser signs in to Docker) and
   `sbx version`.
3. **Setup**, in the same PowerShell, with the state dir the node joined
   with:

   ```powershell
   & "$env:ProgramFiles\fridolin\fridolin.exe" node fride host setup --no-build --state-dir "$env:LOCALAPPDATA\fridolin-node"
   ```

   A node that joined from inside the Claude desktop app has its state
   dir at `"$env:USERPROFILE\.fridolin-node"` instead
   ([Inside an app package](#inside-an-app-package)).
4. **Approve** `fride_host` on the core, as above, and check with
   `fridolin node fride host status` (same `--state-dir`): `Ready: yes`.

How it runs there:

- **sbx is the signed-in user's.** Its login and its VMs belong to the
  user who ran `sbx login`. The node's service runs every sbx call in
  the signed-in user's session, as that user: with their token
  (`WTSQueryUserToken`, the console's session first, else an active
  Remote Desktop one), their environment and their profile folder,
  without a window, and their `sbx.exe` from their `PATH` or WinGet's
  links. While nobody is signed in, `fride_host` is not ready ("nobody
  is signed in to Windows: sbx runs in the signed-in user's session"),
  and the instances' FrIDEs stop with their VMs; once that user signs
  in again, the agent starts those it keeps running (within its wait of
  up to 5 minutes), and the core starts the others when work needs
  them. So the user who set up sbx is the one who signs in; another user
  has another sbx, without the instances. Started from a terminal as the
  user (`fridolin node run`), the agent runs sbx itself.
- **The tokens** wait in the instance's `tokens` folder in the state
  dir, which only that user and SYSTEM may open (its ACL inherits
  nothing). sbx reads one with the node's own `fridolin.exe node fride
  host sbx-secret FILE`, by short paths without spaces, instead of
  `/bin/cat`. The history's and the carry's folders are theirs and
  SYSTEM's alone too, since `sbx cp` writes them as the user.
- **Memory**: `available_memory_mb` is what `GlobalMemoryStatusEx` calls
  available.
- **Not yet on Windows**: shared caches (where sbx puts a Windows folder
  in the VM is not settled; setup keeps none, `--no-caches`), Windows on
  Arm (sbx has no build), a sandbox while nobody is signed in. Building
  the template with Docker there is untried: take the core's.

### What the node offers

The capability `fride_host` (`nodeproto.FrideHostCap`) says, for the
core's placement:

- `version` (sbx's) and `ready`: sbx answers and is signed in (on
  Windows: someone is signed in to Windows, whose session sbx runs in),
  fridolin's settings hold, and sbx has the template the node built;
  else `problem` says what is missing.
- `needs_template`: the node built no template (`host setup
  --no-build`), so each start names the core's; a core that knows none
  places no instance there.
- `total_memory_mb` and `available_memory_mb`: what the OS can give
  now. On macOS that is the free, inactive, purgeable and speculative
  pages (`vm_stat`), not only the free ones; on Linux `MemAvailable`; on
  Windows what `GlobalMemoryStatusEx` calls available.
- `reserve_mb`: what the machine keeps for itself, which the core
  starts no instance into: 6144 MB, or `node.json`'s
  `"fride_host": {"reserve_mb": N}` (`host setup --reserve-mb N`
  writes it).
- `instances`: each with its `state` (`starting`, `running`,
  `stopped`, `failed`), `memory_mb`, FrIDE's `url` and `repos` while it
  runs, and a `problem`.

The agent reads it again at most every minute, and right after an
instance changes state. A node in a box never offers it.

### What an instance gets

`fride.instance.start` (from the core) makes the instance when the
node does not have it, and then, at every start, brings it up to date.
It answers once FrIDE answers and is paired, with the instance as
`fride_host` lists it. A first start takes a minute or two, plus the
clones of large repositories; the node gives it 20 minutes at most.

- **The sandbox** `fride-<name>`: from the template (the node's own,
  `--pull never`; else the one the start names, `--pull missing`),
  `--memory` the start's `memory_mb`, `-p 127.0.0.1:P:P` for FrIDE's
  port, a 40 GB Docker disk ([Docker disk](#docker-disk)) and its own Go
  and npm caches ([Shared caches](#shared-caches)). No other folder of
  the host's goes in. `downloads.claude.ai` and `bridge.claudeusercontent.com` are
  denied: the template pins Claude Code, and remote control stays off.
- **Its network**, on top of the global `deny-all` and the hosts sbx's
  Claude kit opens: Anthropic's API, Go's module proxy and checksum
  database, npm, Docker Hub, its webhook port on `localhost`; GitHub's
  hosts with a GitHub identity, each identity's host, OpenAI's hosts
  when sbx holds the ChatGPT login Codex runs with, and the start's
  `egress` (a host, `*.domain`, with a port when not 443). Every start
  adds what is new and removes what went.
- **Its git identities**, as placeholders ([Tokens as
  placeholders](#tokens-as-placeholders)): the node pushes each to
  FrIDE (`PUT /api/companion/git/identities/<id>`) with the placeholder
  as its token, and removes the ones it pushed before that are gone. An
  instance takes one identity per host.
- **The Claude login** of FrIDE's agents ([The Claude login](#the-claude-login)).
- **The git identity** for commits outside an identity's workspaces:
  the start's `git` name and email.
- **Its repositories**: each URL of the start that its base folder
  lacks is cloned in the VM, over https, with the placeholders; FrIDE
  then reads its base folder again. The core's `fride_repo_add` for an
  instance clones (or makes, with `gh repo create` inside) there too.
- **FrIDE** runs from the template, its home, workspaces and base
  folder in `~/fride-companion` in the VM. Its config starts from the
  box's ([container/fride/companion.json](../container/fride/companion.json),
  from the checkout a template was built from, else the template's own
  copy), with the VM's base folder and workspaces; Claude Code trusts
  the workspaces.

A start of an instance that runs with the same memory only brings the
network, tokens, login, identities and repositories up to date: the
core starts it again when a token changes. Another memory makes the
sandbox anew at the new size (sbx cannot resize a running VM), and
carries over what lived only on the old VM's disk ([History](#history)).
A start that fails stops the VM and leaves the instance `failed`, with
why, until the next start.

`fride.instance.stop` stops the VM; its disk stays, and the next start
runs it again within seconds. `fride.instance.remove` saves the
instance's history, removes the sandbox with its workspaces and
repositories, and the instance's state, pairing and tokens on the node.

### Tokens as placeholders

No token ever enters a VM. For each identity the node calls
`setHostToken(sandbox, host, token)` (`internal/nodeagent/sbx_tokens.go`):

- the token waits in `fride-instances/<name>/tokens/<host>.token` in
  the node's state dir (0600; on Windows the folder is the signed-in
  user's and SYSTEM's alone), which sbx reads (`--command`: `/bin/cat`,
  on Windows `fridolin.exe node fride host sbx-secret`) when it sets the
  secret and when it refreshes it;
- a sandbox-scoped custom secret (`sbx secret set-custom`) has sbx's
  proxy swap the placeholder for the token in every request to the
  host (for github.com also api.github.com), in a Bearer header and
  inside git's Basic auth;
- for github.com, sbx's github service gets the token too, so that `gh`
  and git work as they are;
- git's credential helper for `https://<host>` in the VM hands git the
  placeholder (user `x-access-token` for GitHub, else `oauth2`).

The core sends the real tokens over the node channel with each start;
a new token replaces the old one under the same placeholder.
`removeHostToken` undoes it all for a host an instance no longer has.
The token is never an argument, a log line or chat.

### The Claude login

FrIDE starts its agents without any `CLAUDE_CODE_*` variable
(`agent.CleanEnv`), so the token cannot come from `fride serve`'s
environment. Each start puts the Claude token of this machine's host
secret store (`fridolin.claude.oauth.token`, which `fridolin core
secrets set claude.oauth.token` stores; on a Mac in the login keychain)
where Claude Code reads it itself: `env.CLAUDE_CODE_OAUTH_TOKEN` in
`~/.claude/settings.json` in the VM (0600), on stdin. Without it, FrIDE
runs and the instance's `problem` says so.

This one token sits inside the VM, unlike the git tokens, until sbx
can inject subscription logins: an agent turned against you could read
it. It is a long-lived Claude Code token (`claude setup-token`), which
you revoke in your Claude account.

### How it runs

While an instance's state says run (a start sets it, a stop clears it),
the node's agent holds `sbx exec fride-<name> sh -c <serve>` open: sbx
stops a sandbox 30 seconds after its last exec ends, and a published
port does not count. The script starts `fride serve --under-fridolin
--port P` in a session of its own, unless it runs, with two forwarders,
as in the box: the VM's address, where sbx publishes P, to FrIDE on the
loopback, and the loopback's P+1 to `host.docker.internal:P+1`, where
the agent hears the instance's webhook. With the same ports inside and
out, FrIDE's Host check and its webhook URL work unchanged. The agent
sends the news on to the core as the instance's (`FrideEvent.instance`),
whatever the news says.

When the exec ends, the agent starts it again: 2 seconds later, then
twice as long after each quick end, up to 5 minutes; a run of 2 minutes
resets that. When the agent itself stops or updates, FrIDE runs on until
the VM stops, and the agent's next start holds it again. FrIDE's log is
`serve.log` in its home in the VM: `fridolin node fride instance logs
NAME`. An idle instance costs about 1.1 GB.

### Docker disk

A sandbox's `/var/lib/docker` is a disk of its own, which sbx never
grows; at its default of 10 GB, a compose stack soon fails with `No
space left on device`. Each instance's is 40 GB (`host setup
--docker-disk`; sbx v0.50.0 has no flag for it, so fridolin sets
`DOCKER_SANDBOXES_DOCKER_SIZE` for that one `sbx create`). It is a
sparse file on the host, so it takes only what is used.

### Shared caches

Each instance gets `~/.cache/fridolin-sbx/<name>` mounted read-write at
the same path in the VM, with `gomod/`, `gobuild/` and `npm/`, and
`GOMODCACHE`, `GOCACHE` and `npm_config_cache` set there (and
`GOFLAGS=-modcacherw`, so that the host can delete the module cache).
sbx puts them into every exec, so FrIDE and its agents use them. A
sandbox made anew for the same instance finds what the last one
downloaded and built. `host setup --no-caches` leaves them out; on
Windows there are none yet.

The caches are the instance's alone: what one sandbox writes, the next
one of the same instance uses, never another trust domain's. Go checks
a module against `go.sum` and its checksum database when it downloads
it, and npm checks a lockfile's integrity hashes, but what is in a cache
already is trusted, and so is Go's build cache. The host never reads
them, and keeps its own. Delete a folder to start afresh.

### History

FrIDE's agents' sessions and FrIDE's own state live on the VM's disk,
which goes with the sandbox. So removing an instance first ends FrIDE
and saves, with `sbx cp`, into `fride-instances/<name>/history/` in the
node's state dir:

- Claude Code's `~/.claude/projects` (what `--resume` lists),
  `history.jsonl` and `file-history`, and Codex's `~/.codex/sessions`
  and `history.jsonl`;
- FrIDE's home, `~/fride-companion/home`: `tasks.json` (its tasks and
  threads), the journal, its companion pairing and its config.

Each save merges into what an earlier one saved, so a session an agent
pruned since stays. The next sandbox of that name gets it all back,
owned by the VM's user. A save that fails keeps the sandbox.

A remake (another memory, or a new template) keeps more: it ends FrIDE,
saves the history, the workspaces, the repositories, the Claude login
and Claude Code's and git's settings into `fride-instances/<name>/carry/`,
makes the sandbox anew, puts them back, and removes the carry. Should
it fail on the way, the carry stays, and the next start finishes it.
What Docker held in the old VM (images, volumes) does not come along.

### On the machine

```sh
fridolin node fride instance ls                 # state, memory, ports, FrIDE's address, git hosts
fridolin node fride instance stop NAME          # the VM stops; the core's next start runs it again
fridolin node fride instance rm NAME            # asks; saves the history first (--no-history: not)
fridolin node fride instance logs NAME          # the end of FrIDE's log (--lines N)
```

They work on the state dir while the agent runs; the core sees the
change within a minute. The core's own `fride.instance.*` calls are the
usual way.

### Rollback

Stop and remove the instances (`fridolin node fride instance rm NAME`),
then remove `fride-host.json` from the node's state dir: the node stops
offering `fride_host`. The template stays in sbx until `sbx template rm`
(`sbx template ls` lists it: `fridolin-fride-sbx:<hash>` built here, or
`fridolin-fride-sandbox` pulled from the core).
The machine's node and the native companion are not touched by any of
it.

The sandboxed FrIDE of before, on a second node of its own (state dir
`…-sbx`, service `fride-sandbox`), is gone: `!revoke ID` for that node,
`fridolin node stop --state-dir <its state dir>`, `fridolin node
uninstall --state-dir <its state dir>`, `sbx rm fride-sandbox`, then
remove its state dir. Until then its node keeps git identities from its
FrIDE.

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

## core-host

A *core host* is a machine the core may move to one day (#187). This
first slice gives it its facts (`core.check`) and lets the host secrets
go from one core host to another over the node channel, end to end,
instead of `fridolin core send`/`receive` and the four words. The second
moves the core itself between two native core hosts, `fridolin nodes
move SRC DST --yes` ([CORE.md, "The core moves itself"](CORE.md#the-core-moves-itself)),
to and from a box too ([The core in a box](#the-core-in-a-box)),
with the methods `core.image.pull`, `core.rehearse`, `core.release`,
`core.handoff` and `core.move.status`, each a fixed `fridolin core`
subcommand of the agent's own binary. The third lets the owner move it
from WhatsApp: `!movecore <node>`, a report and a poll ([CORE.md,
"Moving the core from WhatsApp"](CORE.md#moving-the-core-from-whatsapp)).
`core send`/`receive` stay for a manual move and for an old machine that
is gone.

**Every node is a core host** unless its owner turns it off on that
machine, and it needs no approval of its own:

1. **On the machine**, nothing. A node without `core-host.json` in its
   state dir offers core-host; `fridolin node core-host off` there
   writes that file and stops it, `on` turns it on again. The core
   cannot change it: it is not `node.json`, and a `node.json` that
   names `core_host` does not load.
2. **The owner's vote** on the first move to the machine confirms it as
   a core host: `!movecore <node>` says in its report that the machine
   never held the core and what that means
   (`nodeproto.CoreHostApproval`), and "Move now" records the
   confirmation (`core_host_confirmed_at` in the registry). Only that
   vote, in the owner's WhatsApp chat, sets it: approving a node's
   commands on the dashboard or with `fridolin nodes approve` does not,
   and `fridolin nodes move`, `POST /api/nodes/move` and the transfer
   below refuse a machine that is not confirmed. A core host approved
   before this version counts as confirmed. Revoking a node drops it.

`!fleet`, `fridolin nodes` and the Nodes page show `core-host`; the
Nodes page also shows the device key's fingerprint and whether the
owner confirmed it, and `!movecore` alone lists every core host the
core could move to.

```sh
fridolin node core-host show                 # on or off, the device id, key and fingerprint, the pins
fridolin node core-host off | on
fridolin node core-host trust ID --key KEY [--name NAME]    # optional: pin a target
fridolin node core-host untrust ID
```

**Whose key the secrets go to.** A source encrypts its secrets only to
an offer signed by the target's device key. That key is the one the
core approved when the target joined, and the core names it to the
source in `core.secrets.send` (`target_key`) only for a target the owner
confirmed. The source checks that the key is the target's (the device
id is derived from it) and that it signed the offer. A core that named
another key would gain nothing: it runs with these secrets already. A
pin (`fridolin node core-host trust` on the source, with the key
`core-host show` prints on the target) wins over the key the core names,
and lets the secrets go to that target without the owner's
confirmation; it is no longer needed for a move. A source that runs a
fridolin from before this sends to pinned targets only, and the
pre-flight says to update it (`core.check`'s `trusts_core`).

**Moving the secrets.** On the core:

```sh
fridolin nodes core-secrets-transfer SRC DST          # a dry run: both sides' names, the pin, what keeps it from running
fridolin nodes core-secrets-transfer SRC DST --yes    # move them
fridolin nodes core-secrets-transfer SRC DST --yes --replace   # let a value replace a different one on DST
```

(`POST /api/nodes/core-secrets-transfer` with `source`, `target`, `run`,
`replace`.) SRC and DST are each a node's id or its name. Both devices
must be approved and online, and DST pinned by SRC or confirmed by the
owner. Then:

1. DST (`core.secrets.offer`) makes a one-time post-quantum age key
   (ML-KEM-768 + X25519), held in memory for 10 minutes, and signs it,
   the move id, its device id and the expiry with its device key
   (`node.key`).
2. SRC (`core.secrets.send`) checks that signature against the key it
   pinned for DST, or else the key the core names, and refuses an offer
   it does not verify, one that expired, or one for a target it has
   neither a pin nor a key for. It encrypts its host
   secrets (the names `core send` sends, without the Tailscale auth key)
   to the offer's key, with the secret files of its core's
   credentials folder ([CORE.md](CORE.md#the-core-moves-itself),
   "Secret files").
3. DST (`core.secrets.receive`) decrypts them in memory with the one-time
   key, which goes with the first bundle, and stores them as `core
   receive` does: a name a move does not carry refuses the whole bundle,
   a value that differs from one stored refuses it unless `--replace`,
   and a failed write puts back the earlier ones. The secret files go
   into the same folder under its core's home, 0600, never over one
   there.

The core relays the offer and the ciphertext and sees names only. Both
sides append a line per step to `secrets-transfer.log` in the state dir
(`"via": "node-channel"`; the source's says `"trust": "pin"` or
`"core"`), names only. `core.secrets.forget` removes on a machine what moves brought it
and it did not hold before, never what was there: the host secrets it
received over the node channel, the secret files they wrote, and the
core's data dir when a move's import put it into an empty one, once
another device's core holds the lease (#266; CORE.md, ["Forgetting a
former core host"](CORE.md#forgetting-a-former-core-host)). The agent
records each as it comes, in `core-secrets-received.json` in the state
dir; a data dir that held data when a move imported into it is the
machine's own and stays. After a move the owner is asked whether the
machine the core left keeps its copy (no vote in 24 hours forgets it),
and revoking a core host that is connected has it forget first.
`fridolin doctor --node` warns on a former core host that still holds
what moves brought it, or its own data dir from before them.
`core.secrets.offer/send/receive` are refused through `fridolin nodes
call`, and a lockdown refuses the transfer and every `core.secrets.*`.

**Where the secrets live.** A native node keeps them where `fridolin
core` does (the macOS keychain, else files under the user's config). A
node in a box keeps them in its volume, sealed with a key derived from
its device key, and the core it runs reads them from there. A native Windows node keeps none: the service runs as
LocalSystem, whose Credential Manager is not the owner's (use `core
send`/`receive` there for now).

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
may replace it ([Joining a machine](#joining-a-machine)). `status` needs
no administrator: it asks the service manager only for the service's
state and command line, which every user may read, and shows the state
dir the service runs with where it is not the one asked about. The
service reads its state dir as LocalSystem, outside any app package, so
it must not lie in an app's copy of AppData ([Inside an app
package](#inside-an-app-package)). On a machine
that joined a core and has no bridge config, `fridolin doctor` checks the
node: Tailscale, `node.json` and the device key, the core's node port,
the service, and on Windows whether the state dir it was installed with
holds `node.json` (`--node` and `--state-dir` say so explicitly), and on
Linux that the unit is enabled into `default.target.wants` and, as a warning
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
   its declaration it may serve. Nothing but `close` comes before
   `welcome`. Each approval after the core recorded the node sends
   `state`, also one that came while the node was still connecting,
   and the node hears it before any `request` it allows.
4. The node sends `heartbeat` every 20 seconds with its free resources;
   the core closes a connection it heard nothing from for 90 seconds.
5. The core sends `request` (`id`, `method`, `params`) and the node answers
   `response` with the same `id`: a `result`, or an `error` with a code
   (`denied`, `outside-root`, `not-found`, `bad-request`, `timeout`,
   `failed`, `unavailable`, `unsupported`).
   The node may also send, on its own, `event` (news, such as FrIDE's,
   with a `kind`) and `declare` (what it offers now, when FrIDE comes or
   goes, or the core's programs changed); the core answers a `declare`
   with `state`. `exec.programs` is the core's signed list of
   [programs from the core](#programs-from-the-core).
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
