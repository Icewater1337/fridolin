# Fridolin architecture

This is the implemented package layout and ownership model. User-facing
contracts are documented in [API.md](API.md), [CONFIGURATION.md](CONFIGURATION.md),
[BRAINS.md](BRAINS.md) and [COMPATIBILITY.md](COMPATIBILITY.md).

## Entry points and package ownership

`cmd/fridolin` contains the executable entry point and delegates to `internal/cli`.
`internal/app` constructs and owns the running application. The repository root
has no Go package; build with `go build -o bin/fridolin ./cmd/fridolin`.

| Packages | Responsibility |
| --- | --- |
| `cli`, `controlcli`, `toolcli` | Flags and signals; dashboard control commands; commands used by brain tools. |
| `app`, `app/apptest` | Runtime composition, policy projections, host adapters and an external application test harness. |
| `config`, `manifest`, `registry`, `route` | Configuration transactions, brain documents and discovery, and pure routing. |
| `brain`, `claudecli`, `prompt`, `sandbox` | Queues and runs, provider process protocol, prompt construction and sandbox policy. |
| `skill`, `audit` | Skill files: the frontmatter parser (a YAML subset, with a fuzz target), validation and the policy under `metadata.fridolin`; the audit log's one schema, appender and filtered reader. |
| `mcp` | The MCP server protocol (JSON-RPC 2.0 over stdio) and the tool registry with a per-caller availability predicate; `fridolin mcp --run` serves a run's tools with it. |
| `whatsapp`, `access`, `pipeline` | Transport events, authorization/classification and inbound delivery. |
| `outbound`, `otp`, `media` | Reply delivery, one-time-code handoff and attachment storage. |
| `backup`, `google`, `plaud`, `site` | Integration services, each with injected host capabilities. |
| `files`, `wiki` | What the Files and Wiki pages read ([FILES.md](FILES.md)): folders held to their root through `os.Root`, following no symlink and hiding what a reader may not see; Markdown parsed into a tree of a fixed set of elements (never HTML), and a brain's wiki indexed with its links, backlinks and search. `httpapi` serves both to the dashboard and, through the site bridge (`internal/app/site_bridge.go`), to the site. |
| `lease`, `coreexport`, `hostsecrets`, `secretxfer`, `tailnet` | The core lease in the vault bucket, the core's encrypted export and import, the host's secret store (macOS keychain, Windows Credential Manager, Linux files), the host secrets' move between machines over the tailnet, and the core's own tailnet node (tsnet behind an interface with a loopback fake) (docs/CORE.md). |
| `nodes`, `nodeproto`, `nodeagent` | The fleet ([NODES.md](NODES.md)): the core's node registry, pairing and node port; the versioned protocol both sides speak, pure; the `fridolin node` agent and its service, which also runs natively on Windows, and is the companion of FrIDE on its machine. The core's FrIDE side (placement, relaying news, moves, archives) is `internal/app` (`fride_runtime.go`, `mcp_fride.go`). |
| `httpapi` | HTTP route handlers, authentication, guards, response types and error mapping. |
| `store`, `history`, `activity` | SQLite persistence, append-only conversation history and bounded activity feed. |
| `vault`, `cloud`, `snapshot` | Tiered storage (docs/VAULT.md): the vault.db index, placement policy, upload, eviction, hydration, rebuild and brain scopes; the object-store interface with its rclone implementations (one process per call, and a long-lived `rclone rcd` that logs in to B2 once a day; remotes from environment variables only), a folder fake and the per-day count of B2 transactions by class; restic and a supervised Litestream, each run with an environment of its own. `internal/app` runs them as the vault service. |
| `housekeeping` | The housekeeping job's rules, pure but for the processes it lists and signals: which processes are the bridge's (a run's token in their environment, the vault's own paths in their command lines), which of those are orphaned, which are heavy, and stopping a tree after checking each PID is still the process it saw. `internal/app` (`housekeeping_runtime.go`) schedules it, adds viewer sessions, run records, `bridge.lock` and restic locks, asks the owner and reports. |
| `memindex`, `intent` | A brain's derived memory index (SQLite FTS5 with provenance) and its standing intents and scheduled jobs. |
| `fsx`, `platform`, `jsonx`, `textx`, `chat`, `trigger` | Filesystem/process boundaries and shared value operations; `trigger` holds keyword triggers and schedules. |
| `logx`, `version`, `apperr`, `testutil`, `archtest` | Logging, build identity, domain errors, test helpers and dependency checks. |
| `web`, `examples`, `site`, `container` at the repository root | Embedded dashboard, starter files, site runtime assets, and the core image's build files with the compose file `fridolin core start` runs. |

