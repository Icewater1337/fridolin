# Changelog

What changes for people who run Fridolin. There are no tagged releases
yet, so everything is under Unreleased until the first one.

## Unreleased

### Changed

- **In an allowed group, only the numbers in `allowed_numbers` reach the
  brain now**, unless the group has a `group_members` entry. Before, every
  member of an allowed group could start a run with all of the brain's
  permissions. What the others write is no longer read at all: no run, no
  `!stop`, no download, and nothing in the history but their votes in the
  bridge's polls. If other people in your groups should still be heard,
  add an entry to `config.json` as you upgrade, right before `./setup.sh`
  (an older bridge ignores the key, but its dashboard drops it when it
  saves): `"group_members": {"<group JID>": ["*"]}` keeps the old
  behaviour, or list their numbers. The log names every allowed group
  without an entry at startup. Nothing changes while `allowed_numbers` is
  empty and `allow_any_dm` is `true`.
- `setup.sh` reads `FRIDOLIN_DATA_DIR` and `FRIDOLIN_INSTALL_DIR`.
  `DATA_DIR` and `INSTALL_DIR`, the names it read before, are ignored with
  a warning, since other software exports them too. When one of them names
  an existing config or install, `setup.sh` stops before it installs
  anything and says how to re-run, such as
  `FRIDOLIN_DATA_DIR=/path/to/data ./setup.sh`.
- `setup.sh` builds and tests with `make build` and `make test`, so it
  needs `make` (the Command Line Tools bring it on macOS,
  `build-essential` on Debian and Ubuntu). It asks the new build for the
  config file and the data folder instead of working them out itself.
- A new config, from `fridolin init`, `setup.sh` or the first start, holds
  its `data_dir`, so later starts no longer pick the data folder by which
  folders exist. Configs written before keep working as they are.
- The transcription engines go into a venv of their own,
  `~/.local/share/fridolin/venv`, which the transcription skill runs the
  tool with; a plain `pip install` fails on Homebrew's and Debian's Python.
  `uv run --script tools/transcribe.py` works too.
- The bridge looks up tmux, rclone and the Plaud CLI, which it runs
  outside the sandbox, only in the folders of its `PATH` that are in your
  home folder or one of `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`,
  `/usr/local/bin`, `/opt/homebrew/bin` and `/opt/local/bin`, so that a
  brain cannot plant one in a folder such as `/opt/homebrew/opt/go/bin`.
  One that is only in another folder of `PATH` is no longer found there:
  set `backup.rclone_path` or `plaud.cli_path` to it, or link tmux into
  `~/.local/bin`.
