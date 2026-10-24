# Changelog

What changes for people who run Fridolin. There are no tagged releases
yet, so everything is under Unreleased until the first one.
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) says what stays the same
from one version to the next, and how something that goes is first kept
for compatibility.

## Unreleased

### Added

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
  `box.ps1`, every 2 minutes as the owner; `--no-updater` leaves it out)
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

- Brain runs no longer get `BRIDGE_CREDENTIALS_DIR`, which named the
  folder of the calendar tokens; nothing in `tools/` read it any more.

### Fixed

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
- `fridolin nodes join-code` prints the line for a node in a box first,
  as `!join` does, when the core knows where its box script is
  published.
- `fridolin node status` in a box says it has no service of its own
  instead of "unknown: exit status 1".

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
