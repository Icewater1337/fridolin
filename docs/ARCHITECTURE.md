# Fridolin architecture

This document describes how the Go code is organised: the packages, the
rules between them, and the contracts that hold the program together. It is
written for contributors and coding agents. When the code and this document
disagree, fix one of them in the same change.

The code is in the middle of a restructure: it is moving, step by step, from
one `main` package into the packages below. Section 0 says how far that has
got. The other sections describe the layout the steps lead to, and say where
today's code differs from it. Behaviour that later work will change is
marked *Planned* and collected in section 14. For what the bridge does
today, the README, [CONFIGURATION.md](CONFIGURATION.md), [API.md](API.md),
[BRAINS.md](BRAINS.md) and the other user documents are the reference.

## 0. Where the restructure stands

Each step leaves the bridge building, tested and working, and changes no
behaviour unless its title says so. The steps so far:

- **R1**: the characterization net. Golden files of every GET endpoint,
  config writes, sandbox profiles, prompts, the Claude CLI's arguments and
  what `fridolin check` and `init` print; contract tests of the script API;
  the route inventory; the smoke test; the end-to-end harness
  (`newHarness`).
- **R2**: the `main` package moved into `cmd/fridolin`, and the files the
  binary embeds into the packages `web`, `examples` and `site`. The
  repository's root has no Go files.
- **R3**: the foundation packages `fsx`, `textx`, `version`, `trigger` and
  `platform` below `internal/`, and `internal/archtest`, which enforces the
  rules of section 4 that already apply.
- **R4, first slice**: `logx` owns the log ring and whatsmeow logger adapter;
  `store` owns the database, poll, outbox and inbound queue repositories.
  The shared file permission helper lives in `fsx`. Process logger setup and file rotation
  remain in `cmd/fridolin`.
- **R4, history slice**: `history` owns the conversation entry format and
  rooted file reads, appends and rewrites; `fsx` owns the regular-file opener.
  Runtime history locking, inbound bookkeeping and outbox merging remain in
  `cmd/fridolin`.

- **R4 (chat)**: the shared message content, quotes, changes, connection
  and group DTOs, and pure JID/merge helpers live in `internal/chat`. The
  command keeps the runtime wrapper carrying logged history and merged
  parts until the brain extraction.

- **R4, OTP slice**: `otp` owns code recognition, recent request lookup,
  handoff records and expiry. The command keeps runtime settings, inbound
  source checks and bridge callbacks.
- **R4 (media)**: the `media` package owns bounded attachment creation,
  per-brain inboxes, path confinement and the legacy media migration. Its
  caller supplies the shared migration lifecycle and working directory.
- **R4, activity slice**: `activity` owns the dashboard's bounded feed and
  edit/delete updates. The command supplies history entries and the lazy
  seed reader, which still runs under the feed lock.

- **R5, shared readers**: `jsonx` owns recursive unknown-field inspection;
  `fsx` owns bounded regular-file reads through pinned brain folders.
  Configuration and manifest policies remain at their callers.

- **R5, manifest slice**: `manifest` owns portable brain descriptions,
  validation, normalized defaults and rooted publication. Runtime addressing
  remains command-owned; `config` owns the staged manifest transaction.

- **R5, routing slice**: `route` owns set validation and pure chat/reply
  selection. The manager still owns live maps, held routes and brain identity.

- **R9, scaffolding slice**: `registry` owns starter-template loading,
  confined folder creation and retained-root rollback. The command supplies
  embedded assets and the runtime's identity-map path; registration workflows
  remain in the command until their later extraction.

- **R5, configuration values**: `config` owns the file shape, section types,
  defaults, validation, migrations and approved brain references.

- **R5, configuration store**: `config` owns loading, starter files, atomic
  snapshots, ordered notifications and retained-root manifest transactions.
  The command supplies brain-set validation, run timing and startup permission
  changes. Store validation runs under the commit lock before any write.

- **R6, explicit inputs**: `Resolve` receives captured paths and platform. Store
  open receives those inputs, the retired-environment lookup and the brain-set
  checker; later saves reuse the same paths/platform. Configuration reads no
  ambient environment or home, so its temporary architecture exception is gone.
  Boot path projections and the full field table follow in the next slice.

Everything else is still `package main` in `cmd/fridolin`, one file per
subject (section 3 names the file each package will come from). A package
that is not yet extracted is marked *(to come, Rn)* with the step that
creates it.

How a step moves code: it first makes the code movable inside
`cmd/fridolin` and writes the tests that guard the move; then it `git mv`s
the files into their package, exports what the rest still uses and keeps
the old names in a temporary `cmd/fridolin/aliases.go`; then it rewrites
the call sites with a recorded script and deletes `aliases.go`, adding the
package's row to archtest's import table. Each commit builds and passes the
tests. Types, JSON tags, error strings, timings and goroutine structure stay
the same, and known bugs move unchanged.

## 1. What Fridolin does

Fridolin links one WhatsApp account to Claude Code. A message that reaches the
account is checked against the access rules, routed to a *brain*, and answered
by running `claude -p` in that brain's folder. A brain is a folder with its own
system prompt, memory files, conversation history and a `brain.json`
manifest. One process serves:

- the WhatsApp connection (whatsmeow), or an in-memory fake in dev mode
  (`--no-whatsapp`);
- the brains: one queue and one worker per brain; every run is a fresh
  `claude` process, wrapped in a macOS sandbox profile when the sandbox is
  on, with a deadline and a record in `<data_dir>/runs/` while it lasts;
- the outbox: replies wait in `bridge.db` until WhatsApp took them, and go
  into the brain's history only once delivered;
- a loopback HTTP server: the dashboard (plain JavaScript embedded in the
  binary, every API call needs the dashboard token or a login session),
  the script API `/send`, `/ingest`, `/health` and `/ready`, and the brain
  API `/brain/` that the tools a run starts call with that run's token;
- integrations: Google Drive backups through an isolated rclone, per-brain
  Google Calendar sign-in, the Plaud voice recorder, and the site process
  (a Node program the bridge unpacks and supervises, docs/SITE.md).

It runs on macOS and Linux as a user service (launchd or systemd, installed
by `fridolin service install`) and listens on loopback only.

### External contracts

These are relied on by running installs, scripts and tools. A change to any of
them needs its own migration plan, not a refactor.

| Contract | Owner | Today |
| --- | --- | --- |
| `bridge.db`: the whatsmeow device store plus `polls`, `poll_votes`, `ignored_chats`, `outbox` and `inbound_queue`, versioned in `fridolin_version`. The retired `sessions` and `process_state` tables stay in the schema and are never dropped. Opening an existing file must never force a re-pair. | `internal/store` | `internal/store/store.go`, `internal/store/outbox_store.go` |
| `config.json` keys and meanings. The retired keys `model`, `work_dir`, `brain_mode` and `brains` still load. | `internal/config` | `config.go`, `configstore.go` |
| `brain.json`, version 1. | `internal/manifest` | `manifest.go` |
| `conversation-history.jsonl`: one JSON object per line, `{ts, from, chat, text, type}`. | `internal/history` | `brain.go` |
| `POST /send`, `POST /ingest`, `GET /health`, `GET /ready` (docs/API.md): plain-text error bodies, today's status codes, the scripts token and run tokens. | `internal/httpapi` (`script.go`) | `server.go`, `scripttoken.go` |
| The brain API `/brain/` and the commands brains run with it: `fridolin history`, `fridolin polls`, `fridolin calendar`. | `internal/httpapi`, `cmd/fridolin` | `brainapi.go`, `toolcmd.go`, `*_cmd.go` |
| Dashboard headers `X-Bridge-Token` and `X-Bridge-Dashboard`; the token files `<data_dir>/dashboard.token` and `<data_dir>/scripts.token`; the login sessions of `fridolin open`. | `internal/httpapi` | `dashboard.go`, `dashsession.go` |
| Environment variables a run gets: `FRIDOLIN_BRAIN_ID`, `FRIDOLIN_BIN`, `FRIDOLIN_TOOLS_DIR`, `FRIDOLIN_API`, `FRIDOLIN_BRAIN_TOKEN_FILE`, `BRIDGE_DATA_DIR`, `BRIDGE_DB_PATH`, and for one release `BRIDGE_BRAIN_ID`, `BRIDGE_CALENDARS`, `BRIDGE_CALENDAR_DEFAULT`; in the sandbox the brain's own state, `CLAUDE_CONFIG_DIR` (unless `inherit_claude_user_config` is on), `TMPDIR`, `CLAUDE_CODE_TMPDIR`, `TMPPREFIX`, `XDG_CACHE_HOME` and `npm_config_cache` (docs/CONFIGURATION.md). | `internal/app`, `internal/google` | `childenv.go` |
| Credential files `google_calendar_{credentials,token}_<brain>.json` in Google's "authorized user" format. | `internal/google` | `google.go` |
| The OTP hand-off file `<data_dir>/otp/<brain>.json`. | `internal/otp` | `bridge.go` |
| The service command line `fridolin --config PATH` (a leading flag means `run`), and the install in `~/.local/lib/fridolin`. | `cmd/fridolin` | `main.go`, `service.go` |
| The legacy data dir `~/.whatsapp-claude-bridge`, used when `~/.fridolin` does not exist. | `internal/config` | `config.go` |

