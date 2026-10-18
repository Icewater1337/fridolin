# Compatibility

What keeps working when you upgrade Fridolin, and how what does not is
changed. Scripts, brains, backups and an older build you go back to all
rely on these; [CHANGELOG.md](../CHANGELOG.md) says what changed in each
version.

## What stays

| What | Described in | How it changes |
| --- | --- | --- |
| `config.json`: its keys and what they mean | [CONFIGURATION.md](CONFIGURATION.md), [schema](schemas/config.schema.json) | Keys are added freely. A key renamed or removed, or one that comes to mean something else, raises the file's [`version`](CONFIGURATION.md#version), with a migration that reads the older file as it was meant; the next save writes the new version. A file of a newer version than the build reads stops it, so that it cannot drop settings it does not know. |
| `brain.json`, version 1 | [BRAINS.md](BRAINS.md#brainjson), [schema](schemas/brain.schema.json) | Fields are added. One renamed, removed or given another meaning comes with a new version. |
| A line of `conversation-history.jsonl` | [BRAINS.md](BRAINS.md#conversation-historyjsonl), [schema](schemas/history-line.schema.json) | Fields and types are added, never renamed or removed, and lines carry no version. Lines written before stay as they are. |
| `bridge.db`: the WhatsApp pairing, and the bridge's own tables (`polls`, `poll_votes`, `ignored_chats`, `outbox`) | [How it works](../README.md#how-it-works), `testdata/bridge-v1.sql` | Opening a database an older version wrote never forces a new pairing. The bridge's tables change in numbered migrations (`fridolin_version`) that only add tables and columns; the retired `sessions` and `process_state` tables stay, for an older build you go back to. |
| `GET /health`, `GET /ready`, `POST /send`, `POST /ingest`: their requests, answers and status codes | [API.md](API.md), schemas of [/send](schemas/send-request.schema.json) and [/ingest](schemas/ingest-request.schema.json) | Keys are added to requests and answers, never renamed or removed. |
| The token files `scripts.token` and `dashboard.token` in the data dir, the `X-Bridge-Token` header, and the OTP hand-off file `otp/<brain>.json` | [API.md](API.md#who-may-call), [CONFIGURATION.md](CONFIGURATION.md#otp_guard-and-otp_markers) | Their names and places stay. The OTP file gains keys, but none is renamed or removed. |
| The commands and flags `fridolin help` lists, and the service's command line, `fridolin --config PATH` (a first argument that is a flag means `run`) | `fridolin help` | Commands and flags are added. The JSON that `paths --json`, `history`, `polls` and `calendar` print gains keys, but none is renamed or removed. |
| The variables a brain's run gets, such as `FRIDOLIN_API`, `FRIDOLIN_BIN` and `BRIDGE_DATA_DIR` | [CONFIGURATION.md](CONFIGURATION.md#what-reaches-brains) | Variables are added. |
| The tools in `tools/` and their flags | the docstring of each | `transcribe.py` stays. `history_search.py`, `poll_results.py` and `calendar_tool.py` hand over to `fridolin history`, `polls` and `calendar`, and go after one release. |

Not covered: the dashboard and its API under `/api/`, which change with
the dashboard; what the bridge logs; the prompt a run is given and the
arguments it runs the Claude CLI with; the sandbox profile; and anything
else the golden files in `testdata/golden` pin, which tests keep from
changing by accident, not for others to rely on.

## How something goes

Something in the table that goes away goes in two steps. First it keeps
working, marked `compat:` in the code, with a warning in the log, or in
the answer of the request that uses it, saying what to use instead, and
CHANGELOG.md lists it under *Kept for compatibility*. At least one
release later it is removed, and CHANGELOG.md lists it under *Removed*.
A change that cannot wait for that, such as one that closes a security
hole, is listed under *Changed*, with what to do. A migration of
`config.json` to a new [`version`](CONFIGURATION.md#version) is marked
and listed the same way, but stays for good, so that a file of any
earlier version loads.

## Going back to an older build

`fridolin service rollback` runs the build before the one installed. It
reads the files the newer build wrote, as long as their version is one it
knows: a `config.json` a newer build saved as a newer version stops it,
and it says so; see [`version`](CONFIGURATION.md#version) for what to do
then. A build from before `config.json` had a version reads one of
version 2 as it is. In `bridge.db` the newer build only adds, so an older
build finds what it had.