- The activity viewer runs `/usr/bin/osascript`, and its tmux and
  osascript with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`; a tmux server the
  bridge starts keeps that `PATH` for windows you open in it.
- The Plaud CLI gets its own folder first on `PATH` only when that holds
  a `node`, as nvm installs them, and otherwise finds `node` in the
  folders the helpers are looked up in. A `node` that is only elsewhere,
  such as a keg-only Homebrew `node@22`, is no longer found, and every
  Plaud command fails with "env: node: No such file or directory": link
  it into `~/.local/bin` or `/opt/homebrew/bin`
  (`brew link --force node@22`). Brains that `plaud.brain_access` lets
  use the CLI get its folder last on `PATH` unless it holds a `node`.
- A brain's backup refuses to mirror a deletion also when more than a
  fifth of its backed-up files are gone (once that is ten or more), when
  250 or more are gone, or when a top-level folder with at least five
  backed-up files lost more than half of them, so that a wiped
  `memories/` is no longer mirrored. "Mirror the deletion…" on the
  backup card goes ahead with one that was meant.
- At most two Claude runs go at once, all brains together
  (`max_concurrent_runs`, Settings → Claude); a brain whose turn it is
  waits for a free one, requests ahead of untagged chatter, and a request
  that has to wait gets the "Queued" notice. Set it to 0 for runs without
  a limit, as before.
- A brain's queue takes each chat's messages in order and the chats in
  turn, so a busy chat no longer keeps the brain's other chats waiting
  behind all of its messages. "Queued (#n)" counts the same way.
- `!reset` starts the chat afresh: besides what `!stop` does, it writes a
  `reset` line into the history of each brain that answers in the chat,
  and a run's prompt shows only the chat's messages after it. The file
  keeps the earlier ones; `fridolin history` and the dashboard's feed
  leave the marker out. Scripts that read `conversation-history.jsonl`
  themselves meet a new line type, which has no text.
- The OTP guard catches a code only in the chat that was asked for one,
  and only for ten minutes after: a number in the brain's other chats, or
  long after the request, reaches the brain like any other message. It
  also catches codes written in groups (`483 920`, `483-920`) and
  letter-and-digit codes (`G-483920`), matches `otp_markers` in any
  letter case as whole words, tells the sender that the code was kept
  out, and leaves text handed in through `/ingest` alone. The hand-off
  file's `code` has the spaces and dashes taken out; `typed` holds the
  message as it was sent.
- The OTP hand-off file, `<data_dir>/otp/<brain>.json`, names the request
  a code answers: `/send` answers a message that asks for a code with an
  `otp_request` id, which the file repeats as `request`, with
  `requested_unix_ms` and `expires_unix_ms`. The bridge deletes the file
  ten minutes after the code came, and at startup one that expired while
  it was stopped; a script deletes it once it has the code.
- A run's prompt looks for the chat's last messages in the last 16 MB of
  the brain's history, read back from its end, instead of reading the
  whole file for every run. A chat that was quiet behind more than 16 MB
  of other chats' messages starts without its earlier ones. A history
  line longer than 4 MB is skipped; before, a line over 1 MB stopped the
  reading, and every chat's prompt stayed at the messages before it.
- A message's `in` line in `conversation-history.jsonl` has the time the
  message was sent as its `ts`, not the time the bridge wrote it, a few
  seconds later. The line is written once the brain took the message: one
  its full queue turned away ("Try again in a bit") is no longer recorded,
  so the sender's retry is no longer in the history twice.
- On macOS a brain writes only its own places: its folder, its media
  inbox (`media/<brain id>/`) and its own state in
  `<data_dir>/brains/<brain id>/`, with a Claude Code config folder
  (`CLAUDE_CONFIG_DIR`), a temp folder (`TMPDIR`) and a cache. Writing
  anywhere else is denied, `/tmp` included, and so is writing your
  preferences (`defaults write`), so a skill that writes there or
  installs programs or global packages fails
  ([Writing](docs/CONFIGURATION.md#writing)).
- Brains no longer read your keychains, so the login you made with
  `claude` does not reach them: run `claude setup-token` and set
  `CLAUDE_CODE_OAUTH_TOKEN` in the service's environment before you
  upgrade. Unless `inherit_claude_user_config` is on, each brain starts
  with an empty Claude Code config folder of its own; its earlier CLI
  settings and transcripts stay in `~/.claude`, which brains no longer
  read. Its folder, memory and conversation history stay as they are.
- Each brain has a media inbox of its own, and no brain reads another's.
  On the first start what older versions left in the media folder, its
  folders (`media/plaud/` among them) and the attachments at its top,
  moves to `media/.old/`, which no brain can read.
- `/ingest` takes files only at the top of the media folder or in the
  named brain's own inbox, and from a brain's own run token only those in
  its inbox.
- Brains cannot start code outside the sandbox through `launchctl`,
  `open`, `osascript`, Apple events or the sockets of Docker and tmux, and
  runs start without a terminal.
- `sandbox.git_config` `"deny"` hides `~/.config/git` too, and both modes
  protect every file `~/.gitconfig` includes.
- A brain folder that is, holds or lies inside a path the sandbox protects
  is left out with an issue, also when `brain_dirs` was edited by hand;
  restores refuse such a target too.
- `fridolin check` exits with status 3 when a brain you approved will not
  run, and `setup.sh` shows check's issues and stops before it installs
  then. The chats of such a brain go to no other brain until you fix it:
  a DM brain left out used to hand your DMs to the default brain.
- The bridge runs no program from a folder of its `PATH` inside a brain's
  folder (a `bin/` there, say): it takes it off its own `PATH`, logs it,
  and hands it to that brain's runs only, first on their `PATH`.
  `fridolin service install` writes such folders to `FRIDOLIN_BRAIN_PATH`
  in the service file instead of `PATH`.
- Backups never upload a brain folder's `.env` files, private keys or
  calendar tokens unless `backup.include_secret_files` is on, and wait up
  to two minutes for a busy brain before they copy its folder.
- With `plaud.brain_access`, brains may rewrite the shared Plaud login
  but not remove or replace it, and the bridge no longer runs the Plaud
  CLI with a `~/.plaud/tokens.json` that is a symlink or a hard link, or a
  `~/.plaud` that is a symlink: remove the link and connect again.

### Added

- `group_members`: who in each allowed group reaches its brain, by phone
  number, or `["*"]` for everyone (docs/CONFIGURATION.md).
- `fridolin paths` prints the config file and the data dir in use, and
  where each came from (`--json` for programs).
- `fridolin check --redact` masks phone numbers, JIDs, email addresses and
  the home folder, for bug reports.
- The bridge's log names the config file and the data dir at every start,
  and where each came from.
- `plaud.phone_dir`: recordings can come from the Plaud Sync phone app
  (`plaud-android/`) instead of Plaud's cloud. The app pulls each
  recording off a NotePin as soon as it stops and has Plaud transcribe
  it; Fridolin reads the transcripts from the folder the app's files are
  synced to, with no Plaud CLI or login (docs/PLAUD.md).

### Removed

- Brain runs no longer get `BRIDGE_CREDENTIALS_DIR`, which named the
  folder of the calendar tokens; nothing in `tools/` read it any more.

### Fixed

- "Connect again" on a Plaud login that Plaud refuses signs that login
  out first, giving the sign-out up to 30 seconds. Before, it took the
  CLI's "Already logged in" for a new sign-in and showed Connected until
  the next check was refused again.

### Kept for compatibility

What is still read or sent under an older name or in an older format,
each marked `compat:` in the code.

From before the project was renamed (whatsapp-claude-bridge), kept
because they are on disk or because scripts rely on them:

- The data dir `~/.whatsapp-claude-bridge`, used while `~/.fridolin` does
  not exist, with a warning at every start; set `data_dir` to keep it.
- The launchd label `com.whatsapp-claude-bridge`, whose agent
  `fridolin service install` retires.
- The sandbox's denies of `~/.config/whatsapp-claude-bridge` and
  `~/Library/Logs/whatsapp-claude-bridge`.
- The personal-data check's denylist at
  `~/.config/whatsapp-claude-bridge/personal-denylist`, read with a note
  to move it to `~/.config/fridolin/personal-denylist`; it goes once a
  release has shipped with the note.

From earlier versions of the bridge:

- The calendar variables `BRIDGE_BRAIN_ID`, `BRIDGE_CALENDARS` and
  `BRIDGE_CALENDAR_DEFAULT`, which nothing in `tools/` reads now that the
  bridge makes the calendar calls; they go after one release.
- `BRIDGE_DATA_DIR` and `BRIDGE_DB_PATH`, which the tools no longer need
  since the bridge answers them, but which brains and their scripts rely
  on, as on the other `BRIDGE_*` variables; they stay.
- The `X-Bridge-Token` header, which scripts that call the dashboard API
  send the token in; the dashboard itself signs in with a session.
- The first layout of Plaud's `state.json`, without a version field and
  with the times in Unix seconds, which the bridge still reads.
