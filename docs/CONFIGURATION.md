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
not sandboxed. The profile is allow-by-default with reads denied to:

- every **other brain's** working directory
- `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`,
  `~/.config/rclone`
- `~/.git-credentials`, `~/.netrc`, shell history files
- `~/Documents`, `~/Downloads`, `~/Desktop`, `~/Movies`, `~/Music`, `~/Pictures`
- `~/Library/Mail`, `Messages`, `Calendars`, `Safari`, Chrome and Firefox
  profiles, `Containers`, `Group Containers`, Contacts, Notes
- other Claude Code sessions' transcripts (`~/.claude/projects`, `~/.claude/sessions`)
- the bridge's data directory — except `media/` and the brain's own calendar
  credentials

`~/Library/Keychains` is deliberately **not** denied: the Claude CLI reads its
credentials from the keychain and fails with "Not logged in" without it.

`deny_paths` adds your own. `allow_paths` re-opens something the defaults
closed (read access).

A brain's own folder is always readable and writable, even inside a denied
directory. Other tools it runs may still need paths the defaults deny, so
keeping brains somewhere like `~/assistant/<name>` is simplest.

### `sandbox.git_config`

`"read"` (default): brains may read `~/.gitconfig` and the files it includes,
so they can commit in their own folder, but never write them. `"deny"` hides
git configuration entirely. `~/.git-credentials` is denied either way.

### `sandbox.credentials_dir`

A directory of per-brain credential files kept outside the data directory.
Brains are denied all of it except their own calendar pair
(`google_calendar_credentials_<id>.json`, `google_calendar_token_<id>.json`).

This is containment, not a jail — it raises the cost of a prompt injection
rather than eliminating it. Linux has no `sandbox-exec`: the bridge refuses a
config with `sandbox.enabled` on there (the Linux default is off). Run the
bridge as a dedicated user instead.

---

## Runtime

### `data_dir` *(restart)*

Database (including the WhatsApp session), downloaded media, the dashboard
token, backup state, the bridge's rclone config and calendar credentials.
Default `~/.fridolin`. `db_path` and `media_dir` override
individually. Brains cannot read it, except the `media/` inbox and their own
calendar credentials.

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
tmux — without it the bridge logs a warning and carries on.

### `log_level` *(restart)*

`debug`, `info`, `warn`, or `error`. `debug` is very verbose — it includes the
whatsmeow protocol chatter.

---

## Environment variables

Environment variables override `config.json`. A `.env` file next to the config
(or in the working directory) is loaded automatically and overrides the
process environment. The dashboard shows settings an environment variable
controls as locked — remove the variable to edit them there.

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
