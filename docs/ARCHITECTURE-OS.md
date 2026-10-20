# fridolin — Architecture & Plan

Oct 17, 2026. Living copy with diagrams: https://claude.ai/code/artifact/18871bb3-d281-410f-a0ee-b53344c880ea

## Decisions already made

fridolin becomes the OS: one Go binary is the kernel, and wherever it runs is the mother node. Everything below builds on these choices from 17 Oct 2026.

| Decision | Choice | Why |
| --- | --- | --- |
| Kernel | fridolin, extended in place | Channels, brains, skills, dashboard, Plaud and backups already exist |
| Mother node, now | Mac mini at home | macOS sandbox works today; no cloud box needed |
| Mother node, later | Must move to Linux or another Mac | Portability is a design constraint from day one |
| Execution | FrIDE as the coding and fleet subsystem | Tasks, worktrees, fleet mode and the inbox already exist |
| Backups | restic to Backblaze B2 (EU), second repo on Google Drive | Cheapest under about 1.5 TB, most durable, both providers see only ciphertext |
| Network | Tailscale only, nothing public | Phone and every node on one tailnet |
| Irreversible actions | Model drafts, Mathias confirms on WhatsApp | Prompt injection is the main threat |
| Containers | Decide later; keep it possible | WhatsApp session and CLI logins make containers awkward today |
| OpenClaw | Study later for channel adapters and skill format | Separate task |

## Vision and target use cases

fridolin is one always-on assistant you reach from your phone; it decides where work runs and asks before doing anything irreversible.

