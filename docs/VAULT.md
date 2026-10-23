# The vault

The vault is the tiered store of [PLAN.md](PLAN.md), Workstream D. With
`vault.enabled` in `config.json` ([settings](CONFIGURATION.md#vault)) the
bridge keeps a complete, encrypted copy of its files in Backblaze B2 (the
*cold tier*) and only a working set on this machine (the *hot tier*),
takes restic snapshots of the hot tier, and replicates its two SQLite
databases with Litestream. Off, nothing of this runs and nothing changes.

## What is in it

The hot tier is the data dir, of which these parts are vault files:

| Path in the data dir | What |
| --- | --- |
| `config.json` | the settings, when the config file lives in the data dir |
| `secrets/` | age-encrypted secrets |
| `audit/` | the audit log, one JSONL file per month |
| `vault/documents/<brain>/` | documents filed for a brain |
| `vault/workspaces/` | archived FrIDE workspaces |
| `media/<brain>/` | each brain's media inbox, when `media_dir` lies in the data dir (the default) |
| brain folders in the data dir | in the container, `/data/brains-folders/<name>/`, less what their backup settings leave out ([below](#brain-folders-and-their-backup-settings)) |

Never vault files: `bridge.db` and `vault.db` with their `-wal`, `-shm`
and `-journal` files and Litestream's `.<db>-litestream/` folders (a file
sync could copy a live database halfway through a write: Litestream
replicates them instead, and the snapshots take `VACUUM INTO` copies),
the brains' own state under `brains/` (CLI sign-ins, TMPDIR, caches), run
tokens and run records, the dashboard and scripts tokens, the dashboard
sessions, the `rclone/`, `google/`, `plaud/`, `otp/`, `site/`, `tsnet/`,
`nodes/`, `core/` and `backup/` folders and lock files; inside the vault's
folders also `.git/`, `node_modules/`, `.venv/`, `__pycache__/`,
`.DS_Store` and the brains' `claude-stream.log`. The nightly snapshot
covers the rest of the data dir. A brain folder outside the data dir (a
native install's `~/brains/...`) is not in the vault; the snapshots cover
it.

Every file is uploaded within a minute or so of being written (the
service watches the hot tier, and walks all of it every six hours), so the
cold tier always holds everything. The cold manifest, which lists every
cold file with its hash for a rebuild, goes up at most every ten minutes
and when the vault stops. Files then leave the hot tier by the
policy of PLAN.md: pinned files (`config.json`, `secrets/`, every
`brain.json`, `brain-prompt.md`, `skills/`, `memories/**/*.md`,
`conversation-history.jsonl`, this month's `audit/`) never; a recording or
other binary in `media/` as soon as its transcript or summary exists
beside it (`<name>.transcript.md`, `<name>.md`, `<name>.txt`, …);
documents after `activity_days` unused; a binary without any markdown
companion only after `cold_after_days`; past months of `audit/` and
archived workspaces at once; and, while the hot tier is over its budget
less the headroom, whatever was used longest ago. A file is deleted from
disk only after its cold copy is checked: the hash recorded at upload is
the file's hash on disk, and the cold object has its size. Eviction runs
every 15 minutes.

A folder with cold files carries a `.cold.json` listing them with size,
date and hash, so `ls` still shows everything. Files of a message a brain
is running or has waiting (an *open thread*) stay hot, and when one went
cold it is brought back before the run starts.

## Brains and the vault

Every run is told in two lines of its prompt how cold files look, and gets
four tools from Fridolin's MCP server:

| Tool | Does |
| --- | --- |
| `vault_ls` | lists a vault folder, each file with its tier (`hot`, `both`, `cold`) |
| `vault_get` | brings a file back (when cold) and answers with a path the run can read |
| `vault_put` | stores a file (given as text, or copied from the run's own folder, inbox or temp folder) |
| `vault_search` | finds files by words in their path |

A brain's vault paths are `vault/documents/<brain>/`, its media inbox and,
when it lies in the data dir, its folder. The core brain also reaches the
paths of every brain its `brain.json` grants `files`
([BRAINS.md](BRAINS.md#the-core-brain-and-its-grants)); a delegated run
reaches its own only. `vault_get` of a file the run cannot read where it
is (a document in the data dir, another brain's inbox) answers with a copy
in the run's own temp folder, `brains/<brain>/tmp/vault/`. A file that
would take the hot tier over its budget is refused until the person agreed
and the run asks again with `allow_over_budget`.

## Snapshots

restic, to the repository `b2:<snapshots_bucket>:restic`:

- every night after 03:00 local time, the whole data dir (less what is
  rebuilt: run tokens, the brains' `tmp/` and `cache/`), every brain folder
  outside it (each as its backup settings say, below), and `VACUUM INTO` copies of `bridge.db` and `vault.db` in
  `core/snapshot-staging/`; never the live database files;
- every hour, the brain folders and `audit/`;
- after the nightly one, `restic forget --prune` keeps 24 hourly, 14 daily,
  8 weekly and 12 monthly snapshots (`--group-by host,tags`; every
  snapshot has the host `fridolin-core`, so retention spans a move);
- `restic check` every week, and `restic check --read-data-subset=5%`
  every month;
- every week, `restic copy` to a second repository on Google Drive,
  `rclone:<backup.remote>:fridolin-restic`, through the Drive connection of
  the [Backup page](BACKUP.md) (with your own Google client, inside the
  `Fridolin` folder: [BACKUP.md](BACKUP.md#use-your-own-google-client)). Once that copy has completed once, the old
  Drive sync's daily run stops (Back up now and restores still work there);
  until then it goes on, so turning the vault on never leaves a gap.

### Brain folders and their backup settings

Each brain folder goes to the cloud as its `brain.json`'s `backup` says,
in the snapshots (nightly and hourly, and with them the copy on Drive) as
in the Drive backup ([BACKUP.md](BACKUP.md#excluding-things)), and in the
cold tier for a folder in the data dir:

- `backup.enabled: false` leaves the folder out whole;
- `backup.exclude` leaves out what it names, with the same meaning as in
  the Drive backup: `/work/` is the `work` folder at the top of the brain
  only, `*.log` and `node_modules/` match at any depth in that brain;
- the secret files the Drive backup never uploads (`.env`, `*.pem`, …)
  stay out too, unless `backup.include_secret_files` is on.
- `backup.targets: {"cloud": false}` leaves the folder out of the cloud
  whole, and `backup.node_only` leaves out what it names, both going to the
  [node targets](#node-targets) the brain chooses instead.

The patterns become restic `--exclude` values under the brain's own
folder, so one brain's patterns never leave out another brain's files.
Where restic cannot say exactly what rclone does, they leave out more,
never less: `secret` (no trailing `/`) also leaves out a folder of that
name, and a pattern that cannot be translated (rclone's `{{regexp}}`, a
broken `[`) leaves out the whole folder. The settings of the `brain.json`
on disk and of the copy the bridge runs the brain with both count: if
either has the backup off or an exclude, it holds.

A brain folder whose `brain.json` cannot be read (broken JSON, a newer
version, gone) is left out whole: the vault does not upload what might be
excluded. The log says so once, the Vault status lists it under
`skipped_brains`, and `fridolin doctor` warns (`vault-brain-backup`)
until it is fixed.

Leaving out a folder that was in the snapshots before makes the next
snapshot lose those files, which may set off the mass-deletion guard
(below); allow the pruning once the change was meant. The snapshots taken
before keep the files until they are pruned or deleted by hand (`restic
forget <id> && restic prune`).

The mass-deletion guard of the Drive backup carries over: a snapshot that
lost more of the files of the one before than the Drive backup lets go (more
than half; more than a fifth once that is ten files or more; 250 or more)
holds pruning, so the snapshots that still have them are not thinned out.
The Vault page shows the hold; *Allow pruning…* lifts it.

Litestream replicates `bridge.db` and `vault.db` to
`<snapshots_bucket>/litestream/bridge.db` and `.../vault.db`, uploading
what changed each minute, as a child process the bridge restarts when it
ends. It compacts the uploads every hour and every day and takes a
snapshot daily, which keep its calls to B2 within the free caps (below). Its config file is generated in
`core/` with mode 0600 and holds no key: the snapshots key reaches it as
`LITESTREAM_ACCESS_KEY_ID` and `LITESTREAM_SECRET_ACCESS_KEY` in its
environment only. Litestream switches both databases to WAL mode.

## B2 transactions

Backblaze B2 bills its API calls by class, and a free account caps two of
them at 2,500 a day each, reset at midnight UTC: **Class B** (downloads,
HEAD) and **Class C** (logging in, every listing). **Class A** (uploads,
deletes) is free. Past a cap, B2 refuses every call of that class with
`403 transaction_cap_exceeded`, logging in too, until the reset. On
2026-10-21 that took the core down for seven minutes: it could not read
the lease (see [CORE.md](CORE.md#the-core-lease), which now keeps
running instead).

What the bridge spends, for an idle core with the default settings (the
vault and the lease on), by measurement: each operation counted against
a counting S3 proxy in front of a local `rclone serve s3`, plus the
B2-only log-in of each process:

| | before (per day) | now (per day) |
| --- | ---: | ---: |
| the lease: every 60 s three rclone processes (stat, cat, rcat), each logging in; now every 300 s on one `rclone rcd` | 5,760 C, 7,200 B | ~1 C, 577 B |
| the cold tier's reachability probe: every 10 minutes, an rclone process; now only after 30 quiet minutes | 144 C, 288 B | ~1 C, ≤ 49 B |
| Litestream at its defaults (the L0 retention check every 15 s and compaction every 30 s list the replica for both databases); now hourly and daily compaction, six-hourly retention check | ~34,500 C | ~210 C (+34 per start), ~50 B |
| restic: hourly backup (about 8 C, 3 B each), nightly backup and prune, weekly check | ~210 C, ~80 B | ~210 C, ~80 B |
| **total** | **~40,600 C, ~7,600 B** | **~460 C, ~760 B** |

Uploading a changed file costs nothing capped but the size check after
it (one Class B); bringing one back two Class B. The rclone daemon
(`rclone rcd`, one for the cold tier and one for the lease, each on a
unix socket in a new 0700 folder with a user and password made for each
start, in its environment only) logs in once a day instead of once per
call, which is most of the difference.

The bridge counts these per UTC day, from what each call costs (the
stores count their own calls; restic's and Litestream's are counted at
their measured cost per run and per hour), in `core/b2-usage.json`. The
Vault page's *B2 transactions*, `fridolin vault status` and `fridolin
doctor` (`b2-transactions`) show them against `vault.b2_daily_budget`
(default 2,000 of each class) and warn past 70%; a refusal for a cap is a
doctor failure with the fix: raise the caps under B2 → *Caps & Alerts*, or
lower the vault's activity. Once a B2 call succeeded after the refusal
(the stores note their last success in the counts; the lease's renewal,
the cold tier's probe, a sync and a snapshot that went well count too),
the caps were raised since: doctor then only warns about it until
midnight UTC.

## Encryption

Everything fridolin puts outside the machine is encrypted **twice, with
independent keys**, and the one asymmetric layer (the exports' age) uses a
**post-quantum** key exchange, ML-KEM-768 + X25519, so that a copy taken
today cannot be opened later by a quantum computer. The symmetric layers
are 256-bit ciphers, which a quantum computer does not break (Grover's
search leaves about 128 bits).

| Store | Where | Outer layer (key) | Inner layer (key) |
|---|---|---|---|
| Cold tier | vault bucket, `vault-sealed/` | rclone crypt, XSalsa20-Poly1305, names too (`fridolin.vault.crypt.password`, `.password2`) | seal, XChaCha20-Poly1305 per file (`fridolin.vault.seal.key`) |
| Snapshots (restic) | snapshots bucket, `restic-crypt/` | rclone crypt (`fridolin.snapshots.crypt.password`) | restic, AES-256-CTR + Poly1305 (`fridolin.restic.password`) |
| Weekly copy on Drive | Drive, `fridolin-restic-crypt/` | rclone crypt (`fridolin.snapshots.crypt.password`) | restic (`fridolin.restic.password`) |
| Node targets | the target's folder on the node | rclone crypt's format, written by the core, names in the clear (`fridolin.snapshots.crypt.password`) | restic (`fridolin.restic.node.<name>`) |
| Litestream (bridge.db, vault.db) | snapshots bucket, `litestream-sealed/` | rclone crypt (`fridolin.snapshots.crypt.password`) | seal (`fridolin.litestream.seal.key`) |
| Core exports | vault bucket, `core/exports/` | age, ML-KEM-768 + X25519 (`fridolin.age.identity.pq`) | seal (`fridolin.export.key`) |
| Secret transfer (`core send`) | the tailnet | WireGuard | age to a one-time ML-KEM-768 + X25519 key |

*seal* is fridolin's own symmetric layer (`internal/seal`): a stream in
64 KiB chunks, each sealed with XChaCha20-Poly1305 under a key derived per
file (HKDF-SHA256 with a random salt), the STREAM construction, so that a
file cut short, reordered or spliced fails to open; a key id in the header
tells a wrong key from damage. Litestream 0.5 does not encrypt at all, so
it now replicates into `core/litestream-replica/`, and the vault uploads
that folder each minute through both layers (one listing at start, deletes
in a batch every six hours: about 5 Class C a day instead of about 210).

When a database moves to a new, empty replica (as with this move from
the bucket to `core/litestream-replica/`) after Litestream has pruned its
first level-0 files, Litestream 0.5 can never upload that database's
changes again: it logs `LTX file is missing` every two minutes, stays up,
and copies only the daily snapshot. The bridge sees that line, stops
Litestream, runs `litestream reset` for that database and empties its
folder in `core/litestream-replica/`, then starts Litestream again, which
writes a fresh snapshot (at most once an hour per database; the database
itself is never touched). The reset alone is not enough: its transaction
ids start at 1 again, and a restore from a replica that still holds the
older snapshot returns that older copy without a word. A replica in a
bucket is not emptied from here, so there the bridge only reports it.
`fridolin doctor` (check `vault-litestream`) warns while Litestream has
logged a warning or error in the last 15 minutes, and counts the repairs.

The keys of the second layers are made by `fridolin core start` (and by a
native vault) when missing, and printed by name, never by value. New keys
always go with new folders (`vault-sealed/`, `restic-crypt/`,
`litestream-sealed/`), so a new key never hides data under a lost one.

`fridolin doctor` (check `vault-encryption`) and `fridolin vault status`
list each store with its layers; a store with one layer, or a
single-layer copy still waiting to be retired, is a warning.

### Moving existing data: migrate-encryption

A vault from before keeps working: what is not copied yet is read from the
single-layer folders, everything new goes to the double-encrypted ones.

```sh
fridolin vault migrate-encryption --dry-run        # counts what it would copy
fridolin vault migrate-encryption --run            # copies, then checks each copy
fridolin vault migrate-encryption --run --delete-old
```

`--run` copies the cold tier's files into the sealed folder (each checked
by size), the snapshots into the repositories behind rclone crypt
(`restic copy`, every snapshot checked by time, host and paths) and makes
sure the Litestream mirror is current. `--delete-old`, once a store's copy
is verified, deletes its single-layer folder (on B2 a delete hides the
files, and the bucket's lifecycle rules remove them) and records it as
retired in `core/encryption.json`; the vault then stops reading it.
A node target with a repository from before stays as it is, since its
folder cannot hold both: add the target again with a new, empty folder
(it gets both layers), take a snapshot, then remove the old target.
Exports: run `fridolin core export` in the core once; older exports stay
readable as long as you keep the X25519 identity (`fridolin.age.identity`).

### Recovery sheet

Keep these in your password manager; with them alone, a fresh machine
restores everything (`fridolin core secrets set NAME` stores each one):

| Secret | Opens |
|---|---|
| `fridolin.age.identity.pq` (AGE-SECRET-KEY-PQ-1…) | **new**: the exports' outer layer |
| `fridolin.export.key` | **new**: the exports' inner layer |
| `fridolin.vault.crypt.password`, `fridolin.vault.crypt.password2` | the cold tier's outer layer |
| `fridolin.vault.seal.key` | **new**: the cold tier's inner layer |
| `fridolin.restic.password` | the snapshots' inner layer |
| `fridolin.snapshots.crypt.password` | **new**: the outer layer of the snapshots, the Drive copy, the node targets and Litestream |
| `fridolin.litestream.seal.key` | **new**: Litestream's inner layer |
| `fridolin.restic.node.<name>` | a node target's inner layer |
| `fridolin.age.identity` (AGE-SECRET-KEY-1…) | only exports made before; keep it until you no longer need them |
| `fridolin.b2.vault.keyid`/`.key`, `fridolin.b2.snapshots.keyid`/`.key` | access to the buckets (not encryption) |

Read one back on macOS with `security find-generic-password -a fridolin
-s NAME -w`.

## Keys and passwords

The bridge reads them from its environment, as the core container gets
them from `fridolin core start` ([CORE.md](CORE.md#secrets)):
`B2_VAULT_KEY_ID`/`B2_VAULT_KEY`, `FRIDOLIN_VAULT_CRYPT_PASSWORD`/`..._PASSWORD2`,
`B2_SNAPSHOTS_KEY_ID`/`B2_SNAPSHOTS_KEY` and `RESTIC_PASSWORD`. A native
install that lacks one reads it from the host's secrets (the keychain,
account `fridolin`). The cold tier's two crypt passwords and restic's
password are made there when missing, 32 random bytes each, but only while
the cold tier, or the restic repository, is still empty: a vault that has
data was encrypted with passwords that are lost here, and new ones would
only hide that. The bridge then says so in its log, without the values:
**write them on the recovery sheet** (`security find-generic-password -a
fridolin -s fridolin.vault.crypt.password -w`, and `.password2`,
`fridolin.restic.password`). Without them nobody can read the cold tier or
restore a snapshot. Inside the container nothing is made: it has no
keychain.

## Commands and the dashboard

The dashboard's Vault page shows hot usage against the budget, the cold
size, the last upload, the snapshots and checks, the copy to Drive,
Litestream, the last restore drill and the eviction log, and starts an
upload, an eviction, a snapshot, a check or a restore drill.

```bash
fridolin vault status [--json]   # what the page shows; works with the bridge stopped too
fridolin vault sync              # upload now
fridolin vault evict             # evict now
fridolin vault get media/main/recording.m4a   # bring one back, print where
fridolin vault rebuild           # rebuild vault.db from the cold tier, bring back the pinned files
fridolin vault restore-drill     # restore the latest snapshot into an empty folder and check it
fridolin vault restore-drill --target nas   # the same for a node target
fridolin vault snapshot [--target nas] [--hourly]
fridolin vault check [--target nas]
```

The restore drill restores the latest nightly snapshot into a new temp
folder and checks that each brain folder in it has its `brain.json`, that
the database copies pass SQLite's `integrity_check`, and that Litestream
restores both databases; the result is on the Vault page and in `fridolin
vault status`. `fridolin doctor` checks, with the vault on, that the cold
tier answered within 45 minutes (the vault's own uploads and downloads
show it; it probes only after 30 quiet minutes), that no file has waited
more than an hour for its upload, that a snapshot is less than 26 hours
old, that Litestream runs, that no brain folder is left out for a
brain.json it cannot read, and that the day's B2 transactions stay within
the budget.

## Node targets

Besides the cloud, the snapshots can go to folders on your nodes: a
restic repository on an external disk of a Windows PC at home, on a NAS a
Linux node mounts, on a second Mac. What goes there is chosen per brain
and per path, so a work folder that must never reach the cloud can still
be backed up, to a disk in the house.

**Setting one up.**

1. On the machine with the disk, lend its drives to the core's backups:
   `fridolin node backup on` (or `--root D:\` / `--root
   /Volumes/Backup` to lend one drive or folder only; `fridolin node join
   … --backup` does it when joining), then restart the agent
   (`fridolin node stop`, `fridolin node start`). The node now declares
   the capability `backup` ([NODES.md](NODES.md#backups)).
2. On the core, approve what it offers (Nodes → *Approve what it offers*,
   or `fridolin nodes approve <id> --commands`). Lending more drives
   later needs this approval again.
3. Vault → Backup targets → *Add target…*: pick the node, browse its
   drives and folders (the node lists folder names only, never a file),
   pick a folder or name a new one, give the target a name and a
   schedule. The node must be online: the core approves the folder on the
   node (the node may then write restic's files there and nowhere else)
   and makes it.
4. Choose the brains that go there: *Brains…* on the target's card, or
   in `brain.json` ([BRAINS.md](BRAINS.md#backups-cloud-and-node-targets)):

   ```json
   "backup": {"enabled": true, "exclude": ["node_modules/", "*.log", "/work/"],
              "targets": {"node:nas": true}, "node_only": ["/work/"]}
   ```

   Here `/work/` never goes to the cloud (it is in `exclude`, and
   `node_only` keeps it out of the cloud whatever `exclude` says), and goes
   to `nas`; `*.log` goes nowhere. Without `targets` a brain goes to the
   cloud only, as before.

**How the data travels.** restic runs on the core, as for the cloud, and
encrypts there. Its repository is a REST backend on the core's loopback,
for the length of one run, with a user and password made for that run
(in restic's environment only), whose requests go over the node's
connection to the core as the node's `backup.*` calls: the node writes
restic's files, by restic's own names (`config`, `data/ab/ab12…`,
`keys/…`, …), into the folder, checking each upload's size and SHA-256
before it appears. The data never passes through the cloud; on the
tailnet WireGuard encrypts the connection, and what lands on the node is
restic's ciphertext. The node needs no restic, and never sees the
password. A folder already holding a repository (the picker marks it)
is opened with the password the core keeps for that target.

**The password.** Each target has its own, 32 random bytes made on the
core when its repository is made, never sent to the node. It is read from
`FRIDOLIN_RESTIC_NODE_<NAME>` (the name upper-cased, `-` as `_`), else the
host's secrets as `fridolin.restic.node.<name>` (the keychain, the Windows
Credential Manager, or 0600 files on Linux); inside the core container,
which has no keychain, from `secrets/restic-node-<name>.age` in the data
dir, encrypted to the core's age identity (`FRIDOLIN_AGE_IDENTITY`, which
is on the recovery sheet already, and which the vault and exports carry
the file with). The log says when it made one: **write it on the
recovery sheet** (`security find-generic-password -a fridolin -s
fridolin.restic.node.<name> -w` on macOS; in the container, `age -d -i
<identity file> secrets/restic-node-<name>.age`). A folder that holds a
repository without its password is refused rather than given a new one.

**Schedule and retention**, per target (`vault.targets`,
[CONFIGURATION.md](CONFIGURATION.md#vault)): a nightly snapshot at
`nightly_at` (default 03:00) and, with `hourly`, one each hour, of the
folders of the brains that choose the target (each less its `exclude`,
the secret files, and what is in `node_only` excepted); with
`include_data_dir` the nightly snapshot takes the data dir too, less the
live databases (their `VACUUM INTO` copies go instead) and less the
brain folders in it that do not choose the target. After each nightly
snapshot `restic forget --prune` keeps `retention` (default as the
cloud's: 24 hourly, 14 daily, 8 weekly, 12 monthly), with the same
mass-deletion hold; `restic check` every `check_days` (7) and with 5% of
the data read every `read_data_days` (30). Every snapshot has the host
`fridolin-core` and the tags `fridolin` and its kind. They run whether or
not the vault itself is on, while the node is online; a snapshot that
failed (the node went away, the disk was unplugged) is tried again after
an hour, and every minute the core looks whether one is due.

**Status.** `fridolin vault status` lists each target: its node and
folder, the brains that go there, the last nightly and hourly snapshot,
the repository's size and the space left on its disk, the last check and
restore drill, a hold. The Vault page shows a card per target with
*Back up now*, *Check*, *Restore drill*, *Brains…* and *Remove…*.
`fridolin doctor` (`vault-targets`) warns about a target without a
snapshot within `window_hours` (default 26), with the last error.
`core/vault-targets.json` keeps it all.

**Restore drill.** `fridolin vault restore-drill --target nas` (or the
card's button) restores the target's latest snapshot through the node
into an empty folder on the core and checks that every brain folder in
it has its `brain.json`, and, for a snapshot with the data dir, that the
database copies pass SQLite's `integrity_check`. `fridolin vault
snapshot --target nas` and `fridolin vault check --target nas` take a
snapshot and check now.

### Restoring from a node target

The folder is a restic repository inside rclone crypt's format (a target
made before #158: a plain restic repository, opened with `restic -r
<folder>` alone). Any machine with restic, rclone, the target's password
and the snapshots' crypt password restores from it, without fridolin and
without the core:

```sh
export RESTIC_PASSWORD='…'               # fridolin.restic.node.<name>
export RCLONE_CONFIG_NODECRYPT_TYPE=crypt
export RCLONE_CONFIG_NODECRYPT_REMOTE=/Volumes/Backup/fridolin
export RCLONE_CONFIG_NODECRYPT_FILENAME_ENCRYPTION=off
export RCLONE_CONFIG_NODECRYPT_DIRECTORY_NAME_ENCRYPTION=false
export RCLONE_CONFIG_NODECRYPT_SUFFIX=none
export RCLONE_CONFIG_NODECRYPT_PASSWORD="$(rclone obscure '…')"   # fridolin.snapshots.crypt.password
restic -r rclone:nodecrypt: snapshots
restic -r rclone:nodecrypt: restore latest --target ~/restore
```

On Windows, `restic -r D:\Backups\fridolin restore latest --target
C:\restore` (restic for Windows: `winget install restic.restic`); the
paths of the core's files come back below the target folder as they
were on the core. To restore on another machine, plug the disk in there,
or copy the folder, or point restic at it over a share. Removing a
target leaves the repository on the node.

## A new core

On a fresh data dir with the vault on, `fridolin vault rebuild` lists the
cold tier, rebuilds `vault.db` and brings back the pinned files; the rest
comes back when asked for. That is how a core on another machine gets its
hot tier without copying it.

## Trying it

`FRIDOLIN_VAULT_LOCAL=<folder>` makes a dev-mode bridge use folders there
for the cold tier, both restic repositories and Litestream, instead of
Backblaze: never for a core with real data. The gate's 2 GB case (a 2 GB
recording goes cold once its transcript exists and comes back with
`vault_get`) is a manual step: `TestVaultGate` does it with 64 MB.
`TestLiveB2` runs one sync, eviction and hydration and one restic backup
and restore against the real buckets, under test prefixes it deletes
again; it runs only by hand (its comment says how).