The package import allowlist lives in `internal/archtest/archtest_test.go`.
Changing a dependency requires updating that table in the same review. Runtime
packages accept their own settings and capability interfaces; application
projection functions translate global configuration for them. The global
configuration schema stays at composition, registry and configuration HTTP
boundaries. External test-package imports are checked separately from production
imports. Domain services do not import the executable.

## Configuration publication

`config.File` is the persisted JSON schema. Optional pointers distinguish omitted
values from explicitly configured zero or false. Migrations preserve the meaning
of older files and reject unsupported newer versions.

`config.Resolve` takes explicit paths/platform inputs and derives
`config.Settings{Boot, Live}` without creating directories. Global runtime policy
uses resolved booleans, integers, durations and enums. Brain reference documents
retain optional inheritance fields; they are documents, not unresolved global
policy. `EffectiveFile` projects settings back to the compatible API JSON shape.

`Boot` identifies startup resources. Application readers retain that startup
snapshot while receiving current `Live` values. Dashboard authentication may
tighten during a run; live changes cannot silently remove the startup requirement.
Changes that need a restart remain visible through the restart inventory.

`config.Store` validates and writes a transaction before publishing an immutable
snapshot. Subscribers receive the old and new settings from that publication in
order. File and manifest mutation callbacks remain persisted-document operations.
Consumers must not mutate published settings. The field registry supplies shared
metadata; schema and documentation checks guard the JSON contract.

## Application lifetime

`app.New(Options)` acquires the instance lock and constructs services. Failed
construction releases acquired resources. Options provide configuration, optional
listener/restart capabilities, the dashboard log ring and lifecycle logger.
Construction does not begin outbound delivery before a listener can be bound.

`App.Run(ctx)` owns its cancellation cause and teardown. The supervisor binds the
HTTP listener before starting intake, resume and background work. Bind failures
and unexpected server failures reach the caller. Requests share the server
lifetime context. `App.Stop` cancels a running application and waits for it; a
stopped instance cannot be started again.

Shutdown runs once, in dependency order:

1. Close admission to inbound work.
2. Shut down HTTP within its grace period, then close remaining requests.
3. Stop backup, Plaud and site producers.
4. Persist held debounce messages and stop brains, retaining never-started
   WhatsApp work and interrupted-run information for replay.
5. Stop outbox delivery and OTP work.
6. Disconnect transport and join accepted intake before closing storage.
7. Close the shared database owner and release the instance lock.

Brain workers use bounded grace for active runs. Non-WhatsApp callers receive a
completion/drop result and may retry themselves. Persistent replay uses current
routing and expiry rules. The outbox records delivery progress so restart can
resume from the failed part without treating unsent output as delivered.

Restart requests return through the same lifecycle as cancellation, with
`app.ErrRestart`. The CLI performs process replacement after teardown. A second
shutdown signal force-stops supervised process groups. No HTTP handler executes
a replacement process while the old application's resources are still owned.

## Messages and child processes

Transport admission normalizes identities and checks access before expensive
work. Ordered intake and debounce feed the brain queues. Routing can retain a
held destination for a brain that cannot run, rather than redirecting its
messages into a different brain. Worker retirement transfers or persists pending
work through the owning manager.