Every compatibility shim carries a `// compat:` comment next to its code, so
`grep -rn 'compat:'` lists them all.

## 2. Repository layout

```
cmd/fridolin/        CLI and, until the restructure is done, all code not yet in a package
internal/            Go packages, one directory per package (section 3)
web/                 the dashboard (HTML, CSS, ES modules, no build step) and embed.go
examples/            brain templates (prompt, skills) and embed.go
site/                the site process (Node, TypeScript) and embed.go
docs/                user and contributor documentation, including this file
tools/               Python helpers that brains run
scripts/, .githooks/ personal-data guard, release build, smoke test and git hooks
setup.sh, Makefile   build, test and install the service
```

The module path is `github.com/Icewater1337/fridolin`; nothing outside the checkout imports it.

There are no Go files at the repository root. Build with `make build` or
`go build -o bin/fridolin ./cmd/fridolin`, which is what `setup.sh` does
through make. Running `go build .` at the root fails with "no Go files" and
leaves an existing `./fridolin` alone. This is deliberate. With a non-main
package at the root, `go build -o fridolin .` would exit 0 and replace the
binary a service set up by an older `setup.sh` still starts with an `ar`
archive. `make root-check` (in `make check` and CI) and archtest's
`TestRootHasNoGoFiles` keep it so.

`web/embed.go` (package `web`) embeds `index.html`, `favicon.svg` and
`assets/`. `examples/embed.go` (package `examples`) embeds the prompt
template and the skills. `site/embed.go` (package `site`) embeds the site
process, the starter template of a site repository and the builder plugin.
Only `internal/app` will import them; today `cmd/fridolin` does, and tests
may too.

The build stamps its version into `github.com/Icewater1337/fridolin/internal/version.version`
(`-ldflags "-X github.com/Icewater1337/fridolin/internal/version.version=v1.2.3"`), as `make build`
and `scripts/build-release.sh` do.

## 3. Package map

Packages are listed from the bottom of the dependency graph up. Every package
under `internal/` has a package doc comment that repeats its one-line
responsibility and says what it must not do.

### Foundation

- **`fsx`**: file-system primitives. Bounded brain-file readers keep the
  existing 256 KiB limit, reject non-regular entries and symlinks, and may
  reuse an already pinned `os.Root`. `WriteFileAtomic` (temp file, fsync,
  rename, optional `.bak`), `ExpandHome` and `Tilde` (the home dir is always a
  parameter), `Canonical` (clean and resolve symlinks), `Resolve` (the real
  path of an absolute path, through the part that exists, which `Canonical`
  falls back on for a path that does not exist yet), `Within` (containment
  that avoids the sibling-prefix trap), `Exists`, and `RestrictToOwner`
  (remove group/other access while preserving owner access and special bits).
  `OpenInFolder` opens a regular file through an `*os.Root`, refusing
  symlinks, special files and a file replaced during the open.
- **`jsonx`**: standard-library-only JSON field inspection and diagnostic
  formatting. `UnknownKeys` reports recursively sorted field paths while
  respecting custom decoders and case-insensitive JSON field matching.
  `RenameTypeError` lets the owning package retain pre-extraction type names
  in decoder messages; the original error, offsets and field context remain
  available through `errors.As`. Config and manifest select their own names.
- **`textx`**: `Truncate` and `Tail`, which shorten text from its end or its
  start to a byte limit without splitting a rune. `SBPLString` quotes profile
  strings without changing UTF-8; `EscapeSBPL` applies the same escaping to
  diagnostic redaction. `ValidEnvName` checks child environment variable names.
  Configuration and sandbox code share these pure validators.
- **`version`**: `String()`, which returns the `-ldflags -X` value, or
  `dev-<rev>[+dirty]` from the build info, and `Line()`, the build, Go
  release and platform for the first log line.
- **`apperr`** *(to come, R9)*: error kinds (`NotFound`, `Invalid`,
  `Conflict`, `Busy`, `Precondition`, `Unavailable`) and an `Error` type
  whose message is exactly the text a user sees. Domain packages classify
  errors with it. Only `httpapi` turns kinds into status codes.
- **`trigger`**: group trigger keywords. `List` accepts a string or an
  object per entry in JSON; `Matches`, `Strip` and `First` match
  Unicode-correctly on byte offsets in the original text. `BoundaryAt` and
  `BoundaryBefore` are the word rule, which @-mentions follow too.
- **`chat`**: shared message vocabulary with no internal dependencies.
  `Inbound` holds message content, an optional explicit `Brain` target and
  a structured `Quote`; `Merge`, `TextWithQuote` and `HistoryText` preserve
  the existing text and attachment formatting. `Change` names edits and
  deletions, with `Names` checking chat, sender and message ID. `LinkState`
  and `Group` retain the dashboard JSON tags; `NormalizeJID`, `IsGroupJID`
  and `DMJID` share the JID rules. Source and change-kind constants keep
  their existing string values. Transport envelopes and script principals
  remain in the command until the adapter and pipeline extractions.
- **`platform`**: OS integration behind build tags. It provides the instance
  `Lock` (`AcquireLock`, `Release`; a value, not a global, which `NewBridge`
  takes today and `main` will own), process isolation (a session and
  process group of its own, without a controlling terminal) and the group's
  kill, `Exec` for the re-exec on restart, `OpenBrowser`, `FolderPicker`
  and `IsTerminal`. `unsupported.go`, built alone on every system that is
  not Unix, stops the build with a clear message.
  `Viewer` owns the optional tmux activity window; the caller supplies its
  resolved helper path, allowlisted environment and timeout. Trusted helper
  lookup stays in the composition code. *Planned:* process start times
  from `procstart_*.go`.
- **`logx`**: the in-memory `Ring` and `Line` the dashboard reads, and
  `NewWaLogger`, the whatsmeow logger adapter. The process logger setup,
  configured ring capacity and file rotation remain in `cmd/fridolin`.

### Formats and storage

- **`manifest`**: portable `brain.json` descriptions, validation, normalized
  defaults, copied backup exclusions and accepted effort/observe values.
  `Read` and `Write` retain bounded regular-file reads and complete exclusive
  publication. `OpenRoot`, `Encode` and `WriteDataRoot` let configuration
  transactions keep pinned folders and published inode handles until commit
  or rollback. `Spec` retains the manifest, canonical folder, configured path,
  relative prompt file and configuration entry index. The package uses only
  `chat`'s group suffix predicate, `fsx` and the pure `jsonx` inspector.

- **`history`**: `conversation-history.jsonl`.
  It provides `Entry`, `Append`, `Tail` and `ForChat`, all through the
  brain's `os.Root`. A file is read back from its end, a block at a time,
  and `ForChat` stops at the chat's newest reset marker. `Scan` supports
  bounded reverse searches; `Rewrite` and `HoldsEntry` support message edits
  and deletions. The package takes chat IDs as strings and knows nothing of
  inbound queues, the outbox or delivery. Callers retain the history mutex
  and invoke callbacks after releasing it.
- **`store`**: `bridge.db`, opened with `Open(dbPath, logLevel)`.
  Fridolin's own tables are versioned in the `fridolin_version` table, kept
  apart from whatsmeow's `whatsmeow_version`, and migrations only add.
  Repositories handle polls, poll votes, ignored chats, the outbox and inbound
  queue rows. Inbound JSON encoding and decoding stay in `cmd/fridolin`,
  alongside the message and history types; store handles opaque bodies and
  preserves SQL ordering and transactional deletion after the codec visits
  every row. A failed transaction exposes none of the decoded rows.
  The raw SQL connection stays private; integration tests open separate
  connections to their scratch databases for assertions and failure injection.
  *Planned (R19):* one `*sql.DB` shared by the whatsmeow device container
  and Fridolin's tables.
- **`media`**: the media folder and each
  brain's inbox in it, `<media_dir>/<brain id>/`, the one part of it that
  brain can open. Attachments arrive in `.incoming/`, which no brain can
  read, and move into the inbox of the brain that takes their message
  (`Inbox.Claim`), through the media folder's `os.Root`, under a sanitised
  name that never replaces a file. `/ingest` takes files only from the top
  of the media folder or the named brain's own inbox: absolute paths only,
  symlinks resolved, regular files only. `Inbox.Resolve` preserves both the
  script and brain-token rules. `Inbox.Save` gives the transport a bounded
  random-access `File`; WhatsApp types stay in its adapter. `Migration`
  holds the process-start cutoff and mutex shared by every inbox; the
  command constructs it once and passes it with the working directory.
  Name shortening reuses `textx.Truncate`, so this leaf also imports `textx`.

