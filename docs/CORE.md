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
against), ffmpeg, restic, rclone, Litestream, bubblewrap, git, curl,
ca-certificates and tzdata. restic, rclone and Litestream come from their
releases, pinned and checked against the SHA-256 each release publishes;
the base images are pinned to version tags. tini is PID 1, so `docker stop`
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
as before, and from nowhere else until the tailnet listener (`core.tailnet`)
comes. Keep the host side of the mapping at the same port as
`http_listen`: a request to another port is refused as misdirected.

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
the container's environment to anyone who may use Docker on the host, and
inside the container brains run as the same user as the bridge for now, so
a brain that goes looking can read the bridge's environment
(`/proc/<pid>/environ`), as on a native Linux install. Per-brain users in
the container are a later M1 step. `child_env_passthrough` still decides
what brains are handed; `CLAUDE_CODE_OAUTH_TOKEN` is among the variables
they always get.

## Moving the core

The cold tier in the `fridolin-vault` bucket carries the hot tier from one
machine to the next. Inside the core:

```bash
docker exec fridolin-core fridolin core export
```

writes `core/exports/<UTC time>.tar.gz.age`, encrypted to the age identity,
and points `core/exports/latest` at it. It holds the data dir: `config.json`,
`bridge.db` and `vault.db` (each copied with SQLite's `VACUUM INTO`, so a
running bridge is exported consistently), `secrets/`, `audit/`, `brains/`
(the per-brain state), `nodes/`, `tsnet/`, the dashboard's token and
sessions, `google/`, `plaud/`, `otp/`, `rclone/`, and brain folders that lie
in the data dir. It leaves out what the next machine rebuilds or the cold
tier already holds completely: `runs/`, `run-tokens/`, `media/`, `vault/`,
`core/`, lock files, SQLite's `-wal`, `-shm` and `-journal` files, the
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
`fridolin-vault`).

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