Each brain has a queue and worker. Shared run slots bound concurrency across
brains. The runner seam isolates process execution from queue policy. Provider
stream parsing handles live questions, result events and oversized lines.
Deadlines and post-result cleanup terminate supervised process groups, including
children that keep output pipes open. Run records support startup cleanup after
a crash. Sandboxed runs receive projected paths and filtered child environments.

Outbound formatting, chunking, polls and delivery are separate from transport
intake. Delivery history is recorded after successful delivery. Observe-only runs
and OTP handoff preserve their explicit policy boundaries. Chat notice strings
are centralized in `internal/outbound/messages.go` and pinned by golden tests.

## HTTP boundary

`internal/httpapi` owns route-level behavior. Application adapters supply service
operations and snapshots. The route inventory and guard tests cover methods,
host checks, authentication and mutation requirements. Dashboard reads accept
the dashboard token or session; writes also require the dashboard header and
JSON content type. Brain and script APIs use their own authorization scopes.

JSON decoding has bounded bodies and explicit malformed/oversized-input errors.
Domain error kinds map to HTTP responses at the boundary. Static files have
content ETags, no directory listing, and a strict CSP. Security headers prevent
framing and cross-origin resource use. The dashboard uses DOM construction and
Trusted Types without permitting arbitrary HTML parsing.

The dashboard's modules live in `web/assets`: `app/` owns shared lifecycle and
resources, `pages/` owns routes, `components/` owns reusable views and `lib/` owns
pure helpers. Shared resource polling preserves edits and focus across refreshes.
Pages do not import one another. See `AGENTS.md` for frontend conventions.

## Persistence and compatibility

`store.Open` creates one `*sql.DB` shared by the whatsmeow container and Fridolin
queries. Foreign keys are enabled, the container upgrades its own schema, and
Fridolin applies ordered migrations tracked in `fridolin_version`. Fridolin does
not use `PRAGMA user_version` for its schema. The container-backed owner closes
once. Journal mode is preserved; this refactor does not switch existing databases
to WAL or change their locking policy.

The database and its sidecars contain private state. Tests use synthetic database
fixtures and scratch directories. Never delete or recreate the user's pairing
database to repair an upgrade. History and brain documents keep their existing
on-disk formats. File/config changes use atomic writing helpers and explicit
transaction boundaries. Compatibility names such as `bridge.db`, `X-Bridge-Token`
and documented environment aliases remain intentional.

## Logging

The CLI installs a `logx.TextHandler` through `slog.SetDefault`, writing to the
console and dashboard ring. Existing standard log calls use this same bridge.
Lines retain their timestamp/message shape; structured attributes are appended
using slog's text encoding. `app.Options.Logger` supplies lifecycle records to
an embedding host. If no dashboard ring is supplied, those lifecycle records
also reach the application's fallback ring. Other legacy package logs use the
process default. Transport-specific log thresholds are validated configuration.

The log ring and activity feed use bounded circular storage. Ordinary writes do
not copy all retained entries. Activity edits and startup seeding may linearize
the feed while preserving chronological reads and stable log cursors.

## Testing and development

Package tests cover policy, formats and injected adapters. Integration tests live
primarily in `internal/app`; existing white-box tests remain there alongside the
external `app/apptest` lifecycle harness. CLI tests live in `internal/cli`.
Characterization fixtures remain under `cmd/fridolin/testdata`; package-local
fixture links preserve those original bytes. A behavior change must be explicit
when updating a golden, rather than silently recording a refactor regression.

Tests use scratch HOME/data directories and fake providers. Native integration
tests include the site runtime when `site/node_modules` is installed. Linux and
macOS checks exercise platform-specific behavior. Full validation includes race
and shuffled tests, both vet targets, staticcheck, module tidy, schema and import
checks, and dev-mode smoke. `make check` adds script, Python and frontend checks.

Builds in a checkout do not update the installed service. `setup.sh` and service
installation do; use only an explicitly authorized deployment workflow. A scratch
`--no-whatsapp --config PATH` run is the development boundary. Keep the live data,
service and pairing state separate from refactoring validation.