### Configuration and brain set

- **`config`**: owns the `Config` file shape, defaults, migrations, validation,
  `Open`/`Load`/`LoadExisting`, starter files and the live `Store` with `Tx`.
  `Open` accepts command-owned brain-set validation; transactions retain
  config-last publication and rollback through pinned manifest roots. Resolution
  receives explicit paths and platform. The field table and Boot/Live projections
  follow in the next R6 slice; pointer-free settings follow in R18 (section 5).
- **`route`**: pure validation and routing for approved, runnable specs.
  `Build` validates the set and returns an immutable `Table` with `For`, `Has`,
  `Default`, `Dirs` and `ReplyChat`. It keeps configured ordering and copies
  mutable construction data. The free `ReplyChat` helper takes the runtime's
  current DM decision so callers preserve held routes and exact brain pointer
  identity. The manager's maps and unavailable-route holds remain in the
  command until the runtime extraction; the new table does not replace them.

- **`registry`** *(to come, R9; `brainreg.go`, `brain_changes.go`)*: the
  configured brain set. It resolves `brain_dirs` into specs and issues
  (drift, missing, invalid, unapproved, retired), checks folders, scaffolds
  new brains, and runs every workflow that changes `brain_dirs` or a
  `brain.json`: add, update, approve, remove, assign group, per-brain backup
  and calendar settings. It is the only code that writes `brain.json`.

### Runtime

- **`prompt`** *(to come, R8; `prompt.go`)*: renders the prompt a run gets
  on stdin from the inputs it gathered: system prompt, recent history for
  the chat, identity map, attachments, quote, sender, text, and whether the
  run only observes. It does no I/O, which makes it golden-testable.
- **`claudecli`** *(to come, R8; `claudecli.go`, `stream.go`,
  `tailbuf.go`)*: the Claude CLI contract. `Args` builds the flags. `Parse`
  reads stream-json and reports text, tool use and the result through
  callbacks. `Run` runs one supervised process: process group, a signal
  after the result, a grace kill, a deadline, and exit classification.
  `Prober` reports the CLI path and version. It knows nothing about brains
  or chats.
- **`sandbox`** *(to come, R8; `sandbox.go`)*: the macOS `sandbox-exec`
  profile. `Profile(Policy, brainID)` is pure. `Profiler` holds the current
  policy and wraps a command: the profile goes inline with `-p`, and if the
  sandbox is on but cannot be applied, the run fails instead of starting
  unsandboxed.
- **`otp`**: decides whether a message is a
  one-time code its chat asked for, in the last ten minutes of the brain's
  outbound history through `Requested`. `Code` normalizes a bare code;
  `HasMarker` and `DefaultMarkers` recognize requests. `Handoff.Write`
  writes a caught code as a `Record`, and the request it
  answers, to the file local scripts read.
- **`brain`** *(to come, R10; `brain.go`, `runrecord.go`, `observe.go`,
  `runslots.go`)*: the brain runtime. `Manager` owns the brain set, swaps
  the routing table atomically on `Apply`, keeps unchanged brains (and
  their running process) across reloads, retires removed ones in the
  background, and holds the run slots all brains share
  (`max_concurrent_runs`). `Brain` owns the queue, with a lane per chat
  and the chats taking turns, and the worker, the per-run settings
  snapshot, stop and reset per chat, observe-only runs, and the runtime
  snapshot for the dashboard. Two
  consumer interfaces cut it off from processes and transports. `Runner`
  does one run, and `Sink` receives the working notice, forwarded
  questions, finished runs and history lines.
- **`access`** *(to come, R11; `addressing.go`, `group_policy.go`,
  `whatsapp.go`)*: decides who may talk to the bridge, in a group who of
  its members (`group_members`), and whether a message addresses it.
  `Decide(Policy, Envelope)` is one pure, table-tested function used by
  WhatsApp, dev mode and `fridolin check --route`.
- **`pipeline`** *(to come, R12; `bridge.go`, `debounce.go`)*: the single
  intake for every message source. It handles chat commands, the junk
  filter, choosing the brain, the OTP gate and debouncing. It dispatches to
  `brain.Manager`, turns finished runs into deliveries, and provides `Ingest`
  and `SendExternal` for `/ingest`, `/send`, Plaud and the site.
- **`outbound`** *(to come, R12; `chunks.go`, `polls.go`, `outbox.go`,
  `chaterr.go`)*: sending on top of a minimal `Transport`: the durable
  outbox, chunking, pacing, polls with a text fallback, saving sent polls,
  and the catalogue of every chat-facing message the pipeline sends.
- **`whatsapp`** *(to come, R11; `whatsapp.go`, `waconn.go`,
  `groupcache.go`)*: the WhatsApp transport and nothing else. It connects,
  pairs, tracks connection state, keeps the group list, sends text and
  polls, decrypts poll votes, turns protobuf messages into an `Envelope` (a
  pure function), and downloads media into the inbox.
- **`devchat`** *(to come, R11)*: the in-memory transport for dev mode. It
  keeps sent messages for the dashboard and the tests.
- **`activity`**: the dashboard's in-memory feed of recent history lines.
  `Feed` owns the bounded buffer, ordering, and edit/delete updates. The
  caller supplies history entries and a lazy seed reader, called once per
  brain under the feed lock; runtime brains and transports stay outside.

### Integrations

- **`backup`** *(to come, R13; `backup.go`, `connectjob.go`)*: Google Drive
  backups through an isolated rclone (its own `--config`, `RCLONE_*`
  stripped from the environment, stdout never logged). It covers connect,
  run, the daily schedule, prune, versions and restore, and has no HTTP
  code.
- **`google`** *(to come, R14; `google.go`, `google_calendar.go`)*: the
  OAuth client and per-brain calendar sign-in with PKCE. It handles tokens,
  the calendar list and the calendar calls the brain API makes for runs,
  revocation, credential file naming and the calendar variables for runs.
  Endpoints and the HTTP client are injected.
- **`plaud`** *(to come, R15; `plaud.go`)*: the Plaud recorder. It runs the
  CLI with a private HOME, polls, waits for transcripts and hands them off
  through narrow function dependencies.
- The site manager (`site*.go`) is not in the plan's steps yet. It will get
  a package of its own below `internal/`; the name `site` is the embed
  package's.

### Surface and wiring

- **`httpapi`**: HTTP request guards, token files and JSON codecs. `Token`
  loads or creates a named token file, rotates it and compares values;
  `ReadToken` reads the dashboard token without creating it. `APIGuard`
  receives callbacks for the current token requirement and session check.
  Host, origin, write and security-header guards keep the existing response
  contract. `DecodeJSON` and `DecodeOptionalJSON` retain strict unknown-key
  and single-value decoding; `LoginURL` contains a one-use login code.
  Resource handlers, login sessions, routes, script and brain APIs, static
  files, OAuth and server lifecycle still live in `cmd/fridolin`
  (`dashboard*.go`, `server.go`, `scripttoken.go`, `brainapi.go`,
  `dashsession.go`, `site_api.go`) until R16.
- **`app`** *(to come, R17)*: the composition root and lifecycle. It builds
  every component, projects config into each package's own settings,
  reconciles on config change, supervises the `Run(ctx)` components and
  handles restart.
- **`app/apptest`** *(to come, R17)*: the end-to-end harness in dev mode,
  today `harness_test.go` in `cmd/fridolin`.
- **`testutil`** *(to come, R8)*: shared test helpers. It makes the test
  binary act as the Claude or Plaud CLI, and provides `WaitFor` and golden
  files. It uses only the standard library. Today these are in
  `cmd/fridolin`'s `helpers_test.go`, `golden_test.go` and
  `plaud_fake_test.go`.
