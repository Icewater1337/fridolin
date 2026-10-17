# Configuration

The dashboard edits all of this for you (`./fridolin open`).
This page is the reference behind it.

The configuration is `~/.fridolin/config.json`, or `config.json`
in the current directory if one exists, or wherever `--config` / `CONFIG_FILE`
points. On first start a safe starter config is written there. Changes made
in the dashboard apply immediately; a few runtime settings (marked *restart*)
need a restart, which the dashboard offers.

Validate at any time:

```bash
./fridolin check
```

Paths may start with `~`, which expands to your home directory.

---

## Access control

### `allowed_numbers`

Phone numbers permitted to DM the bridge. Country code, no `+`, no spaces.

```json
"allowed_numbers": ["15551234567", "15559876543"]
```

What an empty list means depends on `allow_any_dm`. New configs set it to
`false`: nobody may DM until you add a number. Older configs without the
setting keep the original behaviour — an empty list allows **anyone** who can
message the account — and the dashboard warns about it.

Newer WhatsApp clients identify senders by an internal ID rather than a phone
number; the bridge resolves that back to the number before checking, so plain
numbers here are correct.

A message someone sends to a broadcast list you are on counts as a DM from
them and is answered in your direct chat with them, which is where WhatsApp
shows it. WhatsApp Status updates and channel posts are ignored, whoever
posts them.

### `allowed_groups`

Group JIDs the bridge will respond in. Groups are deny-by-default — an empty
list means no groups at all.

```json
"allowed_groups": ["120363000000000000@g.us"]
```

To find a JID: add the bridge to the group and send a message. The group shows
up in the dashboard under **Chats → waiting for access**, with an *Allow*
button that also assigns it to a brain.

### `triggers`

Keywords that address the bridge in a group. Matched case-insensitively and
stripped before the text reaches Claude. A plain string matches anywhere in the
message; an object can restrict it:

```json
"triggers": ["@claude", {"text": "@c", "match": "start"}, {"text": "bot", "match": "word"}]
```

| `match` | Fires when |
| --- | --- |
| `anywhere` (default) | the keyword appears anywhere |
| `start` | the message starts with it — `@c hi` yes, `@carla` no, `hi @c` no |
| `word` | it appears as a whole word — `hey bot` yes, `robotics` no |

An @-mention of the bridge's own account always counts, whatever this list
says. So does sending a file, and so does forwarding a message.

Untagged group messages still run through the brain — so it keeps up with the
conversation — but produce no reply. Very short messages ("ok", "👍") are
skipped entirely.

DMs never need a trigger.

---

## Claude

### `claude_path`

Path to the Claude Code CLI. `"claude"` uses your PATH.

On macOS the desktop app keeps it at:

```
~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude
```

That path contains a version number and changes when Claude updates. Either
point `claude_path` at a stable `claude` on your PATH, or re-run `setup.sh`
after an update.

### `model` and `brain_model`

Model to use — `"sonnet"`, `"opus"`, `"haiku"`, or a full model ID. Empty uses
the CLI default. `model` applies to classic mode, `brain_model` to brains, and
an individual brain can override it with its own `model` field.

### `brain_effort`

Passed as `--effort`: `low`, `medium`, `high`, `xhigh` or `max`. Empty uses
the CLI default. Brains can override it.

### `disallowed_tools`

Tools brains may not use, passed as `--disallowed-tools`, e.g.
`["WebFetch", "WebSearch"]`. Brains can override the list.

### `dangerously_skip_permissions`

Passes `--dangerously-skip-permissions`, so Claude uses tools without asking.

Effectively required: nobody is at the keyboard to approve a prompt, so
without it every tool call hangs. Set to `false` only if you want a
conversation-only assistant with no tool access.

