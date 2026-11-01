# Changelog

What changes for people who run Fridolin. There are no tagged releases
yet, so everything is under Unreleased until the first one.
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) says what stays the same
from one version to the next, and how something that goes is first kept
for compatibility.

## Unreleased

### Added

- **GitHub in your chats.** A brain's `github` in brain.json (`{"repos":
  ["you/project"], "permissions": {"issues": "write"}}`) logs the runs
  that answer you, your direct messages and Plaud voice questions, in to
  `gh` with a token for exactly that, minted per run and revoked when it
  ends, as scheduled agents' `github` does. Groups, other people,
  hand-offs and delegated tasks get none. Off unless you set it
  (docs/BRAINS.md, "GitHub in your chats").
- **Approve by reaction.** A 👍 on an approval poll (`request_approval`,
  `node_exec`, a skill's approve step) from your own number counts as Yes,
  a 👎 as No, as a tap in the poll does. A reaction from anyone else, any
  other emoji, or one on a poll that is not an approval counts for
  nothing, and the poll keeps working. Reactions are logged as
  `approval_vote` lines in the audit log.
- **A live Claude session per brain, for your direct messages.** With
  `"live_session": "owner_dm"` in a brain's `brain.json`, the brain keeps one
  Claude Code session running and wakes it with each message you write to it
  in a direct chat, instead of starting `claude -p` for each, so it keeps the
  conversation and Claude's prompt cache. It uses Claude Code's channels
  (a research preview) with the development-channels flag and the brain's own
  sandbox and login; groups, other senders, scheduled agents and the rest
  still run `claude -p`, which is also the fallback whenever the session is
  not up, was refused channels, or died. Off by default.
  [docs/BRAINS.md](docs/BRAINS.md#live-session)
- **The Plaud Sync phone app updates itself.** Each build on the Plaud
  card (**Build and update phones**), or one the core brain starts with
  `plaud_app_update` when you ask it, is the release the paired phones
  install at their next check-in. The app fetches it through your site,
  encrypted for its own keystore key, checks its SHA-256 and that it is
  signed with the app's own key, and installs it with Android's
  PackageInstaller once the sync is idle; its pairing and what waits to
  be sent stay. The card shows the version each phone runs against the
  latest build, and how its last update went. Once per phone: install
  one build from the card by hand (0.6 and older cannot update
  themselves), allow Plaud Sync to **install unknown apps** (the app's
  **Allow app updates** button), and tap the notification of the first
  update; later ones install without asking. Builds must keep being
  made on the same machine, whose debug key signs them: the card says
  when a phone cannot update to a build signed with another key.
  [docs/PLAUD.md](docs/PLAUD.md#updates)

### Changed

- **The core's and FrIDE's images run Node.js 26** (was 22), the LTS
  line, which brains' scripts and the Claude, Codex and Plaud CLIs in
  the container now run on.

- **In voice mode, whatever you record on the pin reaches the voice brain
  whole, and the brain decides what it is.** A NotePin recording used to
  be sent as a question: its speaker labels and timestamps were dropped,
  the brain answered a meeting in a few spoken sentences, and the
  instruction for recordings (`plaud.message`, `skills/plaud.md`) was
  never applied. Now the Plaud Sync app (0.8) sends the whole transcript
  through the voice route, marked as a recording from the pin, and the
  voice brain (`plaud.voice_brain`, not `plaud.brain`) gets it as it gets
  any recording: a file in the `plaud` folder of its inbox and the
  `[Plaud] New recording` line with the instruction for recordings. Spoken
  to (a question, a task, a reminder), it does that and answers aloud;
  otherwise (a meeting, a dictation) it files it as a recording, sends the
  summary and action items to WhatsApp, and says in one spoken sentence
  what it filed. Questions asked with **Ask** stay plain questions, with
  the same prompt as before. A recording may now be 512 KiB long, as an
  upload (a question stays at 16 KiB), and one Fridolin refuses (too long,
  `plaud.voice` off, the brain's queue full) goes as a transcript to the
  Plaud brain instead; a refused question is dropped. Before, one long
  recording was refused and blocked every later question on the phone for
  good, and the app now also stops a transcript too long for Fridolin from
  holding up the ones after it. Build the app again on the dashboard so the
  phones update. [docs/PLAUD.md](docs/PLAUD.md#voice-mode)

### Fixed

- **Moving a scheduled agent to an earlier time today is no missed run.**
  After an edit that changes `when`, a scheduled agent counts from the
  edit: it no longer runs at once (or logs a skipped run "while the bridge
  was down") for a time that already passed today, and `schedules list`
  no longer shows a negative next (#417).
- **A node whose FrIDE instances run on the core's Claude login no
  longer says it has no login after an update.** The node agent kept in
  memory only that the core lent its token, so once an update restarted
  it, `fridolin nodes` and `fridolin node fride host status` said "no
  Claude login" until the core started an instance again, while the
  running instance was logged in all along. The node now keeps it in
  the instance's state.
- **WebFetch is no longer on by default for brains without
  `dangerously_skip_permissions`** (#410). Since #406 such a brain got
  `--allowed-tools WebSearch,WebFetch`, while the docs said it could not
  reach the web: WebFetch fetches any URL the model picks, a way out for
  what a brain read, in the mode chosen for brains that strangers may
  message. It now gets WebSearch only; one that is to fetch pages gets
  `"WebFetch"` from a `permissions.allow` rule in its folder's
  `.claude/settings.json`. To give one brain the web while the others keep
  the global `disallowed_tools`, set `"disallowed_tools": []` on that
  brain. The docs say so, and the sandbox section names the network as
  what it does not limit.

- **`!approved` approves.** Typed in the past tense, the owner's
  `!approve`, `!deny` and `!revoke` went to the brain, which knows
  nothing of the bridge's own approvals and answered that nothing waited
  (2026-10-29, while a node's request did). `!approved`, `!denied` and
  `!revoked` now mean the commands.

- **FrIDE's questions reach your chat whole.** Every piece of FrIDE's news
  was cut to three lines and 300 characters, inside a word if need be, so
  a question you had to answer often ended mid-sentence ("… more in the
  dashboard"). Now a question comes whole (only a runaway text over 4000
  characters is cut), a failure keeps its reason up to 1500 characters,
  and other news is up to five lines and 600 characters, cut at the end of
  a sentence or a word.

- **FrIDE's agents merge their own green pull requests again.** They run
  Claude Code in auto mode, whose own judgement refuses by default to
  merge a pull request no human approved, so now and then a fix the bug
  fixer handed out stayed open with CI green, waiting for you. FrIDE now
  takes `companion.claudeAllow`, Claude Code permission rules every task
  agent has (fride#49), and the FrIDE instances' template and the box's
  FrIDE allow `Bash(gh pr merge:*)`. A companion FrIDE set up by hand
  gets the rule in its `config.json` (docs/NODES.md); the instances are
  made anew from the new template at their next start.

- **The core brain no longer calls a machine offline that is online.**
  Its only look at the nodes was `node_exec_rules`, which lists the
  machines that run commands, so a machine that runs none (a Windows PC
  that joined with `-NoExec`, say) looked offline, and the brain went on
  saying so from what the chat said hours before. It now has
  `node_list`: every machine as the core sees it now, online or when it
  was last seen, what it may do and its FrIDE instances; and
  `node_exec_rules` names the machines it leaves out because they run no
  commands.

- **The Plaud Sync app builds again.** A change to its sync watchdog had
  left `SyncService.kt` without the declaration of `lastCheckIn`, so
  **Build phone app** failed with "Unresolved reference".

- **A Plaud login kept in `plaud-secrets` is now seen and shared.** Later
  versions of Plaud's CLI keep the login in `~/.plaud/plaud-secrets`
  instead of `tokens.json`. Fridolin looked only for `tokens.json`, so
  that login read as signed out (recordings stopped arriving) and, with
  "Let brains use the Plaud CLI", brains could not save a refreshed login
  and their `plaud` failed with `[AUTH_FAILED]`. Fridolin now treats both
  files as the login: it checks neither is a link, removes both on
  sign-out, and lets brains rewrite whichever exists in place, so
  there is one live login for the bridge and the brains.

### Fixed

- **The Plaud Sync app no longer stalls silently.** A connect, scan or
  download the Bluetooth SDK never answered could leave the sync waiting
  for good, with nothing in the log. A watchdog now notices a sync that
  stands still (or a lost alarm) and starts it over, logging
  "watchdog: the sync stood still". The log also says each step now:
  connected, files listed, recordings pulled, and every failed connect
  with its retry; the pin's rotating Bluetooth address is noted once, in
  logcat only.

### Added

- **FrIDE instances on Windows can log in to Claude, and work never goes
  to an instance that cannot.** A Windows node runs as LocalSystem and
  kept no secrets, so its instances' agents had no Claude login: work
  sent there failed with "the orchestrator could not think". Now:
  - `fridolin node secrets set claude.oauth.token` stores the token
    `claude setup-token` prints, typed without echo. On macOS and Linux
    it goes where `fridolin core secrets` keeps it; on Windows into the
    node's own store, sealed in its state dir under a key from its device
    key (run it in an administrator PowerShell, with `--state-dir`).
    `fridolin node secrets` lists it, and `fridolin node fride host
    status` says whether the agents can log in.
  - A node without the token says so in `fride_host`'s new `no_login`,
    with that command, and so does an instance that started without one.
    The core starts no instance on such a node and hands no work to an
    instance whose agents did not log in: the work goes to a node that
    has the token, or waits and says why. The owner hears it once in
    their chat, the Nodes page shows "fride-host (no Claude login)", and
    `fridolin doctor` warns until the token is stored.
  - Once it is, the core's next start or refresh logs the running
    instance's agents in, without stopping it.
  - `fride_close` closes work FrIDE never made a task of (it failed
    there, still waits in FrIDE's queue, or FrIDE answered or finished it
    without one, as when it cancels work that moved) on the core alone,
    so that it no longer keeps its instance on that node; news of it
    still comes. Work whose FrIDE still asks stays open.
  - Or the core lends its own Claude token: an instance whose config
    says `"core_login": true` gets `CLAUDE_CODE_OAUTH_TOKEN` from the
    core's environment with each start (and refresh), as the workstation
    lends it to one session, so that a node needs no token of its own.
    The node never stores or logs it; it sits in the instance's VM as a
    node's own would.

- **The core brain runs commands on your machines** (docs/VISION.md, step
  4). The core brain gets `node_exec`, which runs one command a node's
  exec allowlist allows (no shell, checked on the core and on the node),
  and `node_exec_rules`, which lists what each node allows; both only
  while a node whose `exec` you approved is online. An exec rule in
  `node.json` with `"approve": true` asks you before every call it
  allows: `node_exec` puts the poll in your chat itself ("Run `brew
  upgrade` on mac-mini"), waits like `request_approval`, and runs the
  command on yes; unanswered it answers pending with an `approval_id`,
  which runs exactly that command on that node once. A call another rule
  without `approve` allows does not ask. Turning `approve` on or off is a
  change the node's commands wait for your approval of again. The Nodes
  page marks such rules "asks you each time"; every call is a `node_exec`
  line of the audit log. A node that marks a rule `approve` needs a core
  of this version: an older one approves the rule without it, which the
  node then refuses to run. See docs/NODES.md, "The core brain runs
  commands".
- **A native node in one line.** `!join`, the Nodes page's **New join
  code** and `fridolin nodes join-code` now offer first a line that
  makes the machine a node natively, one agent per machine
  (docs/VISION.md): `container/node.sh` (macOS, Linux) and `node.ps1`
  (Windows) of the core's commit say what they will do and ask first,
  install Tailscale where it is missing and open its sign-in link, fetch
  the core's fridolin from the core with the join code, check its
  SHA-256, install it (`~/.local/lib/fridolin/fridolin`, Windows
  `%ProgramFiles%\fridolin\fridolin.exe` with one UAC prompt for it and
  the service) and run `fridolin node join`. The box comes second, the
  fallback where Docker Sandboxes cannot run, and the plain `fridolin
  node join` last. The node port serves the binary at `GET
  /join/<code>/fridolin` and its SHA-256, size and version at
  `/join/<code>/fridolin.sha256` (`?os=&arch=`), only while the code is
  valid and unused, without spending it, rate-limited. `POST
  /api/nodes/join-code` adds `native_command` and
  `native_command_powershell`; its other fields stay. See
  docs/NODES.md, "The one-liner".
- **FrIDE instances, on the core.** The core starts the FrIDE instances
  `fride.instances` names on nodes that offer `fride_host`, sizes each at
  its start (small 3 GB, medium 6 GB, large 12 GB, or the config's
  `memory_mb`) on the node with the most room beyond its reserve, stopping
  idle instances of other trust domains or giving a smaller size when
  nothing else fits (the chat is told), and stops one after its
  `idle_stop_minutes` with no open thread and no news. Each start carries
  the instance's repositories, egress and the git identities it names,
  with their tokens, sent again when they change. The core brain gets
  `fride_instances`, `fride_spawn` and `fride_stop`, and `instance` on
  `fride_task` and `fride_repo_add`; its prompt lists the instances, so
  it picks one by purpose. Work handed to an instance stays on it
  (`fride_move` refuses it); `bridge.db`'s `fride_threads` gets an
  `instance` column (migration 8, which only adds). `GET /api/fride`
  lists the instances and each thread's instance; new routes `POST
  /api/fride/instances/{name}/stop` and `/start`. Starts and stops are
  audit lines of kind `fride_instance`; `fridolin doctor` gets
  `fride-instances`. Native git identities (`fride.identities`) now go
  only to nodes with a FrIDE of their own. See docs/NODES.md, "The core
  and FrIDE instances".
- **FrIDE instances on a node** (docs/VISION.md, "FrIDE instances"). A
  node runs whole FrIDEs, each in a Docker Sandbox of its own, for one
  trust domain, which the core starts, stops and removes
  (`fride.instance.start|stop|remove|status`). `fridolin node fride host
  setup` in a fridolin checkout builds the template (FrIDE at the pinned
  commit, Claude Code, ponytail) and makes sbx's settings fridolin's;
  then the node offers `fride_host`, with sbx's state, the machine's
  memory (on macOS free, inactive, purgeable and speculative pages), the
  memory it keeps for itself (6144 MB, or node.json's
  `fride_host.reserve_mb`) and its instances. Each instance, `fride-<name>`
  in sbx, gets a pair of ports from 17850 up, its repositories cloned in
  its VM, a network allowlist (Anthropic, Go, npm, Docker Hub, its git
  hosts and the core's `egress`), its git identities as placeholders that
  sbx's proxy swaps for the tokens (which stay in the node's state dir),
  and the Claude login from the machine's host secret store, which sits
  in the VM until sbx can inject subscription logins. A start with
  another memory makes the sandbox anew and keeps its workspaces,
  repositories, sessions, tasks and login. The `fride.*` methods take an
  `instance`, and FrIDE's news says which. `fridolin node fride instance
  ls|stop|rm|logs NAME` show and stop them on the machine. Nothing
  changes for the native companion FrIDE. See docs/NODES.md, "FrIDE
  instances".
- **FrIDE instances from the core's template, and on Windows.** CI
  publishes the instances' template, `ghcr.io/OWNER/fridolin-fride-sandbox`,
  for amd64 and arm64 beside the core's images, tagged with the version
  and with a hash of what goes into it, which the core's image names
  (`FRIDOLIN_FRIDE_SANDBOX_TAG`). `fridolin node fride host setup
  --no-build` (the default where no Docker is found) builds nothing and
  needs no checkout: the node then offers `fride_host` with
  `needs_template`, and makes each new sandbox from the template the core
  names in `fride.instance.start` (`template`), which sbx pulls; a
  template the node built itself still wins. A core without
  `FRIDOLIN_BOX_REGISTRY` places no instance on such a node, and
  `fride_task` and doctor say why. A native Windows 11 x64 node is a
  FrIDE host too: with the Windows Hypervisor Platform, `winget install
  -h Docker.sbx` and `sbx login` as the user who signs in, the node's
  service runs every sbx call in that user's session as that user;
  nobody signed in makes `fride_host` not ready. sbx reads git tokens
  there with `fridolin.exe node fride host sbx-secret`, from folders only
  that user and SYSTEM may open. No shared caches on Windows yet. See
  docs/NODES.md, "Setting a machine up" and "FrIDE instances on Windows".
- **Git identities for FrIDE, and a GitLab connection.** Integrations →
  **GitLab** keeps a self-hosted GitLab's address, an access token and an
  optional commit name and email; **Check** asks GitLab who the token is.
  The token is kept in the data dir's `core/local-secrets/`, on the
  machines only: no restic snapshot, vault cold tier, Drive backup or core
  export takes it, and a core move over the node channel carries it end to
  end, sealed with the host secrets. The GitHub card gets an optional
  commit name and email. The core hands every node whose `fride` is
  approved its FrIDE's identities with their tokens (the new node method
  `fride.identities`): `github`, with the GitHub app's installation token
  (renewed before it ends; only when the app may write code) or the typed
  token, and `gitlab`. The node puts them into a FrIDE that takes them
  (FrIDE c5dd87b9, now pinned) and removes the ones it no longer gets.
  The FrIDE in a Docker Sandbox gets none; the FrIDE instances that
  replace it will get placeholders (`setHostToken`).
  config.json's new `nodes.git_identities` says which a node gets; a node
  it does not name gets both. `fridolin doctor` checks the GitLab token.
  New routes: `GET`/`PUT`/`DELETE /api/gitlab`, `POST /api/gitlab/check`,
  `PUT /api/github/identity`, `GET /api/git-identities`. See
  docs/CONNECTIONS.md, "GitLab", and docs/NODES.md, "Git identities for
  FrIDE".
- **GitHub, a bigger Docker disk, shared caches and kept history for the
  sandboxed FrIDE** (#149).
  - `fridolin node fride sandbox github` gives the sandbox the Mac's `gh
    auth token` (or one on stdin with `--token-stdin`; `--remove` takes
    it): sbx's github service and a placeholder for git, scoped to the
    sandbox. FrIDE's agents push and use `gh` inside; the token never
    enters the VM. `create` does it when `gh` is logged in.
  - The same works for any git host, such as a self-hosted GitLab, in
    the node's code (`setHostToken`): a placeholder that sbx's proxy
    swaps for the token in Bearer headers and in git's Basic auth.
  - `create` makes the sandbox's Docker disk 40 GB (`--docker-disk`),
    and shares Go's and npm's caches in `~/.cache/fridolin-sbx` with the
    next sandbox (`--no-caches`). The Mac never reads them.
  - `rm` saves the agents' sessions and FrIDE's tasks to the node's
    state dir first (`--no-history` does not), and `create` puts them
    back.
  - `update` with a new template now makes the sandbox anew and keeps
    its workspaces, sessions, tasks and login, where it used to only say
    that `rm` and `create` were needed.

  See docs/NODES.md, "FrIDE in a Docker Sandbox".
- **FrIDE in a Docker Sandbox** (#149). `fridolin node fride sandbox
  join|create|login|start|stop|update|status|rm` runs a whole FrIDE, with
  its coding agents, in one sbx microVM on a Mac, beside the native
  companion FrIDE: on `127.0.0.1:17850` (webhook 17851), with the
  repositories in `~/fride-sandbox/repos` (`--base`) and egress for
  GitHub, Go, npm and Anthropic only. A node pairs with one FrIDE, so the
  sandboxed one is paired to a node of its own: `join` makes it in the
  node's state dir with `-sbx`, offering nothing but `fride`, with its own
  copy of fridolin and its own service (`ai.fridolin.node.fride-sandbox`).
  The node's agent keeps FrIDE running while it should. FrIDE's agents
  log in through `login`, which takes the token `claude setup-token`
  prints without echo or from a pipe. Nothing changes for the native
  companion or the machine's node. See docs/NODES.md, "FrIDE in a Docker
  Sandbox".
- **node.json's `service`** (#149) names a second node's service on a
  machine (`fridolin-node-<service>`, `ai.fridolin.node.<service>` on
  macOS), run from its state dir's own `bin/fridolin`; `fridolin node
  install|start|stop|status|uninstall --state-dir` follow it. Unset, as on
  every node so far, nothing changes.
- **OpenAI and ElevenLabs as connections of Fridolin** (#284). The
  Integrations page keeps one OpenAI key (with an optional organization
  and project) and one ElevenLabs key, in `connections/` in the data dir.
  Site modules declare `capabilities.openai` and
  `capabilities.elevenlabs` in module.json (the deploy review lists
  them) and call the providers through the bridge; brains listed in the
  new `connections` section of config.json get the tools
  `openai_transcribe`, `elevenlabs_transcribe`, `elevenlabs_speak` and
  `elevenlabs_voices`. The bridge makes every call with the key, which no
  module or brain sees, checks what the caller declared and its monthly
  limit, and counts the usage per caller and month, with an estimated
  cost; the audit log gets `connection_call` and `connection_key` lines.
  `fridolin doctor` checks each key that is set. An older bridge rolled
  back to ignores the `connections` section, and drops it when the
  dashboard saves; the keys stay in the data dir (docs/CONNECTIONS.md).
- **A scheduled agent's own effort and a longer timeout.** An agent's
  file takes `effort` (`low` … `max`, the dashboard's *More settings*):
  its runs, and those FrIDE's news starts for it, think that hard, while
  the brain's chats keep the brain's effort. Codex mode ignores it, as
  it ignores a brain's `effort`. `timeout_minutes` now replaces
  `run_timeout_minutes` for the agent's runs, longer as well as shorter
  (up to 1440): an agent that set one longer than the global limit, and
  was stopped at the global limit until now, runs as long as it says.
  A bridge rolled back to lists an agent file that sets `effort` as
  not valid (an unknown field) until it is taken out, and holds a
  timeout to `run_timeout_minutes` again.
- **Public paths for site modules** (`server.public` in module.json, e.g.
  `["/intake"]`): those paths of a module's server answer without sign-in,
  so a form opened from an email link or a webhook can reach it. The router
  checks what such a request may do itself (a one-time token, a signature);
  the site limits public paths to 120 requests a minute per client address
  and module. The deploy review lists the field (docs/SITE.md, "Public
  paths").
- **FrIDE is quiet in WhatsApp.** A new setting, `fride.news`
  (Settings → Messages), is `"quiet"` by default: a chat gets only work
  that is done or merged, failures (an identical one once per thread)
  and real questions; "Created task …", FrIDE's echo of the brief and
  progress stay on the new FrIDE page of the dashboard, which keeps each
  thread's news whole. `"all"` sends every piece of news, as before.
  Either way a FrIDE message is at most three lines, and one text a
  node sends for several threads goes out once, naming the threads.
  Work a scheduled agent hands to FrIDE never posts to WhatsApp any
  more: FrIDE's questions, and work done or failed, start a run of that
  agent, which answers FrIDE itself and tells you only what merged,
  failed for good or needs your decision. A question from FrIDE that
  asks nothing is shown as news. `bridge.db` gets a table,
  `fride_news` (migration 7); an older bridge rolled back to ignores it.

- **A brain can act without asking** (`"approvals": "auto"` in
  brain.json, the brain page's *Approvals*): its `request_approval`
  answers approved at once, without a poll, recorded in the Approvals
  page's history and the audit log as approved by `auto`. A lockdown, the
  run's risk level, `policy.max_pending_irreversible` and observe-only
  runs still hold, and a delegated task follows the other brain's own
  setting. It is a security trade-off: set it only on a brain only you
  talk to (docs/BRAINS.md, "Acting without asking"). Brains without the
  field ask, as before; the dashboard and API now show `"approvals":
  "ask"` for them, and a new brain's brain.json has it.

- **The secret folders go to the vault.** With the vault on, the
  credentials folder (`sandbox.credentials_dir`: the Google OAuth
  client, the calendar credentials and tokens, the dashboard users) and
  the folders of the new `vault.secret_dirs` (such as
  `["~/.personal-calendar"]`) are backed up to B2 only: one version a
  day, and one after a change (at most every ten minutes), each a tar of
  every folder sealed with the cold tier's key inside rclone crypt with
  its passwords, into `secret-folders-sealed/` in the vault bucket; never
  to Google Drive or a node target. The vault only reads them. It keeps
  every version of the last day and the newest of each of the
  `vault.secret_keep_days` (30) days before, with the snapshots'
  mass-deletion hold. `fridolin vault restore-secrets [--to DIR] [--at
  DATE]` restores one into a new folder (by default
  `core/restored-secrets-<time>` in the data dir); `--force` restores
  over the live folders, after copying them to
  `core/replaced-secrets-<time>/`. `fridolin vault backup-secrets` takes
  a version now, `fridolin vault status` shows the folders, the last
  backup and the versions, and `fridolin doctor` warns
  (`vault-secret-folders`) while there is none younger than two days.
  The log and the status name folders and count files and bytes, never
  a file's content. A credentials folder that lies in the data dir or a
  brain folder is now left out of the snapshots (which go to Drive and
  node targets too) and of the cold tier; `fridolin core migrate` binds
  `vault.secret_dirs` into the core read-only (docs/VAULT.md, "Secret
  folders").

- **Approvals in plain words, without codes**: everything that waits for
  the owner (a node's device, what a node offers and core-host, a
  brain's scheduled agent, changed brain.json or skill, a run's request)
  is told in their WhatsApp chat the same way: what wants what, when,
  where it connected from and how it was started, what approving lets it
  do in one sentence per capability, and "Was this you? Reply !approve —
  or !deny if not", with the id last, for reference. `!approve` and the
  new `!deny` work alone: on the one thing waiting, or through a poll
  when several wait (only the owner's vote counts).
  Something the owner did not start (a join code from neither `!join`
  nor the dashboard, a node asking for more on its own, a file a brain
  wrote), or that waited over a day, asks for a second `!approve` within
  2 minutes. Brain files, schedules and skills are now told in WhatsApp
  too, within a minute. `!approve <ref>` still works; `!revoke` alone
  is `!deny`. `!fleet` and the Nodes page use the same wording; `GET
  /api/nodes` gives each node `allows` and `joined_via`. The same holds
  for what came since: fride and core-host say what approving them
  means, a scheduled agent that runs a program on a node says what
  approving does to that node's allowlist, and `!movecore`'s poll (Move
  now / Cancel) and the keep/forget poll after a move answer to
  `!approve` and `!deny` too. New audit kind
  `owner_approval`; new file `<data_dir>/owner-approvals.json`
  (docs/NODES.md, "Approving from WhatsApp").

- **The core in a box** (#191 phase 4): a node in a box can take the
  core and give it back, with the same `!movecore` / `fridolin nodes
  move`, plan, lease and automatic rollback as a native core host. The
  core runs inside the box's `node` container, as the agent's child
  (`fridolin run --container --box`), on the volume `fridolin-box-data`,
  with the host secrets of the box's sealed store in its environment;
  the role is `core-role.json` in the node's volume, so a box that is
  the core starts it again after a restart or update. No Docker socket;
  FrIDE never mounts the core's volumes and its address cannot reach the
  core's dashboard or site. The settings come as a second compose file,
  `core.yaml` with `seccomp.json` (`fridolin box core-compose`, `fridolin
  box seccomp`), which `box.sh`, `box.ps1` and the box updaters write
  and name in `.env`'s `COMPOSE_FILE`: a box made before gets them with
  its second update, or `box.sh --update` (`box.ps1 -Update`) once.
  They publish the dashboard and site on the host's `127.0.0.1:8765` and
  `:8780` (`FRIDOLIN_BOX_DASHBOARD_PORT`, `FRIDOLIN_BOX_SITE_PORT`); on
  Ubuntu set `FRIDOLIN_BOX_APPARMOR=fridolin-core` after `sudo fridolin
  core apparmor`. The move's pre-flight no longer refuses a box: it
  checks the box's core settings and its sandbox (bubblewrap), says
  what changes there (brain folders in the volume, not a host folder;
  what is bound to the source stays on its node). `box.sh --update`
  keeps the settings added to `.env` (docs/NODES.md, "The core in a
  box"; docs/CORE.md, "Moving the core to a box").

- **A calendar overview per brain.** A brain's Calendar card shows the
  account, the sign-in's state (working, ends soon, revoked by Google,
  needs reconnecting), when it last worked, its calendars with their
  access and which are ticked, the default, and **Reconnect** next to
  Disconnect. The bridge checks every connected brain's sign-in every six
  hours. A broken one shows on the Overview page (`GET
  /api/calendar/health`) and in `fridolin doctor` (`calendar-grants`), and
  the owner gets one WhatsApp message about it. `GET
  /api/brains/{id}/calendar` adds `health`. The brain's hint now names the
  card's **Reconnect** button instead of "Sign in again".
- **A guided Google setup.** Settings → Google sign-in (and a brain's
  Calendar card before any client is set up) walks through Google Cloud
  Console step by step: a link to each page (in your project once you
  type its ID), the app name, scopes and redirect with copy buttons,
  Google's "unverified app" screen and its 100-user cap explained. The
  client goes in as the JSON file or as its ID and secret, and is checked
  right after: `POST /api/google/client/check` asks Google's token
  endpoint about a made-up code, which grants nothing, and says whether
  Google knows the client. `GET /api/google/client` adds `source`,
  `builtin` and `scopes` (docs/CALENDAR.md).
- **A built-in Google client** for publishers' builds:
  `FRIDOLIN_GOOGLE_CLIENT_ID` and `FRIDOLIN_GOOGLE_CLIENT_SECRET` in the
  environment of `make build` or `scripts/build-release.sh` bake a Desktop
  app client into the binary, so its users connect calendars and Drive
  with one click. Public builds have none. A client pasted under Settings
  always wins, and the dashboard labels the built-in one.
- **Google sign-in from another machine.** A dashboard opened over the
  tailnet opens Google in a new tab and takes the address Google sent it
  back to (`POST /api/google/callback`), since `http://127.0.0.1:8765/`
  does not load on that machine.