- **`archtest`**: tests that enforce section 4.
- **`cmd/fridolin`**: argument parsing and the subcommand table (`run`,
  `check`, `open`, `rotate-token`, `init`, `paths`, `service`, `version`,
  the commands brains run, `history`, `polls` and `calendar`, and
  `mcp-site`, which a site builder's runs start). It owns the instance lock
  and the signal context and re-execs after a restart request. Today it
  also holds all the code not yet in a package.

## 4. Dependency rules

Arrows point at what a package imports. Only internal packages are listed.
This is the target; archtest's import table (`internal/archtest`) holds the
edges that exist today.

```
cmd/fridolin ─► app, config, registry, route, access, sandbox, httpapi, platform, version, logx

app ─► every internal package except testutil and archtest; web; examples; site

httpapi ─► config, registry, brain, pipeline, devchat, store, activity, logx,
           backup, google, plaud, media, claudecli, manifest, chat, platform,
           version, fsx, textx, apperr

registry ─► config, manifest, route, fsx, apperr
config   ─► manifest, trigger, fsx, apperr

pipeline ─► brain, outbound, otp, chat, textx, apperr
whatsapp ─► access, media, logx, platform, chat, textx
devchat  ─► chat, textx
access   ─► chat, trigger
outbound ─► store, textx

brain     ─► route, manifest, history, prompt, otp, claudecli, platform, chat, fsx, textx, apperr
claudecli ─► platform, textx
prompt    ─► history, chat
sandbox   ─► fsx
otp       ─► history, fsx
route     ─► manifest, chat, fsx

backup ─► fsx, textx, apperr
google ─► manifest, fsx, apperr
plaud  ─► media, platform, chat, fsx, textx, apperr

activity ─► history, textx
store    ─► fsx, logx
history  ─► fsx
media    ─► fsx, textx
manifest ─► fsx, apperr

leaves (standard library only): fsx, textx, version, apperr, trigger, chat,
                                platform, logx (plus whatsmeow's log type), testutil
```

Today `cmd/fridolin` imports `web`, `examples`, `site`, `fsx`, `textx`,
`version`, `trigger`, `platform`, `chat`, `history`, `logx`, `store`,
`otp`, `media` and `activity`. History imports `fsx`; store imports `fsx`
and `logx`; OTP imports `history` and `fsx`; media imports `fsx` and `textx`;
activity imports `history` and `textx`. The other extracted packages import
nothing of the module.

The rules:

1. Nothing imports `cmd/fridolin`. Only `cmd/fridolin` and `app/apptest`
   import `app`.
2. Below `app`, only `registry` and `httpapi` import `config`. Every other
   package declares the settings type it needs (`brain.Globals`,
   `sandbox.Policy`, `access.Policy`, `pipeline.Settings`, `backup.Settings`,
   `plaud.Settings`, the options of `google`, `whatsapp` and `httpapi`). `app`
   fills them in from config with small pure projection functions.
3. Only `app` imports `whatsapp`. `httpapi` sees the transport through its
   `Pairing` interface and the `chat` types, so whatsmeow never reaches the
   HTTP layer.
4. `brain` does not import `config`, `registry`, `sandbox`, `store`, a
   transport, `pipeline` or `httpapi`. Processes reach it through `Runner`,
   the rest of the bridge through `Sink`.
5. Only `httpapi`, `google` (as an HTTP client) and `app` import `net/http` in
   non-test code. Domain packages never write HTTP responses.
6. `testutil` and `app/apptest` are imported only from `_test.go` files.
   `testutil` imports only the standard library, so any package's internal
   tests can use it. Builders that need `config` or `manifest` live in tests
   or in `app/apptest` and are used from external (`_test` package) tests.
7. No package under `internal/` reads process-wide state except `platform`,
   `app` and `testutil`. That covers `os.Getenv`, `os.LookupEnv`,
   `os.Environ`, `os.ExpandEnv`, `os.UserHomeDir`, `os.Executable`,
   `os.Getwd` and `exec.LookPath`. The home dir, data paths and the base
   environment for child processes are computed once at startup and passed
   in as values.
8. Every package of the module has a row in archtest's table of allowed
   imports. A new edge is a one-line change to that table, made and
   reviewed in the same PR.

`internal/archtest` checks these on every `go test ./...`: `TestImportRules`
reads every package's imports (its tests' included, for macOS and Linux)
with `go/build` and checks rules 1 and 8 against the table, and
`TestNoAmbientEnv` scans the syntax trees for rule 7. Rules 2 to 6 become
rows of the table as their packages appear. `TestRootHasNoGoFiles` keeps
the root free of Go files (section 2).

## 5. Configuration

*Today:* `Config` in `config.go` is both the file and the running values,
with pointers for "unset", `effective` applies the defaults and `~`
expansion, and `ConfigStore` (`configstore.go`) holds the live copy. The
split described here comes in R5 (the `config` package), R6 (Boot and Live,
a pure `Resolve`, the field table) and R18 (pointer-free `Settings`).

### 5.1 The file

`config.File` is the `config.json` schema, and its JSON tags are the wire
format. Pointers remain only where "unset" means something different from the
zero value: `allow_any_dm`, `queue.notify`, `debounce.quiet_ms`,
`debounce.max_wait_ms`, `dashboard.require_token`, `backup.keep_versions_days`
and `backup.include_bridge_config`. The section types (`SandboxFile`,
`QueueFile`, `DebounceFile`, `IngestFile`, `StartupChecksFile`, `BackupFile`,
`PlaudFile`, `DashboardFile`) and `BrainRef{Path, Approved *manifest.Manifest}`
live in `config`. `config` imports only leaves, so nothing it needs can import
it back.

`Decode` reads the file once into raw JSON. It first applies the retired-keys
table (`config/retired.go`):

| Key | Handling |
| --- | --- |
| `model` | Copied into `brain_model` when that is empty. |
| `brain_mode` | Ignored. A value of `false` logs that classic mode was removed. |
| `work_dir` | Ignored. |
| `brains` | Ignored when empty. A non-empty list stops the bridge with a pointer to the migration in docs/BRAINS.md. |

`File` has no fields for these keys, so the next save drops them. The `.bak`
copy keeps the previous file. Unknown keys are logged once.

### 5.2 Environment

`config.Open(path, paths, env)` takes `env` as a lookup function. `cmd`
passes `os.LookupEnv` and tests pass a map. Only the `FRIDOLIN_`-prefixed
overrides that survived the legacy removal are read. They are applied during
resolve and reported as `env_locked`, which the dashboard shows as a lock on
the field. No `.env` file is loaded and the process environment is never
modified.

### 5.3 Defaults, resolve, validate

`config/defaults.go` is the only place a default value is written. `Resolve`
starts from that table and overlays the file. No consumer applies a default a
second time, so there are no `boolOr`/`intOr` fallbacks and no chunk-size or
media-dir fallbacks in other packages.

`Resolve(file, paths, env) (*Settings, error)` is pure. `config.Paths{Home,
DefaultDataDir}` is computed once by `cmd/fridolin`. `DefaultDataDir` is where
the `// compat:` fallback to the legacy data dir lives, and it is the only
`Stat`. `Resolve` creates no directories. Startup calls `Boot.EnsureDirs()`
next to the instance lock, so `check`, `open`, `init` and every `Update` have
no file-system side effects.

Field validation is done by `config`: effort, queue mode, git mode, loopback
`http_listen`, and the backup and Plaud sections. It returns the first error,
with the same text as before the restructure. *Planned:* report every field
problem at once as `errors.Join` of field errors. Validating the brain set
needs the disk and the other brains, so `registry` does it with `route`.

### 5.4 Settings: Boot and Live

`Settings` is immutable, contains no pointers and has every default filled in:
`time.Duration` instead of `*_ms` integers, expanded paths, and typed enums.

- **`Boot`** holds `ConfigPath`, `Home`, `DataDir`, `DBPath`, `MediaDir`,
  `HTTPListen`, `LogLevel` and `DashboardDisabled`. It is captured when the
  store is opened and is passed by value. Every path derived from these fields
  comes from `Boot` through methods: `TokenPath`, `LockPath`, `BackupDir`,
  `RcloneConf`, `GoogleClientPath`, `PlaudDir`, `OTPDir`, `RunsDir` and the
  media inbox. This includes the sandbox's data-dir deny and media allow, and
  the OAuth redirect URI. Editing a Boot field in the dashboard writes
  `config.json` and is reported in `restart_required`. Nothing switches to
  the new value before the restart.
- **`Live`** holds everything else: access lists, triggers, Claude settings,
  sandbox options (including `credentials_dir`), OTP, viewer, queue, debounce,
  ingest, the script API, startup checks, backup, Plaud, the site,
  `require_token` and `brain_dirs`. A change applies to the next message,
  run, tick or request.

`config/fields.go` has one row for every JSON leaf key, with its Boot flag.
`RestartRequired(old, new)` reports the Boot rows whose resolved value changed,
in table order. Three tests keep the table honest:

- every leaf key of `File` has a row (checked by reflection over the tags);
- every key is documented in `docs/CONFIGURATION.md`;
- the Boot rows match the fields of `Boot`.

`GET /api/config` still returns `{etag, path, config, effective,
env_locked}`. `effective` is `EffectiveFile(settings)`, the resolved values
written back in the `File` shape, so the dashboard JavaScript sees the same
JSON as before.

### 5.5 The store

`config.Store` holds one atomic snapshot `{File, Settings, ETag, EnvLocked}`,
so a reader never mixes two versions. Its API:

- `Get() *Settings` is a lock-free read. The result is shared; treat it as
  read-only.
