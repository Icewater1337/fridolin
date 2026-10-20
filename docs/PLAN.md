# fridolin — Implementation Plan

Oct 20, 2026. Living copy with diagrams: https://claude.ai/code/artifact/e5d565f8-618a-43b4-b036-067785e03fa3

This plan turns the 17 Oct architecture into work another session can execute: fridolin stays the name, becomes the core, and gains a trusted multi-OS fleet, a core brain over sub-brains, and tiered encrypted storage.

## The core idea

fridolin is one assistant that lives on a network of your own machines, remembers everything, and only ever has the files it needs in front of it.

- One trusted network. Every machine you own joins your tailnet and runs a small fridolin node agent. One of them hosts the core; the others offer capacity, files and devices. Joining is one command on macOS, Windows or Linux.
- One core, movable. The core is a container image plus one encrypted data volume. Moving it is stop here, sync, start there: the WhatsApp session, the brains, the memory index and the audit log travel together, and the new machine answers under the same name.
- One core brain, many sub-brains. The core brain is the only thing the outside world talks to. It answers, or hands work to a sub-brain (family, work, taxes, coding), each with its own prompt, memory, skills and vault paths. Delegation runs one way: the core may use a sub-brain you have granted it; a sub-brain can never reach the core or a sibling.
- One vault, two tiers. The complete vault lives encrypted in cloud object storage. The machine keeps a working set: the memory index, the markdown notes, and the files a running job needs. Recordings, archives and finished workspaces go cold; the model asks for a file and the vault brings it back.
- One policy. Risk levels and approval polls from the architecture doc apply unchanged: the model drafts, you submit.

In one line: a personal operating system for agents where the machine is replaceable, the data is yours and encrypted, and the brain that talks to you decides who does the work.

## Consistency with the 17 Oct architecture

The four points fit the plan; two of them flip decisions taken on 17 Oct, and both flips come from the same cause: Windows.

| Point | What it confirms | What it changes |
| --- | --- | --- |
| 1. Trusted multi-OS fleet, movable core | Tailnet-only networking; the mother node is a data directory, not a machine; export and import of the node | Windows rules out `sandbox-exec` and `bwrap` as the isolation story, so the core moves into a container now rather than later. The node agent lives in fridolin, so FrIDE's fleet mode becomes optional instead of required. |
| 2. Core brain over sub-brains | Brains are already isolated folders with their own prompt, memory and skills; a router and policy layer were planned | The router becomes the core brain's job, driven by a model with a closed set of actions, and brains get a hierarchy with explicit grants. |
| 3. Tiered storage | restic to B2 and a vault layout were decided | Backup becomes one function of a general tiered store: the cloud copy is the complete vault, the machine holds a cache. restic keeps the snapshots; day-to-day placement uses an object store and a manifest. |
| 4. OpenClaw | Planned as a Phase 5 study | Pulled forward: node pairing, the skill format, storage locations and sandbox backends shape Workstreams A to D. |

Unchanged: Tailscale only, Backblaze B2 as the cloud, model drafts and you submit, approval polls, FrIDE as the code executor, the name fridolin. Flipped: containers now; "mother node" is replaced by "core host" because any node may host the core.

## Current state of the code

The plan builds on fridolin `main` as of 20 Oct 2026, with the rework in: a restructured Go module with the kernel in `internal/app`, Claude or Codex as the per-brain runner, a hardened macOS sandbox, token-guarded APIs, and a test harness the implementing session must keep green.

Reused as it is:

