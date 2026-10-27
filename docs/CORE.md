# The core in a container

The core is the bridge with its WhatsApp session, brains, memory and audit
log. From M1 of [PLAN.md](PLAN.md) on it can run as one container image,
`fridolin-core`, plus one named Docker volume, `fridolin-data`, which holds
all of its state. That pair is what moves between machines. A native
install (`fridolin service install`) keeps working as before.

## The image

`container/Dockerfile` builds it from the repository's root:

```bash
docker build -f container/Dockerfile --build-arg VERSION=$(git describe --tags --always --abbrev=8) \
  -t fridolin-core:$(git describe --tags --always --abbrev=8) .
```

### On GHCR

CI publishes the image of every commit on `main` to the GitHub Container
Registry, as one image for `linux/amd64` and `linux/arm64`:
`ghcr.io/OWNER/fridolin-core:<version>`, where `<version>` is what
that commit's `fridolin version` reports (`git describe --tags --always
--abbrev=8`, the tag `core start` uses). `:main` names the newest. The
package is public, so pulling it needs no login.

Each image comes with build provenance: a signed statement, made with
GitHub's attestations (Sigstore), that this repository's
`.github/workflows/image.yml` built that digest from that commit on
`main`. Machines take an image by its digest, never by a tag, and check
the provenance before they run it: with GitHub's CLI, which needs no
login for it,

```bash
gh attestation verify oci://ghcr.io/OWNER/fridolin-core@sha256:DIGEST \
  --repo OWNER/fridolin --signer-workflow OWNER/fridolin/.github/workflows/image.yml
```

An SBOM and buildx's own provenance are attached to each platform's
image. The same workflow publishes `ghcr.io/OWNER/fridolin-fride`,
the FrIDE image of a node in a box ([NODES.md](NODES.md#node-in-a-box)),
with the same version tags and provenance.

### What is in it

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
| `TZ` | `UTC` | compose passes `FRIDOLIN_TZ` instead when set, which `fridolin core start`, `run` and `update` set to the host's time zone (or keep from your shell); config's [`timezone`](CONFIGURATION.md#timezone) wins over it for schedules |

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
fridolin core build    # docker build of container/Dockerfile in this checkout, tagged for core start
```

On Windows the compose file goes to `%LOCALAPPDATA%\fridolin\compose.yaml`
(it stays on the machine; `%APPDATA%` roams), and compose gets the
variables Windows programs need (`SystemRoot`, `USERPROFILE`,
`LOCALAPPDATA`, …) besides `PATH` and Docker's own. `core migrate` is
refused there: Windows has no native bridge to move.