- `Boot() Boot` returns the value captured at open.
- `Snapshot()` returns everything `GET /api/config` needs at once.
- `Update(etag, mutate)` copies the file layer, applies `mutate`, resolves and
  validates, then writes atomically (mode 0600, the previous version kept as
  `.bak`) and swaps the snapshot. It returns the `restart_required` list. An
  etag that does not match the current file returns `ErrConflict`; `""` skips
  the check. The mutex is released with `defer`.
- `Subscribe(fn)` registers a function that runs after every successful
  update, after the mutex is released.

*Planned:* `Update(etag, func(*Tx) error)`. The mutate step will be pure, and
side effects such as writing `brain.json` will be registered with
`tx.AfterCommit` and run in commit order after `config.json` is written, so a
failure shows up as drift and never as a wider routing. The same change adds
a check against the file on disk for external edits, panic recovery, and
recursive unknown-key warnings.

### 5.6 How packages get their settings

`app/project.go` holds one pure function per consumer, each tested against
`testdata/config-v1.json`:

| Consumer | Receives | Source |
| --- | --- | --- |
| `brain.Manager` | `Apply([]manifest.Spec, brain.Globals)` | `Live`: Claude path, model, effort, disallowed tools, skip-permissions, run timeout, the number of runs at once, OTP, viewer, queue. Each brain's manifest overrides are merged inside `brain`. |
| `sandbox.Profiler` | `Update(sandbox.Policy)` | `Boot`: home, data, DB and media paths. `Live`: sandbox section, credentials dir, `plaud.brain_access`. Plus the brain folders. |
| `access` (via `whatsapp`, dev API, `check`) | `func() access.Policy` | `Live`: allowed numbers and groups, `group_members`, `allow_any_dm`, triggers. |
| `pipeline` | `func() pipeline.Settings` | `Live`: debounce timings, `queue.notify`, `queue.mode`, first trigger (for `!help`). |
| `outbound` | `func() int` (max chunk) | `Live`. |
| `whatsapp` | `Options` | `Boot`: log level, media inbox. |
| `backup` | `func() backup.Settings`, data dir, targets, bridge files | `Live` backup section; `Boot.BackupDir`, `RcloneConf`. |
| `google` | `Options` | `Boot`: client path, redirect URI. `Live`: credentials dir (a getter). |
| `plaud` | `func() plaud.Settings`, dirs, home | `Live` Plaud section; `Boot.PlaudDir`, `Home`. |
| `httpapi` | `Boot`, `RequireToken func() bool`, `IngestEnabled func() bool`, the script API settings | |
| `otp.Handoff` | `Dir` | `Boot.OTPDir()`. |

### 5.7 Reacting to changes

There is one subscriber, registered by `app`, and it is level-triggered. It
takes its own mutex, reads `Store.Get()` and resolves the brain set with
`registry.Resolve`. It then calls `brain.Manager.Apply`, which hands the new
folder map to the sandbox policy (through `brain.Options.SetDirs`) before the
new set becomes visible, as the profile update always has. Finally it stores
the resolution issues for the dashboard and seeds the activity feed for new
brains. Because it always reads the newest snapshot, notifications that
arrive out of order cannot apply an older config. Every other component reads
its getter when it does its work.

## 6. Lifecycle

*Today:* `runCmd` in `main.go` builds the `Bridge` (`bridge.go`), which
takes the lock in `NewBridge` and whose `Start` and `Stop` run the
components in a fixed order, and `POST /api/restart` stops the bridge and
calls `platform.Exec` from its handler. The supervision described here
comes in R17; the order it stops things in is today's.

### Startup

1. `cmd/fridolin` parses arguments. A leading flag means `run`. It resolves
   the config path from `--config`, then `FRIDOLIN_CONFIG`, then
   `<default data dir>/config.json`. It writes a starter config if none
   exists and opens the store.
2. It calls `boot.EnsureDirs()` (mode 0700) and
   `platform.AcquireLock(boot.DataDir)`. `main` owns the lock value.
3. `app.New(app.Options{...})` does all the wiring and starts no goroutines.
   It opens `bridge.db`, builds the brain manager (first `Apply`), outbound,
   the pipeline, the transport (`whatsapp` or `devchat`), backup, Google,
   Plaud, the site, the activity feed, the dashboard token and the HTTP
   handler, then registers the config subscriber. If anything fails, `New`
   closes whatever it already opened.
4. `app.Run(ctx)` binds the HTTP listener first. A taken port is a fatal error
   that says another bridge is probably running. Then it starts the
   supervisor.

### Components

A component is `Run(ctx context.Context) error`. It returns when its work is
done or `ctx` is cancelled, and only after every goroutine it started has
exited. It returns an error only for conditions that should stop the bridge;
transient failures are logged and retried.

| Component | Run does | On cancel |
| --- | --- | --- |
| `httpapi.Server` | Serves the handler on the bound listener. | Shuts down with a 5 s budget, then closes. |
| `whatsapp.Client` | Connect loop with backoff from 5 s to 2 min; returns once connected. | Stops retrying. |
| Startup checks (`app`, today `health.go`) | Watches `startup_checks.required_files` and, with `notify_whatsapp`, sends an alert through the outbox when the set of missing files changes. | Returns. |
| `backup.Manager` | Minute ticker for the daily schedule. | Cancels a running job or connect flow and waits for rclone to exit. |
| `plaud.Manager` | Poller. | Cancels and waits for an in-flight CLI call. |
| The site manager | Supervises the site process and its deploys. | Stops the process. |
| Stop-only steps (`app`) | Nothing. | Debouncer drop, `brain.Manager.Stop`, the outbox, transport disconnect, store close. |

### Supervision and shutdown order

`app.Run` creates a root context with `context.WithCancelCause` and runs the
components in *layers*. Each layer has its own context derived from
`context.WithoutCancel(root)` and its own `errgroup`. All layers start at
once, since construction already happened in `New`. When the root context is
done or a component fails, the layers are cancelled one at a time in this
order, which is today's `Bridge.Stop`, and each is waited for, up to a
per-layer timeout, before the next:

1. HTTP server.
2. Background work: backup, Plaud, the site, startup checks, the WhatsApp
   connect loop.
3. Pipeline: drop open debounce batches.
4. Brains: stop every run and drop the queues.
5. Outbox: stop sending; what has not gone out stays in `bridge.db`.
6. Transport: disconnect.
7. Store: close `bridge.db`, once, last.

`Run` returns `ErrRestart` when `context.Cause(root)` is `ErrRestart`,
otherwise the first component error, otherwise nil. Teardown runs once,
guarded by `sync.Once`.

*Planned:* stop intake first, flush the debouncer into the brain queues, let
in-flight runs finish and deliver within a grace period while WhatsApp is
still connected, and persist never-started messages in an `inbound_queue`
table that is replayed at startup. This only changes the layer list in `app`
and the stop handlers of `pipeline` and `brain`.

### Restart

`POST /api/restart` answers 202 and calls `app.RequestRestart`, which is
`cancel(ErrRestart)`. The handler does nothing else. The server's shutdown
lets the response finish. `main` sees `ErrRestart`, releases the lock and
calls `platform.Exec` with the same executable, arguments and environment. If
exec fails, the process exits 1 and the service manager starts it again.
Signals, restart requests and fatal errors all go through the same shutdown
path.

### Signals

`signal.NotifyContext` cancels the root context on SIGINT or SIGTERM.
*Planned:* a second signal kills the process immediately. Until then, later
signals are ignored while shutdown runs.

## 7. Inbound pipeline

The steps a message goes through, with the package that owns each one:

1. **Transport** (`whatsapp`). whatsmeow delivers events in order to the event
   handler.
   - Poll votes are decrypted, matched by SHA-256 of the option names against
     the stored poll and saved.
   - Every other message goes through the pure `envelope` function. It drops
     Status updates and newsletters, turns broadcast lists into the sender's
     DM, and extracts chat, sender, text, caption, attachment kind (GIFs do
     not count as files), mentions-me, forwarded and the structured quote.
   - `access.Decide` then gives a verdict. A message that is not allowed is
     recorded as an ignored chat, without its text.
   - For an allowed message, the attachment is downloaded into the media
     folder's `.incoming/` with the size cap, and the adapter calls
     `pipeline.Handle` on a new goroutine. The brain that takes the message
     moves its files into its own inbox.
   - The other sources build the same `chat.Inbound`. The dev API builds an
     `Envelope` and uses the same `access.Decide`. `/ingest`, Plaud and the
     site call `pipeline.Ingest`, which sets `Inbound.Brain` explicitly.
   - *Planned:* a per-chat ordered intake lane instead of a goroutine per
     message; the group-name lookup moved off the event path, which today
     still asks the group cache before the allow check; the download moved
     after routing; edits and revokes as envelope kinds.
2. **Access** (`access`, pure). It applies the DM allow list and
   `allow_any_dm`, the allowed groups, the group-sender policy and the
   start/word/anywhere triggers. A trigger in the text or caption, a
   non-GIF file, an @-mention or a forward tags the message. Untagged group
   text is *silent*: nothing is ever sent back for it. Triggers are stripped
   from the text.