- Layout: `cmd/fridolin` delegates to `internal/cli` (commands) and `internal/app` (the running bridge); `internal/config`, `manifest`, `registry` and `route` hold settings, brain manifests and routing; `internal/httpapi` serves API and dashboard; `internal/outbound` holds every message the bridge sends; `internal/archtest` enforces the package rules in `docs/ARCHITECTURE.md`. Go 1.26, SQLite through cgo.
- Runner: each message is one `claude -p` (default) or `codex exec` process, supervised, one queue and one worker per brain, with a persistent outbox, delivery recorded in history, and a graceful restart that keeps pending messages. Both providers accept MCP servers, which is what Workstream C builds on.
- Sandbox on macOS: a brain writes only its folder, its media inbox and its own state under `~/.fridolin/brains/<id>/` (a private Claude Code config folder, `TMPDIR`, caches); it cannot read the keychain, so brains authenticate with `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` in the service environment. Linux has no sandbox, the bridge refuses to start with one enabled there, and the README's answer is a dedicated user with `NoNewPrivileges`.
- APIs: `/send` and `/ingest` need a Bearer token; scripts use `~/.fridolin/scripts.token`, a brain's run gets a token of its own limited to its chats; `/health`, `/ready`; dashboard sign-in by one-time link and session cookie; `fridolin rotate-token`. The contract is `docs/API.md`.
- Access: `allowed_numbers`, `group_members` per group, a fallback brain, `reply_chat`, `observe`; the OTP guard; deleted and edited messages handled; Status and channels refused.
- Operations: `fridolin service install | status | restart | rollback` into `~/.local/lib/fridolin/` with `fridolin.prev` kept; `fridolin check`, `history`, `init`, `paths`; release archives for macOS and Linux only; `child_env_passthrough` controls what reaches brains.
- Integrations: Google Drive backup through rclone with a mass-deletion guard; Google Calendar per brain; Plaud through `@plaud-ai/cli` plus an Android app that pulls recordings off a NotePin and uploads through your site; an optional website (`site/`, Node and TypeScript) with Google or password sign-in and modules from a private repository.
- Testing: `make check` (gofmt, vet, race tests, staticcheck, shellcheck, ruff, ESLint, actionlint, pinned actions, notices, a dev-mode smoke test), golden files under `cmd/fridolin/testdata/golden`, config migrations (`currentConfigVersion`, `configMigrations`), JSON schemas held to the Go types, a coverage baseline, fuzz targets, CodeQL and govulncheck.