Read [Security](../README.md#security) before running with this on.

### `inherit_claude_user_config`

Set it in `config.json`; the dashboard has no field for it.

Off by default: a brain loads only its own folder's Claude Code setup, never
yours. Every brain runs with `--setting-sources project,local` and
`--strict-mcp-config`, plus `--mcp-config <brain folder>/.mcp.json` when the
folder has that file. So a brain gets the `CLAUDE.md`, `.claude/` settings,
skills, agents and commands and the `.mcp.json` servers of its own folder,
but not your user settings in `~/.claude/settings.json` (their hooks,
plugins, permissions, `env` and `apiKeyHelper`), the skills, agents and
commands in `~/.claude`, the MCP servers in `~/.claude.json`, or those of
plugins and of your claude.ai account. Given with `--mcp-config`, a
`.mcp.json` the CLI cannot use stops it from starting at all, where it used
to be ignored: one that is not a JSON object with an `mcpServers` object
(`{}` or a misspelt key included). The bridge therefore leaves out such a
file, and a symlink, anything but a regular file, or a file over 256 KiB,
and its log says why.

A server the folder's `.claude/settings.json` or `settings.local.json` turns
off with `disabledMcpjsonServers` (where Claude Code records a project
server you decline) stays off: `--mcp-config` would start it, so the bridge
passes the folder's other servers itself, as JSON on the command line. Any
process of your user, another brain's included, can read that with `ps`, as
it can the prompt, so a token in a server's `env` shows there too; to avoid
that, take a server you do not want out of `.mcp.json` instead, and the
bridge passes the file as it is. When it cannot read one of those two
files, it passes no server at all, and its log says why. Every other
server in `.mcp.json` starts, whatever `enabledMcpjsonServers` says, as it
always did for brains; to stop one, take it out of `.mcp.json` or list it
in `disabledMcpjsonServers`.

These flags do not skip the `CLAUDE.md` and `CLAUDE.local.md` files and the
`.claude/` folders (their `CLAUDE.md`, rules, skills, agents and commands)
in the folders above a brain's folder: Claude Code reads them as part of
the project, so a brain in your home folder still loads `~/CLAUDE.md`,
`~/.claude/CLAUDE.md` and `~/.claude/rules`.

`true` brings back the old behaviour: brains load all of your setup, like a
Claude Code session you start yourself in the brain's folder.

When you upgrade to a version with this setting, check what your brains
relied on in your own setup:

- Permission rules and hooks in `~/.claude/settings.json` no longer apply
  to brains, the `deny` rules and `PreToolUse` guards among them. Copy the
  ones a brain should keep into its folder's `.claude/settings.json`.
- MCP servers from `~/.claude.json`, from plugins and from your claude.ai
  account no longer load. Add the ones a brain needs to its `.mcp.json`.
- Then send each brain a test message, asking it which MCP tools it has and
  something only its `CLAUDE.md` answers.

> **Warning:** with this on, every brain runs your hooks, sees your personal
> instructions and can call your MCP servers' tools without asking (see
> `dangerously_skip_permissions`), on behalf of anyone who may message it,
> and every brain gets the same ones. Turn it on only if your brains rely on
> a user-level skill or MCP server. Better: copy what a brain needs into its
> own folder (`.claude/skills/`, `.mcp.json`, `CLAUDE.md`), and an `env`
> or `apiKeyHelper` it needs from `~/.claude/settings.json` into its
> folder's `.claude/settings.json`.

---

## Brains

Brains are configured in their own folders (`brain.json`) and added in the
dashboard; see [BRAINS.md](BRAINS.md). In `config.json` they appear as:

```json
"brain_mode": true,
"brain_dirs": [
  { "path": "~/assistant/personal", "approved": { "id": "personal", "name": "Personal", "routing": { "handles_dms": true } } }
]
```

`approved` is the manifest you accepted — the version that runs.

### Inline brains

Older configs list brains inline. They keep working, and
`fridolin migrate` moves them into `brain.json` files:

```json
"brains": [
  { "name": "personal", "work_dir": "~/assistant/personal", "handles_dms": true },
  { "name": "team", "work_dir": "~/assistant/team", "groups": ["120363000000000000@g.us"], "model": "opus" }
]
```