3. **Intake gate** (`pipeline.Handle`):
   - It trims the text.
   - Chat commands (`!help`, `!stop`, `!reset`) are parsed from the raw text,
     act on the sending chat only, and are accepted only from addressed
     messages, never from `/ingest`. Replies use `outbound.Notice`.
   - Empty messages are dropped, and so is silent junk (a bare emoji, "ok").
   - The brain is `Inbound.Brain` when set, otherwise the routing table's
     owner of the chat. With no brain, the sender gets a notice unless the
     message is silent or came from `/ingest`.
   - The OTP gate checks the raw text of messages without media. A caught
     code goes to `otp.Handoff` and never reaches history or the model.
   - Messages go into the debouncer, keyed by brain, chat and sender.
4. **Debounce** (`pipeline.Debouncer`). It waits for a quiet period and never
   longer than the maximum wait; both are read live. A flush calls
   `brain.Manager.Submit`. A full queue or a "queued (#n)" position becomes a
   notice for messages that are not silent.
5. **Brain** (`brain`).
   - `Submit` appends the inbound line to history and applies the queue
     policy: each chat's messages wait in a lane of their own and the chats
     take turns, work addressed to the brain goes before untagged chatter,
     a newer message from a chat supersedes that chat's queued silent
     messages, a full queue drops silent entries first, and interrupt mode
     works as defined by the queue rules. Silent messages get a run only in
     a brain whose `brain.json` says `observe: run`, and then an
     observe-only one. A run waits for one of the shared run slots.
   - The worker snapshots the settings, reads the system prompt, recent
     history and identity map through the brain's root, renders the prompt and
     calls `Runner.Run`.
   - Ten seconds after the process starts, `Sink.Working` fires, unless the
     message is silent. A forwarded AskUserQuestion goes to `Sink.Question`.
   - The finished run goes to `Sink.Finished`.
6. **Delivery** (`pipeline` and `outbound`).
   - An interrupted run sends nothing.
   - An error sends a notice unless the message was silent.
   - Nothing is sent when a question was already forwarded, the text is empty
     or the message was silent.
   - Otherwise `outbound.Reply` queues each `[POLL]` block as a real poll
     (falling back to a numbered text list) and the rest as paced chunks in
     the outbox; the brain's history gets the outbound line once WhatsApp
     took them.
7. **Side channels**. History lines feed the activity feed through
   `Sink.History`. `/send` goes through `pipeline.SendExternal`, which sends
   chunked text and appends an outbound line to the history of the brain that
   owns the chat, so the OTP guard sees code requests that scripts send.

Known gaps move unchanged and are listed in section 14. Interrupt mode is
still today's behaviour.

## 8. Outbound

`outbound.Sender` sits on top of a two-method `Transport`:
`SendMessage(ctx, jid, text)` and `SendPoll(ctx, jid, question, options, max)`.
Both `whatsapp.Client` and `devchat.Messenger` implement it.

- `Notice(ctx, jid, text)` sends one message.
- `Text(ctx, jid, text)` sends chunks, paced 300 ms apart on WhatsApp and not
  paced in dev mode.
- `Reply(ctx, jid, text)` sends the polls first, then the rest as chunked
  text.

A brain's replies, and the questions it asks mid-run, go through the
durable outbox (today `outbox.go`): they wait in `bridge.db` until WhatsApp
took every message of them, are tried again with backoff (at once when
WhatsApp reconnects, after a restart too), and go into the brain's history
only once delivered, as what the chat got. One that cannot go out within an
hour is given up on, and the dashboard says so. `Chunk` splits by
paragraph, then sentence, then whole characters. It never loses text and
never produces invalid UTF-8, and it has a fuzz target. Every chat-facing
string the pipeline sends lives in `outbound/messages.go`.

## 9. HTTP

### Assembly

`httpapi.New(Deps)` returns
`securityHeaders(hostGuard(hosts, originGuard(hosts, mux)))`, with `/api/`
wrapped in `apiGuard`. `hosts` is the allow list built from the bound address.
`httpapi.Server.Run(ctx, ln)` serves it, with `ReadHeaderTimeout` set to 10 s.
Today these guards are in `dashboard.go` and the handler is
`Bridge.newHTTPHandler`.

The guards:

- `hostGuard` returns 421 when the Host header is not the bound address,
  which stops DNS rebinding.
- `originGuard` checks `Origin` and `Sec-Fetch-Site` on non-GET requests and
  returns 403 for a cross-site request.
- `apiGuard`:
  - requires `X-Bridge-Token` (constant-time compare) or a login session
    while `dashboard.require_token` is on, which is read live;
  - on writes, requires `X-Bridge-Dashboard: 1` and a JSON content type;
  - caps the body at 1 MiB;
  - sets `Cache-Control: no-store`.
- `securityHeaders` sets a strict CSP, `nosniff`, `no-referrer`,
  `X-Frame-Options: DENY` and the cross-origin policies.

### Route table

`routes.go` is the single list of method, pattern, class and handler.
Today `routeInventory` in `cmd/fridolin/routes_test.go` lists them and
checks that no route is missing. A table test checks the class of every
route: each `/api` route rejects a missing token, each write rejects a
missing `X-Bridge-Dashboard` header or a form body, and no GET response
contains the token, the OAuth client secret or a refresh token.

- Public: `GET /health` and `GET /ready`.
- Script class (outside `apiGuard`): `POST /send` and `POST /ingest`, with
  the scripts token, a run token or, for `/send` only, none.
- Brain-run class: `/brain/`, with the token of a run in progress.
- Builder class: `/builder/`, the site builder's calls, with a builder
  brain's own token.
- Login class: how `fridolin open` signs the browser in, with a one-time
  code, no token.
- `GET /` serves static files from `web.FS`. When the query carries `state`
  plus `code` or `error`, it is the Google OAuth callback, which needs no
  token because the state is single-use.
- API class: status, WhatsApp pairing and groups, activity, logs, chats,
  config, restart, brains, folder picking and browsing, backup, Google
  client and calendars, Plaud, the site, and the dev endpoints, which
  exist only in dev mode. An unknown `/api/` path gets a 404, and a known
  one asked with the wrong method a 405 with an `Allow` header.

### Handlers

There is one unexported handler type per resource: `statusAPI`,
`whatsappAPI`, `activityAPI`, `chatsAPI`, `configAPI`, `brainsAPI`, `fsAPI`,
`backupAPI`, `googleAPI`, `plaudAPI`, `siteAPI`, `devAPI`, `scriptAPI` and
`brainAPI`. Each holds only the dependencies it uses, as small interfaces
declared next to it and listing exactly the methods it calls. A handler
decodes the request, calls one service method and encodes the result.
Workflows live in the services: brain changes in `registry`, the backup job
lifecycle in `backup`, calendar status in `google`.

### Responses and errors

Snapshot types that the dashboard already reads keep their JSON tags and are
encoded as before: `brain.Runtime`, `registry.Issue`, `backup.Summary`,
`store.IgnoredChat`, `activity.Entry`, `logx.Line` and `devchat.Message`.
Golden tests pin them. Responses built ad hoc and every new endpoint use
structs in `httpapi/dto.go`. *Planned:* move the remaining tags into DTOs as
part of the dashboard UI restructure.

`/api` errors are `{"error": message}`. During the restructure every handler
keeps today's status code. *Planned:* one `statusFor(err)` mapping from
`apperr` kinds:

| Error | Status |
| --- | --- |
| Not found | 404 |
| Invalid | 422 |
| Conflict or busy | 409 |
| Precondition | 412 |
| Unavailable | 503 |
| Body too large | 413 |
| Bad JSON | 400 |
| Anything else | 500 |

The same planned work makes decoding strict everywhere (unknown fields
rejected, exactly one JSON value; some handlers do this already) and
requires an etag on `PUT /api/config`, after a test checks every request
body the dashboard sends against the request structs.

### The script API

- `/send` decodes `{jid, text}` and calls
  `pipeline.SendExternal(ctx, principal, jid, text)`. It answers 200
  `{"status":"sent"}`.
- `/ingest` checks `ingest.enabled`, decodes the request (`scope` is accepted
  as an alias for `brain`) and confines each path with
  `media.Inbox.Resolve`. It then calls `pipeline.Ingest` and answers 202
  `{"status":"accepted","jid":...}`.

Errors are plain text with today's codes. Both handlers first call
`scriptAPI.authorize(r)`, which reads the caller's token (today
`scripttoken.go`): the scripts token in `<data_dir>/scripts.token` makes the
caller a script, a run token a brain, which may send only to its own chats
and hand files only to itself; without a token a caller may only `/send`,
to the allowed chats, while `script_api.tokenless_send` is on.