- **Forgetting a former core host removes what moves brought it** (#266):
  "Forget them" in the keep/forget poll after a core move (or no vote in
  24 hours), and `!revoke` of a connected core host, now also remove the
  secret files a move wrote into the core's credentials folder (the
  `fridolin-home` volume) and the core's data dir (`fridolin-data`:
  `bridge.db` with the WhatsApp session, `vault.db`, the config, the
  brain folders a move put there), not only the host secrets. Each is
  keyed off a record the node agent keeps as a move brings it
  (`core-secrets-received.json`): a file that was there before, and a
  data dir that held data when a move imported into it (the core's
  original home), stay. The data dir goes only while no move and no core
  runs there and another device's core holds the lease. The owner's
  message lists the secrets, files and entries of the data dir removed,
  or why the data dir stays. New: `fridolin core forget-data`, `fridolin
  core files forget`, `"data_dir"` in `fridolin core status --json`,
  and `fridolin doctor --node`'s `node-core-leftovers` check, which warns
  on a former core host that still holds core data. A data dir imported
  by an older version is not in the record and stays: doctor says so,
  and docs/CORE.md, "Forgetting a former core host", says how to remove
  it by hand.

- **`!movecore`** (#187, third slice): in the owner's direct chat,
  `!movecore <node>` runs the move's pre-flight and answers with a
  plain report (source → target, the expected time offline, what does
  not work on the target, what becomes of the site) and a poll, Move now
  / Cancel. Only the owner's vote counts, the dashboard cannot answer
  it, no vote in 30 minutes is a no, a lockdown refuses it, and it
  answers 3 times an hour. "Move now" runs the same move as `fridolin
  nodes move --yes`; the owner hears when the core stops and, from the
  core that runs afterwards, how it ended. `!movecore` alone lists the
  core hosts and what keeps the core from moving to each;
  `!movecore <old host>` is the way back. After a move a second poll
  asks whether the machine the core left keeps the host secrets (no
  vote in 24 hours forgets them, `core.secrets.forget`), and revoking a
  connected core host has it forget them first. The move's report (and
  `POST /api/nodes/move-check`'s `site`) says whether the site's
  Cloudflare tunnel runs in the core and moves with it, and the new core
  checks that the domain answers. New audit kinds: `core_move_requested`,
  `core_move_approved`, `core_move_ended`, `core_secrets`
  (docs/CORE.md, "Moving the core from WhatsApp").

- **The core moves itself** (#187, second slice) between two native core
  hosts: `fridolin nodes move-check SRC DST` is the pre-flight (versions,
  image by digest, disk, the host secrets and the pin, the lease, what
  stays behind), `fridolin nodes move SRC DST --yes` prepares the target
  while the core runs (image, secrets, a rehearsal on a scratch volume),
  then hands a move plan signed with the core's update key to both
  agents, which stop, export, import, promote and start through the
  lease and the exports alone, and roll back by themselves when the
  target is not ready in time. `fridolin nodes move-status ID`, `POST
  /api/nodes/move-check`, `POST /api/nodes/move`, `GET
  /api/nodes/moves/{id}`. New core commands: `core status --json`,
  `core export --move ID --json`, `core release`, `core pull --from
  REF@sha256:…`, `core rehearse`. Exports now write their info beside
  the archive (`core/exports/<time>.json`: the data dir's device id, the
  move, the size; no data), and their manifest names the device id and
  the move (docs/CORE.md, "The core moves itself").
- **`timezone`**, your IANA time zone (docs/CONFIGURATION.md, "Time
  zone"; Settings → Advanced). Scheduled agents, standing intents, the
  memory consolidation, nightly snapshots and the daily backup run at
  its times of day, and brains are told the time in it and run with it
  as `TZ`. Unset, the bridge's own zone stands in. `fridolin init`
  writes the machine's zone; `fridolin core start`, `run` and `update`
  pass the host's zone into the container as `TZ` (`FRIDOLIN_TZ`
  overrides it). The Schedules page names the zone, and `fridolin
  doctor` warns when it is unset and the bridge runs in UTC.
  `GET /api/schedules` has `timezone: {name, source}`.
- **Command agents bring their program to the node** (#213): a node
  that names folders in `exec.accept_from_core` (node.json) takes the
  program of a command agent you approve, when it lies in one of them, as
  a rule of exactly its arguments; deleting the agent removes it, unless
  another agent runs it. The core sends each node the whole list,
  `exec.programs`, signed with its update key; the node refuses shells,
  interpreters, anything outside the folders with symlinks resolved, and
  checks again before every run (docs/NODES.md, "Programs from the
  core"). The Nodes page shows the folders, the programs added and for
  which agents, and what the node refused and why; the Schedules page and
  the wizard say how each agent's program stands on its node, and
  `fridolin doctor` warns about approved command agents whose node does
  not allow their program. Nothing changes for a node without
  `accept_from_core`; its folders wait for your approval on the Nodes
  page like any other new rule.
- **Files and Wiki, phase 2** (#192, #194; docs/FILES.md, "Changing
  files"): edit a text or Markdown file, or a wiki page, in place;
  upload into a folder; make a file; rename, move and delete (into the
  folder's `.fridolin-trash`), each after asking where it matters, on
  the dashboard and, for a site admin, on the site. Every change is an
  audit line of the new kind `file_change`, listed as the file's
  History, and is committed where the brain's folder is a git
  repository (git in the brain's sandbox, hooks off). A brain's
  `files.uploads` in `brain.json` names folders that take uploads from
  the site roles you let upload (Site → Files and Wiki, *May upload
  to*; `role_uploads` in the site's settings), and whether an upload
  lands there or goes to the brain like a file sent on WhatsApp. A
  file's page has *Ask the brain* (a WhatsApp chat with the question
  begun) and copies its link on your site.
- **Old site addresses**: `modules/redirects.json` in the site
  repository sends paths that moved, `/wiki-<x>` say, to their new
  place (docs/SITE.md, "Old addresses").

- **Core export says when a brain folder is not as you approved it**
  (#189): `core export`, `--list-folders`, `fridolin doctor` (new check
  `brain-drift`) and `core send` print a warning for each brain whose
  `brain.json` or `.brain/schedules/` files on disk differ from the
  approved manifest, naming the `backup.exclude`, `backup.enabled` and
  `backup.targets.cloud` changes. The export refuses while a `brain.json`
  on disk keeps more of its folder home than the approved manifest:
  approve the change first, or pass the new `--use-approved` to take the
  folder along by the approved one. `core export --drift` gives the
  warnings alone. Scripts that run `core export` should expect exit 1
  for such a refusal.
- **Voice mode for the Plaud Sync app** (docs/PLAUD.md, "Voice mode"):
  with `plaud.voice` on and the app's switch on, a NotePin recording or
  a question spoken into the phone goes to `plaud.voice_brain` (default
  the DM brain), and the phone reads the answer aloud. The answer is
  encrypted for the phone, so the site only passes it on. It also goes to
  WhatsApp, after the question, unless `plaud.voice_quiet`. New site
  routes `/_fridolin/devices/voice/ask` and `/_fridolin/devices/voice/reply`.
  The app needs a new build from the dashboard.
- **core-host** (#187, first slice): a node may offer to be a core host
  with `fridolin node core-host on` on the machine, which writes
  `core-host.json` in its state dir, and the owner's approval
  (`!approve`, the Nodes page, `fridolin nodes approve ID --commands`),
  which says what it means. Every change of that file waits for the
  approval again. `fridolin nodes core-secrets-transfer SRC DST [--yes]`
  moves the host secrets from one core host to another over the node
  channel, end to end, to a target device key the source pinned with
  `fridolin node core-host trust` (a dry run without `--yes`).
  `!fleet`, `fridolin nodes` and the Nodes page show it
  (docs/NODES.md, "core-host").
- **One file per scheduled agent** in the brain's new `.brain` folder:
  `<brain>/.brain/schedules/<name>.json`
  ([schema](docs/schemas/schedule.schema.json), docs/BRAINS.md, "The
  .brain folder"). Each file is approved on its own on the Schedules
  page, with what the agent may do shown first; a new, changed or
  removed file runs, or stops, only once approved, and the approved
  version runs on meanwhile. Agents added or edited on the Schedules
  page are written and approved at once. `brain.json`'s approval leaves
  the agents aside. `GET /api/schedules` gives each agent its `state`
  (`pending`, `active`, `paused`, `failing`, `disabled`), `file`,
  `risky` and pending `review`, and counts `pending`; `POST
  /api/schedules/{brain}/{name}/approve` approves a file as reviewed,
  and `GET /api/schedules/preview?when=` says when a schedule runs next.
- **The Schedules page, redone**: agents grouped by brain with filters,
  a state badge each (Pending approval, Active, Paused, Failing,
  Disabled), the next run as relative and absolute time, the last run's
  result and duration, and what each may do; agents waiting for
  approval come first with Review & approve inline (Approve & run now
  too), and the Overview page flags them. *Add agent* is a six-step
  wizard (template, brain, when with the next three runs, vars with a
  GitHub repository picker, permissions in plain words, the exact file);
  agents are edited and duplicated through it, and deleted after asking.
- The Schedules page's reminders now include those a brain keeps itself
  in `.brain/reminders.json` or `skills/reminders.json`, as a remind
  skill writes them; only the bridge's own standing intents were listed,
  so a brain with its own reminders showed none.

- **Box updates** (#191 phase 3): the core updates a node in a box to
  the images of its own version, pinned by digest and signed with its
  update key, from the Nodes page or by auto update. The agent only keeps
  the request; the box's updater on the host (installed by `box.sh` and
  `box.ps1`, every hour as the owner; `--no-updater` leaves it out)
  checks it in a throwaway container of the current image, with the
  images' build provenance, pulls, recreates, and goes back to the images
  of before when the node does not connect again within 5 minutes. No
  service gets the Docker socket. The node service's health check is now
  `fridolin node health` (docs/NODES.md, "Box updates").
- **Node in a box** (docs/NODES.md, "Node in a box"): a machine joins as
  two containers, `node` (the `fridolin-core` image running `fridolin
  node run`) and `fride` (FrIDE's companion), with Docker and one line
  from `!join`, on Windows, macOS and Linux alike. `container/box.sh` and
  `box.ps1` pin the images by digest, write the compose file
  (`fridolin box compose`), sign the box in to the tailnet as its own
  device (tsnet: `fridolin node tailnet`, `fridolin node join --box`),
  join, start it and pair FrIDE; `--update` moves a box to a new
  version. No service gets the Docker socket, and `fride` never mounts
  the node's volumes. A core whose image knows where its box script is
  published (CI's, or `fridolin core build` of a clean GitHub checkout)
  puts a box line first in its `!join` reply, and the join-code API has
  `box_command` and `box_command_powershell`. `fridolin node fride pair
  --code -` reads the pairing code from stdin.
- CI publishes the core's image to GHCR, `ghcr.io/OWNER/fridolin-core`,
  for amd64 and arm64, tagged with the version `fridolin version`
  reports, with build provenance (GitHub's attestations) and an SBOM, on
  every push to `main`. A new image, `ghcr.io/OWNER/fridolin-fride`,
  holds FrIDE's companion with Claude Code, Go, Node, git and ponytail,
  for the `fride` service of a node in a box (docs/CORE.md, "On GHCR";
  docs/NODES.md, "Node in a box"). The images CI built before carried
  the commit's full hash as their version; they now carry the short one
  of `git describe`, as local builds do.
- **Connect GitHub** (Dashboard → Integrations → GitHub, #202) makes a
  private GitHub App of this Fridolin's own through GitHub's manifest
  flow, with no webhook: contents read, issues write, metadata read, and
  pull requests write when asked for. GitHub then asks which repositories
  it may use. A run whose message carries a GitHub grant (a scheduled
  agent's) gets an installation token for just its repositories and
  permissions, as gh's login in a folder of its own (`GH_CONFIG_DIR`;
  never in its environment), revoked when the run ends. The app's key
  goes to `secrets/github-app.age` in the core's volume (encrypted to its
  age identity) or, on a native install, to the host's secret store as
  `fridolin.github.app`; never to `config.json`. A typed token,
  `FRIDOLIN_GITHUB_TOKEN` or the host secret `fridolin.github.token`
  (which `core start` now passes, and `core send` carries), is the second
  choice (docs/GITHUB.md).
- **Files and Wiki**, in the dashboard and on your site (docs/FILES.md).
  The Files page browses every folder Fridolin is linked to — each
  brain's folder and media inbox, and the folders approved nodes lend —
  with search by name, and opens Markdown (rendered), text, images, PDFs,
  audio and video, or downloads the file. The Wiki page reads each
  brain's `wiki/` folder and memory notes as pages, with `[[links]]`,
  backlinks, a table of contents and search. Both only read; every name
  starting with `.`, secret files, what a brain's `backup.exclude` leaves
  out and Fridolin's own state are never shown, no symlink is followed,
  and every view is written to the audit log (`file_view`). Markdown is
  parsed into a tree of a few element names and built with DOM calls,
  never HTML.
- The site serves `/files` and `/wiki` to signed-in accounts, for the
  brains their role is given under Site → *Files and Wiki* (`admin`: all),
  and runs without a repository of modules, with those pages alone.
  `wiki.<domain>` and `files.<domain>` that reach the site redirect to
  `<domain>/wiki` and `/files`. A module whose route is `/files` or
  `/wiki` is no longer loaded: rename its route.
- The Site page walks through linking a domain: the domain, a Cloudflare
  tunnel that Fridolin runs (where to create it, the token to copy, the
  public host name to add) and a check that `https://<domain>` answers.
  docs/SITE.md has the same steps under "From zero to your domain", and
  `examples/site-module/` is a starter module to copy into a repository.
- Scheduled agents (#178): a brain runs a task by itself on a schedule
  (`every 6h`, `daily 07:00`, `weekly mon 09:00`, or cron), or the bridge
  runs a program on a node, defined in the brain's `brain.json` under
  `schedules` (or with the task in its `schedules/` folder), so a schedule
  a brain adds itself waits for your approval. Runs go through the
  brain's queue, sandbox, risk levels and approvals; an agent never runs
  twice at once, runs once after the bridge was down (or skips, with
  `"missed": "skip"`), and is jittered. Results go to a chat always, only
  when worth reporting, or nowhere. The dashboard's new Schedules page
  lists the agents with their next and last run, runs, pauses, resumes
  and edits them, and shows each one's run history (with cost or tokens
  and the message sent) and each run's log; the reminders (standing
  intents with a time) are listed there too. `fridolin schedules
  list|run|pause|resume|logs` does the same from the shell, and
  `/api/schedules` serves it (docs/API.md). The history and logs are in
  `schedules/` in the data dir, kept within the new `schedules` settings
  (`keep_days` 30, `keep_runs` 100, `max_total_mb` 200, `max_log_kb`
  1024) by the housekeeping job, and in the vault's nightly snapshot.
  Built-in templates: `memory-consolidation`, `morning-briefing` (today's
  appointments each morning), and three daily code reviews of GitHub
  repositories, `code-review-security`, `code-review-bugs` and
  `code-review-simplify`, which file issues for real findings
  (docs/BRAINS.md, "Scheduled agents").
- `memory.consolidate` now runs as the scheduled agent
  `memory-consolidation`, meaning what it did: the same skill on the same
  schedule, its answer in the reply chat, counting on from the last run
  an older bridge recorded. Editing it on the Schedules page
  moves it into `schedules`; a bridge from before this version then no
  longer runs it, as it does not know `schedules`.
- GitHub access per run: a scheduled agent's `github` field
  (`{"repos": [...], "permissions": {...}}`) becomes the run's GitHub
  grant (`chat.GitHubGrant`, as Connect GitHub has it): as the run
  starts, the GitHub App mints a token for exactly it, which the run alone
  gets as gh's login (`GH_CONFIG_DIR`, never in its environment) and
  which is revoked when it ends; without an app, the typed
  `fridolin.github.token` (docs/GITHUB.md).
- Claude's and Codex's reports of what a run cost (dollars, tokens) are
  read from their result; scheduled agents' runs record them.

- `fridolin core apparmor` (Linux, as root, once) installs an AppArmor
  profile for the core container, `fridolin-core`, and `core start`,
  `update` and `run` use it on a host with AppArmor
  (`compose.apparmor.yaml`). On Ubuntu 23.10 and newer the brains' sandbox
  could not start in the container at all, `apparmor=unconfined`
  included, since the kernel confines unprivileged user namespaces
  (`kernel.apparmor_restrict_unprivileged_userns`); the profile allows
  them for the core alone (docs/CORE.md, "The brains' sandbox in the
  container").
- Moving the core takes the brain folders along. `fridolin core export`
  carries a brain folder bound in from the machine's home (`~/Personal`)
  as far as the brain's backup settings let it reach the cloud: what
  `backup.exclude` leaves out, secret files, virtualenvs, `node_modules`
  and links stay home. The import puts it into the volume where the
  folder is not there (`brain_dirs` gets `moved_from`), and the move back
  brings the brain's changes home, keeping every file it replaces in
  `core/replaced-<time>/folders/` and deleting nothing. Exports with
  folders are format 2, which an older fridolin refuses to import
  (docs/CORE.md, "Brain folders on another machine").
- `POST /otp/take` hands a script the one-time code the OTP guard caught
  for its request, by the `otp_request` id `/send` answered with, once:
  the code is deleted as it goes out, and a request with no code waiting
  (unknown, not answered yet, expired or taken) gets `404`. Only the
  scripts token takes a code, never a brain's run token or a call
  without a token, and the route is not served on the tailnet. Scripts
  on the computer of a core in a container get their codes this way,
  since they cannot read `otp/` in its volume (docs/API.md).

- Joining a node needs no dashboard token on the new machine. New chat
  commands, for the owner (a number in `allowed_numbers`) in their direct
  chat only, audited: `!join` replies with a single-use join code (10
  minutes) and the lines to paste for sh and PowerShell; `!dashboard`
  with a sign-in link that works once within 10 minutes, at the core's
  tailnet address and on the core machine; `!approve <id>` approves a
  node's device and what it offers in one go, and `!revoke <id>` shuts it
  out. `!join` and `!dashboard` answer 5 times an hour each. When a
  machine joins, the bridge tells the owner in their chat.
  `fridolin open --print --tailnet` prints the tailnet sign-in link
  (`POST /api/login/code?tailnet=1`). `!fleet` now suggests `!approve`
  (docs/NODES.md, "Joining a machine").
- Notifications on Windows nodes are real toasts now instead of a log
  line. The node service, which runs in session 0, shows the toast in
  the signed-in user's session. When nobody is signed in it logs the
  notification and answers `shown: false` with a `reason`, a new field
  of `notify`'s answer (docs/NODES.md, "Notifications on Windows").
- AGENTS.md path D and docs/NODES.md, "Setting up FrIDE on a node":
  a runbook for giving a node its own companion FrIDE. It runs natively
  on macOS and Linux, and in WSL2 on Windows, started by a Scheduled
  Task.
- The companion FrIDE's agents start with the ponytail plugin (MIT;
  minimal code, `/ponytail-review`, `/ponytail-audit`). The runbook
  clones it pinned to v5.3.2 into `~/fride-companion/plugins/ponytail`
  and lists it in FrIDE's `companion.claudePlugins`, which loads it as
  `--plugin-dir` for those agents only, with nothing installed into
  `~/.claude` (docs/NODES.md, "Ponytail for every agent"). A companion
  set up before needs a FrIDE with `claudePlugins` (FrIDE's pull request
  #8), then the clone, the marker file and the config entry from that
  section, and **Re-check** in FrIDE's Settings.
- Everything stored off the machine is encrypted twice, with independent
  keys, and the exports' asymmetric layer is post-quantum (age with
  ML-KEM-768 + X25519; docs/VAULT.md, "Encryption"): the cold tier is
  sealed per file (XChaCha20-Poly1305) inside rclone crypt, in
  `vault-sealed/`; the restic repositories on B2 and Drive sit behind
  rclone crypt (`restic-crypt/`, `fridolin-restic-crypt/`); a node
  target's new folder holds its repository in rclone crypt's format;
  Litestream replicates into `core/litestream-replica/`, which is uploaded
  sealed through rclone crypt (`litestream-sealed/`, far fewer B2 Class C
  calls than Litestream's own S3 replica); exports are sealed under a new
  export key, then encrypted to a new post-quantum identity; `core send`
  encrypts to a one-time post-quantum key. **New secrets, made by
  `fridolin core start` when missing — put them on your recovery sheet:**
  `fridolin.age.identity.pq`, `fridolin.export.key`,
  `fridolin.vault.seal.key`, `fridolin.snapshots.crypt.password`,
  `fridolin.litestream.seal.key`. `fridolin vault migrate-encryption
  --dry-run | --run [--delete-old]` copies what has one layer into the new
  stores, verifies it and, with `--delete-old`, deletes the old folders;
  until then the vault still reads them. `fridolin doctor`
  (`vault-encryption`) and `fridolin vault status` show each store's
  layers. compat: `fridolin.age.identity` (X25519) is no longer made, but
  while it is stored `core start` passes it on, and exports and node-target
  passwords made for it still open; a node target whose folder holds a
  repository from before keeps one layer until it gets a new, empty folder.
  `core send` and `core receive` must both run this version.

- The core updates its nodes' fridolin to its own version
  (docs/NODES.md, "Updates"): Nodes → Update to <version>, `fridolin
  nodes update <id>|--all`, or the core brain's `node_update` tool. A node
  agent of this version offers the new capability `update` (`"update":
  false` in `node.json` turns it off), which waits for `fridolin nodes
  approve <id> --commands`; a node that runs an older fridolin is updated
  by hand once, then approves `update`. The core image carries the
  builds in `/opt/fridolin/node-binaries/`; a native core builds them with
  `make node-binaries`. Binaries are signed by the core's new update key
  (`nodes/update.key`), which nodes pin as `core.pub`; the node keeps
  `<binary>.prev` and goes back to it when the new version does not
  connect within 5 minutes (`fridolin node rollback` by hand). Auto
  updates per node are off by default (`fridolin nodes auto-update <id>
  on`); `fridolin doctor` warns about nodes older than the core.
- Workstation mode for nodes (docs/NODES.md, "Workstation"): with
  `fridolin node workstation on --root PATH` (or `fridolin node join
  --workstation`) a node offers `agent`, a Claude Code or Codex session in
  a granted folder, and `terminal`, a shell there; approve it on the core
  like any capability. The core brain gets `node_agent_run`,
  `node_agent_status`, `node_agent_reply`, `node_agent_stop` and
  `node_terminal_open`, `_send`, `_read`, `_close`: a session's questions
  arrive in the chat, a step it says cannot be undone is an approval poll,
  and its end says what changed. The Nodes page has a **Terminal** button.
  Sessions run in `sandbox-exec` on macOS and bubblewrap on Linux, with
  Claude Code's own permission checks on Windows, and use the node's own
  login (the core's only with `use_core_login`, per session). Every session
  and every typed command line is in the audit log (`node_session`,
  `node_command`); a lockdown stops them; housekeeping stops the ones a
  restart of the core forgot. Off unless a node turns it on.
- Google Drive backups use your own Google OAuth client instead of
  rclone's shared one, which Google retires during 2026. Backup →
  Connect Google Drive signs in through the bridge (also in the core's
  container) with the client under Settings → Google sign-in, or one
  pasted on the Backup page for Drive alone; enable the Google Drive API
  and add the `drive.file` scope in its project, and add
  `http://127.0.0.1:8765/` to a Web application client's redirect URIs
  (docs/BACKUP.md, "Use your own Google client"). The link lives in a new
  folder, `Fridolin`, in your Drive, which holds `fridolin-backup/` and the
  vault's `fridolin-restic/` from then on. compat: a link made before keeps
  working until Google retires the shared client; the Backup page and
  `fridolin doctor` (`drive-client`) say so, and **Switch to your own Google
  client** replaces it, keeping the old link until the new one works. The
  folders the old link made stay in Drive until you delete them; Restore
  lists only the new folder. `POST /api/backup/connect` now answers with
  the `auth_url` to send the browser to and takes `{"reconnect": true}`;
  `PUT`/`DELETE /api/backup/drive-client` keep or forget the Drive client.

- A housekeeping job (`housekeeping` in config.json, docs/CONFIGURATION.md)
  looks every 48 hours for what the bridge and its children left running:
  processes of brain runs that are over, run MCP servers, the vault's
  `rclone rcd`, Litestream and restic without a bridge, activity viewer
  sessions nobody watches, zombies, restic locks of dead processes of
  this host, stale run records. It stops only the bridge's own; in the
  core's container a heavy process that is not the bridge's is put to
  the owner as an approval poll. It starts in dry run
  (`housekeeping.dry_run: true`, also on existing installs): it reports on
  the Overview page, in `fridolin housekeeping status` and `fridolin
  doctor`, and stops nothing until that is set to false. `fridolin
  housekeeping run [--dry-run]` runs it now.
- Dashboard → Integrations → Plaud → **Build phone app** works where the
  bridge has no JDK and Android SDK, as in the fridolin-core container:
  it sends `plaud-android/` (now in the image, at
  `/opt/fridolin/plaud-android`) and the build's settings to a node that
  builds Android apps, which builds it with Gradle in a temp folder,
  sends the APK back and deletes the folder, the developer keys with it.
  A node agent offers the new capability `android-build` (`android_build`)
  while it finds a JDK 17+ and the Android SDK (macOS and Linux;
  `"android_build": false` in `node.json` turns it off); it waits for
  `fridolin nodes approve <id> --commands` like any capability that
  grows. A bridge with its own toolchain keeps building on its own. The
  card says where the app is built, and how to add such a node when
  nothing can; `fridolin doctor` checks it (`plaud-app-build`). Update the
  node agents along with the core.

- Backups to folders on your nodes (#143): a node lends its disks with
  `fridolin node backup on` (or `node join --backup`), the owner approves
  it (`fridolin nodes approve <id> --commands`), and the Vault page's
  *Add target…* picks a folder on one of its drives. The core keeps an
  encrypted restic repository there, sent over the node connection and
  never through the cloud, on its own schedule and retention
  (`vault.targets`). Each `brain.json` chooses its targets with
  `backup.targets` and keeps paths out of the cloud but on the node
  targets with `backup.node_only`; without them nothing changes. `fridolin
  vault status`, `restore-drill --target`, `snapshot --target`, `check
  --target` and doctor's `vault-targets` cover them. Update the node
  agents along with the core to use it.

- The core brain's `fride_repo_add` adds a repository to FrIDE on a node:
  it clones one, or makes a new one with an initial commit and, by
  default, a private GitHub repository through the node's `gh`. The node
  does it in FrIDE's base folder (`fridolin node fride pair --base DIR`,
  else FrIDE's only base folder) with the new methods `fride.repo_clone`
  and `fride.repo_new`, which the `fride` approval covers, and FrIDE
  rescans at once. Update the node agents along with the core.

- Setting up another machine is now a runbook a coding agent follows
  (AGENTS.md: install a new fridolin, join this machine as a node, or
  make this machine the core), with the details in docs/NODES.md
  ("Joining a machine") and docs/CORE.md ("Moving the core to another
  machine"). `fridolin setup-machine --node|--core` shows what a machine
  still needs and the next step, and `fridolin doctor --node` (the
  default on a machine that joined a core and has no bridge config)
  checks Tailscale, the core's node port and the node service.
- On Windows the core's host secrets live in the Windows Credential
  Manager, and `fridolin core start|stop|update|run` work there with
  Docker Desktop (the compose file in `%LOCALAPPDATA%\fridolin`).
  `fridolin core secrets` lists the host secrets and `fridolin core
  secrets set NAME` stores one, typed without echo, on every system.
  `fridolin core build` builds the core's image from a checkout, tagged
  as `core start` expects it.
- `fridolin core receive` and `fridolin core send --to ADDRESS` move the
  host secrets to the machine that takes the core over, directly over the
  tailnet: both ends must be devices of the same Tailscale login, the
  sender proves a one-time code, and the person compares four words on
  both screens before anything is sent, encrypted to a key that exists
  only for that transfer.
- `fridolin nodes join-code` (and the dashboard's join dialog) prints the
  line to paste for PowerShell too, and names the core by its full
  MagicDNS name once it is on the tailnet
  (`fridolin-core.<tailnet>.ts.net:8766`); `POST /api/nodes/join-code`
  answers with `core` and `command_powershell` as well.
- `fridolin core migrate --from-native` moves a native install into the
  core's container: the brain folders, the calendar credentials and what
  else of your home folder the config names stay on the Mac and are bound
  into the container at their own paths (`compose.host.yaml`), with
  `HOME` the Mac's home folder, so every path keeps working; it copies the
  launchd agent's Claude token into the keychain, exports the data dir
  read-only (`--local` through a local folder instead of the vault
  bucket), imports it and prints the cutover and the rollback.
  `--dry-run` changes nothing, `--rehearse` runs the whole move on a copy
  beside the running bridge, and `--to-native` brings the core's state
  back into `~/.fridolin` (what was there moves to
  `~/.fridolin/migration/<time>/`). `fridolin check --try-sandbox ID`
  shows what a brain's sandbox sees, and `fridolin check --try-sandbox ID
  -- PROGRAM ARGS...` runs a program there. The image now has the Codex
  CLI, the Plaud CLI, gh, cloudflared, age, jq and sqlite3, and a pinned
  Python venv first on PATH with local transcription (torch on the CPU,
  transformers), Google's API client and yt-dlp; scripts that call
  `~/.local/bin/gh` and the like find the image's tools there
  (`FRIDOLIN_HOME_BIN`). See
  [Moving a native install into the container](docs/CORE.md#moving-a-native-install-into-the-container).

- Policy and approvals (M5). Skills take a frontmatter in the portable
  Agent Skills format (`skills/<name>.md` or `skills/<name>/SKILL.md`),
  with `risk`, `approve`, `vault` and `triggers` under
  `metadata.fridolin`; the starter skills have one. What applies is the
  policy you approve on the brain page's new *Skills* section: a skill a
  brain writes or changes counts as read-only until you do.
  `request_approval` now asks you with a WhatsApp poll (Yes / No / Show me
  the draft) that only your own numbers can answer, no answer within
  `policy.approval_timeout_hours` counting as no; `ask_user` asks a
  question; `check_approval` waits again. Claude runs get
  `--allowed-tools mcp__fridolin`, so these tools work without
  `dangerously_skip_permissions` too. Every run's prompt now says to
  ask before any irreversible step. `!lockdown` and `!unlock` (owner
  only, and on the dashboard) pause all but read-only work. A chat starts
  at most `policy.max_runs_per_hour` runs an hour (20), which applies to
  existing setups too. The audit log (`audit/YYYY-MM.jsonl`) has one
  schema for runs, tool calls, approvals, votes, delegations and
  lockdowns; the dashboard shows it on new Threads, Approvals and Audit
  pages. `fridolin doctor` warns about approvals waiting over an hour and
  shows a lockdown. See
  [Skills, risk and approvals](docs/BRAINS.md#skills-risk-and-approvals).

- Exports (`fridolin core export`) copy a brain's Codex `*.sqlite`
  databases with `VACUUM INTO` like the bridge's, and leave a native data
  dir's `migration/` folder out.

- The core runs in a container: `container/Dockerfile` builds the
  `fridolin-core` image (the bridge, the Claude CLI, Python, ffmpeg,
  restic, rclone, Litestream, bubblewrap; tini as PID 1; user `fridolin`),
  `container/compose.yaml` runs it on the named volume `fridolin-data`.
  `fridolin core start | stop | update | run` drive it from the host with
  the secrets from the macOS Keychain (0600 files on Linux);
  `fridolin core export | import | promote | status` move it between
  machines through an age-encrypted export in the vault bucket and the
  core lease. `fridolin run --container` binds the dashboard's port for
  Docker's port mapping. See [docs/CORE.md](docs/CORE.md).
- Nodes (docs/NODES.md): other machines join the core with
  `fridolin nodes join-code` and `fridolin node join <code> --core
  host:port`, and serve what you approve, in two stages (`fridolin nodes
  approve <id>`, then `--commands`): allowlisted commands without a shell,
  files under granted folders, host facts and notifications. The node
  agent runs on macOS, Linux and Windows and installs itself as a service
  (`fridolin node install|start|stop|status|uninstall`). The dashboard has
  a Nodes page, `!fleet` lists the nodes in a direct chat, and `fridolin
  doctor` checks the node port and how recently each node was heard from.
  `nodes.enabled` and `nodes.listen` now wait for a restart.
- A memory index per brain (`memory/<id>.db` in the data folder): SQLite
  full-text search over `memories/`, `conversation-history.jsonl` and
  `transcripts/`, each piece labelled by where it came from (owner, agent,
  untrusted, system). `memory.inject` in a brain's `brain.json` (off by
  default) puts the owner's messages and the brain's own notes that match
  a new message into its prompt, with their sources; never what anyone
  else wrote. Brains search all of it with `fridolin memory search`, and
  `fridolin memory reindex|status|search` work on the indexes from a
  shell. `fridolin doctor` checks the index for brains that use it. See
  [Memory](docs/BRAINS.md#memory).
- Standing intents: a brain turns "remind me on Friday at 9" or "tell me
  when Sam mentions the boat" into `fridolin intent add`, which the bridge
  fires; only the owner's own direct messages may set one. The Brain page
  lists them. See [Standing intents](docs/BRAINS.md#standing-intents).
- New brains get daily notes and `memories/core.md` in their starter
  prompt, notes comments for trigger phrases, importance and sources, and
  `skills/memory-consolidation.md`, which `memory.consolidate` (for
  example `"weekly sun 18:00"`) runs on a schedule. Existing brains keep
  their files; copy the skill from `examples/skills/` to use it.
- The core joins your tailnet as a device of its own with
  `core.tailnet.enabled` (embedded Tailscale, no Tailscale on the host)
  and serves the dashboard there under its MagicDNS name, HTTPS when the
  tailnet issues certificates. The dashboard token is always required
  there, and the caller's tailnet identity is logged and audited.
- With `core.lease.enabled` the bridge takes the core lease before it
  starts its channels and exits with status 3 when another device holds
  it; a core that loses the lease stops WhatsApp and outbound sending and
  says so on the Overview and in `/ready`; a copy that was taken over
  stays down until `fridolin core promote`.
- In the container each brain's run is sandboxed with bubblewrap as a user
  of its own: only its folder, state and inbox are there, nothing else of
  the volume. `sandbox.enabled` is allowed (and on by default) in the
  container; `compose.yaml` drops every capability and adds a seccomp
  profile. `fridolin doctor` checks the tailnet, the lease and the
  sandbox there.
- FrIDE through nodes (docs/NODES.md, FrIDE): `fridolin node fride pair
  --url http://127.0.0.1:PORT` pairs a node with FrIDE running as `fride
  serve --under-fridolin`, and the node offers the capability `fride`
  while FrIDE answers. With nodes on, the core brain hands every change to
  code to FrIDE (`fride_task`, `fride_status`, `fride_reply`,
  `fride_move`, `fride_close`; its prompt says so), placed on a node by
  name or tag and FrIDE's memory check, or waiting until one can take it.
  FrIDE's questions and news arrive in the chat that asked; `!move
  <thread> <node>` moves a task through the vault; closing one archives
  its workspace into the vault as `vault/workspaces/<task>.fride.tar.gz`. The
  Nodes page shows each node's FrIDE, and `fridolin doctor` warns about a
  node whose FrIDE does not answer. `!help` lists `!move`.

- Settings → AI mode switches new brain runs between Claude (default) and
  Codex. Codex defaults to `gpt-6.1-sol` with high reasoning effort, with
  separate model settings, isolated per-brain sign-in, streamed activity,
  the existing sandbox and cancellation, and Fridolin site-builder tools.
  See [AI mode](docs/CONFIGURATION.md#ai-mode) for sign-in and the
  Claude-specific settings that do not transfer.
- `fridolin doctor` checks this machine: config, data dir and free disk,
  the Claude CLI and the login sandboxed brains need, and what the coming
  milestones need (Tailscale and its auth key, B2 keys, Docker, restic,
  rclone, ffmpeg, litestream, age), each line with a fix; `--json` for
  scripts. See [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

- config.json takes the sections `core`, `vault`, `nodes` and `policy`
  for the containerized core, the B2 vault, nodes and approvals. All are
  off by default and nothing uses them yet; the bridge checks their
  values, and the dashboard's Settings keeps them when it saves. See
  [Core, vault and nodes](docs/CONFIGURATION.md#core-vault-and-nodes).
  The config version stays 2.
- A core brain and grants: `brain.json` takes `"role": "core"` on one
  brain and, on it only, `grants` per other brain (`delegate`, `read`,
  `files`), edited on the core brain's page under *Other brains*. Every
  run now starts Fridolin's own MCP server (`fridolin mcp --run <id>`,
  passed with a second `--mcp-config`, or Codex's `mcp_servers.fridolin`),
  which serves the tools the bridge gives that run: `delegate` and
  `list_brains` for the granted brains on core runs, `read_memory`, and
  `ask_user` / `request_approval`, which say they are not available yet.
  A delegated task runs in the sub-brain's own queue and sandbox, its
  answer goes back to the core only, and each delegation is a line in
  `<data_dir>/audit/YYYY-MM.jsonl`. See
  [the core brain](docs/BRAINS.md#the-core-brain-and-its-grants). Without
  a core brain nothing changes but the tools every run is offered.

- The tiered vault (docs/PLAN.md, M3), with `vault.enabled`: the data
  dir's files (attachments, recordings, documents, `secrets/`, `audit/`)
  are uploaded, encrypted with `rclone crypt`, to the B2 bucket
  `fridolin-vault` and evicted from this machine by policy and budget (a
  recording once its transcript exists); folders with cold files carry a
  `.cold.json`; runs get the tools `vault_ls`, `vault_get`, `vault_put`
  and `vault_search` and two prompt lines about them. restic snapshots the
  hot tier to `fridolin-snapshots` (nightly, hourly for brain folders and
  `audit/`, kept 24 hourly / 14 daily / 8 weekly / 12 monthly, checked
  weekly and 5% of the data monthly), Litestream replicates `bridge.db`
  and `vault.db` (which puts them in WAL mode), and a weekly `restic copy`
  goes to a second repository on Google Drive. New: the dashboard's Vault
  page, `fridolin vault status|sync|evict|get|rebuild|restore-drill`, and
  four `fridolin doctor` checks that apply with the vault on. A native
  install makes the vault's crypt passwords and restic's password in the
  keychain when they are missing and the bucket is still empty, and says
  to put them on the recovery sheet. See [docs/VAULT.md](docs/VAULT.md).
  Off by default: without `vault.enabled` nothing changes.

### Changed

- **Remaking a FrIDE instance on Windows no longer stops at a symlink
  in a repository.** The repositories, workspaces and settings are carried
  out as a tarball made inside the VM and unpacked inside the new one, so
  the node never creates a symlink on the host. A carry folder an older
  version left is still put back.

- **`fridolin doctor --node` says the next step, like `setup-machine
  --node`.** Both print the node machine's checks the same way now: a
  `Node machine (state dir …)` line, the checks, and `Next:` with the
  first fix to take (`next` in `--json`). The runbooks ask for `doctor
  --node` alone.

- **Every node is a core host, and moving the core is one vote.** A
  node no longer needs `fridolin node core-host on`, an approval of
  core-host, or a `core-host trust` pin on the machine the core runs on:
  every approved node may take the core unless `fridolin node core-host
  off` ran there. `!movecore <node>` says when the machine never held
  the core, and the owner's "Move now" is what lets it hold the secrets:
  the core then names the target's device key, the one it approved at
  the join, to the source, which checks the target's signature against
  it. Only that WhatsApp vote confirms a new core host: `fridolin nodes
  move`, `POST /api/nodes/move` and `core-secrets-transfer` refuse an
  unconfirmed target, so a stolen dashboard session alone still cannot
  send the secrets to a new machine. Core hosts approved before count as
  confirmed; pins still work and win over the core's key. `!approve`
  and the Nodes page no longer ask about core-host. Update the source
  node too (`fridolin nodes update ID`): an older one sends to pinned
  targets only, and the pre-flight says so.

- **Several approvals at once, in one poll.** With several things
  waiting, `!approve` (and `!deny`) alone sends a multi-select poll: "N
  things wait for you. Tick the ones you approve." Each tick approves
  that one, and one message per vote says what the vote approved; taking
  a tick back undoes nothing. A message under the poll lists the ref of
  each option, for `!approve <ref>`. Ticked ones that ask for a second
  `!approve` (unexpected, waited over a day) are listed together, and one
  `!approve` within 2 minutes approves them all. Only the owner's vote
  counts, as before; with one thing waiting nothing changed.

- `!approve` without an id no longer answers with its usage: it approves
  what waits. The node alerts' and `!fleet`'s wording changed (pinned in
  `messages.golden`), and `nodeproto.CoreHostApproval` reads plainer.

- **The Plaud Sync app lets the NotePin sleep** (docs/PLAUD.md, "The
  pin's battery"). A permanent Bluetooth link kept the pin awake and
  emptied its battery fast. The app's new **Sync** setting is **smart**
  by default: it connects only to fetch and lets go afterwards. It checks
  in every 10 minutes, and at once when the app is opened, the phone is
  plugged in or unlocked, or the pin comes back in range. It stays
  connected while the pin records, so the stop is seen at once. **Every N
  minutes** checks in only on its timer and when the app is opened.
  **Instant** keeps the old permanent link. With voice mode on it checks
  in at least every 2 minutes, and **Fetch now** (in the app and the
  notification) fetches at once. A pin out of range is tried less and
  less often, up to an hour apart. Rebuild the app (Integrations →
  Plaud → Build phone app) and install it over the old one. It keeps
  its pairing, and **Unpair** now connects to the pin itself.
- **Approving `fride` says what it is**: arbitrary execution on that
  machine, whatever `exec` allows. The Nodes page shows it while `fride`
  waits (`fride_approval` in `GET /api/nodes`), `!approve` adds it to its
  answer, and `!fleet` marks nodes with `fride` approved
  (docs/NODES.md, "FrIDE").
- **Signing in to a container core.** The dashboard's sign-in page names
  the steps for the way the bridge runs: for the core in its container,
  `docker exec fridolin-core fridolin open --print` and `!dashboard`,
  and the token in the volume (`/data/dashboard.token`) rather than
  `~/.fridolin/dashboard.token` on the host, which is not that bridge's.
  `fridolin open` on a machine whose core runs in the `fridolin-core`
  container asks the container for the link and opens it, so the old
  instruction works there too (`--config` keeps it native). `/ready`
  has a new field, `container` (docs/CORE.md, "Signing in to the
  dashboard").
- **The core image has an SSH client** (`openssh-client`). The site's
  "Check for changes" fetches the site repository over SSH with its
  deploy key, which failed in the container with `ssh: not found`.
- **Scheduled agents move out of `brain.json`** (compat): a bridge
  starting up moves each entry of a brain's `brain.json` `"schedules"`
  into its file, keeping the rest of `brain.json` as it was, and still
  reads entries left there (a file of the same name wins). Nothing
  approved changes. Going back to a version before this one, the bridge
  runs the agents you approved (from `config.json`) but does not see the
  files, and shows `brain.json` as changed on disk.

- The vault and the core lease spend a fraction of Backblaze B2's capped
  transactions: an idle core went from about 40,600 Class C and 7,600
  Class B a day (the free tier caps each at 2,500) to about 460 and 760.
  The cold tier and the lease each talk to one long-lived `rclone rcd`,
  which logs in to B2 once a day instead of once per call; Litestream
  compacts hourly and daily and syncs each minute instead of listing its
  replica every few seconds; the cold tier is probed only when nothing
  else showed it answering for 30 minutes, and the manifest goes up at
  most every 10 minutes. `core.lease.renew_seconds` and `stale_seconds`
  now default to 300 and 1200 (a config that sets them keeps its
  values). The day's transactions per class are on the Vault page, in
  `fridolin vault status` and in `fridolin doctor` (`b2-transactions`),
  which warns past 70% of the new `vault.b2_daily_budget` (default 2000)
  and fails when B2 refused a call for a cap. A refusal that a successful
  B2 call came after (the caps were raised since) only warns, so doctor
  passes again as soon as the calls do. See
  [docs/VAULT.md](docs/VAULT.md#b2-transactions).
- The core lease no longer stops WhatsApp when its store does not
  answer (a used-up B2 cap, the network, a 5xx): it is *unverified*,
  shown on the Overview, `/ready` (still ready) and doctor, retried in
  the background, and the channels stop only when the store shows
  another device holding the lease. A core that held the lease last also
  starts while the store cannot be read; one that never held it still
  refuses. See [docs/CORE.md](docs/CORE.md#the-core-lease).
- compat: with the vault on, the Drive backup's daily sync stops once the
  vault's weekly `restic copy` to Drive (`<remote>:fridolin-restic`) has
  completed once; until then, and with the vault off, it runs as before.
  *Back up now* and restores from `fridolin-backup/` keep working.
- `fridolin core start` also writes `seccomp.json` beside the compose
  file, which the container now needs; a compose file of your own
  (`--compose`) needs one beside it too. Building Fridolin needs Go
  1.26.6 or newer (tsnet).

- Fridolin now builds for Windows (amd64 and arm64, without cgo). It is
  not supported there yet: there is no service and no sandbox
  (`sandbox.enabled` is refused, as on Linux), and a crash leaves runs
  going; docs/DECISIONS.md lists what else does less. macOS and Linux
  behave as before.

- SQLite is now the pure-Go `modernc.org/sqlite`, so building Fridolin no
  longer needs a C compiler (`make check`'s race tests still do). An
  existing `bridge.db` opens as it is: the WhatsApp pairing, polls and
  queued messages carry over, times are written in the format the old
  driver wrote, and a locked database is still waited on for 5 seconds.

- Configuration rejects unknown log levels and chunks larger than 65536
  characters. `check` identifies disabled dashboards and unavailable brains.
  Config backups are published atomically without following backup symlinks;
  concurrent starter creation preserves the first complete file. Instance-lock
  errors preserve their operating-system cause and identify the recorded owner.

- Plaud retry-exhaustion notices keep raw provider errors on the dashboard
  instead of sending stderr into the destination chat.

- Saving restart-only data, database, media or listener settings now keeps all
  running components on their startup paths until restart. Backup and OAuth
  files, Plaud state, OTP handoffs, attachment inboxes, brain/site state and
  tokens no longer move early. The sandbox and folder browser keep protecting
  the running data. Provider, queue and explicit credential/Plaud options
  continue to apply immediately.

- Brain folder workflows now live in the registry package. Dashboard responses,
  approval checks and transactional manifest/config writes remain unchanged.

- Configuration resolution uses explicit home, default-directory and platform
  inputs, captured when its store opens. Later saves keep those inputs even if
  the process environment or default folders change. Loading and saving still
  create no runtime data directories; `init` retains its intentional starter
  config write.

- **In an allowed group, only the numbers in `allowed_numbers` reach the
  brain now**, unless the group has a `group_members` entry. Before, every
  member of an allowed group could start a run with all of the brain's
  permissions. What the others write is no longer read at all: no run, no
  `!stop`, no download, and nothing in the history but their votes in the
  bridge's polls. If other people in your groups should still be heard,
  pick **Members** next to each group under Chats → Allowed groups once
  the new version runs, or add an entry to `config.json` as you upgrade,
  right before `./setup.sh` (an older bridge ignores the key, but its
  dashboard drops it when it saves): `"group_members": {"<group JID>":
  ["*"]}` keeps the old behaviour, or list their numbers. The log names
  every allowed group without an entry at startup. Nothing changes while
  `allowed_numbers` is empty and `allow_any_dm` is `true`.
- The bridge's Go code is in `cmd/fridolin`. `./setup.sh` and
  `make build` build it as before; a build by hand is
  `go build -o bin/fridolin ./cmd/fridolin`. `go build .` at the
  checkout's root now fails with "no Go files" and leaves an existing
  `./fridolin` as it was. A build stamps the version `fridolin version`
  prints with `-ldflags "-X github.com/Icewater1337/fridolin/internal/version.version=v1.2.3"`;
  `-X main.version` no longer reaches it.
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
- In dev mode, a group message from the **Dev** page or
  `/api/dev/inbound` passes `group_members` as one from WhatsApp does: from
  a member the group's entry does not admit, or with no sender, it is
  ignored, and the Dev page says why instead of "Message injected".
- `fridolin check`, `fridolin open` and `fridolin rotate-token` fail when
  there is no config file where they look, a mistyped `--config` say,
  instead of going on with the defaults, and make no `~/.fridolin`. The
  bridge makes its data folder and the folders of `db_path` and
  `media_dir` as it starts, not when one is saved in Settings.
- A `brain.json` with a `version` newer than 1 is refused instead of being
  read as version 1 and written back without the fields this version does
  not know: the brain shows as changed on disk, its approved settings keep
  running, and no edit or approval writes over the file. Unknown fields in
  `brain.json`, and unknown keys inside a section of `config.json`, are
  logged by their path.
- A second Ctrl-C or SIGTERM while the bridge stops ends it at once: it
  kills the brains' runs still going and exits with code 1. Restart in
  the dashboard stops the bridge as SIGTERM does, and a SIGTERM while
  it restarts leaves it stopped.
- When the bridge stops or restarts, a brain's run in progress gets 15
  seconds to finish, and its answer goes out. One that takes longer is
  stopped then. Before, every run in progress was stopped at once.
- The WhatsApp messages still waiting when the bridge stops, in a brain's
  queue or in a debounce batch, and those that arrive while it stops, are
  kept in `bridge.db` and answered after the next start, unless they were
  sent more than an hour before it. A chat whose run the stop cut short is
  told so after the start, and asked to send its message again. Before,
  all of them were dropped without a word.

### Added

- `group_members`: who in each allowed group reaches its brain, by phone
  number, or `["*"]` for everyone (docs/CONFIGURATION.md), set with
  **Members** next to the group under Chats → Allowed groups. `fridolin
  check` says for each allowed group whose messages are read there.
- `config.json` has a `version`, 2 today, which every save writes. A
  config without one is version 1 and is read as version 2, its retired
  keys folded in as before; starting does not rewrite it. A bridge
  refuses to start with a config of a newer version than it reads, and
  the dashboard refuses to save one, instead of dropping the settings it
  does not know. In a version 2 config, the version 1 keys `model`,
  `work_dir` and `brains` are unknown settings.
- `docs/schemas` has JSON Schemas of `config.json`, `brain.json`, a line
  of a brain's `conversation-history.jsonl`, and the requests and answers
  of `/send` and `/ingest`, for an editor or a script to check against.
  docs/BRAINS.md describes the history line, whose fields and types are
  added but never renamed or removed.
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
- `plaud.phone_upload`: the phone app uploads its transcripts through your
  site instead. The Plaud card builds the app with the site's address and
  a one-time pairing code (it needs a JDK and the Android SDK), and lists
  the paired phones, each of which can be unpaired. A phone pairs a key
  made in its keystore and signs every upload with it; the site and the
  bridge both check the signature (docs/PLAUD.md, docs/SITE.md).
- Release archives carry `THIRD_PARTY_NOTICES`, the licences of the
  modules a binary links (with libsignal's GPL-3.0, a binary as a whole
  is GPL-3.0), and the dashboard names the fallback brain, which answers
  the chats no brain claims and your DMs while no brain handles them.

### Removed

- **Brains in Docker Sandboxes**, added earlier in this release cycle
  (#149), are gone again: every brain runs under bubblewrap in the
  core's container, or sandbox-exec natively, as before. A microVM per
  brain cost a node, memory and a second login, and kept nothing out that
  the brain's own sandbox does not (docs/VISION.md, "Why not Docker
  Sandboxes for brains"). `config.json` is now version 3: a version 2
  file with `sandbox.runtime` or `sandbox.node` loads, and the next save
  leaves them out. A brain.json `sandbox` key is ignored. A node that
  still declares the `sandbox` capability joins and works, and the core
  ignores that capability and its approval. `fridolin node sandbox
  setup|status` and doctor's checks of it are gone; the dashboard no
  longer shows a brain's runtime. FrIDE in a Docker Sandbox stays.
- **The sandboxed FrIDE's second node**, added earlier in this release
  cycle (#149), is gone: FrIDE instances replace it, run by the
  machine's own node. `fridolin node fride sandbox
  join|create|login|start|stop|update|status|rm|github` and its `-sbx`
  state dir are gone, and so is joining a second node on a machine.
  node.json's `service` stays. A node set up so before still holds git
  identities back from its FrIDE until its owner removes it
  (docs/NODES.md, "Rollback" under "FrIDE instances").
- Brain runs no longer get `BRIDGE_CREDENTIALS_DIR`, which named the
  folder of the calendar tokens; nothing in `tools/` read it any more.

### Fixed

- **A core built with `fridolin core build` no longer has every FrIDE
  instance made anew at each update.** It named the FrIDE template by
  its own version, as it did not know the template's own tag, so after
  each update the nodes took the template for a new one and remade
  every stopped instance (and on Windows that remake stopped at the
  repositories' symlinks). It now gives the image the template's tag as
  image.yml does (a hash of the template's Dockerfile, its FrIDE config
  and FrIDE's repository), which changes only with the template.

- **A FrIDE host waiting for your approval asks once, not every
  minute.** A node that runs FrIDE instances declares again whenever
  its free memory or an instance changes. While its `fride_host` waited
  for approval, each new declaration counted as a new request, so the
  same "asks to do more than you approved" alert came again every minute
  or two. Now a request is what the node asks for: what it only reports
  (a FrIDE host's memory, state and instances, FrIDE's repositories,
  the JDK) changes nothing, and asking for more, such as another folder,
  is still a new request.

- **A wipe after a failed or incomplete snapshot now holds pruning too.**
  The vault compared a new snapshot only with the last try of its kind,
  and only when that try had no error. After a nightly that failed (a B2
  timeout, say), the next one was compared with nothing; and once one
  unreadable file made every snapshot incomplete, the guard never fired
  again. Folders wiped in between were pruned away without a hold. Now a
  snapshot is compared with the last one of its kind that was taken,
  incomplete ones included, whatever failed in between. The vault status
  shows them as `taken_hourly` and `taken_nightly`, for the cloud
  snapshots and for each node target. The first snapshot after the
  upgrade still compares with the last try, when that one was taken.

- **A module.json key in other letters, or written twice, no longer
  grants what the approver did not see.** Fridolin read a manifest's keys
  case-insensitively (`"Capabilities"` counted as `capabilities`) and
  merged a key written twice, while the site and the approval review read
  them exactly. A module so written could get programs, files, secrets or
  an `app.build` command into the live site's sandbox and deploys that the
  review never listed. Now such a manifest grants nothing: Fridolin leaves
  the module out of the sandbox and the build, and the review marks a key
  written twice as invalid. Write every key exactly as docs/SITE.md does.

- **Plaud Sync 0.6: recordings that come off the NotePin together all
  reach Fridolin.** When a check-in found several recordings, only the
  first was transcribed. The others waited in the app until the next
  recording, hours or days later, and nothing about them was logged. Now
  every recording saved while one is transcribed is transcribed too, and
  each check-in (also opening the app) tries again what still waits:
  recordings to transcribe, transcripts to upload, voice questions. The
  app renews Plaud's token by its age, also while the phone sleeps, and
  once more when Plaud refuses it; before, a sleeping phone could keep a
  token that had run out, and transcription failed. A voice-mode
  recording with nothing said in it is now logged instead of dropped
  silently. Build the app again and install it over the old one: it keeps
  its pairing and what waits in it.
- **Your own FrIDE threads stay in your chat** (#337). At every start,
  the bridge gave a scheduled agent the open threads it seemed to have
  started, among them any that began while a run of the agent was
  queued or went on. A thread your DM started in that time went to the
  agent at the next restart: its news no longer reached you, and its
  questions started runs of the agent. Now only threads from before
  agents kept their FrIDE news (#300, 25 October 2026) go to an agent.
  A thread that was already moved stays with the agent.
- **Connections follow no redirect** (#335). A call to OpenAI or
  ElevenLabs that answers with a redirect now fails with that answer
  ("it answered 302 Found"). Before, the bridge followed it, and Go
  copies ElevenLabs' `xi-api-key` header on to another host, so a
  redirect there would have sent the key along. Neither API redirects.
- **Pairing FrIDE again works at once.** After `fridolin node fride
  pair` on a node whose agent runs, FrIDE takes only the new token,
  while the agent kept the old one until its next check, up to half a
  minute later. Until then the core's FrIDE work failed with "FrIDE
  refused the node's token: pair again", right after the pairing, and
  the node refused FrIDE's news, which FrIDE does not send again. The
  agent now reads the token again when FrIDE refuses it or news is
  signed with another, and goes on with the new one.
- **A node hears an approval that came while it connected** (#324).
  An approval (`!approve`, the Nodes page, `fridolin nodes approve`)
  or a backup folder approved just as a node connected could reach no
  connection: the node stayed "commands pending" and served nothing
  until it connected again. One a moment later reached the node before
  its welcome, which broke the connection. The core now welcomes a node
  first and then tells it what changed since. A call that a change just
  allowed, such as a FrIDE task once FrIDE is offered again, could also
  reach the node before the change did, and the node refused it; the
  node now hears the change first.
- **A box's core stops and starts again cleanly** (#311). A stop of the
  core in a box returned before the box let go of the run it stopped:
  a start right after it, as a move's source does when the stop failed,
  could start nothing while the move said the core runs again, and the
  box could still count the core as running. A core that ended by
  itself and waited to start again could also start after a move stood
  the box down. A stop now drops that start, and the agent ends only
  once no core runs and none waits to start.
- **`fridolin nodes` takes a node's name** (#251). `move-check`,
  `move`, `core-secrets-transfer`, `approve`, `revoke`, `update`,
  `auto-update`, `tags` and `call` took only the 12-hex id, and said
  `no such node "mac-mini"` to a name. Each now takes the id or the
  name, in any case, as `!movecore` did. So do the API's node routes
  (`/api/nodes/{id}/…`, and `source` and `target` of a move or a
  transfer) and `!approve`, `!deny` and `!revoke`. A name more than one
  node has is refused, with each node's id; a revoked node answers to
  its name only when no other node has it.
  A scheduled agent that runs a program on a node finds its node the
  same way: its runs, the programs the core adds to a node's allowlist,
  the Schedules page, the agent wizard and `fridolin doctor` took the
  first node with the name, which could be a revoked one or either of
  two. A name two nodes have now runs nothing and says which nodes have
  it; the wizard offers such a node by its id.
- **FrIDE's stall notices stay quiet** (#306). "Agent … has not moved
  for 2h", every half hour, reached the DM in quiet mode, and in a
  scheduled agent's chat started a run of it each time. It is now
  progress (the FrIDE page only), the same notice again within six hours
  is dropped, and FrIDE's news starts at most one run an hour per
  agent, with what came meanwhile. Open threads a scheduled agent
  started before its news stayed with it go back to that agent.
- **`fridolin nodes update` waits for a box as long as the core does.**
  It gave up after 12 minutes with "still updating after 12m0s", while a
  box's hourly updater takes up to an hour and the core waits an hour
  and a half. The core now gives each update's end of wait
  (`wait_until` in `GET /api/nodes`), the command waits until then, and
  says every minute that the update is still going.

- **`!deny <ref>` no longer shuts out an approved node.** With nothing
  waiting under that ref, for instance a second `!deny` of a node's
  request for more, it fell through to `!revoke`: the node was shut out
  and its core-host secrets forgotten. It now answers that nothing
  waits and leaves the node as it was; only a node still waiting to
  join is shut out, and `!revoke <ref>` is the way to shut out an
  approved one.

- **A box's FrIDE no longer stores a Claude token that cannot sign in.**
  On Windows, `box.ps1` piped the token through Windows PowerShell,
  which ends it with a carriage return (and can start it with a byte
  order mark) that `set-claude-token` kept, and the prompt took
  anything, such as the browser's sign-in code (`…#…`) still on the
  clipboard; FrIDE's agents then failed with "401 Invalid bearer
  token". `set-claude-token` now drops a byte order mark, carriage
  return and spaces, refuses what is not a token from `claude
  setup-token` (`sk-ant-oat…`) and says why; `box.sh` and `box.ps1`
  name that token at the prompt and ask again, three times at most. A
  box whose stored token has another shape gets a note in `docker
  compose logs fride` at FrIDE's start, which also drops a stored byte
  order mark or carriage return; store a new token with
  `set-claude-token` (docs/NODES.md, "The FrIDE image").
- **`fridolin doctor --node` in a box no longer fails what a box does
  not have.** Inside a box's `node` container it reported the host's
  Tailscale missing, the core's port unreachable (a dial from the
  container misses the box's own tailnet device) and no node service or
  systemd unit, while the box was connected. With `box` in node.json it
  now leaves those out and checks `node-box-core` instead: that the
  agent was connected to the core within the last 90 seconds, as
  `fridolin node health` does. `setup-machine --node` checks the same.

- **FrIDE work no longer sticks on a node whose FrIDE has no Claude
  login.** When FrIDE fails a thread with "Not logged in", the node says
  so in what it offers (`fride (Claude not logged in)`, and `fridolin
  doctor`'s "FrIDE on the nodes" warns with the fix), the thread goes to
  another node's FrIDE that can take it, and new work goes elsewhere
  while one can. With no other node the chat gets the fix: in the box,
  `docker compose exec fride claude setup-token` and `set-claude-token`,
  then `docker compose restart fride`. The mark clears when FrIDE
  restarts, sends other news, or after an hour.

- **The box updater no longer flashes a window on Windows.** The
  Scheduled Task *fridolin box updater* starts PowerShell through
  `conhost.exe --headless`; with `-WindowStyle Hidden` alone a console
  window opened and closed every 2 minutes. The updater now runs every
  hour on every system, and the core waits for a box's update as long
  as the box says its updater takes. A box made before gets the quiet,
  hourly updater with its next `box.sh --update` or `box.ps1 -Update`.

- **Site modules find the packages of a `package.json` in a subfolder.**
  The deploy ran `npm install` only in the site's folder, and the live
  site started on a deploy whose `node_modules` a core move had left out,
  so modules failed with "Cannot find package 'express'". Now the deploy
  installs into every folder that holds the `package.json` nearest above
  a module's `module.json` (`dashboard/package.json` for
  `dashboard/modules/*`, or a module's own), the builder's workspace and
  its previews do the same, the site installs what a deploy is missing
  before it starts, and the sandbox lets the site run what each of them
  installed. Installs with a lock file are kept in
  `<data_dir>/site/npm-cache` by its hash, so a deploy whose dependencies
  did not change copies them. The load error names the `package.json`
  it went by (docs/SITE.md, "Where the package.json goes").

- **A scheduled agent can hand work to FrIDE.** `fride_task` and
  `node_agent_run` refused every scheduled run ("this run answers no
  chat"). A run of an agent that reports somewhere now sends FrIDE's,
  or the session's, questions and news to the chat it reports to: its
  `chat`, else the brain's reply chat, else your DM. A `silent` agent
  and an observe-only run are still refused (docs/BRAINS.md, "FrIDE and
  workstation sessions from an agent").

- **A scheduled agent's summary is no longer lost** when it wrote it and
  then only called tools: the CLI's result is empty then, and the run
  failed with "the agent gave no answer". The last text the run wrote is
  its answer now, for every run. A run that ends in an error keeps that
  text in its history and log.

- **One poll for a delegated step, not two.** The core brain asked you
  to approve the delegation and the delegated brain asked again for the
  step. Now the core asks once, for the step, and hands the approval over
  with `delegate`'s new `approval_id`; the delegated run takes that step
  without asking again and still asks for any other (docs/BRAINS.md,
  "You are asked once, at the start").

- **Voice mode no longer posts the question to WhatsApp.** A NotePin
  recording or a spoken question in voice mode used to arrive in the chat
  as `🎙️ <the question>` before the answer. Now only the answer goes
  there (and none with `plaud.voice_quiet`). The brain is told to file the
  question with its notes, the way its `skills/plaud.md` files recordings,
  without mentioning it in the answer (docs/PLAUD.md, "Voice mode").

- **Plaud Sync 0.5 reaches the NotePin with the screen off.** In 0.4 the
  check-ins connected to the pin's last known address and missed it all
  night, until the app was opened. Each check-in now looks for the pin
  first, with a scan filtered by Plaud's company id that Android runs
  with the screen off, and connects to the address the pin uses then.
  With voice mode on, a pin out of reach is tried at least every 10
  minutes, not every hour.
- **Voice mode: a NotePin recording reaches Fridolin again.** Its
  transcript was saved as a question, but the question was sent only at
  the app's next start. It is sent right after transcription now.
- New in the app: the **NotePin's battery**, asked with each synced
  recording, on its screen and in its log.

- A core move to a fresh core host: the pull of the core's image (several
  GB), the rehearsal and the secrets with their files may take longer than
  a minute, which the core cut off ("did not answer core.image.pull
  within 1m0s"); a target whose core cannot be read yet (no image, or no
  host secrets before the move brings them) is a warning in the
  pre-flight, not a refusal. In the core's container doctor no longer
  warns that the process's TZ differs from `timezone` (#187).
- A core move carries the secret files of a hidden credentials folder
  (`~/.something`): the folder check refused a leading dot, and the
  move went on with the host secrets and no files. A move now stops
  before anything stops when the source's secret files cannot be read,
  and the pre-flight says so (#187).
- A core export carries `core/encryption.json`, the buckets' encryption
  state (which stores are encrypted twice, which single-layer copies are
  retired), and the import puts it in place, keeping the target's earlier
  one as `encryption.json.before-<time>`. The core on another machine
  thought the single-layer copies still there (#187).
- A core move carries the secret files of the core's credentials folder
  (`sandbox.credentials_dir` under `~`: the Google OAuth web client,
  `dashboard-users.json`, each brain's calendar client and token) with
  the host secrets, sealed to the target's key, and the target writes
  them 0600 into the same folder under its core's home, never over a file
  it has. The container's home folder is now the volume `fridolin-home`,
  so they stay there; before, the core on the target missed them
  (calendar, startup check) (#187). The core's process now runs in
  config's `timezone` when it is set, not in the TZ of the host that
  started it (#250).
- `fridolin core run` no longer makes the post-quantum age identity and
  the second layers' keys when the machine has none; only `core start`
  and `core update` do. The move pre-flight runs `core run core status`
  on the target, which made keys of its own there, and the core's then
  could not replace them: the move stopped at the secrets. On a machine
  that has such keys and is not the core, remove them before moving the
  core there (`fridolin core secrets`, docs/CORE.md) (#187).
- `fridolin core pull` (the first step of a core move) checks the
  image's provenance for an owner whose GitHub name has capitals: the
  repository named from the registry (`ghcr.io` lowercases it) did not
  match the attestation's signer, so the pull always failed. It takes
  the attestation's spelling, which `gh` still verifies (#187).
- The move pre-flight no longer refuses a target that never ran the
  core's version: reading its core needs the image, which the move
  pulls first. It says so as a warning instead (#187).
- `fridolin core` finds Docker when it runs with a short PATH, as the
  node agent of a core host does under launchd: the move pre-flight
  (`fridolin nodes move-check`) said "docker is not installed or not on
  PATH" on a Mac with Docker Desktop. It finds `gh` the same way, and
  `fridolin core pull --check` fails when the image is missing and `gh`
  is too, so the move pre-flight refuses such a target instead of the
  move failing at its first step.
- **Schedules in the core ran in UTC.** The container has `TZ=UTC`, so a
  `"daily 21:00"` agent ran at 23:00 in Zurich. After upgrading, run
  `fridolin core start` once (it now passes the host's zone), or set
  `timezone`. Across daylight saving changes a schedule keeps its wall-clock
  time; a time the change skips runs once at the next minute that exists,
  and a time it repeats runs once, at its first showing (before, a stored
  anchor with a fixed offset could move a run by an hour).
- A node in a box on a host without Tailscale (a fresh server) joins at
  the first try: the join dialed the core before the box's first netmap
  named it, the host's DNS then found no such host, and box.sh stopped
  with "joining failed". The join now waits up to 30 seconds for the
  tailnet to know the core.
- The box's updater no longer makes the node's volume before `docker
  compose` does (box.sh starts the updater first). Such a volume made
  every compose command warn that it "was not created by Docker
  Compose", and `docker compose down -v` left it, with the node's key,
  behind. A box that has one keeps it; `docker volume rm fridolin-node`
  after `down -v` removes it.
- A node in a box joins from Windows. `box.ps1` kept the core's
  settings in `$core`, which in PowerShell is `-Core` too: the join got
  `core.yaml` instead of the core's address and stopped with "fridolin
  node join takes the join code", and `core.yaml` came out as one
  comment line. The core's settings then repeated `box.yaml`'s
  `no-new-privileges:true`, which compose refuses once it merges the two
  ("security_opt items at 0 and 3 are equal"), so every compose command
  of the box stopped; the repeat is now spelled `no-new-privileges`, and
  a box gets it with its next update. Under Windows PowerShell 5.1, a
  line on a program's stderr ended `box.ps1`, the first time while it
  waited for FrIDE. And `box.sh` and `box.ps1` check the images'
  provenance against the repository as GitHub spells it, not as the
  registry's lowercase name: an owner with a capital letter got "no
  valid build provenance".
- `fridolin nodes join-code` prints the line for a node in a box first,
  as `!join` does, when the core knows where its box script is
  published.
- `fridolin node status` in a box says it has no service of its own
  instead of "unknown: exit status 1".
- On the Build page, a message that could not be sent and a preview that
  could not be started now show as an error toast, read out at once and
  kept for 9 seconds. They showed as a plain note with the info icon,
  for 5 seconds.
- The Files page says "Only the first entries are shown." under a folder
  the bridge lists only in part. The note was never shown.

- A brain folder that a core move put on another machine goes back
  through the vault bucket only as far as the brain's backup settings
  let it, as on its way there. The export from the other machine took
  the visiting folder whole, so what the brain made there that its
  settings leave out (`*.log`, secret files, `node_modules/`) went
  through the cloud and was written home, where an excluded file that
  also existed at home showed up as a conflict (#195).
- On Windows, `fridolin core` commands no longer warn "syncing directory
  after publishing …: Access is denied" for every file they write
  (`compose.yaml`, `seccomp.json`): Windows cannot sync a folder and
  needs none, so the bridge no longer tries there.
- The vault leaves out a brain's rotated activity log,
  `claude-stream.1.log`, like `claude-stream.log`; it looked for
  `claude-stream.log.1`, a name the bridge never writes.
- `fridolin vault migrate-encryption --run --delete-old` retires the
  single-layer Drive repository (`fridolin-restic`) also where Google
  Drive does not let fridolin delete it. The Drive link's `drive.file`
  scope reaches only what its own Google client made: the delete failed
  with `403 appNotAuthorizedToChild`, and after a switch to your own
  Google client the folder is not even listed ("directory not found"),
  so the copy was never marked retired and `fridolin doctor` kept
  warning "the single-layer copies of drive are still there". A folder
  that is not there now counts as deleted; one Drive refuses is emptied
  of what fridolin reaches and retired. Either way the run, `fridolin
  vault status` and the doctor say once which folder may stay in Drive
  for you to delete by hand (docs/VAULT.md, "Moving existing data").
- `fridolin doctor` no longer fails "Litestream is not running (N
  restarts)" for the first minutes after the bridge starts on a data dir
  that ran before, while Litestream replicates. `core/vault-status.json`
  still held the last run's Litestream until the vault's first heartbeat,
  which waits for the first sync of the hot tier. Now the vault starts
  that part of its status over when it starts, and writes Litestream's
  own state as soon as it runs; the status file says when the vault
  started (`started_at`) and when Litestream's state was copied in
  (`litestream.at`), and doctor takes a Litestream state from before the
  start as not known yet: a warning for its first 5 minutes, a failure
  after them.
- A brain folder a core move put into the volume stays out of the vault's
  cold tier (#182). The vault had uploaded it, and after the way back
  `vault.db` still listed it: the vault kept its cold copies and wrote
  `.cold.json` files for them into `/data/brains-folders/`. Now a brain
  folder with `moved_from` is not a vault file, the import that brings a
  folder home leaves `core/vault-forget.json`, and the vault forgets that
  folder at its next start: rows and cold copies go, files on disk stay.
  `fridolin vault forget PREFIX` (`POST /api/vault/forget`) does it by
  hand, for leftovers of a move made before this version
  (docs/VAULT.md, "Forgetting a folder").
- The way back of a core move no longer loses an edit made at home
  meanwhile (#182). The import that puts a brain folder into the volume
  records each file's sha256 (`brains-folders/.moved-<name>.json`), and
  the way back keeps a file that changed at home since: the brain's
  version of one it changed too goes to `core/replaced-<time>/folders/`
  and import reports it as a conflict. And `moved_from`, and the brain's
  path after the way back, are the path as typed in `config.json`
  (`~/Personal`) instead of the expanded one (docs/CORE.md, "Brain
  folders on another machine").

- A Windows node joined by a coding agent in the Claude desktop app no
  longer ends with a node service that fails to start with only error
  1053. The app runs as an MSIX package, and Windows keeps what any
  process it starts writes under AppData, elevated ones too, in the
  package's own copy (`%LOCALAPPDATA%\Packages\<family>\LocalCache`),
  which the LocalSystem service never sees: `node.json` and `node.key`
  went there, and the service, registered with the real
  `%LOCALAPPDATA%\fridolin-node`, stopped before it could answer
  Windows. Now `fridolin node join` (and `node backup`, `node
  workstation`, `node fride pair`) refuses a state dir whose writes
  Windows would redirect, before
  it spends the join code, and `node install` one whose `node.json` it
  sees as a package's copy; both say to run in a PowerShell started from
  the Start menu or to pass a state dir outside AppData, such as
  `--state-dir "$env:USERPROFILE\.fridolin-node"`. `fridolin doctor
  --node`, `setup-machine --node`, `node status` and a failed `node
  start` read the state dir the service was installed with and say when
  it has no `node.json`, and where a package's copy of it lies
  (docs/NODES.md, "Inside an app package", also says how to recover a
  node that already hit it). `fridolin node status` and `doctor --node`
  on Windows also tell the service's state without an administrator
  instead of "cannot tell: Access is denied".
- A node built from the core's own commit no longer counts as older.
  Versions name a commit by `git describe`, which abbreviates the hash as
  long as each clone needs, so one commit was `0284f03` on one machine and
  `0284f03e` on another (and `dev-06ab6d8` for a build without a stamp):
  `fridolin doctor` warned about such nodes and the core offered them an
  update. The stamps now pin `--abbrev=8` (Makefile, setup.sh, the
  container's build in docs/CORE.md, an unstamped build's `dev-` name),
  and two hashes of one commit compare as the same build; a dirty build
  still differs.
- Litestream stopped uploading `bridge.db` and `vault.db` after the move
  to the local replica that the vault mirrors (`core/litestream-replica/`):
  it logged `LTX file is missing` every two minutes and copied only the
  daily snapshot, while `fridolin doctor` said it replicated. The bridge
  now resets such a database and starts its replica over by itself, and
  doctor warns while Litestream logs errors (docs/VAULT.md, "Encryption").
- A Linux node comes back after a reboot. Its systemd user unit was
  written with kardianos/service's `WantedBy=multi-user.target`, a
  target a user's systemd never reaches, so the node stayed off until
  someone started it. The unit is now wanted by `default.target`, and a
  unit written before is moved there (compat) by `fridolin node
  install`, `start`, `join` and the agent's own start, which an update
  from the core brings. `fridolin doctor --node` (and `setup-machine
  --node`) checks on Linux that the unit is enabled for boot and warns
  when the user does not linger (docs/NODES.md, "The service").
- The site's modules load again after the core moved to another machine.
  A core export leaves `node_modules` out, but the live deploy's folder
  still counted as built, so every module with server code failed with
  "Cannot find package 'express'". A deploy folder whose dependencies are
  gone is now checked out and installed again the next time its commit
  is deployed (a rollback to it, or `POST /api/site/deploy` with the live
  commit). A moved repository's deploy folders, which git still knows by
  their old paths, are replaced too.
- The site answers again when the core runs in the container. The site
  process listened on the container's own loopback and compose did not
  publish its port, so a Cloudflare tunnel on the host pointed at
  `http://127.0.0.1:8780` got no answer (Cloudflare's "Bad Gateway"). In
  the container the live site now binds its port on the container's
  interfaces, compose publishes it as `127.0.0.1:8780:8780`, and the
  site trusts the client address forwarded from Docker's private
  addresses as it does cloudflared's on the loopback (docs/CORE.md,
  "The site"). Run `fridolin core start` of this version to recreate the
  container with the new mapping.
- `fridolin doctor` no longer warns that a connected node was not heard
  from (`nodes-seen`) or cannot take FrIDE work (`nodes-fride`) while
  `fridolin nodes` shows it online. The core kept each heartbeat's time
  only in memory, so `nodes/nodes.json` held the time the node
  connected; it now writes `last_seen` and the node's resources there at
  least once a minute and when the node disconnects, and doctor asks the
  running bridge for its nodes first, reading the file only when the
  bridge does not answer.

- Privacy: the vault's restic snapshots (nightly and hourly, to B2, and
  the weekly copy to the restic repository on Drive) now follow each
  brain's backup settings, as the Google Drive backup always did. Before,
  they took every brain folder whole, with what `backup.exclude` names
  (such as a `/work/` folder) and the secret files (`.env`, `*.pem`, …).
  Now a folder with `backup.enabled: false` is left out, `backup.exclude`
  leaves out the same files in both, each brain's patterns only in its
  own folder, and the secret files stay out unless
  `backup.include_secret_files` is on; the cold tier does the same for a
  brain folder in the data dir. A folder whose `brain.json` cannot be
  read is left out whole, with a warning in the log and from `fridolin
  doctor`. Snapshots taken before still hold those files: delete them
  with `restic forget` and `restic prune`. The first snapshot after the
  upgrade may hold pruning (the mass-deletion guard) when it leaves out
  many files; allow it on the Vault page.
- A brain run with `claude_path` or `codex_path` left at its bare name
  (`"claude"`, `"codex"`, the default) is no longer refused in the
  container with `bwrap policy ...: "codex" is not an absolute path`:
  the bridge looks the CLI up on its own PATH and starts it by its
  absolute path, in both sandboxes and without one, and a CLI it cannot
  find fails the run with a message that says which setting to change.
  The startup log names that absolute path, and `fridolin doctor` checks
  it. `core migrate --from-native` now sets the Mac's `claude_path` and
  `codex_path` to the image's `/usr/local/bin/claude` and
  `/usr/local/bin/codex` instead of removing them.
- An approval or an answer to `ask_user` now reaches the brain once.
  When the vote came just as the waiting `request_approval` call looked
  at it, or just as its wait ended, or before a `check_approval`, the
  call got the answer and the brain was also sent it as a new message,
  which started a second run told to take the approved step again. Now
  it goes to the waiting call, or, when none waits, as that one message;
  a call that comes too late answers pending and says the answer comes
  as a new message.
- A vote on a poll the bridge sent, an approval's included, is no longer
  dropped as one for an unknown poll when it arrives before WhatsApp has
  acknowledged the poll: the bridge now chooses the poll's message id
  and keeps the poll before sending it, and forgets it again if the send
  fails.
- A reply that went out just as the bridge began to stop is no longer
  sent again after the next start, nor left out of the brain's history:
  the outbox now records what went out before the stop closes
  `bridge.db`, however the stop interrupts it.
- An upgrade with `./setup.sh` no longer ends with the first-time steps
  (pair WhatsApp, add a brain, allow your number), nor tells you to pair a
  bridge that is still connecting: the install waits up to ten seconds for
  a paired WhatsApp to connect, and an upgrade points to the upgrade notes
  instead.
- In Codex mode the log names the Codex CLI on one readable line at
  startup, as it does the Claude CLI, and warns when it is not found.

- A message displaced from a full brain queue now completes with a dropped
  error, so callers waiting for it do not wait forever. Its history remains
  available to later runs, and the queue keeps the same ordering rules.

- A WhatsApp sender known only by an internal LID cannot match
  `allowed_numbers` merely because its digits equal a listed phone number.
  A resolved phone number is required for that check; an empty list with
  `allow_any_dm: true` continues to admit everyone.
- Manifest transactions keep their brain folder open through reads, writes
  and rollback, so a moved or replaced folder cannot redirect those operations.
  Rollback preserves a manifest replaced or edited by another writer.

- Adding a brain no longer creates starter files through directory symlinks
  that leave its folder. The dashboard's folder browser and inspector keep
  file metadata checks inside your home folder, including child symlinks
  and symlinked marker files such as `.git`.
- When adding a brain fails, cleanup stays in the folders it opened and
  removes only unchanged files and directories it created. Replaced or
  edited files and pre-existing empty directories are preserved.
- A build chat on the Site's Build page shows that Claude's usage limit is
  reached, that its message is queued, or that the queue is full. It left
  out every notice that starts with ⏳, to leave out "Working on it".
- Messages waiting for a brain that you remove, or move to another
  folder, are answered by the brain that takes them now: the brain in
  its new folder, or the one its chat falls back to. Before, they were
  dropped without a word.
- An HTTP server that fails after the bridge started stops the bridge
  (exit 1, which the service manager restarts). Before, the bridge ran on
  without the dashboard, `/send` and `/ingest`, and looked healthy.
- `fridolin check` removes brain-owned PATH folders before probing the
  Claude version, so a CLI planted there cannot run outside the sandbox
  during the check.

- A second shutdown signal waits until each run's process supervisor has
  acknowledged stopping its process group before the bridge exits. Merely
  requesting cancellation could leave a Claude or Codex CLI running.
- A site search starts Claude as brain runs do: its prompt goes on stdin
  instead of the command line, where every process listing showed it,
  only the end of what Claude writes to stderr is kept, and what the
  agent left running is stopped when it ends.
- A command a run started that left the run's process group (a daemon,
  or one started with `setsid`) and holds the CLI's output open no longer
  keeps the brain and its run slot busy for as long as it runs, past
  `run_timeout_minutes` and `!stop`, nor turns the answer the run already
  gave into a "took too long" error. The bridge stops waiting for that
  output five seconds after the CLI exited.
- A run whose output has a line longer than 4 MiB, a tool's large result
  say, keeps its answer. The bridge failed it with "token too long" and
  lost the result that came after; now it skips the line and notes it in
  the brain's `claude-stream.log`.
- A chat's messages reach the bridge in the order they were sent: a
  photo and "what is this?" right after it no longer merge the wrong way
  round, `!stop` no longer runs before the message it is to stop, and an
  edit or a deletion comes after the message it changes. A file that
  takes long to download holds up only the messages its own chat sent
  after it, no longer every chat's, and at most three files download at
  once. Shutdown hands over every accepted event before closing the
  store; a drain longer than five seconds warns and keeps waiting rather
  than dropping the remaining messages.
- A group the bridge does not allow is looked up on WhatsApp, for its
  name in the log and on the Chats page, at most once an hour instead of
  for every message it sends, and an allowed group not at all. A lookup
  that hangs gives up after five seconds and holds up only that group.
- The log names who voted for what in a poll only at the debug level,
  and a vote it fails to record only masked. It masks the numbers and
  LIDs of the senders it ignores, as it already did for group members
  `group_members` does not admit.
- "Connect again" on a Plaud login that Plaud refuses signs that login
  out first, giving the sign-out up to 30 seconds. Before, it took the
  CLI's "Already logged in" for a new sign-in and showed Connected until
  the next check was refused again.
- A brain's settings saved while `config.json` could not be written, or
  refused by a later check, no longer leave its `brain.json` edited and
  the brain shown as changed on disk: `brain.json` is written only once
  the edit passed its checks, and put back when the save fails.
  Allowing a group for a brain puts back every `brain.json` it changed
  when one of them cannot be written.
- A `config.json` edited by hand, or put back from a backup, while the
  bridge runs is no longer written over by the next save from the
  dashboard, a calendar sign-in or a Plaud sync: the bridge reads the
  file before each save, runs on it from then on, and makes the change
  on top of it. Saving the whole Settings page over it asks you to
  reload first, and a file that does not load makes the save fail
  instead of being written over (docs/CONFIGURATION.md).
- A brain that cannot run, after a hand edit of `config.json` say, no
  longer keeps every other brain from being saved or approved with its
  own error: an edit is refused only for a clash it brings in. Allowing a
  group for a brain is checked the same way, so it can no longer move
  your DMs to another brain without a word.

### Kept for compatibility

What is still read or sent under an older name or in an older format,
each marked `compat:` in the code.

- A Google Drive link that an older version made through rclone's shared
  Google client (a Drive remote without `client_id`) keeps backing up as
  it is, until Google retires that client during 2026. The Backup page
  and `fridolin doctor` warn about it until you switch it to your own
  client (docs/BACKUP.md, "Switching an existing link").

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

- A brain.json with a `sandbox` key, which chose the Docker Sandbox
  runtime for brains, loads without a warning; the next edit in the
  dashboard drops it.
- A node whose node.json says `"service": "fride-sandbox"`, the
  sandboxed FrIDE's second node of before FrIDE instances, keeps holding
  git identities back from its FrIDE, whose VM must never hold a real
  token; it goes once no such node is left.
- Malformed configuration and brain manifest JSON keeps the Go type names
  shown before those types moved into packages, including manifests nested
  in configuration or dashboard requests. This preserves the existing
  diagnostics during the refactor without changing decoder error metadata.

- A `config.json` without `version`, version 1, which is read with its
  retired keys `model`, `work_dir` and `brains` folded in. It stays, as
  every migration of `config.json` does, so that a config of any earlier
  version loads.
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