| Field | Meaning |
| ----- | ------- |
| `name` | Identifier (becomes the brain's id). No spaces or slashes. |
| `work_dir` | The brain's folder. Created if missing. |
| `handles_dms` | Routes all direct messages here. At most one brain. |
| `groups` | Group JIDs routed here. A group may belong to only one brain. |
| `default` | Fallback for allowed chats no brain claims. Defaults to the first brain. |
| `prompt_file` | Override the prompt path (default `<work_dir>/brain-prompt.md`). |
| `model`, `effort` | Override `brain_model` / `brain_effort` for this brain. |

A brain whose settings clash with another (same id, a group claimed twice, two
DM brains, a folder inside another brain's) is left out and reported on the
dashboard; the others keep running.

### Classic mode

With `brain_mode: false`, the bridge keeps one resumable Claude session per
chat, all running in `work_dir` (default: your home directory). No isolation,
no per-chat prompt, no chat-history preface. Simpler, and fine for a
single-user setup.

---

## Sandbox (macOS)

```json
"sandbox": {
  "enabled": true,
  "deny_paths": ["~/clients", "~/Projects/secret"],
  "allow_paths": []
}
```

When enabled, each Claude run is wrapped in `sandbox-exec` under a generated
profile. Changes (including added or removed brains) apply to the next run of
every brain. If the profile cannot be built or `/usr/bin/sandbox-exec` is
missing, the run is refused: the chat gets an error, the bridge log names the
cause, and Claude never starts unsandboxed. Classic mode (`brain_mode` off) is
not sandboxed. The profile is allow-by-default. Brains can neither read nor
write:

- every **other brain's** working directory
- `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`,
  `~/.config/rclone`
- `~/.git-credentials`, `~/.netrc`, shell history files
- `~/Documents`, `~/Downloads`, `~/Desktop`, `~/Movies`, `~/Music`, `~/Pictures`
- `~/Library/Mail`, `Messages`, `Calendars`, `Safari`, Chrome and Firefox
  profiles, `Containers`, `Group Containers`, Contacts, Notes
- the bridge's data directory — except `media/` and the brain's own calendar
  credentials — under both names it has had, `~/.fridolin` and
  `~/.whatsapp-claude-bridge`, whichever is in use (an old one left behind
  can still hold the WhatsApp keys and tokens)
- the database (`db_path`, with its `-wal`, `-shm` and `-journal` files)
  wherever it is, and its folder when that lies outside the data directory,
  unless the folder is your home folder or a temp folder
- the config file (with its `.bak` and the temp file each save writes next
  to it) and the `.env` files loaded with it, when `--config` puts them
  outside the data directory
- the bridge's logs in `~/Library/Logs/fridolin`
- `~/.config/fridolin`, which holds the personal-data denylist the commit
  hooks read

Other Claude Code sessions' records (`~/.claude/projects`,
`~/.claude/sessions`, yours included) are unreadable, but the CLI writes its
own run's files there, so writing stays allowed. Transcripts cannot be moved
or deleted; the small per-process files in `sessions/` can (the CLI deletes
its own on exit). Nor can a brain plant a symlink in either folder, such as
a new project's folder linked into its own, through which a later session's
transcript would reach it.

Brains can read but never write the files that are loaded or run outside
their sandbox, so a manipulated brain cannot plant code there:

- Claude Code's own configuration: `~/.claude/settings.json`,
  `settings.local.json`, `CLAUDE.md` and the `hooks`, `rules`, `agents`,
  `commands`, `skills`, `plugins` and `output-styles` folders. Your own
  Claude Code sessions load them, and so may every brain's next run (see
  `inherit_claude_user_config`). Brains therefore cannot change Claude Code
  settings or install plugins; do that yourself.