## 10. Persistence

| What | Where | Rules |
| --- | --- | --- |
| WhatsApp device and Fridolin tables | `bridge.db` (`db_path`, default `<data_dir>/bridge.db`) | `internal/store`: mode 0600. App migrations are versioned in `fridolin_version` and are additive only; version 1 is the original `CREATE TABLE IF NOT EXISTS` set. Never `PRAGMA user_version`, which whatsmeow's own versioning must not collide with. Journal mode unchanged. *Planned (R19):* one `*sql.DB` with `_foreign_keys=on`, built with `sqlstore.NewWithDB` plus `Container.Upgrade`, closed once, last. |
| Outbox | `bridge.db`, table `outbox` | `outbound` with `store`: a reply stays until WhatsApp took it, or an hour passed. |
| Configuration | `config.json` | `config.Store`: atomic write, 0600, `.bak` of the previous version. |
| Brain manifest | `<brain>/brain.json` | `registry` writes it, through `manifest.Write` on the brain's root. The approved copy in `config.json` is what runs; a difference on disk is reported as drift until approved. |
| Conversation history | `<brain>/conversation-history.jsonl` | `history`: append only, 0600, through the brain's root. |
| Run log | `<brain>/claude-stream.log` | Written by the run's parse callbacks, through the brain's root, and rotated. |
| Run records | `<data_dir>/runs/` | `brain`: one file per run while it lasts, so that a bridge that crashed stops what is left of its runs when it starts again. |
| Dashboard and scripts tokens | `<data_dir>/dashboard.token`, `<data_dir>/scripts.token` | `httpapi`: 24 random bytes, hex, 0600. |
| Dashboard login sessions | `<data_dir>/dashboard-sessions.json` | `httpapi`. |
| Instance lock | `<data_dir>/bridge.lock` | `platform.Lock`, flock. |
| Backup state, filters, staging | `<data_dir>/backup/` | `backup`. |
| rclone config (holds the Drive token) | `<data_dir>/rclone/rclone.conf` | `backup`; never logged, never backed up. |
| Google OAuth client | `<data_dir>/google/oauth_client.json` | `google`. |
| Calendar credentials | `credentials_dir` (default `data_dir`) | `google`; the sandbox denies them to every brain, since the bridge makes the calendar calls for runs. |
| OTP hand-off | `<data_dir>/otp/<brain>.json` | `otp.Handoff`, 0600. |
| Plaud state and private CLI home | `<data_dir>/plaud/` | `plaud`. |
| The site: its process, deploys and secrets | `<data_dir>/site/` | the site manager (docs/SITE.md). |
| Brain state: Claude config dir, temp files, caches | `<data_dir>/brains/<brain>/` | the sandbox's child environment (`childenv.go` today): created as real folders before a run, and the only places outside its folder and inbox a sandboxed brain may write. Left behind when a brain is removed. |
| Media folder and inboxes | `media_dir` (default `<data_dir>/media`): `.incoming/`, `<brain>/`, and `.old/` for what older versions left | `media`: a brain reads and writes only its own inbox. |

All writes the bridge makes outside SQLite go through `fsx.WriteFileAtomic`
or an `O_EXCL` create. Directories are 0700 and files 0600. The bridge runs
outside the sandbox, so any file it opens inside a brain folder is opened
through that brain's `os.Root`. A brain cannot redirect those reads or writes
with a symlink.

## 11. Sandbox and child processes

### The sandbox

The profile allows reading by default and denies writing everywhere. The
sandbox applies the last rule that matches, so its sections run from the
weakest to the strongest: the write deny (but for a few devices); soft read
denies (private home folders, your Claude Code setup, `deny_paths`); the
brain's own folder (read and write) and `allow_paths` (read); hard denies
(other brains' folders, the bridge's own files and the programs it runs, the
keychains, your instructions above the brain's folder); what the bridge
gives the brain inside those (its media inbox, its state, its runs' tokens
to read); what stays closed inside the brain's own places; and starting
code outside the sandbox (launchd, LaunchServices, Apple events, the
sockets of programs that run commands). A brain folder that is, holds or
lies inside a protected path is refused, by the dashboard, by
`ResolveBrains` and by every profile. This is containment, not a jail.

Paths are canonicalised and quoted for SBPL. `sandbox.Profile(Policy, id)`
is pure. `fridolin check --print-sandbox ID` prints the same profile a run
would use. A run is `sandbox-exec -p <profile> claude ...`. If the sandbox is
enabled but `sandbox-exec` is unavailable or the profile cannot be built, the
run fails with an error and no process starts.

### Claude runs

`brain` builds `claudecli.Args` and a `RunSpec`. The production `Runner` in
`app` wraps the command with `sandbox.Profiler` and calls `claudecli.Run`:

- The process leads a session of its own, so it has no controlling
  terminal and leads its own process group. `cmd.Cancel` kills the group
  and `WaitDelay` is 5 s.
- The prompt goes in on stdin. stdout is parsed line by line. stderr is kept
  for the error message, capped (`tailbuf.go`).
- A run is stopped at `run_timeout_minutes`, and five seconds after its
  result when something it started still holds its output open.
- Exit classification gives interrupted (the context was cancelled), CLI
  error, timed out, or no result. The error texts are the ones users see
  today.

The child environment is the base environment computed once in `app` (the
allowlist from the hardening phase, `childenv.go` today) plus the brain's
variables: its id, the binary and tools folder, the brain API and the run's
token file, and in the sandbox its own state (`prepareBrainState`). A brain
whose folder holds folders of the bridge's `PATH` gets them first on its
`PATH`; the bridge itself runs nothing from them (`brainpath.go`).
`Started(pid)` lets the brain show the PID and start the working-notice
timer.

*Planned:*

- one kill path: cancel the context, `cmd.Cancel` sends SIGTERM to the group,
  `WaitDelay` escalates, and the group is reaped after `Wait` before the run
  is marked finished;
- worker contexts derived from the manager's base context;
- closing the tmux viewer when its brain is retired.

All of these change `claudecli`, `brain` and `platform` only.

### Other child processes

rclone, the Plaud CLI, the site process, the folder picker and the Claude
version probe run with a context and a process group. They get explicit
environments: rclone has `RCLONE_*` stripped and its private config; Plaud
gets a private HOME and has telemetry disabled. The helpers the bridge runs
outside the sandbox (tmux, rclone, the Plaud CLI, node) are looked up only
in the folders of `PATH` that `helperbin.go` keeps. Each owning component
waits for them on shutdown.

## 12. Testing

### Layers

1. **Pure functions** are tested with tables, fuzz targets and golden files.
   This covers `access.Decide`, `whatsapp`'s envelope builder (on recorded
   `waE2E` protobuf fixtures), `route.Build`/`For`/`ReplyChat`, brain queue
   admission, `prompt.Build`, `claudecli.Parse` (on recorded stream-json),
   `outbound.Chunk` and `ExtractPolls`, `trigger`, `sandbox.Profile`,
   `config.Resolve`, the backup schedule, prune and filter generation, the
   Plaud parsers, and `otp`. Fuzz seeds and the failing inputs fuzzing found
   are committed under the package's `testdata/fuzz`, so they run as
   ordinary tests.
2. **Packages with their seams** run in process:
   - `brain` with an in-process fake `Runner` and a recording `Sink`;
   - `pipeline` with a real `brain.Manager`, a fake `Runner` and a recording
     `Transport`;
   - `backup` with a scripted `RunFunc`;
   - `google` against `httptest` endpoints set through its options;
   - `plaud` with fake functions;
   - `registry` and `config` on temp dirs with injected `Paths` and `Env`;
   - each `httpapi` handler type with fakes for its interfaces.

   Timing (debounce, the 10 s notice, stop waits, the backup scheduler, the
   Plaud interval, supervisor timeouts) is tested inside `testing/synctest`
   bubbles, so it runs instantly and deterministically. There is no `Clock`
   interface. Durations are `Options` fields with defaults, and pure
   functions take `now` as a parameter.
3. **Real processes** are used where they are the point:
   - `claudecli.Run` (process groups, a grandchild holding stdout, cancel);
   - `platform`'s lock in a second process, `Exec`, and killing a process
     group;
   - `sandbox-exec` tests on macOS (the own folder readable, another brain
     and the data dir denied, symlinked homes);
   - the real-rclone round trip where rclone is installed;
   - store tests that open the committed fixture of `bridge.db`.

   The test binary acts as the fake Claude or Plaud CLI. Each package that
   uses it has `func TestMain(m *testing.M) { testutil.Main(m) }`.
   `testutil.FakeClaude` refuses to work without it, because otherwise the
   child would run the whole test suite again. The scenario reaches the child
   through the run's explicit environment, never `t.Setenv`. A scenario can
   set stream lines, delays, stderr, the exit code, AskUserQuestion, a
   grandchild and an argv log. Today `cmd/fridolin`'s `TestMain`
   (`helpers_test.go`) does this.