`core build` of a clean checkout whose `origin` is on GitHub also gives
the image the address of that commit's box script and the registry of
its images (`ghcr.io/<owner>`), so that the core's `!join` reply offers
a [node in a box](NODES.md#node-in-a-box); the images of that version
must be on GHCR, which is the case once the commit is on `main`. A build
of a checkout with local changes leaves them out.

`--image` and `--tag` pick another image (default `fridolin-core` and this
binary's version); `--compose PATH` runs a compose file of your own.
`stop` never removes the volume. Never run `docker compose down -v` or
`docker volume rm fridolin-data` without a fresh export: the volume holds the
WhatsApp pairing.

### Signing in to the dashboard

The dashboard token is the container's, in the volume
(`/data/dashboard.token`), not in a `~/.fridolin` on the host. To sign a
browser in on the machine that runs the core:

```bash
fridolin open                                     # asks the container, opens the browser
docker exec fridolin-core fridolin open --print   # the same by hand: open the link it prints
```

The link works once, within a minute. From another device, send
`!dashboard` in your WhatsApp chat (a link that works once, within 10
minutes), or `fridolin open --print --tailnet`. The sign-in page knows
the bridge runs in the container (`"container": true` in `/ready`) and
names these steps; pasting the token is its last resort, read with
`docker exec fridolin-core cat /data/dashboard.token`. `fridolin open`
with `--config PATH` or `$FRIDOLIN_CONFIG` always means a native
bridge's config.

### Secrets

`core start` and `core update` read the secrets from the host and put them
in the environment of the `docker compose` process only: not on a command
line, not in a file. Compose passes them on to the container by name. On
macOS they are generic passwords in the login keychain, account
`fridolin`; on Windows generic credentials in the Windows Credential
Manager, target name the secret's name, user `fridolin`, kept for this
user on this machine; on Linux files of mode 0600 in
`$XDG_CONFIG_HOME/fridolin/secrets/` (default `~/.config/...`), named
after the service.

| Keychain service | Container variable | |
| --- | --- | --- |
| `fridolin.age.identity.pq` | `FRIDOLIN_AGE_IDENTITY_PQ` | post-quantum (ML-KEM-768 + X25519); made by `core start` when missing |
| `fridolin.age.identity` | `FRIDOLIN_AGE_IDENTITY` | the X25519 identity of before #158: passed on while stored, only to read old exports and node-target passwords |
| `fridolin.export.key` | `FRIDOLIN_EXPORT_KEY` | the exports' inner layer; made by `core start` when missing |
| `fridolin.claude.oauth.token` | `CLAUDE_CODE_OAUTH_TOKEN` | from `claude setup-token` |
| `fridolin.tailscale.authkey` | `TS_AUTHKEY` | |
| `fridolin.b2.vault.keyid`, `.key` | `B2_VAULT_KEY_ID`, `B2_VAULT_KEY` | the `fridolin-vault` bucket |
| `fridolin.b2.snapshots.keyid`, `.key` | `B2_SNAPSHOTS_KEY_ID`, `B2_SNAPSHOTS_KEY` | the `fridolin-snapshots` bucket |
| `fridolin.vault.crypt.password`, `.password2` | `FRIDOLIN_VAULT_CRYPT_PASSWORD`, `..._PASSWORD2` | made by a native bridge with the vault on, while the cold tier is empty ([VAULT.md](VAULT.md#keys-and-passwords)) |
| `fridolin.restic.password` | `RESTIC_PASSWORD` | the same, while the restic repository does not exist |
| `fridolin.vault.seal.key` | `FRIDOLIN_VAULT_SEAL_KEY` | the cold tier's inner layer; made by `core start` when missing |
| `fridolin.snapshots.crypt.password` | `FRIDOLIN_SNAPSHOTS_CRYPT_PASSWORD` | the outer layer of the restic repositories and Litestream; made by `core start` when missing |
| `fridolin.litestream.seal.key` | `FRIDOLIN_LITESTREAM_SEAL_KEY` | Litestream's inner layer; made by `core start` when missing |
| `fridolin.github.token` | `FRIDOLIN_GITHUB_TOKEN` | optional, and never named as missing: a typed GitHub token, the second choice to the GitHub App ([GITHUB.md](GITHUB.md)) |

The GitHub App that the dashboard connects is not a host secret: the
container cannot write the keychain. Its key goes to
`secrets/github-app.age` in the volume, encrypted to the post-quantum
identity, and moves with the export ([GITHUB.md](GITHUB.md#where-the-keys-are)).

Which store each key opens, and the recovery sheet to keep: [VAULT.md](VAULT.md#encryption).

A secret that is missing is named and left out. `fridolin core secrets`
lists which are stored (never their values); to store one, on any system:

```bash
fridolin core secrets set claude.oauth.token     # or the full name, fridolin.claude.oauth.token
```

It reads the value from the terminal without echo (or from stdin when
that is no terminal), checks that an age identity or a key has the right form, and refuses to
replace a stored value without `--replace`. On macOS `security
add-generic-password -a fridolin -s fridolin.claude.oauth.token -w` works
too (`-w` last makes it prompt), on Windows `cmdkey
/generic:fridolin.claude.oauth.token /user:fridolin /pass` (it prompts).

### Moving the host secrets

`fridolin core receive` on the machine that takes over and `fridolin core
send --to ADDRESS` on the host of the current core carry the secrets
across directly, over the tailnet, so nobody types them again. Both run
on the hosts, never in the container (the container has no host
secrets).

1. The receiver asks Tailscale's LocalAPI for this device, makes an
   ephemeral post-quantum age key (ML-KEM-768 + X25519) and a one-time code (eight characters),
   listens once on its Tailscale IPv4 address on a free port, and prints
   the address and the code. It waits ten minutes (`--timeout`).
2. The sender connects. Each side asks Tailscale who the other is
   (WhoIs) and refuses a device of another login (tagged devices: one
   without a tag in common).
3. The receiver sends its public key; the sender sends an ephemeral
   public key of its own and an HMAC-SHA256, keyed with the code, over
   both keys: it proves the code without sending it. A wrong code ends
   the receiver; it takes one connection only.
4. Both show four words, the SHA-256 of both public keys mapped onto a
   list of 256 words. A party in the middle would have to swap the keys,
   and the words would differ. The sender's person types `yes` only when
   both screens show the same words; `core send` refuses to run without a
   terminal for that.
5. The sender encrypts the secrets (JSON, names and values) to the
   receiver's key with age and sends them. The receiver decrypts them in
   memory, refuses names it does not take and values that are not one
   printable line, and refuses the whole transfer when a value differs
   from one it already holds, unless `--replace`. It then writes each into
   its host store; when one write fails, it puts back what each earlier
   write replaced. Nothing goes to disk in clear, and no value is
   printed.

The secrets sent are the age identities (post-quantum and, while stored,
X25519), the export key, the Claude token, both B2 key pairs, the vault's
crypt passwords and seal key, the restic password, the snapshots' crypt
password, the Litestream seal key and a typed GitHub token while one is stored; the Tailscale
auth key only with `--with-tailscale-key` (the new machine logs in to the
tailnet itself, and the core's own node key moves in the export). Codex
sign-ins are files in the volume and move with the export. Each side
appends a line to `secrets-transfer.log` beside its compose file: the
time, the direction, the other device and its login, the names stored or
unchanged, the result, never a value.

Between two core hosts (every approved node is one unless it turned
core-host off), a core move, or `fridolin nodes core-secrets-transfer
SRC DST --yes` on the core, moves the same names over the node channel
instead, end to end between the two node agents: the target signs a
one-time key with its device key, and the source encrypts only to the
device key the core approved at the target's join, once the owner
confirmed the target with their `!movecore` vote (or to a key it pinned
itself), so nobody compares four words. docs/NODES.md, "core-host", has
the details.


The post-quantum age identity and the export key encrypt every export.
`core start` makes them the first time and says so: copy them into the
keychain of every machine that may run the core (`core send` does), or
the exports made with them cannot be read there. A new identity on a
second machine does not replace the first; exports made before stay
encrypted to the old one. Exports made before #158 (age X25519, one
layer) still import while `fridolin.age.identity` is stored.

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
a Linux host with AppArmor (Ubuntu, Debian with AppArmor on), Docker's
`docker-default` profile also forbids the mounts, and Ubuntu (23.10 on)
confines the user namespaces even an unconfined process makes
(`kernel.apparmor_restrict_unprivileged_userns=1`: bubblewrap then fails
with "setting up uid map: Permission denied"). So the core runs under a
profile of its own, `fridolin-core`: unconfined like
`apparmor=unconfined`, but allowed to make user namespaces, without
lifting the restriction for the rest of the machine. Install it once, as
root:

```sh
sudo ~/.local/bin/fridolin core apparmor   # writes /etc/apparmor.d/fridolin-core and loads it
```

`fridolin core start`, `update` and `run` then write
`compose.apparmor.yaml` beside the compose file with
`apparmor=fridolin-core` (with `apparmor=unconfined` while the profile is
not loaded, and a warning on a host that restricts user namespaces). On
macOS and Windows, and on Linux without AppArmor, there is no such file.
`fridolin doctor` in the container (`docker exec fridolin-core fridolin
doctor`) tries it; a brain whose sandbox cannot start refuses every
message rather than run without it.

## The site

The site process ([SITE.md](SITE.md)) runs in the container too, started
by the bridge as on the Mac. With `--container` the live site binds the
port of its listen address (`127.0.0.1:8780` by default) on the
container's interfaces, for the same reason as the dashboard's, and
compose publishes it as `127.0.0.1:8780:8780`: the host's Cloudflare
tunnel, whose public host name points at `http://127.0.0.1:8780`, reaches
it as it reached the native site. The site trusts the client address and
scheme forwarded from the loopback and from Docker's private addresses,
which is where the tunnel's requests come from in the container. Keep the
site's listen port and the mapping the same; a site on another port needs
its own mapping in a compose override.

A tunnel run by the bridge (*Run the tunnel* on the Site page) runs in the
container, where `127.0.0.1:8780` is the site itself, and moves with the
core; a tunnel run by a launchd agent on the Mac goes through the published
port. Run one of the two, not both: Cloudflare spreads the requests over
every connector of a tunnel. To move a host's tunnel into the core, turn
*Run the tunnel* on first, check that the domain answers, then stop the
host's `cloudflared` service: the site never goes down. Every host name of
the tunnel must point at the site then (a host port is out of the
container's reach); the built-in wiki replaces a separate wiki server
([FILES.md](FILES.md#on-your-site)).

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

writes `core/exports/<UTC time>.tar.gz.age`, sealed under the export key
and then encrypted with age to the post-quantum identity,
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
outside the data dir (bound in from the Mac's home) travels too, as far
as its backup settings let it, and comes home on the way back
([Brain folders on another machine](#brain-folders-on-another-machine));
one whose backup keeps it home is listed in the manifest, and export and
import both warn.

On the machine that takes over, with its core stopped and an empty volume:

```bash
fridolin core run core import --latest
fridolin core run core promote
fridolin core start
```

The whole sequence, with the host secrets and the way back, is in
[Moving the core to another machine](#moving-the-core-to-another-machine).

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
  staleness window (default 20 minutes), with the holder's id, host,
  version and age in the message.
- `fridolin core status` shows this data dir's device id, who holds the
  lease (live, stale or released) and the latest export.
- A core with `core.lease.enabled` takes the lease at start and renews it
  every `renew_seconds` (default 300). It stops its channels when, and
  only when, it finds another device's record: a takeover. A clean
  shutdown releases the lease.
- A lease store that does not answer is not a takeover. When Backblaze
  refuses (a daily transaction cap used up: `403
  transaction_cap_exceeded`), the network is down or B2 answers 5xx, the
  core keeps its channels and calls the lease *unverified*: `/ready`
  (still 200 while WhatsApp is connected), the Overview, `fridolin
  doctor` and the log say so, and it retries after 30 seconds, then
  twice as long each time up to the renew interval. It stops only if the
  store, once it answers, shows another device holding the lease.
- A core that starts while the store cannot be read starts too,
  unverified, when its own records say this device was the last holder:
  `core/lease-state.json` names this device id with the lease held,
  unverified or released by it, and no `superseded` mark says another
  device took over. A device that never held the lease refuses to start
  without seeing it, so an outage never puts two bridges on one WhatsApp
  session. With the store answering, a start takes the lease as before
  (a stale or released lease, or none) or refuses (another live holder).

A renewal is one read and one write of the lease object. The core keeps
one `rclone rcd` (rclone's remote control daemon) for it, which logs in
to B2 once a day: a renewal costs two Class B transactions (a HEAD and a
download) and an upload, which is free, so the default five-minute
renewal spends 576 of the free tier's 2,500 Class B a day and no Class C
([VAULT.md](VAULT.md#b2-transactions)).

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
first, twenty minutes in which the old core has written nothing.

A core that cannot reach the store for longer than the staleness window
keeps running (above), so a device that takes the stale lease meanwhile
runs next to it until the old core reaches the store again, finds the
new record and stops. That needs the store unreachable from the old core
but reachable from the new one, and a person promoting the new one: the
price of not taking WhatsApp down whenever Backblaze has a bad hour.

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
| each of `vault.secret_dirs` | read-only | folders of secrets the vault backs up to B2 ([VAULT.md](VAULT.md#secret-folders)); it never writes there |
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
   program paths of the Mac's (settings naming a file) are replaced:
   `claude_path` and `codex_path` become the image's
   `/usr/local/bin/claude` and `/usr/local/bin/codex`, and
   `backup.rclone_path` and `plaud.cli_path` are dropped so the image's
   own on PATH run; the `--mount` folders go into
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

## The core moves itself

Between two core hosts ([docs/NODES.md, "core-host"](NODES.md#core-host))
the core moves on its own, without the steps of the next section by hand
(#187). The owner asks for it in their WhatsApp chat with `!movecore
<node>` ([below](#moving-the-core-from-whatsapp)), or from the CLI:

```sh
fridolin nodes move-check SRC DST       # the pre-flight, read only (POST /api/nodes/move-check)
fridolin nodes move SRC DST             # the same, and how to start it
fridolin nodes move SRC DST --yes       # move the core, and follow it to its end
fridolin nodes move-status ID           # how move ID went (GET /api/nodes/moves/{id})
```

SRC and DST are each a node's id or its name, in any case
(`fridolin nodes move-check mac-mini fridolin-droplet`). A name more
than one node has is refused, with each node's id.

**Before.** SRC and DST are approved nodes, both online, neither with
core-host turned off. A DST that never held the core needs the owner's
vote: `!movecore DST` says so in its report, and "Move now" confirms
it; `fridolin nodes move` and `POST /api/nodes/move` refuse it until
then (a stolen dashboard session alone cannot send the secrets to a new
machine). The way back to a machine that ran the core before this
version is confirmed already. Both run the core's version
(`fridolin nodes update ID` first). Each is a native host or a box
([below](#moving-the-core-to-a-box)); a native Windows node service
cannot reach the owner's Docker Desktop and refuses with a reason (make
that machine a box instead). A native DST
needs Docker and a current GitHub `gh` (`brew install gh`; on Linux
from GitHub's own apt repository, <https://cli.github.com>: Ubuntu
24.04's `gh` 2.45 lacks `attestation verify --source-ref`; no login),
which checks the image's provenance before it is pulled; the pre-flight refuses a DST without the image and without
`gh`. On Ubuntu, run `sudo fridolin core apparmor` once on DST.

**The pre-flight** asks both agents (`core.check`): the versions; the
core on each as the lease and the bucket see it (SRC holds the lease, DST
does not); the image of the core's version by digest and whether DST has
it (`core.image.pull`, checking only); DST's free disk against the latest
export; the host secrets by name and the pin; whether DST runs FrIDE
(whose tasks could then read the secrets); and what of SRC stays behind
(a fixed scan, report only: `compose.host.yaml` binds, a Cloudflare
tunnel run by a launchd or systemd job, host jobs that call
`127.0.0.1:8765` or read `scripts.token`, Apple-only work on a Mac). What
blocks the move is listed under "It cannot run".

**The move**, `fridolin nodes move SRC DST --yes`:

1. While the core runs: DST pulls the image by digest after checking its
   build provenance (`fridolin core pull --from REF@sha256:…`, which
   tags it as its version); the host secrets go from SRC to DST end to
   end, as `core-secrets-transfer` sends them; DST rehearses
   (`fridolin core rehearse`: the latest export imported into the scratch
   volume `fridolin-move-rehearsal`, every member and database checked,
   the volume removed; never `fridolin-data`, never the lease). A failure
   here stops nothing.
   **Secret files.** With the host secrets go the secret files of the
   core's credentials folder (`sandbox.credentials_dir`, a folder under
   `~`): `google_oauth_web_credentials.json`, `dashboard-users.json`,
   `google_calendar_credentials_<brain>.json` and
   `google_calendar_token_<brain>.json`, nothing else of the folder.
   SRC reads them through its core's container (`fridolin core run core
   files export`), seals them and the folder's name under `~` beside the
   secrets to DST's one-time key, and DST writes them through its own
   (`core files import`) into that folder under its core's home: each
   0600, only the files it has not got, so a machine that ran the core
   before (a Mac whose `compose.host.yaml` binds its own folder) keeps it
   exactly as it was. Elsewhere the container's home is the volume
   `fridolin-home`, which no export carries. The core relays them and
   sees their names only. A `credentials_dir` in the data dir needs
   none of this: the export carries it.
   **Local files.** What stays on the machines, the data dir's
   `core/local-secrets/` (the GitLab connection and GitHub's commit name,
   [CONNECTIONS.md](CONNECTIONS.md#gitlab)), is in no export. It goes the
   same way, named `local:gitlab.json` and `local:github.json`, and DST
   writes it into its data dir's `core/local-secrets/`, which the import
   keeps; there one that differs is replaced, since the moving core's is
   the newer. Forgetting a former core host removes those a move wrote
   that were not there.
2. The core signs a **move plan** with its update key (the one nodes
   already pin for updates): the move id, SRC and DST, SRC's data dir by
   its lease device id, the version, the image, when it was issued, the
   handoff deadline (15 minutes) and `ready_within` (10 minutes). It
   writes its record of the move into `nodes/moves/<id>.json`, which the
   final export carries, and hands the plan to DST (`core.handoff`), then
   to SRC (`core.release`). An agent refuses a plan that is not signed
   by the pinned core key, does not name it in that role, is older than
   15 minutes, whose deadline passed, whose id it saw before, or that is
   for another version than its own.
3. From here only the two agents act, and they coordinate only through
   the lease and the exports in the bucket:
   - SRC: `fridolin core stop` (a clean stop releases the lease), checks
     that the lease is released by its data dir, else starts the core
     again; then `fridolin core run core export --move ID` (the export's
     manifest, and its info beside the archive, `core/exports/<time>.json`,
     name the data dir's device id and the move).
   - DST: waits until the lease is released by SRC's data dir and the
     latest export is SRC's of this move (until the deadline, else it
     gives up and changes nothing), then `core run core import --latest
     --force`, `core run core promote` (never after the deadline), `core
     start`, and asks `/ready` until WhatsApp is connected and the lease
     held, within `ready_within`.
4. The core that runs then (on DST, the same tailnet name) follows its
   record from the export, asks both agents (`core.move.status`), tells
   SRC that DST runs it, which ends SRC's watch, and tells the owner on
   WhatsApp. `fridolin nodes move --yes` keeps asking while the core is
   away and prints each step; run it against the core's tailnet address
   to see the end from DST.

**Automatic rollback.** Every step is written to `core-moves/<id>.json`
in each agent's state dir, and a restarted agent goes on from there.

| What went wrong | What happens |
| --- | --- |
| The image, the secrets or the rehearsal | Nothing stopped; the move is cancelled. |
| SRC's stop does not release the lease, or its export fails | SRC starts the core again on its own data. |
| DST's import or promote fails, or the deadline passes | DST takes no lease. SRC, at the deadline plus 2 minutes, finds the lease still released by itself: `core run core promote`, `core start` on its own data. |
| DST started, but is not ready within `ready_within` | DST stops, exports (move ID, its own device id: it may have answered messages) and frees the lease (`fridolin core release`). SRC finds the lease released by DST and DST's export of the move: `import --latest --force`, `promote`, `start`. |
| DST released the lease without an export of the move | SRC waits 15 minutes for one, then starts on its own data. |
| DST stopped renewing the lease (power) | Once the lease is stale (20 minutes), SRC imports DST's last export of the move, or starts on its own when there is none; what DST received since is lost, and WhatsApp may ask to pair again ([Recovery](#recovery)). |

The owner hears, just before SRC is told to stop, that the core stops
now and how long it is expected to be offline, and how the move ended
from the core that runs afterwards: "Running on DST now", with what does
not work there and `!movecore SRC` to move back, or "The core move to
DST failed and the core runs on SRC again", with what the hosts said. A
lockdown refuses a move, and every step of one through `fridolin nodes
call`.

The commands the agents run, also by hand on a host: `fridolin core
status --json` (this data dir's device id, the lease, the latest
export's info), `core export --move ID --json`, `core release`, `core
pull --from REF@sha256:… [--check] [--json]` and `core rehearse
[--json]`.

### Moving the core to a box

A [node in a box](NODES.md#the-core-in-a-box) can be SRC or DST of the
same move, with the same plan, lease and rollback: the agent in the
box's `node` container does each step itself, with the same `fridolin
core` commands, on the box's data volume `fridolin-box-data` instead of
a core container's `fridolin-data`, and runs the core as its own child
(`fridolin run --container --box`). Nothing of it reaches Docker:

| Step | A native host | A box |
| --- | --- | --- |
| The image | `core pull --from REF@sha256:…` | the box runs its image by digest; a box on another version is updated first (`fridolin nodes update ID`) |
| The host secrets | the keychain (macOS) or files under `~/.config` | the box's sealed store in its node volume; the core gets them in its environment, the names `core start` passes |
| The secret files | `core run core files import` into the core's home | `core files import` into the box's home volume `fridolin-box-home` |
| The rehearsal | the scratch volume `fridolin-move-rehearsal` | a scratch folder in the node's volume |
| Stop, start | `fridolin core stop`, `core start` (compose) | the agent stops its core (SIGTERM) or starts it, and writes the box's role (`core-role.json`), which a restarted box follows |

What changes for the owner when the core runs in a box (the pre-flight
says so): the brain folders live in the volume `fridolin-box-data`,
not in a folder of that machine (Finder, Obsidian there cannot edit
them), and travel with every export; the dashboard is at
`http://127.0.0.1:8765` on the box's host (`FRIDOLIN_BOX_DASHBOARD_PORT`),
the site at its port 8780 for a Cloudflare tunnel on that host; what was
bound to SRC (scripts on a Mac, Apple builds) stays there and runs on
its node over the node channel while it is online. The box needs its
core settings (`core.yaml`) and, on Linux with AppArmor,
`FRIDOLIN_BOX_APPARMOR` ([NODES.md](NODES.md#the-core-in-a-box)); the
pre-flight refuses it without them. A Windows PC becomes a core host
this way, without WSL commands or a Credential Manager: Docker Desktop
runs the box.

### Moving the core from WhatsApp

In the owner's direct chat with Fridolin (never a group, `/ingest`, the
site, Plaud, or a message the bridge's own account sent; no brain has a
tool for it):

- **`!movecore`** lists the core hosts in plain words: which one runs
  the core now (the one whose data dir holds the lease, as `core.check`
  says), and for each other one whether the core can move there
  ("ready: `!movecore icebase`"), the first thing that keeps it from
  moving there, that it is offline, or that its core-host waits for
  `!approve`.
- **`!movecore <node>`**, by name or id, answers "Checking…", runs the
  same pre-flight as `fridolin nodes move-check`, and sends the report:
  source → target, what the target is (box or node, OS, id, device key
  fingerprint), the expected time offline, what does not work on the
  target (the machine-bound scan), what becomes of the site, and the
  warnings. When something blocks the move, the report ends with it and
  no poll. Otherwise a poll follows: **Move now** / **Cancel**.
- The poll is an approval of the question kind (M5's approvals): only a
  vote from the owner's numbers counts, a vote from anyone else is
  recorded in the audit log and ignored, the dashboard cannot answer it
  (a stolen dashboard session alone cannot move the core), and no vote
  within 30 minutes is a no ("No vote in time"). One move at a time, and
  `!movecore <node>` answers at most 3 times an hour.
- **Move now** runs the move exactly as `fridolin nodes move SRC DST
  --yes` does (the same code, `nodes.StartMove`, which checks again
  first): "Moving the core…", then "Stopping the core on SRC now", then
  the end, from whichever core runs. During a lockdown `!movecore` is
  refused, and a "Move now" from before is a no.
- **The first move to a machine** that never held the core says so in
  the report ("🔐 … has never held the core", and what that means), and
  the owner's "Move now" is what confirms it as a core host: the core
  then names its device key to the source. Nothing on either machine
  needs to be run first.
- **The way back** is the same command the other way round:
  `!movecore mac-mini`.

**Keep or forget the secrets.** After a move, the core asks in a second
poll whether the machine it left keeps its copy of the host secrets:
**Keep them on SRC** makes the next move faster; **Forget them on SRC**
runs `core.secrets.forget` there, which removes what moves brought that
machine and nothing it held before ([Forgetting a former core
host](#forgetting-a-former-core-host)): the host secrets it received
over the node channel, the secret files they wrote, and the core's data
dir when a move's import put it into an empty one. No vote within 24
hours forgets them. After a move that failed once the secrets had gone
to DST, the same poll asks about DST. Revoking a core host (`!revoke`,
the Nodes page, `fridolin nodes revoke`) has it forget them first when
it is connected, and `!revoke` says what it forgot.

### Forgetting a former core host

A machine the core moved away from must not keep the WhatsApp session or
the credentials a move brought it once the owner says forget (#266). It
must not lose what it held before either: the core's original home keeps
its own data dir, to move back to, and a machine keeps the secrets typed
on it. So the node agent records each thing a move brings as it comes,
in `core-secrets-received.json` in its state dir, and forgetting removes
exactly what that record names:

| What | Recorded when | Removed by forgetting |
| --- | --- | --- |
| Host secrets | `core.secrets.receive` stored one the machine did not hold | from its secret store (keychain, files, a box's sealed store) |
| Secret files of `sandbox.credentials_dir` | the move wrote one that was not there (one that was is kept as it was, and never recorded) | `fridolin core run core files forget`, those names only, regular files only |
| The core's data dir (`fridolin-data`) | the move's import found it empty: nothing but its device id (`core status --json` says `"data_dir": "empty"`) | `fridolin core run core forget-data`: `bridge.db` (the WhatsApp session), `vault.db`, `config.json`, the tokens, the brain folders a move put into `brains-folders/`, `core/` with what later imports moved aside |

A data dir that held a core's data when a move first imported into it
is the machine's own from before the moves (`"own_data"` in the record):
forgetting keeps it, and says so ("Its own data dir stays: it held the
core's data before move … imported into it"). One a move brought stays
the moves' through later moves, also when the core ran on it meanwhile.

The data dir goes only when it is safe, checked by the agent and again
by `core forget-data` itself, which holds the data dir's lock while it
works: no move runs on the machine (it waits a few polls for the
source's watch to end), no core runs on the data dir, and another
device's core holds the lease, live. Otherwise the data dir and the
files stay and the report says why ("the core runs here", "no other
machine's core holds the lease, so this copy may be the core's only
one"); the host secrets go regardless. The order is the data dir, the
files, then the secrets, since reading the lease takes the bucket's
keys. What could not be removed stays in the record for the next forget.

The owner's message lists what went:

```text
🧹 *icebase* forgot what moves brought it:
• host secrets: fridolin.claude.oauth.token, fridolin.b2.vault.keyid, …
• secret files: ~/.config/fridolin/dashboard-users.json, …
• the core's data dir: bridge.db, brains-folders/Personal, config.json, core, vault.db
```

The volume `fridolin-data` stays, empty: a later move into it is a move
into a fresh core host. The `fridolin-home` volume keeps everything but
the files named. Removing is deleting files; on an SSD nothing can
promise the blocks are overwritten, so rotate what the machine held when
it is lost or no longer trusted ([A core host that is
gone](#recovery)).

`fridolin doctor --node` on the machine says what is left there
(`node-core-leftovers`), from the record alone: a warning for a former
core host (the source of its latest move, done, or the target of one
that did not end there) that still holds what moves brought it, or its
own data dir from before them, which forgetting never removes.

**By hand.** A data dir an older fridolin imported (before #266: the
record has no word on it, and doctor says so) or one forgetting could
not remove: on that machine, once the core runs elsewhere, `fridolin core
run core status` must show another device holding the lease; then
`fridolin core run core forget-data` empties the volume (or `docker
volume rm fridolin-data` with no container on it). The secret files are
in the `fridolin-home` volume, under the folder `sandbox.credentials_dir`
names; `fridolin core secrets` lists the host secrets, and the machine's
keychain or `~/.config/fridolin/secrets/` holds them.

The audit log has `core_move_requested` (each `!movecore <node>`, and
whether a poll went out), `core_move_approved` (the vote: started,
cancelled, expired, refused), `core_move_ended` (done, rolled back,
failed) and `core_secrets` (kept, or forgotten with the number of host
secrets, secret files and entries of the data dir removed).

### The site's tunnel

The site and its Cloudflare tunnel token live in the data dir
(`site/`), which the export carries. **When the tunnel runs inside the
core** (*Run the tunnel* on the Site page, `site.tunnel`), it moves with
the core: once the core runs on DST, `cloudflared` connects from there
with the same token, and Cloudflare sends the domain's requests to the
new connector by itself. That holds the same way for a core in a box
(#191): the box has no `cloudflared` of the host's, the tunnel is a
process of the core in the box, and the token moves in the export like
every other file of the data dir, not as a host secret.

The pre-flight says what becomes of the site:

| The site | The report says |
| --- | --- |
| tunnel in the core, running | it moves with the core: the domain answers from DST once the core runs there |
| tunnel in the core, turned on but not running | the same, and a warning: the domain does not answer now either |
| tunnel run by a launchd/systemd job on SRC (the machine-bound scan) | the domain goes offline once the core moves: turn on *Run the tunnel* first ([The site](#the-site)) |
| on, without a tunnel in the core | the site moves to DST, reachable only as the core is there |

After a move that is done, the core that runs on DST asks whether the
site answers at its domain (`https://<domain>/_fridolin/health`, a few
times over three minutes, while the tunnel connects) and tells the owner
"The site answers at …, from DST", or what the domain check found.

## Moving the core to another machine

The runbook a coding agent follows when the owner clones the repository on
another machine and says "move the core to this machine" (AGENTS.md, path
C), to test a takeover or for good. When the machine is (or can become) a
[node in a box](NODES.md#the-core-in-a-box) and both machines are core
hosts, [the core moves itself](#moving-the-core-to-a-box) instead, with
no steps by hand. The new machine runs the same
container image with Docker; on Windows that is Docker Desktop on WSL2.
`fridolin setup-machine --core` says at any point what is done and what
is next; it only looks.

What moves, and how:

| What | How |
| --- | --- |
| The data dir: `bridge.db` (the WhatsApp session), brains' state, memory, audit, the core's tailnet node key, nodes, Codex sign-ins | `fridolin core export` on the old machine, `core import --latest` on the new one, through the vault bucket |
| The host secrets: the age identity, the B2 keys, the vault and restic passwords, the Claude token | `fridolin core send` → `fridolin core receive`, directly over the tailnet ([Moving the host secrets](#moving-the-host-secrets)) |
| The core lease | released by `fridolin core stop` on the old machine, taken by `core promote` on the new one |
| Brain folders in the volume (`/data/brains-folders/`) | with the export |
| Brain folders bound in from the Mac's home (`compose.host.yaml`, a migrated native install) | with the export too, as far as each brain's backup settings let them reach the cloud; they come home on the way back: see [Brain folders on another machine](#brain-folders-on-another-machine) |

**WhatsApp continuity.** The WhatsApp session is `bridge.db`, and it moves
whole: the new core answers as the same linked device, without a new QR
code, as long as the old core stopped before the export and never runs
again on its old copy. Two cores on one session fight over it; the lease
refuses a second start, and `fridolin core stop` removes the old
container, so Docker's restart policy does not bring it back. Never delete
`bridge.db` or the `fridolin-data` volume to "fix" anything.

### 1. Tools on the new machine

Ask once, naming them, then install.

Windows (PowerShell):

```powershell
winget install --id Git.Git -e --accept-source-agreements --accept-package-agreements
winget install --id GoLang.Go -e --accept-source-agreements --accept-package-agreements
winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements
# WSL2, once, as administrator; Windows asks for a restart afterwards:
Start-Process wsl -Verb RunAs -Wait -ArgumentList '--install','--no-distribution'
winget install --id Docker.DockerDesktop -e --accept-source-agreements --accept-package-agreements
$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')
```

Tell the owner when a restart is due (after `wsl --install`, and Docker
Desktop may want one), and continue after it. Start Docker Desktop for its
first run (`Start-Process "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe"`):
the owner accepts its terms and keeps the WSL 2 engine (Settings →
General → "Use the WSL 2 based engine"). Then `docker info` must answer.
For a test, also turn off sleep while on power, or the core sleeps with
the PC.

macOS: `brew install go git` and `brew install --cask tailscale orbstack`
(or Docker Desktop). Linux: Docker Engine (`curl -fsSL
https://get.docker.com | sh`, then `sudo usermod -aG docker $USER` and a
new login), `golang`, `git`, and Tailscale's install script; with AppArmor
(Ubuntu), after the build below, `sudo bin/fridolin core apparmor` once
([the sandbox](#the-brains-sandbox-in-the-container)).

Tailscale: `tailscale up`, open the link it prints for the owner, who
signs in with the account of the core's tailnet; `tailscale status` then
lists the old core's machine and `fridolin-core`.

### 2. Build fridolin and the image

In the clone:

```powershell
git checkout --detach <commit>   # the commit the old core runs, e.g. f4bd5e47
$env:CGO_ENABLED = "0"
$v = git describe --tags --always --abbrev=8
go build -trimpath -ldflags "-X github.com/Icewater1337/fridolin/internal/version.version=$v" -o bin\fridolin.exe .\cmd\fridolin
$fridolin = "$PWD\bin\fridolin.exe"
& $fridolin version             # the same version string as the old core's
& $fridolin core build          # docker build of container/Dockerfile, tagged fridolin-core:<this version>
```

(`make build` and `bin/fridolin core build` elsewhere.) Build the same
commit the old core runs, or newer: `fridolin core start` runs the image
of the binary's own version. Set the version as above: a plain `go build`
calls itself `dev-<commit>`, so the core's version would differ from the
old machine's for the nodes and `fridolin doctor`. The image build takes
about ten minutes.
On Windows `fridolin core` keeps its compose file in
`%LOCALAPPDATA%\fridolin\`.

### 3. The host secrets, machine to machine

On the new machine, in the owner's normal (not elevated) shell, since the
Windows Credential Manager and the keychain are per user:

```powershell
& $fridolin core receive
```

It prints its tailnet address and a one-time code, and waits ten
minutes. On the old core's host (the Mac mini), in a terminal there: the
owner runs, or an agent on the Mac runs in a terminal the owner sees,

```sh
fridolin core send --to 100.x.y.z:PORT --code abcd-efgh
```

Both machines then show four words. The owner compares them and types
`yes` on the Mac only if they are the same; the secrets go across and
the receiver prints which it stored (names only). Windows Defender
Firewall may ask whether `fridolin.exe` may accept connections: the owner
allows it, or the sender cannot connect. Check with `fridolin core
secrets` (stored or missing, never the values).

Without the old machine (it is gone), the owner enters each from their
password manager on the new machine, typed without echo:

```powershell
& $fridolin core secrets set age.identity
& $fridolin core secrets set b2.vault.keyid
& $fridolin core secrets set b2.vault.key
& $fridolin core secrets set claude.oauth.token
# and, with the vault on: b2.snapshots.keyid, b2.snapshots.key,
# vault.crypt.password, vault.crypt.password2, restic.password
```

The owner types the values into that terminal, never into the chat.

### 4. The move

```sh
# old machine (Mac mini)
fridolin core stop                       # releases the lease; WhatsApp is off from here
fridolin core run core export            # the final export, of the stopped volume
```

```powershell
# new machine
& $fridolin core run core import --latest     # --force when the volume has data; it moves that aside
& $fridolin core run core promote             # takes the lease
& $fridolin core start
```

Run the new machine's three commands there, in its own terminal (or have
an agent on it run them). Nothing reaches the new machine through the
core once it is stopped: a node's FrIDE or files are only reachable while
a core runs. Wait for the export's `Exported … to core/exports/<time>…`
line before the import, or `--latest` fetches the export before it.

Stopping first is what keeps WhatsApp whole: an export taken while the old
core runs misses what arrives until it stops. `promote` is refused (exit
status 3) while the old core holds the lease: stop it, or, when it is
gone, wait until `fridolin core run core status` says the lease is stale
(20 minutes after its last renewal).

### 5. Verify

```powershell
curl.exe -s http://127.0.0.1:8765/health                 # ok
curl.exe -s http://127.0.0.1:8765/ready                  # "whatsapp":"connected", lease held
docker exec fridolin-core fridolin doctor
docker exec fridolin-core fridolin check
& $fridolin core run core status                          # lease: live, this device
& $fridolin setup-machine --core
```

Ask the owner to send the bridge a WhatsApp message and confirm the
answer. On the tailnet the core is the same device, `fridolin-core`, and
nodes reconnect to it by themselves (`fridolin nodes` shows them online).
On the old machine `fridolin core start` must now exit with status 3.

### 6. The way back

The same in reverse; the Mac already has the secrets, unless one changed
on the new machine (then `core send` from there and `core receive
--replace` on the Mac):

```powershell
# the machine that runs the core now
& $fridolin core stop
& $fridolin core run core export
```

```sh
# the Mac mini
fridolin core run core import --latest --force   # its old volume goes to core/replaced-<time>/
fridolin core run core promote                   # also clears the superseded mark
fridolin core start
```

`compose.host.yaml` is still on the Mac, so the brain folders are bound in
again as before; `--to-native` is not needed, since the Mac's core is the
container.

### Brain folders on another machine

A brain folder that lies in the volume moves with the export as part of
the data dir. A folder bound in from the Mac's home (`~/Personal`, a
migrated native install's) goes along too, under `folders/<brain id>/`
in the archive (format 2, which an older fridolin refuses to import), as
far as the brain's backup settings let it reach the cloud, since the
export goes through the vault bucket:

- what the brain's `backup.exclude` leaves out stays on the Mac
  (`/work/` stays home, say), and so do secret files unless
  `backup.include_secret_files` is on;
- a brain whose backup is off, does not go to the cloud
  (`backup.targets.cloud`), or whose `brain.json` cannot be read, keeps
  its whole folder home; export says so and the brain is left out there;
- programs built for the Mac stay too: `node_modules/`, `__pycache__/`
  and every virtualenv (a folder with a `pyvenv.cfg`), and links.

Before a move, `fridolin core run core export --list-folders` lists every
file of the brain folders that would go along (`<brain id>/<path>`, one a
line) and what stays home, and uploads nothing: check it for what must
not leave the machine, and widen the brain's `backup.exclude` where it
does not catch something.

The backup settings that decide are those of the manifest you approved
in the dashboard, which `config.json` holds, not `brain.json` on disk: an
edit to `brain.json` counts once you approve it. `--list-folders`,
`export`, `fridolin doctor` (check `brain-drift`) and, before the
transfer, `fridolin core send` print a `warning:` for each brain whose
`brain.json` or `.brain/schedules/` files on disk differ from the approved
manifest, saying whether the difference touches `backup.exclude`,
`backup.enabled` or `backup.targets.cloud`. When the `brain.json` on disk
would keep more home than the approved one (an exclude the approved one
lacks, the backup or its cloud target turned off), you probably mean that
to stay on this machine, so the export refuses and uploads nothing until
you either approve the change in the dashboard (Brains), which keeps it
home, or run it again with `--use-approved`, which takes the folder along
by the approved manifest. `fridolin core run core export --drift` gives
the warnings alone, and exits 1 when the export would refuse.

The import on the new machine puts each folder where the brain's path
names it when that folder is there; otherwise into the volume,
`/data/brains-folders/<name>`, and points the brain there, keeping its
home path in `brain_dirs` as `moved_from` ([CONFIGURATION.md](CONFIGURATION.md#brains)).
On the way back, the import on the Mac, where `compose.host.yaml` binds
`~/Personal` in again, brings each such folder home: files the brain
added are added, files it changed are written over after the Mac's
version moved to `core/replaced-<time>/folders/<brain id>/`, the same
ones stay as they are, and the brain points at `~/Personal` again, the
path as `config.json` had it (`~/Personal`, not the expanded one). Nothing
at home is deleted: a file the brain deleted on the other machine is
still there, and what never left (the excluded work folders) is
untouched. A folder below a link is never written through. The export
on the way back leaves out of the visiting folder what the brain's backup
settings leave out on the way there (excluded paths, secret files,
`node_modules/`, virtualenvs): what the brain made there of that kind
(its `claude-stream.log` under `*.log`, a `.env`) stays on the other
machine, as it never goes through the vault bucket.

A file changed at home while the folder was away is not lost. When the
import puts a folder into the volume it records each file's sha256 in
`/data/brains-folders/.moved-<name>.json`, beside the folder (it travels
with the data dir, is no vault file, and the brain does not see it). On
the way back a file whose home version no longer has that hash stays as
it is at home: when the brain's copy did not change it either, nothing
happens; when it did, the brain's version goes to
`core/replaced-<time>/folders/<brain id>/` instead and import reports a
`conflict:` line for it, to merge by hand. A move made before this record
existed brings the brain's version home as before.

While a folder visits the volume, the vault leaves it out: no cold copies
of it go to the vault bucket (the snapshots cover it). When the way back
brings it home, the import tells the vault to forget the volume's copy
(`core/vault-forget.json`), so that what a vault of an older fridolin
uploaded of it leaves the bucket and `vault.db` at the vault's next start
([VAULT.md](VAULT.md#forgetting-a-folder)).

Settings that name Mac paths (`sandbox.credentials_dir`,
`allow_paths`) are missing on the other machine, so Google Calendar
sign-ins do not work there, and a brain's own scripts that call
`launchctl`, `osascript` or Homebrew fail ([What does not move](#what-does-not-move)).

### Never

- print or paste a secret, the dashboard token or `scripts.token`, or
  ask the owner to type one into the chat;
- start the new core while the old one runs, `force` a promote, or remove
  `/data/core/superseded`;
- delete `bridge.db` or `docker volume rm fridolin-data`;
- confirm the four words for the owner: they compare what both screens
  show.

## Recovery

- **The old machine is gone.** On the new one: import the latest export,
  wait until `fridolin core status` shows the lease stale (20 minutes
  after the old core last renewed), promote, start. What happened after the
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
- **A core host that is gone** (lost, stolen, or revoked while
  offline, so it could not forget what moves brought it first): rotate what
  it held. New B2 application keys (the vault's and the snapshots'), a
  new Claude token (`claude setup-token`), new vault, restic and
  Litestream passwords; then `fridolin core secrets set NAME` for each on
  the core host. A new age identity and export key only protect the
  exports made from then on: the old machine can still decrypt the
  exports it could reach.
- **WhatsApp asks to pair again** after a move: the export was older than
  the session's last key update or `bridge.db` did not come along. Never
  delete `bridge.db` to fix it; import again or pair again.

## Housekeeping

The housekeeping job ([CONFIGURATION.md](CONFIGURATION.md#housekeeping))
runs in the core every 48 hours. In the container every process is the
core's, so besides the bridge's own leftovers (runs that are over, MCP
servers, `rclone rcd`, Litestream and restic without the bridge as their
parent) it lists processes over `housekeeping.cpu_percent` or `rss_mb`
that the bridge did not start, such as a forgotten `docker exec` shell,
and asks the owner in an approval poll before it stops one. It starts in
dry run: let it report for a week (`docker exec fridolin-core fridolin
housekeeping status`, or the Overview page), then set `housekeeping.dry_run` to
`false`.

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
- [ ] From another tailnet device: the dashboard address `fridolin doctor` names (`https://fridolin-core.<tailnet>.ts.net/` with HTTPS on in the tailnet, else `http://`)
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

Windows (core C), Docker Desktop on WSL2 ([Moving the core to another
machine](#moving-the-core-to-another-machine)):

- [ ] The same as on the Linux VM, with the secrets in the Windows
      Credential Manager: `fridolin core receive` there, `fridolin core
      send` on B; `fridolin core secrets` lists them.
- [ ] B stopped; C imports, promotes, starts and answers; B refuses to
      start afterwards.