- the `CLAUDE.md`, `CLAUDE.local.md` and `.mcp.json` in every folder above a
  brain's folder, up to and including your home folder (or up to `/` for a
  brain outside it), and the `.claude` folder in each of them below your
  home folder. Claude Code loads them for every brain below them, and your
  own sessions load your home folder's. A brain's own folder is its own.
- Claude Code's auto-memory, `~/.claude/projects/<project>/memory`, which
  every new session of that project loads, yours included
- `~/Library/LaunchAgents`
- shell startup files: `~/.zshenv`, `.zprofile`, `.zshrc`, `.zlogin`,
  `.zlogout`, `.bash_profile`, `.bash_login`, `.bashrc`, `.profile`,
  `~/.config/fish`
- `~/.tmux.conf` and `~/.config/tmux`, read by the activity viewer's tmux
- `~/.gitconfig` and the files it includes (see `git_config` below)
- the bridge's own binary and, when it runs from one, its source checkout
  (launchd runs it outside any sandbox)
- the programs the bridge runs unsandboxed, when they live in your home
  folder: the Claude CLI (as found on `PATH` and the file it links to),
  rclone, the Plaud CLI with its whole npm package and the `node` that runs
  it, and tmux when the viewer is on. Brain runs therefore cannot update the
  Claude CLI or these tools, so the bridge turns the CLI's auto-updater off
  for them (`DISABLE_AUTOUPDATER=1`); your own sessions still update it.
- where the bridge would find such a program next, so that a brain cannot
  plant one: in every folder of the bridge's `PATH` (and the Plaud CLI's)
  that lies in your home folder, such as `~/.local/bin`, the names it runs
  by name (`claude`, `rclone`, `plaud`, `node`, `tmux`, `sh`, `tail`,
  `touch`, `osascript`, `open`), whether they exist there yet or not;
  `~/.local/bin/rclone`, where it looks when rclone is not on `PATH`; and,
  unless `plaud.cli_path` is set, all of `~/.nvm/versions/node`, because
  the bridge takes the Plaud CLI of whichever node version sorts last.
  Brains therefore cannot install node versions or global npm packages with
  nvm.

Renaming counts as writing. The sandbox matches paths, not files, so a brain
that renamed `~/.ssh`, or the folder that holds another brain, used to read
it under the new name. Nothing denied can be renamed or deleted, apart from
the per-process files in `~/.claude/sessions`, and neither can any folder
above it, up to your home folder (or up to `/` for a path outside it).
Brains can still create and change files inside those folders. Nor can a
brain create such a folder while it does not exist yet, for example a
missing `~/.config` or `PATH` folder: it could create it as a symlink into
its own folder, or move a folder of its own there, and so redirect what
lies below it. The exception is `~/.claude` and the folders in it, which
the Claude CLI creates on its first run: brains can create them too, though
not as a symlink, and from the next run on they are pinned too.

`~/Library/Keychains` is deliberately **not** denied: the Claude CLI reads its
credentials from the keychain and fails with "Not logged in" without it.

`deny_paths` adds your own (denied for reading and writing). `allow_paths`
re-opens reading something the defaults closed; writing there stays denied.
It cannot re-open another brain's folder, nor the bridge's own data
directory, database, config file or credentials folder.
Entries must be absolute or start with `~/`; the bridge refuses a config with
a relative one, which would protect nothing.

The sandbox compares real paths, so every deny covers both the path as
configured and the path it reaches through symlinks (a home on another
volume, a cloud-synced folder, `/var` → `/private/var`). Symlinks are
resolved again on every run, and a folder that does not exist yet is still
covered once it is created. Re-allowed paths (`allow_paths`, a brain's own
folder, media, calendar files) are resolved only where your home, data or
credentials folder itself is a symlink, never through any other symlink,
because a brain could plant one to reach something else. If an
`allow_paths` entry is a symlink or goes through one, list its real path.

