# The core in a container

The core is the bridge with its WhatsApp session, brains, memory and audit
log. From M1 of [PLAN.md](PLAN.md) on it can run as one container image,
`fridolin-core`, plus one named Docker volume, `fridolin-data`, which holds
all of its state. That pair is what moves between machines. A native
install (`fridolin service install`) keeps working as before.

## The image

`container/Dockerfile` builds it from the repository's root:

```bash
docker build -f container/Dockerfile --build-arg VERSION=$(git describe --tags --always) \
  -t fridolin-core:$(git describe --tags --always) .
```

It holds the `fridolin` binary with the Python helpers next to it
(`/opt/fridolin/fridolin`, `/opt/fridolin/tools/`), python3, Node 22 and
the Claude CLI (a pinned version, at least the one the bridge is tested
against), the Codex CLI and the Plaud CLI (pinned too), ffmpeg, restic,
rclone, Litestream, GitHub's `gh`, `cloudflared`, age, bubblewrap, git,
curl, jq, sqlite3, ca-certificates and tzdata. restic, rclone, Litestream,
gh and cloudflared come from their releases, pinned and checked against
the SHA-256 each release publishes; the base images are pinned to version
tags.

A Python venv at `/opt/fridolin/venv`, first on `PATH`, so that `python3`
and `#!/usr/bin/env python3` are it, holds what brains' tools import:
torch (CPU only) and transformers for local transcription with
`tools/transcribe.py` (and scripts like it), groq, Google's API client
and sign-in libraries for calendar scripts that talk to Google
themselves, youtube-transcript-api and yt-dlp. `container/python/requirements.in`
lists them and `requirements.lock` pins every package with its hashes,
the same for arm64 and amd64 (`make container-python-lock` makes it
again, from releases at least two weeks old); torch comes from PyTorch's
CPU index. Transcription runs on the CPU (the image has no GPU; the Mac's
own scripts ran Whisper on the CPU too, since MPS gave garbage). A model
is downloaded on a brain's first transcription into its cache in the
volume (`XDG_CACHE_HOME`, `/data/brains/<id>/cache/huggingface`, about
1.6 GB for large-v3-turbo), once per brain, and kept. tini is PID 1, so `docker stop`
reaches the bridge as SIGTERM and it shuts down in order (15 s for a reply
in progress, as on a native install), and the brains' orphaned processes
are reaped.

The bridge runs as the user `fridolin` (uid 10001), never as root.
`HOME` is `/var/lib/fridolin` inside the image, which is not kept; everything
that must last lives in the volume:

| Variable | Value | Meaning |
| --- | --- | --- |
| `FRIDOLIN_HOME` | `/data` | the volume: the data dir, and where `fridolin core` commands look |
| `FRIDOLIN_CONFIG` | `/data/config.json` | the config file, in the volume |
| `TZ` | `UTC` | compose passes `FRIDOLIN_TZ` instead when set |

On a new, empty volume the entry point writes a starter config whose
`data_dir` is `/data` before the bridge first starts. The image's command
is `fridolin run --container`: `--container` binds the dashboard's port on
the container's interfaces instead of its loopback, because Docker forwards
a published port to the container's own address. `http_listen` stays
`127.0.0.1:8765`, the host guard still only answers requests addressed to
`127.0.0.1:8765` or `localhost:8765`, and compose publishes the port on the
host's `127.0.0.1` only. So the dashboard is reachable from this machine,
as before, and from elsewhere only through the core's own tailnet node
when `core.tailnet` is on (below). Keep the host side of the mapping at
the same port as `http_listen`: a request to another port is refused as
misdirected.

The image sets `FRIDOLIN_CONTAINER=1`. Configs are then resolved for the
container: brains' runs are sandboxed with bubblewrap (`sandbox.enabled`
is allowed, and on in a new config), new brain folders go in
`/data/brains-folders/` (a brain path `~/name` means
`/data/brains-folders/name` there), and `fridolin doctor` leaves out the
host's checks (Docker, the host's Tailscale, the keychain).

A health check asks `/health` every 30 s; `docker ps` shows the result.

Dev mode works in the container as anywhere else:

```bash
docker run --rm -v fridolin-test-data:/data -p 127.0.0.1:8765:8765 \
  fridolin-core:TAG run --container --no-whatsapp
docker exec <container> fridolin check
```