Not there yet, and the subject of this plan: a node agent and registry; a Windows build (three blockers, found in the tree at commit 245cc1b: \`github.com/mattn/go-sqlite3\` needs cgo; \`internal/platform\` is built with \`//go:build unix\` and \`unsupported.go\` stops any other build on purpose, because the bridge uses \`flock\`, process groups and a POSIX shell; and \`ci.yml\` builds only darwin and linux); container packaging; `role` and `grants` on brains; the vault and its index; restic and Litestream; a Telegram channel; the policy layer with risk levels. The rework makes several of these easier: token-scoped APIs let the node agent call `/ingest` as a script; `CLAUDE_CODE_OAUTH_TOKEN` in the environment means the container needs no interactive login; the per-brain state folders map one to one onto per-brain Unix users.

What the rework changes in the plan: M0's data-directory task uses the existing `configMigrations` mechanism instead of a new version field; M1 drops the interactive Claude login; every task that touches CLI arguments, prompts, sandbox profiles or dashboard routes updates the golden files and `routeInventory`; new packages obey `archtest`.

## Target architecture

*[Diagram: target architecture · core host, 3 sub-brains, 3 nodes, 2 storage tiers — see the living copy linked at the top]*

Channels enter from outside the tailnet and reach only the core; nodes hold outbound connections to it; the core brain alone has arrows to sub-brains; the vault service is the only thing that talks to B2, and the cold tier there is the complete copy. Any node can take the core's place by importing the hot tier and taking the lease.

## Workstream A — Trusted network and nodes

Every machine runs the same `fridolin` binary in node mode; the core is a lease that exactly one node holds at a time, and moving it is export, import, promote.

Network: the tailnet stays the trust boundary. The core binary embeds Tailscale (`tsnet`), so it is its own tailnet device named `fridolin-core` wherever it runs, inside a container or not, with no dependency on the host's Tailscale install. Node agents use the host's Tailscale when present, `tsnet` otherwise.

Node agent (`fridolin node`):

- Outbound WebSocket from the node to `fridolin-core` over the tailnet; the core listens on one port, nodes listen on none.
- Two-stage pairing, as OpenClaw does it: first the device is approved (`fridolin nodes approve <id>`, or the dashboard), then the command surface the node declares is approved separately; later expansions need approval again. Each node has a signed device keypair stored locally; the tailnet identity alone is not trusted.
- Capabilities a node can declare: `exec` (allowlisted commands only, platform defaults exclude shells), `fs` (read and write under granted roots), `host` (OS, CPU, memory, disk, battery), `notify`, `fride` (see Workstream E), later `camera`, `screen`, `clipboard`.
- Registry on the core: id, display name, OS, tags from `nodes.json` (`macos`, `windows`, `linux`, `docker`, `xcode`, `gpu`, memory in GB), capabilities, free resources, last seen, approval state. Shown on a Nodes page in the dashboard.
- Service install per OS through one Go service library (`kardianos/service`): launchd on macOS, systemd user unit on Linux, a Windows Service on Windows. `fridolin node install`, `start`, `stop`, `status`, `uninstall`.
- Join flow: on the core, `fridolin nodes join-code` prints a one-paste command with a single-use code; on the new machine, `fridolin node join <code>` pairs and installs the service.

Moving the core:

- `fridolin core export` writes an age-encrypted archive of the hot tier (config, brains, `bridge.db` with the WhatsApp session, secrets, audit, vault index) to the cold tier under `core/exports/<timestamp>`.
- `fridolin core import --latest` on the target node restores it into that node's core volume.
- `fridolin core promote` takes the core lease: an object in the vault bucket holding device id, hostname and time. The core renews it every minute; a core that cannot renew for 5 minutes stops its channels; a core that finds the lease held by another live device refuses to start. This is what keeps the WhatsApp session from running twice.
- The new core comes up under the same `fridolin-core` name; nodes reconnect to it without reconfiguration.

Platform matrix:

|  | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Node agent | native | native | native (Go, pure-Go SQLite) |
| Core | container (Docker Desktop or OrbStack) | container (Docker or Podman), or native | container (Docker Desktop on WSL2) |
| Service | launchd | systemd | Windows Service |
| Executors on the node | native, or per-run container | native, or per-run container | native only (container path mounting is not usable on Windows) |

## Workstream B — Containerized core

The core is one Linux image plus one named volume, and that pair is what moves between machines; Windows made this the simplest answer, and it gives macOS and Linux the same isolation story for free.

Image `fridolin-core`: the Go binary, the Claude CLI with its Node runtime, `ffmpeg`, `restic`, `rclone`, Python for `tools/`, and `bwrap`. Built in CI for amd64 and arm64, tagged with the fridolin version.

Volume `fridolin-data`: `FRIDOLIN_HOME`, the hot tier. A named volume, not a bind mount: on Docker Desktop (macOS and Windows) bind mounts are slow and have path and permission problems; a named volume lives inside the Docker VM and performs like a local disk. Nothing on the host needs to see inside it, because the cold tier in B2 is the complete copy.

What runs where on the core host:

| Process | Where | Why |
| --- | --- | --- |
| Core: channels, brains, router, policy, vault, dashboard, `tsnet` listener | container | portable, isolated, identical on every OS |
| Node agent | native on the host | needs host files, host commands, FrIDE stacks, later camera and screen |
| Per-brain runs (`claude -p`) | inside the container, each brain its own Unix user with a 0700 home, `bwrap` where the runtime allows it | replaces `sandbox-exec`; `sandbox.deny_paths` carries over as bind-mount denials |
| Optional later: per-brain sibling containers through the Docker socket | one container per brain | OpenClaw's `scope: agent`; stronger, heavier |

Start, stop, update: `fridolin core start` is a host-side wrapper that reads the age identity from the host keychain (macOS Keychain, Windows Credential Manager, `secret-service` or a 0600 file on Linux), passes it as an environment variable, and runs `docker compose up -d` with the pinned image. `fridolin core update` pulls the next image and restarts; the volume is untouched.

Inside the container:

- Authentication: `CLAUDE_CODE_OAUTH_TOKEN` (and a Codex sign-in per brain, when Codex mode is on) is passed into the container the same way as the age identity, as today it is passed to the service; the per-brain Claude Code config folders under `brains/<id>/` live in the volume and move with it. No interactive login inside the container.
- The WhatsApp session (`bridge.db`) lives in the volume; one bridge per volume stays the rule, and the core lease enforces it across machines.
- Secrets: `secrets/` is age-encrypted; the identity arrives only as an environment variable at start and is never written to the volume.
- Resource limits: memory limit from `config.json` (default 8 GB), one CPU set aside for channels so a runaway brain cannot starve WhatsApp.

Windows specifics: Docker Desktop with the WSL2 backend; the volume lives in the WSL2 VM; the native node agent runs as a Windows Service. If a Windows machine should only be a node and never host the core, it needs no Docker at all.

Plan B, kept open by the interfaces: a native core with build-tagged `sandbox_*`, `service_*` and `paths_*` files, as in the architecture doc. The `Sandbox` and `Service` interfaces are the same either way, so the decision can be revisited without touching the kernel.

## Workstream C — Core brain and sub-brains

One brain per instance has `role: core`; it is the only brain that gets a delegation tool, and it gets it only for the sub-brains you have granted.

Manifest changes in `brain.json`:

```json
{
  "id": "core",
  "role": "core",
  "grants": {
    "family": ["delegate", "read"],
    "taxes":  ["delegate", "read", "files"],
    "coding": ["delegate"]
  }
}
```

- `delegate`: the core may hand a bounded task to that sub-brain and receive its result.
- `read`: the core may read that sub-brain's `memories/**/*.md` and skills.
- `files`: the core may fetch files from that sub-brain's vault paths.
- Sub-brains have no `grants` field; the loader rejects one. Depth is one: a delegated run never gets a delegation tool.

Routing stays as it is: a chat is bound to a brain (your DMs to `core`, the family group to `family`). What changes is that `core` can reach across; nothing else can. Grants are edited on the dashboard under Brains, and like every `brain.json` change today they wait for your approval if a brain edits them itself.

Mechanism: fridolin ships an MCP server inside its own binary, `fridolin mcp --run <id>`, that `claude -p` loads for every run over stdio. The tools a run sees depend on its brain and grants:

| Tool | Core run | Sub-brain run |
| --- | --- | --- |
| `delegate(brain, task, files?, timeout)` | granted brains only | absent |
| `list_brains()` | granted brains with their one-line description | absent |
| `read_memory(brain, path)` | brains with `read` | own brain only |
| `vault_ls`, `vault_get`, `vault_put`, `vault_search` | own paths plus `files` grants | own paths |
| `ask_user(question)`, `request_approval(action)` | yes | yes |

A delegation writes a request into the sub-brain's queue as a message from `core` on a synthetic chat; the sub-brain's worker runs it with its own prompt, memory and sandbox; the result (text plus vault paths it produced) returns to the waiting core run or, past the timeout, lands in the core's thread as an event. Every delegation is one audit line: who, to whom, what, which files, outcome.

Isolation is enforced by the runner, not by the prompt: a sub-brain run's sandbox denies the core directory and every sibling, exactly as brains deny each other today, and its MCP process is started with that brain's identity, so there is no tool to escalate through.

Policy inherits: a delegated run keeps the core thread's risk budget, and an irreversible step inside the sub-brain still stops for the poll on your phone.

Memory: the core keeps its own `memories/`; what it learns about a sub-brain it writes in its own notes, never into the sub-brain's files. Sub-brains write their own memory during their own runs.

## Workstream D — Tiered storage

The cold tier in Backblaze B2 is the complete, encrypted vault; the hot tier on the core host is a cache with a budget, and the vault service moves files between them by policy.

Two buckets, two tools:

| Tier | Where | Tool | Holds |
| --- | --- | --- | --- |
| Hot | `FRIDOLIN_HOME` in the core volume | the filesystem | the working set: config, brains, markdown, indexes, files in use |
| Cold | B2 bucket `fridolin-vault`, object versioning on | `rclone crypt` (encrypted names and contents, per-file, random access) | every file, always, as soon as it is written hot |
| Snapshots | B2 bucket `fridolin-snapshots` | `restic` for files, Litestream for the SQLite databases | point-in-time copies of the hot tier; the undo button |

The invariant: cold is complete. The vault service watches the hot tier (`fsnotify`) and uploads every new or changed file within minutes, so hot can be dropped and rebuilt from cold at any time. That is also why a core move is cheap: the new host hydrates the pinned set and pulls the rest on demand.

Vault index `vault.db` (SQLite, hot, pinned): one row per path with size, SHA-256, tier, last access, pinned flag, policy class, cold object key, and the provenance class of whatever produced it. Rebuildable from the cold listing if lost.

Placement policy, defaults in `config.json`:

| Class | Examples | Rule |
| --- | --- | --- |
| pinned | `config.json`, `bridge.db`, `secrets/`, `vault.db`, every `brain.json`, `brain-prompt.md`, `skills/`, `memories/**/*.md`, current month of `audit/`, the recent window of `conversation-history.jsonl` | always hot |
| active | any file read or written in the last 60 days; files referenced by an open thread; files a job hydrated (7 days after the job ends) | hot while active |
| cold-eligible | `media/` originals once transcribed or filed; `documents/**` originals older than 60 days; archived FrIDE workspaces; `run/` logs older than 14 days; audit months before the current one | evicted when inactive or when the budget is hit, least recently used first |

Markdown-first: a binary becomes cold-eligible only once a markdown companion exists (transcript, summary or index entry) or after 90 days regardless. The transcription, media-management and plaud skills already produce the companions; the filing skill from the architecture doc produces them for documents.

Hot budget: default 50 GB on the Mac mini; eviction keeps 10% headroom; a job that needs more than the free space asks before hydrating.

How a brain sees it: cold files are not on disk. Each directory with cold members carries one `.cold.json` listing them with size and date, so a plain `ls` still shows what exists, and the MCP tools `vault_ls`, `vault_get` (hydrate, returns the path), `vault_put` and `vault_search` do the rest. The brain prompt gets two lines explaining this.

Memory index: markdown files stay the truth; a derived SQLite FTS5 index over `memories/`, `conversation-history.jsonl` and transcripts is hot and rebuildable, with an optional local embeddings column later. Every indexed chunk carries a provenance class (`owner`, `agent`, `untrusted`, `system`) set by the kernel from where the text came, and only `owner` and `agent` entries are ever injected into a prompt without being asked for.

FrIDE workspaces: when a task is closed, the node agent asks FrIDE for the task's git bundle, uncommitted diff and rationale document, packs them into `workspaces/<task>.tar.zst`, and the vault sends it cold. `vault_get` plus `fride task import` brings it back on any node.

Keys: one `rclone crypt` password and salt, one `restic` password, kept in `secrets/`; the age identity in the host keychain; a printed recovery sheet. B2 lifecycle rules keep 30 days of object versions, so an eviction or a bad write is reversible without restic.

## Workstream E — FrIDE integration

FrIDE stays a complete standalone product; under fridolin it becomes the default destination for every coding task, and fridolin takes over only the fleet, placement and moves.

Two modes in one FrIDE binary, switched by `fride serve --under-fridolin` (or detected when a node agent is paired):

|  | Standalone, as today | Under fridolin |
| --- | --- | --- |
| Fleet, pairing, placement | FrIDE fleet mode (Tailscale, Google sign-in) | fridolin nodes; FrIDE's fleet mode is off |
| Who calls FrIDE's API | the UI and local scripts on loopback | the fridolin node agent on the same machine, with a token FrIDE issues when it is paired |
| Who decides what to build | FrIDE's orchestrator with its coding rules | still FrIDE's orchestrator; the core brain only hands over the request |
| Task moves | Continue on, direct node to node | `fride task export`, the vault, `fride task import` on the target node |
| Thread news | the Inbox page | a webhook to the node agent, relayed into the chat thread |
| Workspaces, stacks, slots, kanban, rationale, ledger, ideas | FrIDE | FrIDE, unchanged; a closed task's workspace is archived cold by the vault |

On the fridolin side, coding is a routed intent, not a brain:

- The core brain gets MCP tools `fride_task(message, repos?, node?)`, `fride_status(thread)`, `fride_reply(thread, text)`, `fride_move(thread, node)` and `fride_close(thread)`. They route through the node registry to the node agent of a node with the `fride` capability; fridolin picks the node by tags and by FrIDE's own memory check for the stack.
- The core's prompt carries one rule: anything that changes code goes to FrIDE. The core answers coding questions itself and never edits a repository, so no `coding` sub-brain is needed; the `coding` entry in Workstream C's example is just a name.
- FrIDE's questions ("which empty state, the no-results one or the error one?") arrive as events in the fridolin thread and reach your chat; your answer goes back through `fride_reply`.
- On `done`, the branch name and the rationale document come back to the chat; `fride_close` archives the workspace cold and frees the slot.
- Policy: `fride_task` is a `write` action. Anything FrIDE's agent wants to do beyond its workspace (publish, merge, deploy) stays behind FrIDE's own gates plus the fridolin approval poll.
- Dashboards: each node's FrIDE UI is published by the node agent over the tailnet with the node's identity; the hub on the core links them.

What FrIDE needs changed: the `--under-fridolin` mode (fleet off, a pairing token for the node agent, a webhook on thread news) and `fride task export | import` as a file bundle, most of which exists in `task move`. What it loses: nothing; with the mode off it is exactly today's FrIDE.

## Workstream F — What to take from OpenClaw

OpenClaw solved the same four problems at scale; take its shapes, not its code, and leave the plugin ecosystem alone.

| Take | Where it lands | Why |
| --- | --- | --- |
| Two-stage pairing: device approval, then command-surface approval; later expansions re-approved; nodes connect outbound only; explicit per-node command allowlists with conservative platform defaults | Workstream A | Exactly the trust model a fleet of your own machines needs, and it keeps `exec` off by default on a node you only want files from |
| Delegation as config: a coordinator with `subagents.allowAgents` and `delegationMode: prefer`; specialists return artifacts and never delegate further | Workstream C | This is the core-brain grants model; the sentence about specialists is the depth-one rule |
| Session hosting on nodes: worker slots per CPU core, Auto placement by least-busy host, an offline device keeps its placement and waits, per-worker container isolation on Linux and macOS only | Workstreams A and E | Placement rules for FrIDE tasks; the Windows limitation is why Windows nodes run executors natively |
| Memory: plain files plus one SQLite index; provenance (`owner`, `agent`, `untrusted`, `system`) stored as index columns the model cannot write; untrusted content never auto-injected; curated core files versus episodic daily notes; trigger phrases and importance as trailing markdown comments; intentions compiled into scheduled triggers instead of prose | Workstream D, Milestone 7 | The single most useful design in their docs; it fits fridolin's files-are-memory rule and turns `trigger.go` into prospective memory |
| Storage location marker and ownership claim: an explicit init marker at the location root, and an `owner.json` with a device id so two installations cannot share one destination | Workstream A | The core lease in B2 is this idea applied to the WhatsApp session |
| Backups: never copy a live SQLite file; use the online backup API; Litestream for continuous replication of the databases; archives verified before upload; restore stages into a fresh directory, never in place | Workstream D | `bridge.db` and `vault.db` get Litestream; `fridolin core import` stages, then activates |
| Skill format: `SKILL.md` with frontmatter `name`, `description`, `requires.env`, `requires.bins`, `os`, `install` | Skills (architecture doc) | Make fridolin skills portable Agent Skills; our `risk`, `vault` and `approve` fields go under `metadata.fridolin` |
| Windows: Gateway in WSL2 or a container, native companion as a node, service through a Scheduled Task or Service | Workstreams A and B | Confirms container core plus native node agent |
| `doctor` and `security audit` as commands that print findings with fixes | Hand-off | Every milestone gate below is a doctor check |

Leave alone: the Node.js runtime, the Gateway WebSocket protocol, the plugin and marketplace system, twenty channels. fridolin stays one Go binary with WhatsApp, Telegram and Plaud.

## Implementation order

Eight milestones, each closed by a gate you can verify from your phone or the dashboard; every checkbox is sized to be one Claude Code session in the fridolin or fride repository, and the implementing session works top to bottom.

*[Diagram: milestones · 8 in order, 7 gates — see the living copy linked at the top]*

M0 to M3 are infrastructure and can be verified without touching a brain; M4 to M7 change what the brains can do. M1 before M2 because nodes need something to connect to; M3 before M4 because delegation hands files around.

M0 — Foundations (fridolin):

- [ ] Replace the cgo SQLite driver with `modernc.org/sqlite` so the binary builds without a C compiler on all three platforms; keep the whatsmeow store and every existing test green
- [ ] `internal/platform` for Windows: `platform_windows.go` with `LockFileEx` for the bridge lock, a Job Object for process groups, and the brain command run through `cmd /C` or PowerShell (`procstart_other.go` and `logfile_unix.go` get Windows counterparts); delete the deliberate build stop in `unsupported.go` once the three are in
- [ ] Extend `make check` and the `ci.yml` matrix to windows-latest (goos windows, amd64 and arm64, tests on); add both to the release archives
- [x] `fridolin doctor` beside `fridolin check`: Claude CLI or token, tailscale auth key, Docker, `restic`, `rclone`, `ffmpeg`, disk; prints fixes
- [ ] Data layout from Workstream D through a `configMigrations` step that raises `currentConfigVersion`; schemas, golden files and `config-v1.json` updated
- [ ] Gate: `make check` green on macOS, Linux and Windows; `fridolin doctor` on the Mac mini reports only the expected missing pieces

M1 — Containerized core with `tsnet` and the lease:

- [ ] `Dockerfile` and `compose.yaml` for `fridolin-core`; named volume `fridolin-data`; image build in CI for amd64 and arm64
- [ ] `tsnet` listener in the core; dashboard and node port served on the tailnet only; MagicDNS name `fridolin-core`
- [ ] `fridolin core start | stop | update | status` host wrapper; age identity and `CLAUDE_CODE_OAUTH_TOKEN` from the host keychain into the container environment
- [ ] `fridolin core export`, `import --latest`, `promote`; core lease object in B2 with heartbeat and refusal logic
- [ ] Per-brain Unix users inside the container mapped onto the existing `brains/<id>/` state folders; `sandbox.deny_paths` mapped to mount denials; `bwrap` when available; the Linux refusal of `sandbox.enabled` lifted for the container sandbox
- [ ] Golden files for the container's CLI arguments and sandbox profile; `fridolin service` keeps working for native installs
- [ ] Gate: the core runs in a container on the Mac mini and answers WhatsApp; `export`, `import`, `promote` moves it to a Linux VM and then to the Windows machine, each in under an hour; the previous copy refuses to start as core afterwards

M2 — Nodes:

- [ ] `fridolin node` agent: outbound WebSocket to `fridolin-core`, signed device identity, reconnect with backoff
- [ ] Two-stage pairing with `fridolin nodes join-code`, `fridolin node join <code>`, `fridolin nodes approve`, and a Nodes page in the dashboard
- [ ] Capabilities `exec` (allowlist, platform defaults), `fs` (granted roots), `host`, `notify`; capability declarations and approvals persisted
- [ ] Service install on all three OSes through one service library; `node install | start | stop | status | uninstall`
- [ ] Registry with tags from `nodes.json`, free resources, last seen; `!fleet` command in the chat
- [ ] Gate: a Windows laptop joins with one pasted command; the core runs an allowlisted command on it and reads a granted folder; revoking it in the dashboard disconnects it within a minute

M3 — Vault:

- [x] `vault.db` schema and the vault service: `fsnotify` watcher, continuous `rclone crypt` upload to `fridolin-vault`, hash verification
- [x] Placement policy engine with the three classes, budget, LRU eviction, `.cold.json` per directory
- [x] Hydration: `vault_get` and automatic hydration for paths referenced by an open thread
- [x] MCP tools `vault_ls`, `vault_get`, `vault_put`, `vault_search` scoped per brain
- [x] `restic` snapshots of the hot tier to `fridolin-snapshots` (nightly, hourly for brains and audit) with retention; Litestream for `bridge.db` and `vault.db`
- [x] Replace `backup.go`'s Drive sync with a second restic repository on Drive through rclone and a weekly `restic copy`
- [x] Dashboard: vault page with hot usage, cold size, last upload, last snapshot, eviction log; restore drill command
- [x] Gate: a 2 GB recording goes cold after transcription while its transcript stays hot; `vault_get` brings it back; a fresh core on another node rebuilds its hot tier from cold within the budget and answers correctly (`TestVaultGate`: 64 MB in CI, 2 GB by hand with `FRIDOLIN_GATE_RECORDING_MB=2048`; the fresh core is a fresh data dir on the same machine)

M4 — Core brain, grants, MCP server:

- [ ] `role` and `grants` in `brain.json` with validation (one core, no grants on sub-brains, depth one)
- [ ] `fridolin mcp --run <id>` stdio server; `claude -p` launched with it for every run; tool set computed from brain and grants
- [ ] `delegate` through the sub-brain's queue as a synthetic chat; result or timeout back into the core thread; audit lines
- [ ] `read_memory`, `list_brains`; dashboard Grants editor with the existing approval flow
- [ ] Gate: the core delegates a summary of last week's family chat to `family` only when granted; without the grant the tool is absent; a sub-brain run has no `delegate` tool, verified by a test that lists its tools

M5 — Policy and approval (from the architecture doc):

- [ ] Risk levels in skill frontmatter under `metadata.fridolin`; `request_approval` MCP tool; WhatsApp poll with 24 h timeout as no; rate limits; `!lockdown`
- [ ] Audit log JSONL per thread; dashboard threads, approvals and audit views
- [ ] Gate: an irreversible skill step stops, the poll arrives, a no vote ends the run and is in the audit log

M6 — FrIDE through nodes:

- [ ] FrIDE: `--under-fridolin` mode: fleet mode off, pairing token for the node agent, webhook on thread news; standalone behaviour untouched and covered by its existing tests
- [ ] FrIDE: `fride task export | import` as a file bundle (branches, uncommitted changes, decision log, agent conversation)
- [ ] fridolin: node capability `fride`: proxy to FrIDE's loopback API with the pairing token; advertised only while `fride serve` answers
- [ ] fridolin: MCP tools `fride_task`, `fride_status`, `fride_reply`, `fride_move`, `fride_close` on the core brain; placement by tags and FrIDE's memory check; the "code goes to FrIDE" prompt rule
- [ ] `!move <thread> <node>` through the vault; `fride_close` archives the workspace cold
- [ ] Gate: a task started from WhatsApp runs on the laptop, asks one question in the chat, reports done with the branch name; closing it leaves `workspaces/<task>.tar.zst` in the cold tier; FrIDE on the laptop still works standalone with the mode off

M7 — Memory index and provenance:

- [x] FTS5 index over `memories/`, `conversation-history.jsonl`, transcripts; provenance column set by the kernel; only `owner` and `agent` auto-inject
- [x] Daily notes `memories/YYYY-MM-DD.md`; a weekly consolidation skill that promotes into `memories/core.md` with source anchors; trigger phrases and importance as trailing comments
- [x] Standing intents: "remind me when" compiled into `trigger.go` entries instead of prose
- [x] Gate: a fact learned on Monday from a web page never auto-injects; the same fact typed by you does, with its source anchor visible in the note

M8 — Later: Telegram channel, Plaud Developer Platform API, MentraOS channel, per-brain sibling containers.

## Open questions for the owner

All eight were decided on 20 Oct; nothing blocks the start.

- [x] The rework is on `main`, and brains still run `claude -p` or `codex exec` per message, so the MCP route in M4 stands.
- [x] The core runs in a container from M1 on (OrbStack or Docker Desktop on macOS, Docker on Linux, Docker Desktop with WSL2 on Windows).
- [x] The core embeds `tsnet` and is its own tailnet device.
- [x] Your DMs go to the core brain; groups keep their direct bindings.
- [x] Hot budget 50 GB and an activity window of 60 days on the Mac mini; the pinned set as in Workstream D until you name more paths.
- [x] Markdown-first: a recording or document goes cold only once a transcript or summary exists, or after 90 days regardless.
- [x] FrIDE stays standalone as today; under fridolin, nodes take over fleet, placement and moves, FrIDE is the default destination for every coding task, and its orchestrator stays inside FrIDE.
- [x] At least one Windows machine must be able to host the core, so Docker Desktop with WSL2 is part of the Windows setup and M1's gate also runs on Windows.

## Hand-off notes for the implementing session

Start by reading, not building: the current fridolin tree, FrIDE's `docs/FLEET.md` and `docs/INBOX.md`, this plan, and the 17 Oct architecture doc; then reconcile M0 with what the code looks like today and say what differs before writing code.

- Repositories: the `fridolin` repository (core, node agent, vault, MCP server) and the `fride` repository (task export and import, webhook). Everything else in this plan lives in fridolin.
- Conventions already in the repository, all to keep: Go 1.26; `make check` before every push; the package rules `internal/archtest` enforces; golden files under `cmd/fridolin/testdata/golden` rewritten with `-update` only when a change is meant to change them, with the reason in the pull request; a new setting goes into `config-v1.json` and the JSON schemas; a renamed or removed setting raises `currentConfigVersion` with a migration; a new route into `routeInventory`; the commit hooks that block phone numbers, JIDs, emails, home paths and tokens; AGENTS.md and CLAUDE.md as entry points.
- Never delete or recreate `bridge.db`, and never run two bridges on one data directory or WhatsApp session; until the core lease exists, experiment only in dev mode on a scratch config.
- Work one milestone at a time, one checkbox per session, on a branch per milestone; one pull request per milestone with the gate's evidence (command output, screenshots) in the description.
- Record decisions as you go in `docs/DECISIONS.md`, one dated line each, so the next session does not re-decide them.
- Ask the owner before anything irreversible: changing the WhatsApp pairing, deleting data, buying anything, touching the B2 buckets' lifecycle rules, rotating tokens, or any step that would log him out of Claude or Codex.
- Prefer the Plan B interfaces when a container feature is unclear: write the `Sandbox` and `Service` interfaces first, the container implementation second.
- Every gate is also a `fridolin doctor` check; add the check in the same pull request as the feature.
- Report back in the chat thread with WhatsApp-sized messages: what shipped, what the gate showed, what is blocked.