1. Voice note to taxes. Record on Plaud while walking. The transcript lands in your brain, the taxes skill matches, a draft return is built from the vault, and WhatsApp asks whether to submit.
2. WhatsApp to coding task. "Fix the empty state on the search page" starts a FrIDE task on whichever fleet machine has room. Progress, questions and the result come back to the same chat.
3. Dashboards from any node. Every machine on the tailnet reaches every dashboard by name, with no VPN setup per machine.
4. Fleet without thinking. Join a machine and it is available; move a running task between machines (FrIDE's Continue on).
5. Later: smart glasses as one more channel, with microphone, camera and display.

Not in scope for now: more than one user, anything reachable from the public internet, running on hardware you do not own.

## Design principles

Eight rules decide every open question below; when two conflict, the earlier one wins.

1. Portable: the mother node is a data directory plus a config file, not a machine. `FRIDOLIN_HOME` moves; OS-specific code sits behind interfaces.
2. Encrypted by default: everything that leaves the node is ciphertext, so storage providers are interchangeable.
3. Tailnet only: no port is reachable from the internet.
4. Model drafts, human submits: every skill carries a risk level; irreversible steps stop for a yes on your phone.
5. Files are memory: brains, skills, history, documents and the audit log are plain files you can `ls`, grep and back up.
6. One binary, few dependencies: the Go binary, the Claude CLI, tailscale, restic, rclone, ffmpeg.
7. Observable from the phone: heartbeat, status, alerts and the kill switch all work from WhatsApp.
8. Reuse before build: FrIDE, OpenClaw's adapters, MentraOS, official APIs.

## Architecture overview

*[Diagram: fridolin architecture · 5 channels, kernel with policy gate, 4 executors, vault, fleet, backups — see the living copy linked at the top]*

Messages enter through any channel and meet the same kernel; the policy gate sits between the router and every executor, so a skill, a FrIDE task and a scheduled job all obey the same risk rules. Brains and skills read only the vault, FrIDE tasks run on fleet nodes, and restic carries the vault off-site as ciphertext. Everything below the channel row lives on the tailnet.

## Mother node

The mother node is wherever the fridolin binary runs with its data directory; today that is the Mac mini, and it must be able to move without anything else noticing.

It owns: the channels (including the single WhatsApp session, since only one bridge may use a data directory), the router, the brains, the skill runner, approvals, the fleet registry, backups, the dashboard hub and the heartbeat. It may also execute FrIDE tasks as an ordinary fleet member.

Mac mini setup (Phase 0):

- A dedicated macOS user `fridolin` with automatic login; fridolin installed as its LaunchAgent from the existing `deploy/` template.
- `sudo pmset -a sleep 0 disksleep 0 autorestart 1` so it never sleeps and returns after a power cut.
- Tailscale with hostname `mother`, Tailscale SSH enabled.
- Homebrew: `restic`, `rclone`, `ffmpeg`, `go`, plus the Claude CLI logged in as that user.
- A small UPS for the mini and the router.
- Heartbeat to healthchecks.io every 5 minutes.

What is macOS-specific today, and what replaces it on Linux:

| Concern | macOS now | Linux later |
| --- | --- | --- |
| Per-run sandbox | `sandbox-exec` profile per brain | `bwrap` (bubblewrap) or `systemd-run` with sandboxing, one Unix user per brain |
| Background service | launchd agent | systemd user unit (template exists in `deploy/`) |
| Secrets at rest | Keychain | `age`-encrypted file, key from a password manager at boot |
| Claude CLI path | `~/Library/Application Support/Claude/...` | `~/.local/bin/claude` |
| Disk encryption | FileVault | LUKS |

Rule: no file outside `sandbox_*.go`, `service_*.go` and `paths_*.go` may know which OS it runs on; Go build tags select the implementation.

Moving the node: `fridolin export-node` writes one `age`-encrypted archive (config, brains, documents, skills, `bridge.db` with the WhatsApp session, tokens, audit log). `fridolin import-node` on the new machine restores it, joins the tailnet under the same name, and starts. If the old machine is dead, the restic repo restores everything except the WhatsApp session, which is re-paired once by QR code.

## Channels

A channel turns an outside event into an inbound message (sender, chat, text, attachments) and carries replies, polls and files back; the kernel never knows which app a message came from.

| Channel | Status | Notes |
| --- | --- | --- |
| WhatsApp (whatsmeow) | exists | Second number; unofficial client, so a ban is possible; stays the primary channel |
| Plaud recorder | exists, via Plaud login | Switch to the Plaud Developer Platform API once access is granted |
| Local API (`/ingest`, `/send`) | exists, loopback only | Becomes the channel for scripts and fleet nodes over the tailnet, with a token |
| FrIDE thread news | new, Phase 1 | A finished or stuck task becomes an inbound event so the chat hears about it |
| Telegram Bot API | new, Phase 4 | Official API, no ban risk; the fallback when WhatsApp is down |
| Signal (`signal-cli`) | optional | If Telegram is unwanted |
| Glasses (MentraOS app) | Phase 5 | Microphone to transcript inbound; short replies to the display |

Interface in Go:

```go
type Channel interface {
    Start(ctx context.Context, inbound chan<- Message) error
    Send(chat ChatID, reply Reply) error // text, poll, file, typing indicator
    Stop() error
}
```

Each channel maps its own identifiers (phone number, Telegram user id) to one person record, so there is one allowlist and one identity map across channels, and an approval vote on Telegram counts the same as one on WhatsApp.

## Kernel

The kernel is `bridge.go` grown into a pipeline: inbound, debounce, identity and allowlist, router, policy, executor, outbound, with one thread per conversation turn.

The router sorts every inbound message into one class:

- conversation: the default; the chat's brain answers via `claude -p` with recent history (today's behaviour)
- skill: a skill's frontmatter trigger matched, or the brain classified it in a short first pass; the skill runner takes it
- task: coding or long-running work; handed to FrIDE's inbox on a fleet node
- command: `!stop`, `!status`, `!fleet`, `!lockdown`, `!reset`
- control: a poll vote or reply that answers a pending approval or question