Use a volume of its own for that, never `fridolin-data`.

## Starting, stopping, updating

`container/compose.yaml` defines the service `core` (container
`fridolin-core`, restart `unless-stopped`, memory limit
`FRIDOLIN_MEMORY_LIMIT`, default `8g`) and the volume. The compose file is
compiled into the binary, and the host commands write it to
`<user config dir>/fridolin/compose.yaml` (on macOS
`~/Library/Application Support/fridolin/compose.yaml`) before each run, so
it always matches the version that runs it.

```bash
fridolin core start    # docker compose up -d, with the image of this version
fridolin core update   # docker compose pull, then up -d --force-recreate
fridolin core stop     # docker compose down; the volume stays
```

`--image` and `--tag` pick another image (default `fridolin-core` and this
binary's version); `--compose PATH` runs a compose file of your own.
`stop` never removes the volume. Never run `docker compose down -v` or
`docker volume rm fridolin-data` without a fresh export: the volume holds the
WhatsApp pairing.

### Secrets

`core start` and `core update` read the secrets from the host and put them
in the environment of the `docker compose` process only: not on a command
line, not in a file. Compose passes them on to the container by name. On
macOS they are generic passwords in the login keychain, account
`fridolin`; on Linux files of mode 0600 in
`$XDG_CONFIG_HOME/fridolin/secrets/` (default `~/.config/...`), named
after the service.

| Keychain service | Container variable | |
| --- | --- | --- |
| `fridolin.age.identity` | `FRIDOLIN_AGE_IDENTITY` | made by `core start` when missing |
| `fridolin.claude.oauth.token` | `CLAUDE_CODE_OAUTH_TOKEN` | from `claude setup-token` |
| `fridolin.tailscale.authkey` | `TS_AUTHKEY` | |
| `fridolin.b2.vault.keyid`, `.key` | `B2_VAULT_KEY_ID`, `B2_VAULT_KEY` | the `fridolin-vault` bucket |
| `fridolin.b2.snapshots.keyid`, `.key` | `B2_SNAPSHOTS_KEY_ID`, `B2_SNAPSHOTS_KEY` | the `fridolin-snapshots` bucket |
| `fridolin.vault.crypt.password`, `.password2` | `FRIDOLIN_VAULT_CRYPT_PASSWORD`, `..._PASSWORD2` | made by a native bridge with the vault on, while the cold tier is empty ([VAULT.md](VAULT.md#keys-and-passwords)) |
| `fridolin.restic.password` | `RESTIC_PASSWORD` | the same, while the restic repository does not exist |

A secret that is missing is named and left out. To store one by hand:

```bash
security add-generic-password -a fridolin -s fridolin.claude.oauth.token -w
```

(`-w` last makes `security` prompt for the value, so it is not on the
command line.)

The age identity encrypts every export. `core start` makes one the first
time and says so: copy it into the keychain of every machine that may run
the core, or the exports made with it cannot be read there. A new identity
on a second machine does not replace the first; exports made before stay
encrypted to the old one.

What this does not protect against: `docker inspect fridolin-core` shows
the container's environment to anyone who may use Docker on the host.
Inside the container a sandboxed brain's run has its own PID namespace,
so it cannot read the bridge's environment (`/proc/<pid>/environ`); with
`sandbox.enabled` off it can, as on a native Linux install.
`child_env_passthrough` still decides what brains are handed;
`CLAUDE_CODE_OAUTH_TOKEN` is among the variables they always get.

## The brains' sandbox in the container

Each brain's run (`claude -p`, `codex exec`, the site builder's) runs under
bubblewrap, as a Unix user of its own, `brain-<id>`, whose home is the
brain's state folder `/data/brains/<id>/` (mode 0700). The run gets:

- user, PID, IPC and cgroup namespaces of its own: inside, it is
  `brain-<id>` with a uid from 20000 to 59999 derived from the id, and it
  sees only its own processes; it has no capabilities, no terminal, and
  ends with the bridge's supervision of it (`--die-with-parent`);
- the system read-only: `/usr`, `/etc` (with an `/etc/passwd` and
  `/etc/group` naming its user, written to `/data/core/sandbox/<id>/`),
  `/bin`, `/lib`, the bridge's install `/opt/fridolin`, and the folders of
  the programs the bridge runs;
- read-write only its own places: its folder (as configured and its real
  path), its state folder (HOME, `CLAUDE_CONFIG_DIR`, `TMPDIR`, caches),
  its media inbox, and a private `/tmp`;
- read-only its run's token folder `/data/run-tokens/<id>/` and the
  `sandbox.allow_paths` entries;
- nothing else of `/data`: not the other brains' folders and state,
  `bridge.db`, `config.json`, `secrets/`, the tokens, `tsnet/` or the
  audit log. They are not mounted, so they do not exist for the run;
- `sandbox.deny_paths`, and the other brains' folders, the database, the
  config file and the credentials folder wherever a bind above would show
  them, masked: an empty read-only folder over a folder, `/dev/null` over a
  file. A deny path that does not exist when the run starts is not masked;
- the container's network: Claude needs it, and the brain API is on the
  container's loopback.

The user exists only in the run's user namespace, which bubblewrap maps
onto the bridge's own user (uid 10001). So no privilege is needed to start
a run, the bridge keeps owning every file a brain writes, and an export
moves no ownership; what keeps brains apart is the mount and PID
namespaces, not file modes. `fridolin check --print-sandbox <id>` prints
the command line, one argument per line.

Docker's defaults forbid what bubblewrap needs, so `compose.yaml` runs the
container with `cap_drop: [ALL]`, `no-new-privileges`, the seccomp profile
`seccomp.json` (written beside the compose file; Docker's default list of
refused calls, with `unshare`, `clone`'s namespace flags, `mount`,
`pivot_root` and `setns` allowed) and `systempaths=unconfined` (Docker
masks parts of `/proc`, which keeps a new `/proc` from being mounted). On
a host with AppArmor (Ubuntu, Debian with AppArmor on), Docker's
`docker-default` profile also forbids the mounts: add
`apparmor=unconfined` to `security_opt` in a compose override there.
`fridolin doctor` in the container (`docker exec fridolin-core fridolin
doctor`) tries it; a brain whose sandbox cannot start refuses every
message rather than run without it.

## The tailnet

With `core.tailnet.enabled` (a restart setting) the bridge joins the
tailnet as a device of its own through tsnet, Tailscale in the process
with a userspace network stack, so it needs neither the host's Tailscale
nor a TUN device. It is named `core.tailnet.hostname` (`fridolin-core`),
keeps its node key in `/data/tsnet/` (exported with the rest, so a moved
core is the same device), and joins the first time with `TS_AUTHKEY`,
which `fridolin core start` passes from `fridolin.tailscale.authkey`.
Without a key and without a node key it waits, and its log shows a
sign-in link.

On the tailnet it serves the dashboard and the API: with TLS on port 443,
the certificate of its MagicDNS name from Tailscale, when the tailnet has
MagicDNS and HTTPS on (and a redirect on 80); else plain HTTP on port 80,
inside WireGuard. The loopback listener stays as it was. On the tailnet:

- the dashboard API always wants the dashboard token or a session, even
  with `dashboard.require_token` off; `/send` and `/ingest` want a token;
  the brain and site-builder APIs, which only local runs call, are not
  served;
- only the node's MagicDNS names and Tailscale addresses are accepted as
  `Host`, and every request's sender is asked of Tailscale (WhoIs): one it
  cannot name is refused, each user and device is logged the first time,
  and every request that changes something is logged and written to the
  audit log (`kind: tailnet`, the login, the device, method and path, the
  status). The identity is recorded, not trusted on its own.

The node port (`nodes.listen`) listens on the tailnet at its port, never on
the container's interfaces, when the tailnet is on.

The Overview's Core card and `/data/core/tailnet.json` show the state and
the dashboard's address there; `fridolin doctor` checks it. tsnet sends
its own diagnostics to Tailscale's log service, as every Tailscale client
does.

## Moving the core

The cold tier in the `fridolin-vault` bucket carries the hot tier from one
machine to the next. Inside the core:

```bash
docker exec fridolin-core fridolin core export
```

writes `core/exports/<UTC time>.tar.gz.age`, encrypted to the age identity,
and points `core/exports/latest` at it. It holds the data dir: `config.json`,
`bridge.db` and `vault.db` (each copied with SQLite's `VACUUM INTO`, so a
running bridge is exported consistently; so are the `*.sqlite` databases
of a brain's Codex state), `secrets/`, `audit/`, `brains/`
(the per-brain state), `nodes/`, `tsnet/`, the dashboard's token and
sessions, `google/`, `plaud/`, `otp/`, `rclone/`, and brain folders that lie
in the data dir. It leaves out what the next machine rebuilds or the cold
tier already holds completely: `runs/`, `run-tokens/`, `media/`, `vault/`,
`core/`, `migration/` (a native install's own snapshots), lock files, SQLite's `-wal`, `-shm` and `-journal` files, the
brains' `tmp/` and `cache/`, `node_modules/` and `*.tmp` files. Symbolic
links are left out and named.

Brain folders: in the container they belong in the volume, under
`/data/brains-folders/<name>/`, and travel with the export. A brain folder
outside the data dir (a native install's `~/brains/...`) is not in the
export; the manifest lists it and export and import both warn.

On the machine that takes over, with its core stopped and an empty volume:

```bash
fridolin core run core import --latest
fridolin core run core promote
fridolin core start
```

`fridolin core run COMMAND...` runs a fridolin command in a one-off
container on the volume (`docker compose run --rm`, no published ports)
with the same secrets as `start`; it makes the volume when there is none.

`import` fetches the export, decrypts it, checks every archive member (no
absolute paths, no `..`, regular files and folders only, counts and sizes
as the manifest says) and runs SQLite's `integrity_check` on every
database, all into `core/import/<time>/` in the data dir. Only then does it
move the files into place, with the data dir's instance lock held, so it
never replaces a data dir a bridge runs on. A data dir with data in it is
refused unless `--force`, which first moves what is there to
`core/replaced-<time>/`; nothing is deleted. A `data_dir` in the imported
config that names another folder (a native `~/.fridolin`) is set to the
volume, and `db_path`, `media_dir` or `brain_dirs` outside it are named.

The device id (`core/device-id`, 128 random bits) is not exported: each
data dir has its own, which is what the lease tells apart.

## The core lease

The lease is the object `core/lease.json` in the vault bucket:
`device_id`, `hostname`, `pid`, `started_at`, `renewed_at`, `version` and
`released`. Exactly one device may hold it, because two bridges on one
WhatsApp session fight over it.

- `fridolin core promote` takes it for this data dir: it is refused, exit
  status 3, while another device holds it and renewed it within the
  staleness window (default 5 minutes), with the holder's id, host,
  version and age in the message.
- `fridolin core status` shows this data dir's device id, who holds the
  lease (live, stale or released) and the latest export.
- A core with `core.lease.enabled` takes the lease at start, renews it
  every `renew_seconds` (default 60), and stops its channels when it has
  not renewed for `stale_seconds` minus one renew interval, so it is quiet
  before another device may take over. It stops at once when it finds
  another device's record. A clean shutdown releases the lease.

In the bridge (`core.lease.enabled`, a restart setting): the lease is
taken before any channel exists. A device that holds it live makes the
bridge exit with status 3 ("not starting: another device holds the core
lease"). The store is the one `fridolin core` uses (`$FRIDOLIN_CORE_STORE`,
else `vault.bucket` with `B2_VAULT_KEY_ID` and `B2_VAULT_KEY`); without one
the bridge does not start. When the lease is lost, the bridge disconnects
WhatsApp and does not reconnect, stops the outbox, and refuses every send
from then on; the dashboard stays up, its Overview and `/ready` (503,
`"lease": {"state": "lost", ...}`) say so, and replies the brains still
make wait in the outbox. A clean stop releases the lease after the
channels have stopped. `/data/core/lease-state.json` records each renewal
for `fridolin doctor`.

A copy that another device took the lease from (found at start, or while
running) writes `/data/core/superseded` and refuses to start from then on,
even after that device releases the lease (to update, say): its
`bridge.db` is older than the one that ran since. Docker's restart policy
keeps restarting it, and each start exits with status 3 at once without
reaching the bucket; stop it with `fridolin core stop`. `fridolin core
promote` there removes the mark, after you imported a newer export.

B2 has no conditional write, so taking the lease is write, wait three
seconds, read back: the device whose record is not the one read back gives
up. Two devices that both take a free lease within those seconds could
both believe they won. That needs two cores started on two machines at the
same moment, which moving the core (export, import, promote, start, one
machine at a time) never does; and taking a held lease needs it stale
first, five minutes in which the old core has written nothing.

The flags of `export`, `import`, `promote` and `status`: `--data-dir`
(default `$FRIDOLIN_HOME`, else the config's), `--store` (a folder, or an
rclone `remote:bucket`; default `$FRIDOLIN_CORE_STORE`, else the B2 bucket
with `B2_VAULT_KEY_ID` and `B2_VAULT_KEY`, reached through rclone with the
key in rclone's environment only) and `--bucket` (default
`fridolin-vault`). `promote` and `status` judge the lease with the
config's `core.lease` timings when there is a config, so they agree with
the cores.

## Moving a native install into the container

A native install (`fridolin service install`, data dir `~/.fridolin`)
moves into the core with one command. Its brain folders stay where they
are on the Mac, and so does everything else of your home folder the config
names: the container binds each at its own path, so `brain_dirs`, every
path in a brain's prompt, CLAUDE.md and scripts, and `~` mean the same
files there.

```bash
fridolin core migrate --from-native --dry-run --local --mount ~/fridolin-private
```

prints the plan and changes nothing. What it binds, from the native
config:

| Host path | How | Why |
| --- | --- | --- |
| each brain folder outside the data dir | read-write | the brain's own folder |
| `sandbox.credentials_dir` | read-write | the calendar tokens: the bridge saves a refreshed token or a new Google sign-in there, as natively. Brains never see the folder |
| `sandbox.allow_paths`, `startup_checks.required_files` | read-only | |
| `~/.plaud` with `plaud.brain_access` | read-write | the Plaud CLI's login, which it refreshes there |
| `plaud.phone_dir` | read-write | |
| `~/.gitconfig` unless `sandbox.git_config` is `"deny"` | read-only | your git identity |
| each `--mount PATH` | read-only, and added to `sandbox.allow_paths` | what brains read on the Mac outside their folders (tools, shared skills) |

It refuses the home folder itself, the native data dir or a folder that
holds it (the WhatsApp session is for one bridge only), `~/.ssh`,
`~/.claude` and the like, and paths outside the home folder (Docker
Desktop shares `/Users` by default). The bindings go into
`compose.host.yaml` beside the compose file (on macOS
`~/Library/Application Support/fridolin/`), which `core start`, `stop`,
`update` and `run` add while it exists. It also sets `HOME` and
`FRIDOLIN_HOST_HOME` to the Mac's home folder, which is an empty tmpfs of
the container's user (the image's own HOME was not kept either), and
passes the native service's `FRIDOLIN_BRAIN_PATH` on for the folders in
brain folders.

Brains' runs: with `FRIDOLIN_HOST_HOME` set, a run's `HOME` is the Mac's
home folder too, an empty, private tmpfs under its binds: the brain's own
folder at its path, its state (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, caches)
in the volume as before, `~/.gitconfig` read-only, `~/.plaud` read-only
but for `tokens.json` with `plaud.brain_access`, and the `allow_paths`.
What a run writes elsewhere in `~` goes with the run. The credentials
folder, the other brains' folders and the rest of the home folder are not
there. `~/.local/bin` is the container's own, read-only for runs: the
entry point fills it at start with links to the image's tools named in
`FRIDOLIN_HOME_BIN` (default `claude cloudflared gh node npm npx`; set it
in `compose.host.yaml`'s environment for others), for scripts and skills
that call `~/.local/bin/gh` and the like. `fridolin check --try-sandbox
ID` runs a shell in a brain's sandbox that says which of these it sees;
`fridolin check --try-sandbox ID -- PROGRAM ARGS...` runs a program there
instead, with the environment a run of the brain has (its PATH with the
brain's own folders first, TMPDIR, caches, `FRIDOLIN_TOOLS_DIR`), which is
how a tool is tried where the brain runs it:

```bash
docker exec fridolin-core fridolin check --try-sandbox personal -- \
  python3 ~/fridolin-private/tools/transcribe.py /data/brains/personal/tmp/note.ogg /data/brains/personal/tmp/out
```

Without `--dry-run`, with the native bridge stopped (it takes the data
dir's lock), the command:

1. writes `compose.host.yaml`;
2. makes the age identity if the keychain has none, and copies the native
   service's `CLAUDE_CODE_OAUTH_TOKEN` (the launchd agent's environment)
   into the keychain as `fridolin.claude.oauth.token` when that is
   missing, on `security`'s stdin, never printed;
3. exports the native data dir, read-only (databases with `VACUUM INTO`
   from a read-only connection, temporary files elsewhere), with `media/`,
   which a native install without the vault has the only copy of; with
   `--local` into a new folder of the user cache folder (or
   `--local-dir`), else to the vault bucket;
4. imports it into `fridolin-data` in a one-off container with the
   bindings (`core import --from-native`): `data_dir` becomes `/data`,
   program paths of the Mac's (`claude_path`, `codex_path`,
   `backup.rclone_path`, `plaud.cli_path` naming a file) are dropped so the
   image's own run, and the `--mount` folders go into
   `sandbox.allow_paths`. `brain_dirs` keep their `~/` paths. A volume with
   data is refused without `--force`;
5. takes the lease when `core.lease.enabled`;
6. prints the cutover and the rollback.

Codex sign-ins are files (`brains/<id>/codex/auth.json`) and move with
the export; Claude logs in with the token from the keychain.

The cutover (macOS):

```bash
fridolin core migrate --from-native --dry-run --local --mount ~/fridolin-private
launchctl disable gui/$(id -u)/ai.fridolin.bridge   # or it comes back at the next login
launchctl bootout gui/$(id -u)/ai.fridolin.bridge
fridolin core migrate --from-native --local --mount ~/fridolin-private
fridolin core start
curl -s http://127.0.0.1:8765/health
curl -s http://127.0.0.1:8765/ready
docker exec fridolin-core fridolin check
docker exec fridolin-core fridolin doctor
```

`fridolin core start` runs the image whose tag is the binary's version:
build it first (The image, above) with that tag (`fridolin version`), or
pass `--tag`, to `migrate` as well.

The rollback:

```bash
fridolin core stop
fridolin core migrate --to-native --local
launchctl enable gui/$(id -u)/ai.fridolin.bridge
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ai.fridolin.bridge.plist
```

Once the core has connected to WhatsApp, the native `bridge.db` holds a
stale session. `--to-native` exports the volume (in a one-off container,
the core stopped), checks the export into
`~/.fridolin/migration/<time>/staged/`, moves what the native data dir
holds to `~/.fridolin/migration/<time>/replaced/` and the checked copy
into its place (nothing is deleted; `migration/` itself stays), and takes
`data_dir`, the program paths and `sandbox.allow_paths` from the native
config it replaced. The volume stays; never start the core and the
native bridge together.

### A rehearsal

```bash
fridolin core migrate --from-native --rehearse --mount ~/fridolin-private
```

runs the move on a copy while the native bridge goes on running: it
exports the native data dir read-only into a temporary folder (encrypted
to a temporary identity unless the keychain has one), imports it into the
volume `fridolin-rehearsal` (never `fridolin-data`) and starts the
container `fridolin-rehearsal` in dev mode (`--no-whatsapp`), with no
published port and every binding read-only, and without `~/.plaud`. The
imported config gets `/usr/bin/false` as its agents, so no run spends
tokens or refreshes a login under the native bridge, and Plaud, the
backup, the lease, the tailnet, nodes and the vault off. It then checks
`/health`, `/ready`, `fridolin check` (the brains at their host paths),
`fridolin doctor`, the credentials folder as the bridge sees it, the
image's tools (gh, cloudflared, ffmpeg and the venv's packages),
`fridolin check --try-sandbox` for each brain, and a transcription of
three seconds of a tone with `tools/transcribe.py` and Whisper's smallest
model in the first brain's sandbox (the model is downloaded into the
rehearsal's volume), and removes the container, the volume and the
temporary export (`--keep` leaves them running).

### What does not move

The container is Linux. Programs built for macOS do not run there: Mach-O
binaries, and Python virtualenvs and `node_modules` made on the Mac in a
brain's folder. Do not make those again in place: the folder is the Mac's
too, and a Linux venv there breaks the Mac's copy (and the rollback). A
brain that needs one in the container makes it elsewhere, in its cache
(`python3 -m venv "$XDG_CACHE_HOME/venvs/NAME"`, kept in the volume) and
points the tool at it; packages that need a browser shown on screen
(Playwright with `HEADED=1`) do not work in the container at all.

Anything that calls `launchctl`, `osascript`, `open` or Homebrew's
`/opt/homebrew` fails in the container. Scripts that schedule launchd jobs
for later (a `wa schedule-once`) are run on the Mac instead, where they
reach the core like any other script; a watchdog that restarts the native
bridge is not needed, since Docker restarts the core (`restart:
unless-stopped`) and its health check shows a stuck one. Scripts that read
the native data dir's files (`~/.fridolin/bridge.db`) read a stale copy
after the move: in the container the data dir is `/data`.

launchd jobs of yours that call the bridge keep working: compose
publishes the core at `127.0.0.1:8765` on the Mac as the native bridge
listened, and they keep using `~/.fridolin/scripts.token`, which the move
copies into the volume unchanged (until you rotate it in the core, after
which copy the new one back). In a brain's run the bridge sets
`FRIDOLIN_API` and `FRIDOLIN_BRAIN_TOKEN_FILE` as before.

## Recovery

- **The old machine is gone.** On the new one: import the latest export,
  wait until `fridolin core status` shows the lease stale (5 minutes after
  the old core last renewed), promote, start. What happened after the
  latest export is lost, except what Litestream replicated of `bridge.db`
  and `vault.db` to the snapshots bucket with the vault on
  ([VAULT.md](VAULT.md#snapshots)); export before planned moves.
- **The old machine comes back.** Its core finds another device's lease and
  stops its channels, or refuses to take it at start.
- **An import went wrong.** The data dir's previous content is in
  `core/replaced-<time>/`, and a staged copy that failed to activate stays
  in `core/import/<time>/`.
- **The age identity is lost.** Exports made with it cannot be decrypted.
  Keep a copy of it outside the machines (a password manager).
- **WhatsApp asks to pair again** after a move: the export was older than
  the session's last key update or `bridge.db` did not come along. Never
  delete `bridge.db` to fix it; import again or pair again.

## The M1 gate on real machines

Rehearsed on one Mac with two containers (dev mode, a folder as the
bucket, renew 2 s and stale 8 s; see the pull request), the gate still has
to run on the machines. On each, `fridolin doctor` first; every step's
output goes into the pull request.

Mac mini (core A):

- [ ] `fridolin.tailscale.authkey`, `fridolin.claude.oauth.token`,
      `fridolin.b2.vault.keyid` and `.key` in the keychain; the age
      identity copied to a password manager.
- [ ] `config.json`: `core.tailnet.enabled` and `core.lease.enabled` on,
      `sandbox.enabled` on; `fridolin core run core promote`, then
      `fridolin core start`.
- [ ] `docker exec fridolin-core fridolin doctor`: tailnet up with its
      MagicDNS name, lease renewed, bubblewrap ok.
- [ ] From another tailnet device: `https://fridolin-core.<tailnet>.ts.net/`
      asks for the token; a setting saved there is in `audit/` with your
      login.
- [ ] A WhatsApp message gets an answer; a brain's run cannot list
      another brain's folder (ask it to).
- [ ] `docker exec fridolin-core fridolin core export`; note the time.

Linux VM (core B), Docker Engine:

- [ ] Install Docker and fridolin; secrets as 0600 files in
      `$XDG_CONFIG_HOME/fridolin/secrets/` (the age identity the same as
      A's).
- [ ] With AppArmor on (`aa-status`), a compose override with
      `security_opt: [apparmor=unconfined]`, or doctor's bubblewrap check
      fails; on Ubuntu 24.04 also check
      `sysctl kernel.apparmor_restrict_unprivileged_userns`.
- [ ] `fridolin core stop` on A; on B `fridolin core run core import
      --latest`, `fridolin core run core promote`, `fridolin core start`.
      Under an hour from A's stop to B's first answer.
- [ ] B answers on WhatsApp without a new pairing, under the same tailnet
      name (A's node key moved with the export); A's tailnet device is
      gone or offline.
- [ ] `fridolin core start` on A again: it exits with status 3 (another
      device holds the lease); `fridolin core status` on A says so.

Windows (core C), Docker Desktop on WSL2:

- [ ] The same as on the Linux VM, with the secrets in the Windows
      Credential Manager once `hostsecrets` has it (until then: the
      variables of `container/compose.yaml` set by hand in the shell that
      runs `docker compose`).
- [ ] B stopped; C imports, promotes, starts and answers; B refuses to
      start afterwards.