A brain's own folder is always readable and writable, even inside a denied
directory. The brain cannot rename, delete or replace the folder itself,
though (with a symlink to another brain's folder, say), only what is inside
it, and the folder re-opens nothing denied inside it: another brain, the
data directory, a `db_path` folder, the credentials folder, the bridge's
checkout or a `deny_paths` entry inside a brain's folder stay as closed to
that brain as to every other, and so does the `CLAUDE.md` above a brain
further down. The sandbox refuses a brain whose folder is your home folder
or holds it (that fails every brain's runs, since each profile names every
brain's folder), and one whose folder is itself something the sandbox
protects: another brain's folder, the data, database or credentials folder,
the checkout, a default-denied folder such as `~/.ssh`. Its runs fail until
you move it. A `deny_paths` entry may be a brain's folder, which keeps that
folder from every other brain. Other tools a brain runs may still need
paths the defaults deny, so keeping brains somewhere like
`~/assistant/<name>` is simplest.

### `sandbox.git_config`

`"read"` (default): brains may read `~/.gitconfig` and the files it includes,
so they can commit in their own folder, but never write them. `"deny"` hides
git configuration entirely: those files and `~/.config/git`.
`~/.git-credentials` is denied either way.

### `sandbox.credentials_dir`

A directory of per-brain credential files kept outside the data directory
(absolute or starting with `~/`). Brains are denied all of it except their
own calendar pair (`google_calendar_credentials_<id>.json`,
`google_calendar_token_<id>.json`).

This is containment, not a jail — it raises the cost of a prompt injection
rather than eliminating it. Linux has no `sandbox-exec`: the bridge refuses a
config with `sandbox.enabled` on there (the Linux default is off). Run the
bridge as a dedicated user instead.

### What the sandbox does not cover yet

The profile allows everything it does not deny, and writing is denied only
for the paths listed above. A manipulated brain can therefore still:

- write anywhere else your user can: other projects in your home folder
  (their `.claude/settings.json`, `.mcp.json`, `CLAUDE.md`, `.git/hooks`),
  scripts your Claude Code settings run from outside `~/.claude/hooks`,
  files a protected `CLAUDE.md` imports with `@path`, what your protected
  shell startup files source or run (such as `~/.nvm/nvm.sh`,
  `~/.oh-my-zsh`, `~/.p10k.zsh`, or a `ZDOTDIR` like `~/.config/zsh`), the
  scripts and programs a `claude_path` wrapper script runs or sources
  (only the wrapper itself is protected, and the bridge runs it outside
  the sandbox for the dashboard's `claude --version`), a new program in a
  folder on your `PATH` that shadows another one (other than the names the
  bridge runs, in folders under your home folder), and programs outside
  your home folder such as `/opt/homebrew` (user-writable on Apple Silicon)
  and `/usr/local`, including a `plaud`, `rclone` or `node` there that the
  bridge would run next, since launchd's `PATH` lists them first
- leave files in its own folder that run or load when you work there
  yourself, outside the sandbox: git hooks and settings in `.git/`
  (`hooks/`, `core.fsmonitor`, `core.hooksPath`), a `.envrc` for direnv,
  and its own `.claude/settings.json` (hooks), `.mcp.json` and `CLAUDE.md`
  for a Claude Code session you start there. Check them before you open a
  shell, git or Claude Code in a brain's folder.
- change the Claude Code state the CLI writes on every run, which all brains
  and your own sessions share: `~/.claude.json` (including the MCP servers
  your own sessions start), other sessions' transcripts in `~/.claude/projects`
  (it can add to them, not read or move them), `~/.claude/shell-snapshots`
  and `~/.claude/session-env` (sourced into running sessions' shell
  commands), and the small per-process files in `~/.claude/sessions`,
  which it can also move and then read
- move a prepared folder into `~/.claude/projects` for a project that has
  none yet (a symlink there is refused), which gives that project's
  sessions an auto-memory the brain wrote; with a `memory` symlink into its
  own folder inside, the brain also reads what those sessions save there
- create `~/.claude`, or a folder in it such as `~/.claude/projects`, while
  it is missing (on a home where the Claude CLI has not created it yet), by
  moving a prepared folder there (a symlink there is refused). What it put
  inside stays after the next run pins the folder: settings, hooks or a
  `CLAUDE.md` that the CLI and your own sessions load, or symlinks into its
  own folder, through which what they write there, transcripts included,
  reaches the brain
- ask launchd (`launchctl`), cron (`crontab`), `at`, LaunchServices
  (`open`), another app (`osascript`) or a tmux server that runs outside
  the sandbox (the activity viewer's, or your own) to start a program
  (`tmux run-shell`), which then runs outside the sandbox
- see the bridge's whole environment, write to the shared media inbox, and
  reach the network

Later changes give each brain its own Claude Code state folder and make
writes deny-by-default.

---

## Runtime

### `data_dir` *(restart)*

Database (including the WhatsApp session), downloaded media, the dashboard
token, backup state, the bridge's rclone config and calendar credentials.
Default `~/.fridolin`. `db_path` and `media_dir` override
individually. Brains cannot read it, except the `media/` inbox and their own
calendar credentials; a `db_path` elsewhere is denied to them too.

### `http_listen` *(restart)*

Address of the dashboard and the local APIs. Default `"127.0.0.1:8765"`;
`"off"` disables all of it. Only loopback addresses are accepted. The bridge
binds it before connecting to WhatsApp and exits if it is taken — usually a
sign another bridge is running.

`POST /send {"jid","text"}` sends a message (see the README); it is appended to
the owning brain's history, so the assistant knows what went out under its
name. Status, broadcast-list and channel JIDs (`…@broadcast`, `…@newsletter`)
are refused with `400 {"error": …}`. `GET /health` returns `ok`. Both work
without the dashboard token but refuse requests sent by web pages.

### `ingest.enabled`

Enables `POST /ingest {"brain","paths","text","sender"}` (`scope` is accepted
as an alias of `brain`). Paths must be files inside the media directory; the
brain receives them like WhatsApp attachments and answers in its
`reply_chat`. Off by default.

### `queue`

```json
"queue": { "mode": "queue", "max_pending": 10, "notify": true }
```

`mode: "queue"` (default) runs a brain's messages one after another and, with
`notify`, tells the sender "⏳ Queued (#2)". `mode: "interrupt"` stops the
current run when a new message arrives. Untagged group chatter waiting in the
queue is dropped when a newer message from that chat arrives — it is already in
the history the next run reads. `!stop` stops the current run and clears the
queue.

### `debounce`

```json
"debounce": { "quiet_ms": 2000, "max_wait_ms": 10000 }
```

Messages from the same person in the same chat that arrive less than
`quiet_ms` apart are merged into one run (an album, a file and its question).
A batch never waits longer than `max_wait_ms`. `quiet_ms: 0` turns it off.

### `startup_checks`

```json
"startup_checks": {
  "required_files": ["~/.config/my-assistant/calendar-credentials.json"],
  "notify_whatsapp": true,
  "hint": "Restore them from your password manager."
}
```

Files the assistant's tools need. Missing ones are logged loudly, shown on the
dashboard and, with `notify_whatsapp`, sent to `notify_jid` (default: the
first allowed number) once WhatsApp is connected.

### `backup`

Google Drive backups; see [BACKUP.md](BACKUP.md).

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | run the daily backup |
| `daily_at` | `"03:30"` | local time, `HH:MM` |
| `root` | `"fridolin-backup"` | folder in Drive |
| `remote` | `"brain-gdrive"` | rclone remote name in the bridge's own rclone config |
| `keep_versions_days` | `30` | `0` keeps old versions forever |
| `include_bridge_config` | `true` | also back up `config.json` and the brain list |
| `rclone_path` | found on PATH | |

### `plaud`

The Plaud recorder integration; see [PLAUD.md](PLAUD.md) for every setting.

### `dashboard`

`dashboard.disabled` turns the web dashboard off (*restart*).
`dashboard.require_token` (default `true`) requires the token from
`<data_dir>/dashboard.token` on every API call. Brains run on this machine and
could otherwise reach the dashboard with `curl`; leave it on.

### `otp_guard` and `otp_markers`

Drops a message that is only a 4–8 digit number when one of the assistant's
last 50 outbound messages contains one of `otp_markers` — by default `🔐`,
`OTP`, `2FA`, `one-time code`, `verification code`. Add your own (a bank's
name in its code prompt, say); setting the list replaces the defaults, so
include them if you still want them.

The code is never written to history or sent to the model — the assistant
never sees it. It is handed to whatever local script asked for it instead:
`<data_dir>/otp/<brain>.json` holds the latest caught code
(`{"code", "chat", "sender", "received"}`), readable only by your user and
denied to brains by the sandbox. A script that requested a code through
`/send` (with one of the `otp_markers` in its message) polls that file for a
`received` time after its request.

### `max_chunk_size`

Longest single outgoing message, in characters. Default 3900 (WhatsApp's limit
is around 4096). Longer replies are split at paragraph, then line, then
sentence boundaries and numbered `[1/3]`.

### `viewer`

Opens a tmux session per brain, tailing its activity log, in a Terminal window
(macOS). Off by default. Handy while tuning a prompt; noisy otherwise. Requires
tmux — without it the bridge logs a warning and carries on. The bridge starts
these sessions on tmux's default server, outside the sandbox, and any brain can
send that server commands (`tmux run-shell`), which then run unsandboxed. The
same goes for a tmux you run yourself; see
[what the sandbox does not cover yet](#what-the-sandbox-does-not-cover-yet).

### `log_level` *(restart)*

`debug`, `info`, `warn`, or `error`. `debug` is very verbose — it includes the
whatsmeow protocol chatter.

---

## Environment variables

Environment variables override `config.json`. A `.env` file next to the config
(or in the working directory) is loaded automatically and overrides the
process environment. The dashboard shows settings an environment variable
controls as locked — remove the variable to edit them there. The sandbox
keeps brains from reading the `.env` file, but every brain inherits the
bridge's environment, including what `.env` sets, so do not put anything
there that brains must not see.

| Variable | Config field |
| -------- | ------------ |
| `ALLOWED_NUMBERS` | `allowed_numbers` (comma-separated) |
| `ALLOWED_GROUPS` | `allowed_groups` (comma-separated) |
| `TRIGGERS` | `triggers` (comma-separated) |
| `CLAUDE_PATH` | `claude_path` |
| `DATA_DIR` | `data_dir` |
| `WORK_DIR` | `work_dir` |
| `DB_PATH` | `db_path` |
| `MEDIA_DIR` | `media_dir` |
| `MODEL` | `model` |
| `BRAIN_MODEL` | `brain_model` |
| `BRAIN_EFFORT` | `brain_effort` |
| `DISALLOWED_TOOLS` | `disallowed_tools` (comma-separated) |
| `BRAIN_MODE` | `brain_mode` |
| `SANDBOX_ENABLED` | `sandbox.enabled` |
| `OTP_GUARD` | `otp_guard` |
| `VIEWER` | `viewer` |
| `HTTP_LISTEN` | `http_listen` |
| `MAX_CHUNK_SIZE` | `max_chunk_size` |
| `DANGEROUSLY_SKIP_PERMISSIONS` | `dangerously_skip_permissions` |
| `LOG_LEVEL` | `log_level` |
| `CONFIG_FILE` | default `--config` path |

Booleans accept `true`, `1`, `yes`, or `on`. Brains are too structured for
environment variables and are configured in `config.json` only.

The Python tools read `BRIDGE_DATA_DIR` and `BRIDGE_DB_PATH` if you moved the
data directory, and `CALENDAR_TZ` for the calendar timezone.