Threads: every inbound creates or continues a thread (FrIDE's thread id is reused for tasks). A thread records its origin channel and chat, the executor, status, pending approvals and the files it produced. `!status` lists open threads; a thread with no activity for 7 days closes itself.

Executor interface in Go:

```go
type Executor interface {
    Accepts(intent Intent) bool
    Run(ctx context.Context, t *Thread, intent Intent, events chan<- Event) error
}
// Event kinds: progress, question, needs_approval, artifact, done, failed
```

Implementations: `BrainExecutor` (`claude -p`, as today), `SkillExecutor` (`claude -p` seeded with the skill file, sandbox limited to the skill's vault paths), `FrideExecutor` (HTTP to FrIDE on the chosen node), `ScheduleExecutor` (the existing triggers, plus cron-style jobs).

Events become outbound messages: `question` reaches the chat as `AskUserQuestion` does today, `needs_approval` becomes a poll, `done` becomes a summary plus files.

## Skills

A skill stays a markdown file in a brain's `skills/` folder, with a frontmatter header the kernel can read without calling a model.

```markdown
---
name: taxes-de
triggers: ["steuer", "taxes", "elster", "steuererklärung"]
risk: irreversible          # read | write | irreversible
executor: skill             # skill | task
requires:
  vault: ["documents/taxes/**", "documents/income/**"]
  secrets: ["elster-cert"]
  tools: ["browser"]
approve: ["submit"]         # steps that stop for a human yes
---
# When this applies
...
# Procedure
1. ...
```

- `risk` is the highest level any step reaches; the policy layer reads it before the run starts.
- `requires.vault` lists the only paths the run may read or write; the sandbox denies everything else.
- `requires.secrets` names entries in the secrets store, resolved into the run's environment and never printed.
- `approve` names the steps where the procedure must stop and ask.
- Skills the assistant writes for itself keep working as today, but get `risk: read` until you edit the frontmatter in the dashboard.

Starter set: transcription, media-management and plaud exist. New in Phase 4: taxes-de, filing (incoming documents into vault folders), coding-handoff (how to phrase a FrIDE task from a chat message), status-report (the daily summary).

## Policy and approval layer

Every run carries a risk level, and anything irreversible stops until you say yes on your phone.

| Risk | Examples | Policy |
| --- | --- | --- |
| read | search history, summarise a PDF, show status | runs immediately |
| write | files inside the vault, create a FrIDE task, a calendar event | runs, then reports; undo is a file revert or closing the task |
| irreversible | submit a form, email a third party, pay, delete outside the vault, publish | stops and sends a poll ("Submit the 2025 return? yes / no / show me the draft"); no vote within 24 h counts as no |

Mechanics:

- The approval request is a native WhatsApp poll (exists); only votes from allowlisted numbers count, and the vote is written to the audit log with the thread id.
- Audit log: append-only JSONL per thread with who asked, what ran, which vault paths were read, which secrets were resolved, what was sent where. Shown in the dashboard, backed up nightly.
- Rate limits: at most N runs per hour per chat and M irreversible requests pending at once (start with N = 20, M = 3).
- Kill switch: `!stop` (exists) ends the current run; `!lockdown` pauses every executor except read until you unlock in the dashboard.
- The OTP guard (exists) stays, extended with one rule: no secret value is ever echoed into any channel.

## Fleet and FrIDE integration

FrIDE stays the execution system for code; fridolin decides what runs and on which node.

Node registry: every machine on the tailnet running `fride serve --fleet` is a node, the mother node included. fridolin reads FrIDE's fleet list and adds capability tags per node from `config.json`: `macos`, `linux`, `docker`, `xcode`, `gpu`, memory in GB.

Placement: for a task, fridolin picks the first online node whose tags satisfy the skill or task and which FrIDE reports as having memory for the stack (FrIDE already refuses to start into a full machine). If no node qualifies, the thread is queued, the chat is told, and placement retries when a node comes online.

Transport: `POST /api/inbox` on the chosen node's FrIDE, over the tailnet. FrIDE binds to `127.0.0.1` with no authentication today, so Phase 1 adds either a tailnet listener that requires a shared token or `tailscale serve` in front of it with the Tailscale identity as the check. Thread news flows back through FrIDE's thread status (polled) or a webhook from FrIDE to the mother node's `/ingest`.

Moving work: `fride task move <task> <node>` already carries branches, uncommitted changes, the decision log and the agent conversation; fridolin exposes it as `!move <thread> <node>`.

Dashboards: each node's FrIDE UI and the fridolin dashboard on `mother` are published with `tailscale serve`; the hub page on `mother` links them by MagicDNS name.

## Vault, encryption and backups

All state lives under one directory that moves with the node; restic snapshots it to Backblaze B2 nightly and copies to Google Drive weekly.

Layout of `FRIDOLIN_HOME`, for example `~/fridolin` of the dedicated user:

```
fridolin/
  config.json          bridge settings                 backed up
  bridge.db            WhatsApp session, polls, votes  export-node only, never in nightly backups
  dashboard.token                                      never backed up
  secrets/             age-encrypted entries           backed up (ciphertext)
  audit/               JSONL per thread                backed up
  brains/<id>/         prompt, history, memories, skills  backed up hourly
  documents/           the personal vault: taxes/, income/, contracts/, inbox/
  media/               staging for attachments         backed up
  run/                 logs and pids                   excluded
```

|  | Primary | Secondary |
| --- | --- | --- |
| Tool | restic | restic (`restic copy` from the primary) |
| Target | Backblaze B2 bucket, EU region | Google Drive through the rclone backend (`rclone:gdrive:fridolin-restic`) |
| Schedule | nightly 03:00 full set; hourly for `brains/` and `audit/` | weekly `restic copy` |
| Retention | 24 hourly, 14 daily, 8 weekly, 12 monthly | 8 weekly, 12 monthly |
| Verification | `restic check` weekly; `restic check --read-data-subset=5%` monthly; a restore drill into an empty folder each quarter | `restic check` monthly |
| Cost | about €2–5 a month under 1 TB | included in the existing Drive plan |

The second repo replaces the rclone `crypt` idea from the discussion: restic already encrypts, so a second restic repo on Drive is simpler than a separate encryption layer, and `restic copy` keeps both repos in step.

Keys: the restic repository password and the `age` identity live in your password manager plus one printed copy; on the mini the restic password sits in the Keychain, not on disk in clear. `sandbox.deny_paths` covers `secrets/`, the restic config and the rclone config, so no brain can read them.

At rest: FileVault on the mini (decision open, see Open decisions), LUKS on Linux.

Switch point: above about 1.5 TB a Hetzner Storage Box (5 TB for roughly €11 a month) becomes the cheaper primary; the repo moves with `restic copy` and nothing else changes.

## Networking

One Tailscale tailnet holds every node and your phone; nothing listens on a public interface.

- Hostnames via MagicDNS: `mother`, `laptop`, `linux-box`; the phone is a device on the tailnet, not a user with a password.
- ACL tags: `tag:fridolin-node` for machines, `tag:fridolin-admin` for your phone and laptop. Admin reaches nodes on the dashboard, SSH and inbox ports; nodes reach `mother` on `/ingest` and the inbox; everything else is denied.
- `tailscale serve` publishes each dashboard with HTTPS and the caller's tailnet identity; fridolin's dashboard token stays as a second factor.
- Tailscale SSH replaces key management across the fleet.
- Firewall on every node: allow the tailscale interface, deny the rest.
- If home power or internet fails, the mother node is unreachable; this is accepted for now. The upgrade path is a small cloud VM holding a standby copy of the node, promoted by `import-node`.

## Dashboards and observability

Your phone learns about problems before you go looking: heartbeat, a daily status and alerts all arrive in the same WhatsApp chat.

- Hub at `https://mother.<tailnet>.ts.net/`: links to the fridolin dashboard, every node's FrIDE UI, restic status, the audit log and open threads.
- Heartbeat: fridolin pings healthchecks.io every 5 minutes; a missed ping pages your phone by push or SMS, independent of the mother node.
- Daily status at 08:00 (a scheduled skill): open threads, tasks finished overnight, last backup result, free disk, nodes online.
- Alerts: backup failed, a node offline for more than 1 hour, an approval pending for more than 1 hour, WhatsApp session logged out, disk above 85%.
- Logs: per-run logs under `run/` kept 14 days; the audit log is kept indefinitely and backed up.

## Security model

The main threat is text reaching the model from someone else, in a message or a file, and making it act; the design limits what any single run can reach and makes irreversible steps need you.

| Threat | Mitigation |
| --- | --- |
| Prompt injection via a message or attachment | per-run sandbox allowing only the skill's `requires.vault` paths; risk levels; approval polls; OTP guard; no secret values in channel output |
| Strangers message the bridge number | DM allowlist and deny-by-default groups (exist); rate limits per chat |
| Your phone is stolen | remove the device from the tailnet; approval votes need the allowlisted number, dashboards need a tailnet device plus the token |
| The mother node is compromised | it is the trust boundary: FileVault or LUKS, dedicated user, secrets `age`-encrypted, restic password in the Keychain, audit log off-site nightly |
| Storage provider breach or subpoena | they hold restic ciphertext only |
| WhatsApp ban or forced logout | Telegram fallback channel; alert; re-pair by QR |
| Lost keys | password manager plus printed copy; quarterly restore drill proves they work |
| FrIDE API reached by another tailnet device | token or Tailscale identity required; off the tailnet it stays loopback-only |
| The assistant writes itself a dangerous skill | new skills default to `risk: read`; frontmatter changes wait for approval in the dashboard, as `brain.json` edits do today |
| A run outlives its purpose | per-run timeout; `!stop`; `!lockdown` |

## Portability and containerization

Design for moving now; decide on containers once the kernel is separated from its executors and runs on Linux.

Now, in Phase 3:

- OS-specific code in build-tagged files: `sandbox_darwin.go` and `sandbox_linux.go`, `service_darwin.go` and `service_linux.go`, `paths_darwin.go` and `paths_linux.go`.
- No absolute path outside `paths_*.go`; everything is relative to `FRIDOLIN_HOME`.
- `fridolin doctor` checks the Claude CLI, tailscale, restic, rclone, ffmpeg and the sandbox for the current OS, and prints what is missing.
- `fridolin export-node` and `fridolin import-node` as described under Mother node.
- CI runs the test suite on macOS and Linux; a Linux VM in the fleet is the standing test target for `import-node`.

Containers: a good fit for executors on Linux, where one container per skill or task run is a cheap sandbox. A poor fit for the kernel today: the WhatsApp session, the Claude CLI login and the macOS sandbox all live outside a container, and Docker on macOS adds a Linux VM for no gain. Revisit when the kernel runs on Linux and executors are separate processes; the Executor interface is what keeps that door open.

## Example flows end to end

Four flows show every layer in use; the taxes flow is the pattern for anything with money or legal effect.

*[Diagram: taxes flow · 4 steps, 1 approval gate, 1 revision loop — see the living copy linked at the top]*

A no vote sends the draft back to the skill with your notes; a yes submits through the browser and files the PDF.

The skill may only read `documents/taxes/**` and `documents/income/**`; the ELSTER certificate is resolved from `secrets/` for that run alone; the draft and the submitted PDF land in `documents/taxes/2025/` and are in that night's restic snapshot. The same shape applies to a cantonal e-tax portal.

Coding task from WhatsApp:

1. You write "fix the empty state of the search page" to the bridge number.
2. The router classifies it as a task; the coding-handoff skill turns it into a FrIDE inbox message with the repositories it implies.
3. Placement picks the laptop (online, has `docker`, 14 GB free); `POST /api/inbox` over the tailnet starts the task on a worktree branch.
4. FrIDE's thread news comes back as events: the agent's question ("which empty state, the no-results one or the error one?") reaches your chat; you answer; the thread continues.
5. On `done`, the chat gets the summary, the branch name and the rationale document; the task holds its slot until you close it or move it (`!move`).

Dashboards from any node:

1. Any device on the tailnet opens `https://mother.<tailnet>.ts.net/`.
2. The hub lists nodes with a green dot, open threads, last backup, and links to each node's FrIDE UI.
3. `tailscale serve` on each node terminates HTTPS and passes the caller's identity; the fridolin dashboard still asks for its token.

Glasses, later:

1. A MentraOS app registered to your own self-hosted MentraOS backend receives microphone audio when you tap the glasses.
2. The app posts the transcript to `mother`'s `/ingest` as the glasses channel; the router treats it like a voice note.
3. Short replies, approvals and status lines are pushed to the display; anything long goes to WhatsApp.

## Roadmap

Six phases, each closed by a gate you can verify from your phone; every bullet below is sized to be one Claude Code task against the fridolin or fride repository.

*[Diagram: roadmap · 6 phases, 5 gates — see the living copy linked at the top]*

Phase 0 is pure operations and can start this week; Phases 1 and 2 are the real engineering; everything after them is incremental. Phases run in order because each gate depends on the one before.

Phase 0 — Foundation on the Mac mini (fridolin repo, ops):

- [ ] Create the `fridolin` macOS user; install fridolin as its LaunchAgent; `pmset` and auto-login as under Mother node
- [ ] Tailscale on mini, laptop and phone; hostname `mother`; ACL with `tag:fridolin-node` and `tag:fridolin-admin`
- [ ] Move the WhatsApp bridge to the mini (export `~/.fridolin`, import, re-pair if needed); retire the old instance
- [ ] `backup.go`: add a restic backend beside rclone — init repo on B2, nightly full set, hourly `brains/` and `audit/`, retention policy, `restic check` weekly, status in the dashboard
- [ ] Second repo on Drive through rclone; weekly `restic copy`
- [ ] Heartbeat to healthchecks.io; alert on backup failure
- [ ] Gate: a restore drill from B2 into an empty folder brings back a brain that answers correctly

Phase 1 — WhatsApp starts FrIDE tasks (both repos):

- [ ] FrIDE: tailnet listener for `/api/inbox` with a shared token (or `tailscale serve` with identity check); keep loopback default
- [ ] FrIDE: webhook on thread news (`started`, `question`, `done`, `failed`) to a configurable URL
- [ ] fridolin: `FrideExecutor` — detect task intent, `POST /api/inbox`, map FrIDE thread id to the chat thread, relay news back
- [ ] fridolin: `!fleet` and `!move <thread> <node>` commands
- [ ] Gate: a task started from the phone runs on the laptop, asks one question in the chat, and reports done with the branch name

Phase 2 — Kernel: router, executors, policy (fridolin repo):

- [ ] Extract `Channel` and `Executor` interfaces; WhatsApp and Plaud become `Channel` implementations; brain runner becomes `BrainExecutor`
- [ ] Router with the five classes; thread store in SQLite beside the existing tables
- [ ] Skill frontmatter parser; `SkillExecutor` with sandbox allow-list from `requires.vault`; secrets store (`age`) with per-run resolution
- [ ] Policy layer: risk levels, approval polls, 24 h timeout, rate limits, `!lockdown`; audit log JSONL
- [ ] Dashboard: threads view, approvals view, audit view
- [ ] Gate: a skill marked `irreversible` stops at its `approve` step, the poll arrives, a `no` vote ends the run and is in the audit log

Phase 3 — Portability (fridolin repo):

- [ ] Build-tagged `sandbox_*`, `service_*`, `paths_*`; Linux sandbox with `bwrap`; systemd user unit
- [ ] `fridolin doctor`; `fridolin export-node` / `import-node` (age-encrypted archive)
- [ ] CI on macOS and Linux; a Linux VM joins the fleet as the standing test node
- [ ] Gate: `import-node` on the Linux VM comes up under the same tailnet name, answers a WhatsApp message, and the mini is switched off during the test

Phase 4 — Skills and a second channel:

- [ ] Skills: `taxes-de`, `filing`, `coding-handoff`, `status-report`
- [ ] Telegram channel (Bot API) with the shared allowlist and identity map
- [ ] Plaud: request Developer Platform access; replace the login flow in `plaud.go` when granted
- [ ] Gate: a Plaud voice note produces a tax draft in the vault and an approval poll on WhatsApp

Phase 5 — Glasses and OpenClaw learnings:

- [ ] Review OpenClaw's channel adapters and skill format; port what fits the `Channel` interface
- [ ] MentraOS app against a self-hosted MentraOS backend; glasses channel to `/ingest`
- [ ] Gate: a tap on the glasses asks for today's status and the first line appears on the display

## Costs

Running cost is about €5–10 a month on top of what you already pay, with the Mac mini's electricity the largest line.

| Item | € per month |
| --- | --- |
| Mac mini electricity (about 10 W idle, 25 W busy) | 3–5 |
| Backblaze B2, under 500 GB stored | 2–4 |
| Google Drive, Tailscale Personal, healthchecks.io free tier, Telegram Bot API | 0 (existing or free) |
| Claude subscription or API usage | existing |
| Later, only if needed: cloud standby node (small Hetzner or Exoscale VM) | 5–30 |
| Later, only if the vault passes 1.5 TB: Hetzner Storage Box 5 TB as primary | about 11 |

## Open decisions

Six choices are still yours; none blocks Phase 0 except the first two.

- [ ] FileVault on the mini: on (manual unlock after a power cut, node down until you are home) or off (physical security plus restic encryption)?
- [ ] Which macOS user runs fridolin: a dedicated `fridolin` account (cleaner sandbox, separate Claude login) or your own?
- [ ] Second channel: Telegram (easiest, official API) or Signal (more private, `signal-cli`)?
- [ ] Plaud: request Developer Platform access now, or keep the login integration until it breaks?
- [ ] Containers: decide after Phase 3, when the kernel runs on Linux and executors are separate processes.
- [ ] Cloud standby node: only if home uptime becomes a problem; the `import-node` path makes it a one-hour job later.
