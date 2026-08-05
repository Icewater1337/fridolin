# Configuration

Everything lives in `config.json` next to the binary, or wherever `--config`
points. `./setup.sh --reconfigure` rewrites it interactively.

Validate at any time:

```bash
./whatsapp-claude-bridge --check
```

Paths may start with `~`, which expands to your home directory.

---

## Access control

### `allowed_numbers`

Phone numbers permitted to DM the bridge. Country code, no `+`, no spaces.

```json
"allowed_numbers": ["15551234567", "442071234567"]
```

An empty list allows **anyone** who can message the account. Only reasonable
for a number nobody else knows.

Newer WhatsApp clients identify senders by an internal ID rather than a phone
number; the bridge resolves that back to the number before checking, so plain
numbers here are correct.

### `allowed_groups`

Group JIDs the bridge will respond in. Groups are deny-by-default — an empty
list means no groups at all.

```json
"allowed_groups": ["120363000000000000@g.us"]
```

To find a JID: add the bridge to the group, send a message, and read it out of
the log line that says the group was ignored.

### `triggers`

Keywords that address the bridge in a group. Matched case-insensitively
anywhere in the message and stripped before the text reaches Claude.

```json
"triggers": ["@claude", "hey bot"]
```

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

### `dangerously_skip_permissions`

Passes `--dangerously-skip-permissions`, so Claude uses tools without asking.

Effectively required: nobody is at the keyboard to approve a prompt, so
without it every tool call hangs. Set to `false` only if you want a
conversation-only assistant with no tool access.

Read [Security](../README.md#security) before running with this on.

---

## Brains

A brain is one isolated assistant. Set `brain_mode: true` and list them:

```json
"brain_mode": true,
"brains": [
  {
    "name": "personal",
    "work_dir": "~/assistant/personal",
    "handles_dms": true
  },
  {
    "name": "team",
    "work_dir": "~/assistant/team",
    "groups": ["120363000000000000@g.us"],
    "model": "opus"
  }
]
```

| Field | Meaning |
| ----- | ------- |
| `name` | Identifier. Used in session keys, log lines, sandbox profiles, and calendar credential filenames. No spaces or slashes. |
| `work_dir` | Where Claude runs. Holds the prompt, history, and memories. Created if missing. |
| `handles_dms` | Routes all direct messages here. At most one brain. |
| `groups` | Group JIDs routed here. A group may belong to only one brain. |
| `default` | Fallback for allowed chats no brain claims. Defaults to the first brain. |
| `prompt_file` | Override the prompt path (default `<work_dir>/brain-prompt.md`). |
| `model` | Override `brain_model` for this brain. |

Startup fails on a duplicate name, a group claimed twice, or two brains with
`handles_dms`.

### What lives in a work_dir

```
<work_dir>/
├── brain-prompt.md               system prompt — prepended to every message
├── conversation-history.jsonl    appended automatically
├── claude-stream.log             activity log (tool calls, results)
└── memories/                     files the assistant writes for itself
    └── people/
        └── whatsapp-identity-map.md
```

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
profile. The profile is allow-by-default with reads denied to:

- every **other brain's** working directory
- `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`
- `~/.gitconfig`, `~/.git-credentials`, `~/.netrc`, shell history files
- `~/Documents`, `~/Downloads`, `~/Desktop`, `~/Movies`, `~/Music`, `~/Pictures`
- `~/Library/Mail`, `Messages`, `Calendars`, `Safari`, Chrome and Firefox
  profiles, `Containers`, `Group Containers`, Contacts, Notes
- other Claude Code sessions' transcripts (`~/.claude/projects`, `~/.claude/sessions`)
- the bridge's data directory — except `media/` and the brain's own calendar
  credentials

`~/Library/Keychains` is deliberately **not** denied: the Claude CLI reads its
credentials from the keychain and fails with "Not logged in" without it.

`deny_paths` adds your own. `allow_paths` re-opens something the defaults
closed.

**Don't put a `work_dir` inside a denied directory.** A brain living in
`~/Documents/assistant` cannot read its own prompt or memories, and fails in
confusing ways. Keep brains somewhere like `~/assistant/<name>`.

This is containment, not a jail — it raises the cost of a prompt injection
rather than eliminating it. On Linux it does nothing; run the bridge as a
dedicated user there.

---

## Runtime

### `data_dir`

Database, downloaded media, QR code, and calendar credentials. Default
`~/.whatsapp-claude-bridge`. `db_path` and `media_dir` override individually.

### `http_listen`

Address for the local send API, e.g. `"127.0.0.1:8765"`. Empty disables it.

```bash
curl -X POST http://127.0.0.1:8765/send \
  -H 'Content-Type: application/json' \
  -d '{"jid":"120363000000000000@g.us","text":"Hello"}'
```

There is no authentication, so the bridge refuses to start the server on a
non-loopback address. `GET /health` returns `ok`.

Messages sent this way are appended to the owning brain's history, so the
assistant knows what went out under its name.

### `otp_guard`

Drops a message that is only a 4–8 digit number when one of the assistant's
last 50 outbound messages contains `🔐`, `OTP`, `2FA`, `one-time code`, or
`verification code`.

The code is discarded before it is written to history or sent to the model.
The assistant simply never sees it.

### `max_chunk_size`

Longest single outgoing message, in characters. Default 3900 (WhatsApp's limit
is around 4096). Longer replies are split at paragraph, then line, then
sentence boundaries and numbered `[1/3]`.

### `viewer`

Opens a tmux session per brain, tailing its activity log, in a Terminal window
(macOS). Off by default. Handy while tuning a prompt; noisy otherwise. Requires
tmux — without it the bridge logs a warning and carries on.

### `log_level`

`debug`, `info`, `warn`, or `error`. `debug` is very verbose — it includes the
whatsmeow protocol chatter.

---

## Environment variables

Environment variables override `config.json`. A `.env` file next to the config
is loaded automatically.

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
