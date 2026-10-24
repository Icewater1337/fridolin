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
| `ghapp`, `ghapp/ghapptest` | The GitHub App client (docs/GITHUB.md): manifest, conversion, the app's JWT, installations and narrowed installation tokens, standard library only; and a fake of GitHub's API for the tests. `internal/app` (`github_runtime.go`) runs the flow and mints the runs' tokens. |
| `files`, `wiki` | What the Files and Wiki pages read ([FILES.md](FILES.md)): folders held to their root through `os.Root`, following no symlink and hiding what a reader may not see; Markdown parsed into a tree of a fixed set of elements (never HTML), and a brain's wiki indexed with its links, backlinks and search. `httpapi` serves both to the dashboard and, through the site bridge (`internal/app/site_bridge.go`), to the site. |
| `lease`, `coreexport`, `hostsecrets`, `secretxfer`, `tailnet` | The core lease in the vault bucket, the core's encrypted export and import, the host's secret store (macOS keychain, Windows Credential Manager, Linux files), the host secrets' move between machines over the tailnet, and the core's own tailnet node (tsnet behind an interface with a loopback fake) (docs/CORE.md). |
| `nodes`, `nodeproto`, `nodeagent` | The fleet ([NODES.md](NODES.md)): the core's node registry, pairing and node port; the versioned protocol both sides speak, pure; the `fridolin node` agent and its service, which also runs natively on Windows, and is the companion of FrIDE on its machine. The core's FrIDE side (placement, relaying news, moves, archives) is `internal/app` (`fride_runtime.go`, `mcp_fride.go`). |
| `httpapi` | HTTP route handlers, authentication, guards, response types and error mapping. |
| `store`, `history`, `activity` | SQLite persistence, append-only conversation history and bounded activity feed. |
| `vault`, `cloud`, `snapshot` | Tiered storage (docs/VAULT.md): the vault.db index, placement policy, upload, eviction, hydration, rebuild and brain scopes; the object-store interface with its rclone implementations (one process per call, and a long-lived `rclone rcd` that logs in to B2 once a day; remotes from environment variables only), a folder fake and the per-day count of B2 transactions by class; restic and a supervised Litestream, each run with an environment of its own. `internal/app` runs them as the vault service. |
| `startupcheck` | The required files of `startup_checks`: which are missing now (the dashboard's warnings), and the owner's alert once per change of what is missing. `internal/app` (`bridge_health.go`) projects the settings and delivers the alert through the outbox. |
| `housekeeping` | The housekeeping job's rules, pure but for the processes it lists and signals: which processes are the bridge's (a run's token in their environment, the vault's own paths in their command lines), which of those are orphaned, which are heavy, and stopping a tree after checking each PID is still the process it saw. `internal/app` (`housekeeping_runtime.go`) schedules it, adds viewer sessions, run records, `bridge.lock` and restic locks, asks the owner and reports. |
| `memindex`, `intent` | A brain's derived memory index (SQLite FTS5 with provenance) and its standing intents. |
| `sched` | The scheduled agents' own logic: when an agent falls due (jitter, missed runs), the message a run is handed, the built-in templates (`examples/schedules`), and the store of their state, run history and logs in the data dir. `internal/app` (`schedule_runtime.go`) starts the runs through the brains' queues or a node's exec; `housekeeping` holds the rule that keeps the history within its limits. |
| `fsx`, `platform`, `jsonx`, `textx`, `chat`, `trigger` | Filesystem/process boundaries and shared value operations; `trigger` holds keyword triggers and schedules (once, daily, weekly, every, cron). |
| `logx`, `version`, `apperr`, `testutil`, `archtest` | Logging, build identity, domain errors, test helpers and dependency checks. |
| `web`, `examples`, `site`, `container` at the repository root | Embedded dashboard, starter files, site runtime assets, and the core image's build files with the compose file `fridolin core start` runs. |

The package import allowlist lives in `internal/archtest/archtest_test.go`.
Changing a dependency requires updating that table in the same review. Runtime
packages accept their own settings and capability interfaces; application
projection functions translate global configuration for them. The global
configuration schema stays at composition, registry and configuration HTTP
boundaries. External test-package imports are checked separately from production
imports. Domain services do not import the executable.

## Moving `internal/app` into packages

`internal/app` still holds about 25,400 lines of production code (and 46,000
of tests), more than three times the next largest package. Agents have to
read it and its tests run long. The move into packages (#230) goes one
piece per pull request. Each piece gets its own package, its own
`Settings` and the capabilities it needs as functions or small interfaces.
`app` keeps a projection from `config.Settings` and an adapter that wires
the piece to the rest of the bridge. The goldens stay unchanged. The
package's own tests cover the moved code at least as well as before
(`cmd/fridolin/testdata/coverage-baseline.txt` for the files it lists).
The new package gets its row in the archtest table. White-box tests in
`app` that only use App methods stay where they are, so a move does not
touch them.

### What is in it (October 2026)

Lines are production code; the App state is the fields of `App` the piece
reads; the tests are those in `internal/app` that are about the piece.

| Piece | Files (lines) | App state | Tests in `internal/app` |
| --- | --- | --- | --- |
| Vault service | 23 files, 6,368: `vault_runtime` 1,089, `vault_targets` 818, `vault_snapshots` 757, `vault_backends` 472, `vault_migrate` 411, `mcp_vault` 272, `vault_encryption` 270, `vault_text` 258, `b2usage` 254, `vault_secrets` 243, `vault_layout`, `vault_target_secrets`, `vault_brain_backup`, `vault_forget`, `vault_retire`, `vault_migrate_text`, `vault_restic_crypt`, `vault_api_project`, `doctor_{vault,b2,targets,encryption,drive}` | `vault`, `targets`, `b2`, `b2Warned`, `nodes`, `brainMgr` | `vault_test`, `vault_targets_test`, `vault_brain_backup_test`, `vault_live_test`, `vault_retire_test`, `vault_migrate_test`, `vault_secrets_test`, `vault_target_secrets_test`, `doctor_{vault,b2,targets,encryption,drive}_test` |
| Brains, runs, sandbox, child processes | 21 files, 2,504: `sandbox_runtime` 608, `childenv` 261, `runner` 231, `runner_settings` 223, `brainpath` 181, `sandbox_try` 146, `brainreg` 118, `brain_manager` 112, `brain_hooks` 109, `runrecord*` 155, `brain_project`, `claudecli`, `helperbin`, `procstart_*`, `viewer_factory`, `brain_policy`, `template_assets` | the brain manager and its runner; free functions over the config and the layout | `brain_queue_test`, `brainreg_test`, `brainpath_test`, `childenv_test`, `claudecli_test`, `codexcli_test`, `runrecord_test`, `sandbox*_test`, `helperbin_test`, `brainwrite*_test` |
| Policy, approvals, audit, skills, MCP registry | 8 files, 2,072: `approvals` 731, `skills` 371, `mcp_tools` 300, `policy` 285, `mcp_delegate` 273, `audit` 95, `command_tokens`, `command_quote` | 54 App methods; `policy`, `audit`, `mcpTools`, `runTokens`, `delegations`, `waiters`, `outbox`, `store`, `brainMgr`, `work`, `agents`, `pipeline` | `policy_test`, `contracts_test`, `commands_test` |
| Composition, lifecycle, config, CLI entry points | 17 files, 1,802: `app` 771, `inspect` 370, `config_runtime` 133, `supervisor` 113, `project` 98, `run`, `stop`, `startup_cli`, `check_runtime`, `cli_runtime`, `config_path`, `layout_runtime`, small error and doc files | all of it | the harness, `lifecycle_external_test`, `supervisor_test`, `stop_test`, `inspect*_test`, `config*_test`, `cli_golden_test`, `run_golden_test`, `layout_test`, `project_test` |
| Scheduled agents and reminders | 4 files, 1,324: `schedule_runtime` 638, `schedules_api_project` 482, `timezone` 106, `reminder_files` 98 | `agents`, `store` | `schedules_test`, `schedule_files_test`, `timezone_test` |
| Workstation (agent sessions, terminals) | 6 files, 1,218: `workstation_runtime` 380, `workstation_terms` 267, `mcp_workstation_agent` 160, `mcp_workstation` 151, `mcp_workstation_term` 148, `workstation_dashboard` 112 | `work`, `nodes`, `nodesEnabled`, `outbox`, `audit`, `store` | `workstation_test`, `tmux_session_test` |
| HTTP and dashboard adapters | 14 files, 1,141: `dashboard_api` 379, `brainapi` 277, `server` 134, `scripttoken` 62, `dashboard` 46, the `*_api_project.go` adapters | 24 fields: the server's, the sessions', the tokens' | `dashboard_test`, `dashboard_api_test`, `brainapi_test`, `server_test`, `scripttoken_test`, `script_contract_test`, `routes_test`, `api_golden_test` |
| FrIDE, the core's side | 2 files, 1,126: `fride_runtime` 856, `mcp_fride` 270 | `fride`, `nodesEnabled`, `store` | `fride_test`, `fride_gate_test` |
| Chat intake, WhatsApp projection, owner commands | 20 files, 1,123: `owner_commands` 227, `inbound_store` 158, `identity` 120, `whatsapp_projection` 103, `intake`, `polls`, `group_policy`, `pipeline_project`, `whatsapp_outbound`, `otp_runtime`, small adapters | `wa`, `outbox`, `store`, `brainMgr`, `otp`, `sessions`, `ownerAsks` | `identity_test`, `group_policy_test`, `inbound*_test`, `media_test`, `observe_test`, `otp_test`, `owner_commands_test`, `polls_test`, `whatsapp_*_test`, `addressing_test` |
| Nodes runtime and programs | 4 files, 1,051: `doctor_nodes` 343, `node_programs` 329, `nodes_runtime` 208, `nodes_update` 171 | `nodes`, `nodesListen`, `programs`, `targets`, `work`, `fride`, `exe` | `nodes_test`, `node_programs_test`, `doctor_nodes_test`, `coremove_test` |
| Doctor, shared checks | 3 files, 1,018: `doctor` 863, `doctor_policy` 114, `doctor_drift` 41 | none: it works from the config, the data dir and the API | `doctor_test`, `doctor_policy_test`, `doctor_drift_test` |
| Memory and files | 6 files, 901: `files_runtime` 257, `memory_runtime` 224, `mcp_memory` 182, `memory_cmd` 153, `memory_api_project`, `filesystem_api_project` | `brainMgr`, `intents`, `wikis`, `fileTickets`, `registry`, `audit`, `nodes`, `programs` | `memory_test`, `brainfiles_test`, `files_*golden_test`, `files_write_test` |
| Housekeeping job | 4 files, 838: `housekeeping_runtime` 636, `housekeeping_text` 95, `doctor_housekeeping` 60, `housekeeping_nodes` 47 | `housekeeping`, `audit`, `outbox`, `store` | `housekeeping_test`, `doctor_housekeeping_test` |
| Core lease and tailnet | 2 files, 789: `core_runtime` 642, `doctor_core` 147 | `core`, `wa`, `listen`, `bind`, `b2`, `outbox`, `audit` | `core_runtime_test`, `core_lease_store_test`, `core_brain_test`, `corehost_test`, `tailnet_signin_test`, `doctor_core_test` |
| Site bridge and host | 8 files, 725: `site_runtime` 172, `site_bridge` 158, `site_host` 143, `site_build_log` 103, the site's API adapters, `site_chat_routing` | `site`, `siteBridge`, `plaud`, `brainMgr` | `site_test`, `site_build_test`, `site_builder_test`, `site_sandbox*_test` |
| GitHub App runtime | 3 files, 604: `github_runtime` 422, `github_secrets` 175, `github_api_project` | `github`, `logger` | `github_test` |
| Plaud host | 6 files, 465: `plaud_host` 163, `plaud_runtime` 124, `plaud_nodes` 82, `doctor_plaud` 73, the API adapters | `plaud`, `brainMgr` | `plaud_test`, `plaud_app_test`, `plaud_golden_test`, `plaud_nodes_test`, `plaud_runtime_test`, `plaud_fixture_test` |
| Backup and Google | 6 files, 323: `backup_runtime` 116, `backup_projection` 86, `backup_restore_target` 60, `google_project`, the API adapters | `backup`, `google`, `registry`, `brainMgr`, `wa` | `backup_test`, `backup_integration_test`, `google_test`, `calendar_cmd_test` |
| Required files (moved) | `bridge_health` 36: the projection and the two App methods left | `cfgs`, `outbox` | `health_test` |

### Order

Low coupling and low conflict first. Two kinds of piece come early: those
nobody else is changing, and the seams most features call (doctor checks,
MCP tool plumbing, owner approvals). Moving the seams first means a
feature can later move with its own `doctor_*.go` and `mcp_*.go`. Pieces
whose files #218/#223 (parallel app tests, injected env) or #228 (sandbox,
setup, lease and run-timeout tests) change wait until those merge.

| # | Piece | To | What it needs from `app` | Waits for |
| --- | --- | --- | --- | --- |
| 1 | Required files (`health.go`) | `startupcheck` | `Settings` per look; `Send(ctx, chat, text, maxChunk)` (the outbox) | done (#230's first PR) |
| 2 | Housekeeping's terminal text (`housekeeping_text.go`) | `housekeeping` | nothing: `internal/cli` calls it with the report's bytes | — |
| 3 | Doctor checks: `DoctorCheck`, `DoctorEnv`, `DoctorRegistry`, `DoctorResult`, the redactor (`doctor.go`, `doctor_policy.go`, `doctor_drift.go`) | `internal/doctor` | config options and paths; a registry each feature adds its checks to, so `doctor_{nodes,plaud,vault,…}.go` later move with their feature | — |
| 4 | MCP tool plumbing: `mcpCaller`, `schema`/`strProp`, `guarded` (`mcp_tools.go`) | `internal/mcp` (a `tools` part) | a `Caller` (core or sub-brain, the run), a `Guard` (lockdown, risk budget), an audit appender | — |
| 5 | Scheduled agents, reminders, timezone (`schedule_runtime.go`, `schedules_api_project.go`, `reminder_files.go`, `timezone.go`) | `sched` | `BrainQueue` (submit a run, learn it finished or was interrupted), `NodeExec` for a program on a node, the run policy and risk check, a clock | 3 |
| 6 | Owner approvals (`approvals.go`) | `internal/approvals` | `Poll`/`Text` delivery (the outbox), the approval store, audit, a clock; each feature registers its kind's decision function (housekeeping, workstation, schedules, nodes) | #218/#223 |
| 7 | GitHub App runtime (`github_runtime.go`, `github_secrets.go`) | `ghapp` (a runtime part) | a host secret store, the runs' token directory, a clock, a logger | #218/#223 |
| 8 | Plaud host (`plaud_*.go`, `doctor_plaud.go`) | `plaud` | `NodeReader` (the nodes and when they count as stale), the media inbox, brain updates, helper lookup | 3; #218/#223 |
| 9 | Site bridge and host (`site_*.go`) | `site` | `Messenger`, a brain runner (child environment, agent binary, brain state), the files API, reply routing | #218/#223 |
| 10 | Housekeeping runtime (`housekeeping_runtime.go`, `housekeeping_nodes.go`) | `housekeeping` | run records (list, is it ours), approvals (6), a notifier, the schedules' history prune (5), restic locks (vault), the workstation's orphans | 5, 6; #218/#223 |
| 11 | Workstation (`workstation_*.go`, `mcp_workstation*.go`) | `internal/workstation` | `NodeCaller`, approvals (6), the outbox, audit, the MCP plumbing (4) | 4, 6; #218/#223 |
| 12 | FrIDE's core side (`fride_runtime.go`, `mcp_fride.go`) | `internal/fridecore` | `NodeCaller`, the vault's views and tracking, the store, the MCP plumbing (4) | 4; #218/#223 |
| 13 | Nodes runtime and programs (`nodes_*.go`, `node_programs.go`, `doctor_nodes.go`) | `nodes` | the core's tailnet state and node listener, FrIDE's declare hook (12), workstation (11), the vault's target service, the program store | 3, 11, 12 |
| 14 | Memory and files (`memory_*.go`, `mcp_memory.go`, `files_runtime.go`) | `memindex`, `files` | the brain manager, the vault (sync, views, change), a run lookup, `mayRead` policy | 4; #218/#223 |
| 15 | Core lease and tailnet (`core_runtime.go`, `doctor_core.go`) | `lease`, `tailnet` | a WhatsApp gate (connect, send, pairing), the listener binding, B2 usage, the outbox, audit | 3; #228 |
| 16 | Child processes, runs, sandbox (`childenv.go`, `runner*.go`, `runrecord*.go`, `sandbox_*.go`, `procstart_*.go`) | `brain`, `sandbox`, `platform` | the environment (`Options.Env`), helper paths, the layout | #218/#223, #228 |
| 17 | Vault service, in steps: layout, secrets and encryption; B2 usage and the status text; backends, targets, snapshots and migration; its MCP tools last | `vault`, `cloud` | `NodeCaller` for node targets, the core state writer, brain states, doctor (3), the MCP plumbing (4) | 3, 4; #218/#223 |

What stays in `app` is the composition (`app.go`, `supervisor.go`,
`stop.go`, `run.go`), the configuration projections (`*_project.go`,
`*_projection.go`), the thin HTTP adapters and the wiring of chat intake.
That should come to about 5,000 lines.

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
The bridge reads its environment through `Options.Env` (nil is the process's)
and its home folder from the config store (`Boot.Home`): what its programs get
(`childEnv`), the variables it looks up, `~`. The binary passes no `Env` and
opens the config with the process's home; tests give each bridge both of its
own, so that bridges in parallel tests never share them.

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
tests include the site runtime when `site/node_modules` is installed. Linux runs
every test; macOS and Windows run the tests of what differs there
(`scripts/os-tests.sh`), and the whole macOS suite runs nightly. Full validation includes race
and shuffled tests, both vet targets, staticcheck, exhaustive, module tidy, schema and import
checks, and dev-mode smoke. `make check` adds script, Python and frontend checks.

Builds in a checkout do not update the installed service. `setup.sh` and service
installation do; use only an explicitly authorized deployment workflow. A scratch
`--no-whatsapp --config PATH` run is the development boundary. Keep the live data,
service and pairing state separate from refactoring validation.
