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
| brain folders in the data dir | in the container, `/data/brains-folders/<name>/` |

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
cold tier always holds everything. Files then leave the hot tier by the
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
  outside it, and `VACUUM INTO` copies of `bridge.db` and `vault.db` in
  `core/snapshot-staging/`; never the live database files;
- every hour, the brain folders and `audit/`;
- after the nightly one, `restic forget --prune` keeps 24 hourly, 14 daily,
  8 weekly and 12 monthly snapshots (`--group-by host,tags`; every
  snapshot has the host `fridolin-core`, so retention spans a move);
- `restic check` every week, and `restic check --read-data-subset=5%`
  every month;
- every week, `restic copy` to a second repository on Google Drive,
  `rclone:<backup.remote>:fridolin-restic`, through the Drive connection of
  the [Backup page](BACKUP.md). Once that copy has completed once, the old
  Drive sync's daily run stops (Back up now and restores still work there);
  until then it goes on, so turning the vault on never leaves a gap.

The mass-deletion guard of the Drive backup carries over: a snapshot that
lost more of the files of the one before than the Drive backup lets go (more
than half; more than a fifth once that is ten files or more; 250 or more)
holds pruning, so the snapshots that still have them are not thinned out.
The Vault page shows the hold; *Allow pruning…* lifts it.

Litestream replicates `bridge.db` and `vault.db` continuously to
`<snapshots_bucket>/litestream/bridge.db` and `.../vault.db`, as a child
process the bridge restarts when it ends. Its config file is generated in
`core/` with mode 0600 and holds no key: the snapshots key reaches it as
`LITESTREAM_ACCESS_KEY_ID` and `LITESTREAM_SECRET_ACCESS_KEY` in its
environment only. Litestream switches both databases to WAL mode.

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
```

The restore drill restores the latest nightly snapshot into a new temp
folder and checks that each brain folder in it has its `brain.json`, that
the database copies pass SQLite's `integrity_check`, and that Litestream
restores both databases; the result is on the Vault page and in `fridolin
vault status`. `fridolin doctor` checks, with the vault on, that the cold
tier answers, that no file has waited more than an hour for its upload,
that a snapshot is less than 26 hours old and that Litestream runs.

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