4. **End to end** tests use `app/apptest` in dev mode:
   `apptest.New(t, cfg)` with `Do`, `Inject`, `Outbox`, `AddBrain`, `WaitFor`,
   `Home` and `Config`. They are external tests (`package app_test`), because
   `apptest` imports `app`. Golden JSON pins every GET endpoint, with
   timestamps, uptime, PIDs and temp paths masked. Contract tests pin `/send`
   and `/ingest`. Today this is `newHarness` in `cmd/fridolin`.
5. **Smoke tests**:
   - `TestSmoke` (`cmd/fridolin`, skipped with `-short`) builds the binary
     and runs it with `--no-whatsapp` on a temp config, data dir and free
     port, using a fake `claude` script; it checks `/health` and
     `/api/status`, sends a dev message and waits for the reply,
     `POST /api/restart`s and checks that the re-exec'd process serves
     again, and sends SIGTERM and expects exit 0 within 10 s;
   - `make smoke` (`scripts/smoke.sh`) does the same from outside with a
     race-enabled build stamped with a version, and checks that
     `fridolin version` prints the stamp.

### Rules

- Tests call `t.Parallel()` unless they cannot. Nothing sets `HOME`; paths
  are injected. Today's tests in `cmd/fridolin` still set `HOME` with
  `t.Setenv` where the code reads it.
- `testutil.Isolate` in every `TestMain` clears ambient variables, changes to
  a temp dir and refuses any data dir outside the temp area. Today
  `cmd/fridolin`'s `TestMain` does: the tests run in an empty temp
  directory with an empty `HOME`, and read the checkout through `repoDir`
  and the package's `testdata` through `pkgDir`.
- No test, smoke run or dev-mode session ever opens the live data dir. Checks
  against real data use copies.
- Goldens update with `-update`. A PR that changes a golden says why in its
  description.
- CI on macOS and Linux runs `gofmt`, `go vet` (also for Linux without cgo),
  `staticcheck` (U1000 catches dead code because names are unexported),
  archtest, `go test -race ./...`, the smoke tests, and `make root-check`.
  *Planned:* `-shuffle=on`, `brain` and `claudecli` with `-count=5`, and
  coverage printed per package.

## 13. Conventions

### Naming

- Package names are short, lower-case nouns that say what the package
  provides (`brain`, `route`, `history`). There are no `util`, `common` or
  `misc` packages. The `x` suffix (`fsx`, `textx`) marks a small extension of
  a standard-library area.
- Constructors: `New` for in-memory objects, `Open` for things backed by a
  file or database. Use an `Options` struct once there are more than three
  parameters; a zero field means the default.
- Long-lived components are named for what they manage: `brain.Manager`,
  `backup.Manager`, `plaud.Manager`, `whatsapp.Client`, `config.Store`,
  `google.Auth`.
- Unexported by default. Export only what another package uses, and delete
  what has no caller.
- Domain words follow the product: brain, chat, run, manifest, inbox, reply
  chat. "Bridge" remains the name of the running program in log lines,
  `bridge.db`, `bridge.lock` and `X-Bridge-Token`.

### Errors

- Wrap with `%w` and context (`"reading %s: %w"`). Messages are lower-case,
  have no trailing punctuation and tell the user what to do.
- Compare with `errors.Is` and `errors.As`, never by string or `==`.
- Conditions a caller acts on are `apperr` kinds or package sentinels built
  from them (`registry.ErrNotFound`, `backup.ErrBusy`, `config.ErrConflict`).
  Status codes are chosen only in `httpapi`.
- Text sent into chats lives in `outbound/messages.go`. *Planned:* stop
  posting raw Go errors into chats.
- Never put tokens, secrets or message text in errors, or in logs above debug
  level.

### Logging

- `log/slog`. `app` builds one logger: a text handler that keeps today's line
  shape, with the level from `Boot.LogLevel`, writing to stderr and to the
  dashboard ring. Components receive it as `Options.Logger`; nil means
  `slog.Default()`.
- Messages are short and lower-case. Identifiers go in attributes: `brain`,
  `chat`, `pid`, `err`.
- *Planned:* the move from `log.Printf` to `slog` happens in one PR after the
  restructure. `slog.SetDefault` keeps old call sites working during the move.

### Context

- `ctx context.Context` is the first parameter of anything that blocks, does
  I/O or starts work.
- A context is never stored in a struct, except a component's base context
  set by its own `Run`.
- `context.Background()` appears only in `main`, `app.Run`'s setup and tests.
- Work started by an HTTP request uses the request context when it must not
  outlive the request. Jobs that do outlive it (a backup, a restore, a sign-in
  flow) use the component's base context.

### Concurrency

- Every goroutine has an owner that waits for it.
- A mutex is never held while calling into another component, doing process
  or network I/O, or writing an HTTP response.
- Read-mostly shared state is an immutable snapshot behind `atomic.Pointer`:
  config, brain settings, the routing table.

### Comments

- Each package has a doc comment, in `doc.go` when it is long.
- Every exported identifier has a doc comment that explains why, not only
  what.
- Compatibility shims are tagged `// compat:` with the contract they keep.
  Security reasoning is written out next to the check it explains.

### Dependencies

Prefer the standard library. A new module needs a reason in the PR
description and a `govulncheck` run. Tools are pinned: `staticcheck` and
`actionlint` by version in the Makefile, which runs them with `go run`.
*Planned:* `tool` directives in `go.mod`, which Dependabot updates.

## 14. Where planned changes land

| Change | Packages |
| --- | --- |
| A single kill path, worker contexts from the manager's base context, closing the tmux viewer on retire (the run deadline, the reachable post-result grace, the capped stderr and the cleanup of orphaned runs at startup have landed) | `claudecli`, `brain`, `platform` |
| Transactional config updates (`Tx.AfterCommit`), external-edit detection, panic recovery, recursive unknown-key warnings, manifest version checks on edit and approve, delta-aware set validation | `config`, `registry`; additive fields in `httpapi` |
| Ordered shutdown with drain and an `inbound_queue` table; second signal kills | `app` (layer list), `pipeline`, `brain`, `store` (migration) |
| Central error mapping, strict decoding everywhere, required etag, server timeouts (dev routes only in dev mode, correct 405s and the removal of `GET /api/runs` have landed) | `httpapi` |
| Ordered per-chat intake, the group-name lookup off the event path, download after routing, edit and revoke handling | `whatsapp`, `pipeline`, `media`, `chat` |
| `log/slog` everywhere | `logx`, every package's `Options` |
| Dashboard UI restructure | `web/`; `httpapi` DTOs; a schema endpoint generated from `config/fields.go` |

These have landed in `cmd/fridolin` and move with their code: observe-only
runs for untagged group chatter (`observe.go`, `prompt.go`), the durable
outbox (`outbox.go`), capability tokens for `/send` and `/ingest` with the
brain API (`scripttoken.go`, `brainapi.go`), the Go-owned `history`,
`polls` and `calendar` subcommands (`toolcmd.go`, `*_cmd.go`), per-chat
lanes in a brain's queue and shared run slots (`brain.go`, `runslots.go`),
a real `!reset`, which writes a reset marker the next prompt stops at
(`history.go`), the group-member policy (`group_policy.go`), and the
sandbox's write deny by default with each brain's own media inbox and
state (`sandbox.go`, `media.go`, `childenv.go`).

### Configuration value extraction boundary

The extracted `config` package uses `manifest`, `trigger`, `fsx`, and the pure
`chat` phone normalization, `textx` validation and `jsonx` inspection helpers.
It imports no runtime manager, provider, transport or HTTP package and performs
no ambient environment/home lookup. `Paths{Home, DefaultDataDir}` and the platform
are passed to pure `Resolve`. The command probes the legacy default directory
when constructing inputs. `Open` accepts `Options`, reads only `BRAIN_MODE`
through the supplied environment callback, and retains paths/platform for saves.
Starter writers receive those inputs explicitly; `init` still writes its
requested starter file. `BrainRef.Dir(home)` expands file-layer paths explicitly.
`Store` retains a single immutable atomic snapshot, locked external-file reread and validation, ordered callbacks,
and manifest-first/config-last publication. A brain-set checker is supplied at
open and runs inside that lock before writes; without it, brain edits fail
closed. Registry workflows and bridge reconciliation stay in the command.
Private rollback state and its tests move together, preserving retained root
handles, writer identity checks, reverse rollback and deferred handle closure.

Configuration owns its saved provider/chunk defaults; direct provider and chunker
calls retain their defensive fallback constants. Composition tests compare those
contracts so a runtime package need not depend on the whole configuration schema.
The runtime's shortened timeout unit remains command-owned: `Config.RunMinutes`
provides the value and the command scales it exactly as before.
