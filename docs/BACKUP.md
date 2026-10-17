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
instead of being lost; versions older than `keep_versions_days` (default 30)
are deleted. A version folder holds only what changed in that run, not a full
snapshot.

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
