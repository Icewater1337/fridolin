# Changelog

What changes for people who run Fridolin. There are no tagged releases
yet, so everything is under Unreleased until the first one.

## Unreleased

### Changed

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

### Added

- `fridolin paths` prints the config file and the data dir in use, and
  where each came from (`--json` for programs).
- `fridolin check --redact` masks phone numbers, JIDs, email addresses and
  the home folder, for bug reports.
- The bridge's log names the config file and the data dir at every start,
  and where each came from.

### Removed

- Brain runs no longer get `BRIDGE_CREDENTIALS_DIR`, which named the
  folder of the calendar tokens; nothing in `tools/` read it any more.

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
