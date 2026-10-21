# Google Drive backups

With the [vault](VAULT.md) on, the snapshots it takes with restic are
copied to a restic repository on this same Drive connection every week,
`<backup.remote>:fridolin-restic` (inside the `Fridolin` folder, see
[Use your own Google client](#use-your-own-google-client)). Once that copy has completed once, the
daily run of the sync described here stops (its overview says
`superseded`); *Back up now* and restores keep working, and the copy in
`fridolin-backup/` stays where it is. Until then, and with the vault off,
everything below runs as before.

Brains are ordinary folders on your machine. Backups copy them to your Google
Drive so a lost or broken machine does not take your assistant's memory with
it — without putting any of it in a git repository.

## Connecting

1. Install [rclone](https://rclone.org/install/) (`brew install rclone`).
2. Have your own Google OAuth client with the Google Drive API enabled
   ([below](#use-your-own-google-client)): the one under **Settings →
   Google sign-in** that brains use for their calendars, or one pasted on
   the Backup page for Drive alone.
3. Dashboard → **Backup → Connect Google Drive**. The browser goes to
   Google's consent screen; approve it, and Google sends you back to the
   Backup page, which sets up the Drive folder and says when it is done.

The bridge signs in to Google itself, the way a brain's calendar does,
and comes back through its own address (`http://127.0.0.1:8765/`), so
connecting works the same in the core's container, where nothing could
open a browser. It writes the token into the Drive remote of its **own**
rclone config file, `~/.fridolin/rclone/rclone.conf` (mode 0600), with
your client's `client_id` and `client_secret`, so that rclone refreshes
the token with your client. Your personal rclone setup and `RCLONE_*`
environment variables are never used or changed, and rclone gets only a
few variables of the bridge's environment, such as `HOME`, `PATH` and the
proxy settings ([details](CONFIGURATION.md#what-reaches-brains)).

The link uses Google's `drive.file` scope: it can only see files your
client created. Your other Drive files stay invisible to it.

A new link is tried out as its own remote (`<backup.remote>-connecting`)
first; it takes the place of `backup.remote` only once it has made its
folder in Drive, and the trial remote is removed whatever happens.

To link another Google account, **Disconnect** first, then connect again:
connecting replaces the link, so the bridge refuses it while Drive is
connected (an abandoned sign-in would otherwise leave backups without
one), and both are refused while a backup or restore runs. Disconnect
stops a sign-in that is still waiting for the browser before it removes
the link, refuses backups, restores and a new sign-in until it is done, and
says so when the link could not be removed. It removes only the
bridge's own link: to take away rclone's access entirely, remove it at
[myaccount.google.com/permissions](https://myaccount.google.com/permissions),
as the Disconnect dialog says (your app's name there is the one on your
client's consent screen). While daily backups are on but Drive is
not connected, the overview says so and the log repeats it once a day.

## Use your own Google client

rclone's shared Google Drive client, which older versions signed in with,
is retired by Google during 2026; a link through it stops working then,
and with it the Drive backup and the vault's weekly copy. So the Drive
link signs in with your own OAuth client, from a Google Cloud project of
yours, like the calendars do.

**Decision: `drive.file`, in a folder of its own.** The link keeps
Google's narrowest Drive scope, `drive.file`, rather than `drive`, which
would let it read and delete everything in your Drive. `drive.file` lets
a client see only the files that client created, so a new client cannot
see, let alone delete, the folders rclone's shared client made (Drive
answers `appNotAuthorizedToChild`). The bridge therefore gives the link a
fresh folder, **`Fridolin`** at the top of your Drive, which your client
creates and the remote points at (rclone's `root_folder_id`). `backup.root`
(`fridolin-backup/`) and the vault's `fridolin-restic/` are made inside
it. A `Fridolin` folder the client can already see is one it made, and is
used again when you connect once more with the same client. The folders
the old link made stay at the top of your Drive, untouched, until you
delete them there; the new link starts afresh, so the first backup after
the switch uploads everything once, and the vault's weekly copy starts a
new repository at its next run.

### In Google Cloud Console

If you already set up a client for Google Calendar (docs/CALENDAR.md),
use the same project and client; else create both as that page says.
Then:

1. **Enable the Google Drive API**: APIs & Services → Library → *Google
   Drive API* → **Enable**
   ([direct link](https://console.cloud.google.com/apis/library/drive.googleapis.com)).
2. **OAuth consent screen** (Google Auth Platform → *Data access*, older
   consoles: APIs & Services → OAuth consent screen → *Scopes*): **Add or
   remove scopes**, tick `.../auth/drive.file` ("See, edit, create, and
   delete only the specific Google Drive files you use with this app"),
   **Update**, **Save**. It is a non-sensitive scope: no verification.
   Under *Audience* (or *Test users*), the Google account whose Drive
   holds the backups must be a test user while the app is in *Testing* —
   and Google expires a Testing app's sign-ins after 7 days, so publish
   it (*Publish app* → In production) to keep backups running.
3. **The client**: Credentials → your OAuth client.
   - A **Desktop app** client needs nothing more: Google lets it return
     to `http://127.0.0.1:<port>/`.
   - A **Web application** client must list the bridge's address under
     **Authorized redirect URIs**: add `http://127.0.0.1:8765/` (the
     address the dashboard runs on, with `/` at the end; the Backup page
     shows it) and **Save**. The calendar sign-in needs the same entry,
     so a Web client that already signs brains in has it.
4. If the client is not under Settings → Google sign-in yet, paste its
   JSON there, or, for Drive alone, on the Backup page (*Use a separate
   client for Drive…*). That copy is kept in
   `~/.fridolin/google/drive_oauth_client.json` (0600), which brains
   cannot read; Settings' client is in `google/oauth_client.json`.

### Switching an existing link

compat: a link made by an older version keeps working as it is until
Google retires the shared client. The Backup page says the link uses
rclone's shared client, and so does `fridolin doctor` (`drive-client`,
a warning while any Drive remote in the bridge's rclone config has no
`client_id`). Click **Switch to your own Google client** and approve at
Google. The old link stays in place, and backs up as before, until the
new one has made its folder; a switch that is abandoned or fails leaves
it as it was. Backups and restores cannot start in the few seconds the
link is replaced; a backup that started meanwhile makes the switch fail
with "connect again". Then:

- the next backup fills `Fridolin/fridolin-backup/`, and the vault's next
  weekly copy fills `Fridolin/fridolin-restic/`;
- **Restore** lists only what the new link made. Older versions are in
  the old `fridolin-backup/` folder at the top of your Drive: download
  one from drive.google.com to restore it by hand;
- once the new folder holds what you need, delete the old
  `fridolin-backup/` and `fridolin-restic/` in Drive, and remove "rclone"
  at [myaccount.google.com/permissions](https://myaccount.google.com/permissions).

## What is backed up

```
Fridolin/                             the link's folder (root_folder_id)
├── fridolin-backup/                  backup.root
│   ├── <brain-id>/
│   │   ├── current/                  the brain folder as of the last backup
│   │   └── versions/<timestamp>/     files that run changed or deleted
│   └── _bridge/current/
│       ├── config.json               the bridge's settings
│       ├── brains/<id>.brain.json    the approved brain manifests
│       └── README.txt
└── fridolin-restic/                  the vault's weekly copy (VAULT.md)
```

A link made with rclone's shared client by an older version has no
`Fridolin` folder: there `fridolin-backup/` is at the top of the Drive.

Each run is an incremental `rclone sync`: only changed files upload. When a
file changes or disappears, the previous copy moves to `versions/<timestamp>/`
instead of being lost. The timestamp is the time of that run in UTC
(`YYYYMMDD-HHMMSSZ`), so that the hour the clocks go back cannot reorder
runs or give two the same name; folders from before are in local time,
without the `Z`, and are read as such. The whole folder is deleted
`keep_versions_days` (default 30, `0` keeps them forever) after that run,
however old the files in it are. A version folder holds only what changed
in that run, not a full snapshot. Folders in `versions/` whose name is not
such a timestamp are never deleted.

A backup is a copy of a live folder, not an atomic snapshot. While a brain
runs, it changes its folder (its history, its memories, a git repository
in it), so a brain's backup waits for the brain to finish what it is
running, up to two minutes, and then copies the folder anyway; the log says
when it did. A file that changes while rclone copies it fails that try, and
rclone tries the sync again up to three times, ten seconds apart, which the
log notes too.

**Never uploaded:** the WhatsApp session (`bridge.db`), the dashboard token,
the rclone config, downloaded media and every brain's own state in the data
folder. Nor, from brain folders, whatever their excludes say: `.env`,
`.env.*`, `*.pem`, `*.p12`, `id_rsa*`, `id_ecdsa*`, `id_ed25519*`,
`google_calendar_*.json` and `.credentials.json` files, in any folder of
the brain. Set `backup.include_secret_files` to `true` in `config.json` to
upload them too (the dashboard has no field for it). Files a version from
before this rule uploaded stay in Drive's `current/` and `versions/` until
you delete them there.

## Excluding things

Each brain has exclude patterns (Backup page, or `backup.exclude` in its
`brain.json`), in rclone's filter syntax, relative to the brain folder:

| Pattern | Excludes |
| --- | --- |
| `node_modules/` | every `node_modules` folder |
| `*.log` | every `.log` file |
| `/work/` | the `work` folder at the top of the brain only |
| `/exports/**.zip` | zip files anywhere under `exports/` |

New brains exclude `node_modules/`, `*.log`, `.DS_Store`, `.venv/` and
`__pycache__/`. Exclude anything large you can rebuild, and anything that
should not leave the machine (for example source code that belongs to an
employer).

The same patterns, the secret files above and a brain's `backup.enabled`
also hold for the vault's restic snapshots and their copy on Drive
([VAULT.md](VAULT.md#brain-folders-and-their-backup-settings)).

## Schedule

Turn on the daily backup and pick a time (default 03:30). If the machine was
asleep, the backup runs as soon as it wakes. Switching the schedule on, or
moving it to a time that has already passed today, does not start a backup
at once: the first run is tomorrow's (a time still to come today runs
today, unless today's scheduled backup already ran). The next run shown on
the Backup page and the overview is the one that will happen; there is none
while Google Drive is not connected.
*Back up now* runs one immediately; one brain or all.

## Mass deletions

Before each brain's sync the bridge compares the brain folder with
`current/`. If the folder is empty or missing, or too many of the backed-up
files are gone from it (more than half; more than a fifth, once that is ten
files or more; or 250 files or more), or more than half of those in one of
its top-level folders that had at least five (a wiped `memories/`, say), it
does **not** mirror that: the brain's result says "refusing to mirror a mass
deletion" and what is gone, `current/` in Drive stays as it was, and the
error shows on the Backup page and the overview. Otherwise a
wiped folder (a brain told to delete everything, an unmounted disk, a botched
move) would reach Drive in one run, and its files would leave `versions/`
after `keep_versions_days` without anyone noticing.
Ordinary housekeeping can be refused too: renaming a top-level folder, or
a `git gc` in a brain folder that is a git repository, which packs its
loose objects (`.git/` is backed up unless you exclude it). Mirror that
with **Mirror the deletion…** (below), or exclude `.git/`.

When a **scheduled** run refuses, the brain's chat also gets one WhatsApp
message about it (your DM for the DM brain, or the brain's `reply_chat`, as
for Plaud). Later refusals of that brain stay quiet until one of its backups
succeeds again; if the message could not be sent within 30 seconds, the next
scheduled refusal tries again. A refusal from *Back up now* sends nothing,
since you are looking at the dashboard.

Look at the folder first. If the deletion was intended, click **Mirror the
deletion…** next to that brain on the Backup page and confirm. This runs a
forced backup of that brain (`POST /api/backup/run` with
`{"brains": ["<id>"], "force": true}`; `force` without a `brains` list is
rejected, so one request cannot mirror every brain's deletions). The files it
removes from `current/` move to `versions/<timestamp>/` like any other
deletion. Scheduled runs never force; they keep refusing until you do.

Only files that are gone count: changed files do not, and neither do excluded
ones (a sync never deletes those from Drive). As a backstop for files that
disappear while the backup runs, the sync gets rclone's `--max-delete` at the
most files those counts let go. rclone deletes up to that limit and then
stops, so in this rare case Drive is not left untouched: up to that many
files have already moved from `current/` to `versions/<timestamp>/`, and the
error names that folder. They expire with that folder after
`keep_versions_days`, so restore them from there if the deletion was a
mistake. The bridge settings (`_bridge`) are not checked; the bridge writes
them itself.

A brain folder that rclone can only partly read (a subfolder it may not open,
a folder removed while it was being listed) is never taken for a wiped one.
It is still synced, but with no deletion allowed: the new and changed files
that can be read upload, everything already in `current/` stays, and the run
fails with the read error so you can fix the folder. (If the sync itself reads
the whole folder and finds files to delete, it stops at the first one and the
run is refused as above, with nothing removed.) If the backup in Drive cannot
be listed, the run fails without syncing.

## Restoring

Backup → Restore: choose a brain, `current` or a version, and a **new, empty**
folder. The bridge never restores over an existing brain. The folder must
pass the checks a new brain's folder does (see [adding a brain](BRAINS.md)):
inside your home folder, not in or around a brain, and not in the bridge's
data folder or the credentials folder. Nor may it lie inside what the
sandbox keeps from brains, such as `~/.claude`, `~/.ssh`, `~/Library/Mail`
or a `deny_paths` entry, since the bridge writes the copy outside any
sandbox. The folders of your own files (`~/Documents`, `~/Desktop`,
`~/Downloads`, `~/Movies`, `~/Music`, `~/Pictures`) are fine, as they are
for a new brain. Each check
looks where the folder really is, through any symlink on the way. The
restore runs in the background (one at a time, and not while a
backup runs), and the Backup page shows how the last one went, a failure
included, with what rclone said. Inspect the result, then add it as a brain
(the dashboard picks up its `brain.json`) or swap folders yourself, which
the bridge picks up when it restarts or you next save a setting (see
[Moving a brain](BRAINS.md#moving-a-brain)). Until then, every brain can
read the copy: the sandbox keeps brains out of each other's folders, and a
restored copy is not a brain's folder yet.

On a new machine: install the bridge, connect Drive, restore each brain into a
new folder, add them, and pair WhatsApp again. `_bridge/current/config.json`
has your old settings for reference.

## Other storage

Any rclone remote works. A remote that is not of type `drive` never
needs the Google client. To use one, add a section named like `backup.remote`
(default `brain-gdrive`) to the bridge's rclone config, e.g.

```
rclone --config ~/.fridolin/rclone/rclone.conf config
```
