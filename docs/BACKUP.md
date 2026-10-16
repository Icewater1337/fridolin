# Google Drive backups

Brains are ordinary folders on your machine. Backups copy them to your Google
Drive so a lost or broken machine does not take your assistant's memory with
it — without putting any of it in a git repository.

## Connecting

1. Install [rclone](https://rclone.org/install/) (`brew install rclone`).
2. Dashboard → **Backup → Connect Google Drive**. A browser tab opens with
   Google's consent screen; approve it. (If no tab opens, the dashboard shows
   the link.)

The bridge drives rclone with its **own** config file,
`~/.fridolin/rclone/rclone.conf`. Your personal rclone setup and
`RCLONE_*` environment variables are never used or changed.

The link uses Google's `drive.file` scope: rclone can only see files it
created itself. Your other Drive files stay invisible to it.

## What is backed up

```
fridolin-backup/        (backup.root)
├── <brain-id>/
│   ├── current/                      the brain folder as of the last backup
│   └── versions/<timestamp>/         files that run changed or deleted
└── _bridge/current/
    ├── config.json                   the bridge's settings
    ├── brains/<id>.brain.json        the approved brain manifests
    └── README.txt
```

Each run is an incremental `rclone sync`: only changed files upload. When a
file changes or disappears, the previous copy moves to `versions/<timestamp>/`
instead of being lost. The timestamp is the local time of that run
(`YYYYMMDD-HHMMSS`), and the whole folder is deleted `keep_versions_days`
(default 30, `0` keeps them forever) after that run, however old the files in
it are. A version folder holds only what changed in that run, not a full
snapshot. Folders in `versions/` whose name is not such a timestamp are never
deleted.

**Never uploaded:** the WhatsApp session (`bridge.db`), `.env` files, the
dashboard token, the rclone config, downloaded media in the data folder.

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

## Schedule

Turn on the daily backup and pick a time (default 03:30). If the machine was
asleep, the backup runs as soon as it wakes. *Back up now* runs one
immediately; one brain or all.

## Mass deletions

Before each brain's sync the bridge compares the brain folder with
`current/`. If the folder is empty or missing, or more than half of the
backed-up files are gone from it, it does **not** mirror that: the brain's
result says "refusing to mirror a mass deletion", `current/` in Drive stays as
it was, and the error shows on the Backup page and the overview. Otherwise a
wiped folder (a brain told to delete everything, an unmounted disk, a botched
move) would reach Drive in one run, and its files would leave `versions/`
after `keep_versions_days` without anyone noticing.

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
disappear while the backup runs, the sync gets rclone's `--max-delete` at half
of the backed-up files. rclone deletes up to that limit and then stops, so in
this rare case Drive is not left untouched: up to that many files have already
moved from `current/` to `versions/<timestamp>/`, and the error names that
folder. They expire with that folder after `keep_versions_days`, so restore
them from there if the deletion was a mistake. The bridge settings (`_bridge`)
are not checked; the bridge writes them itself.

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
folder. The bridge never restores over an existing brain. Inspect the result,
then add it as a brain (the dashboard picks up its `brain.json`) or swap
folders yourself, which the bridge picks up when it restarts or you next
save a setting (see [Moving a brain](BRAINS.md#moving-a-brain)).

On a new machine: install the bridge, connect Drive, restore each brain into a
new folder, add them, and pair WhatsApp again. `_bridge/current/config.json`
has your old settings for reference.

## Other storage

Any rclone remote works. To use one, add a section named like `backup.remote`
(default `brain-gdrive`) to the bridge's rclone config, e.g.

```
rclone --config ~/.fridolin/rclone/rclone.conf config
```
