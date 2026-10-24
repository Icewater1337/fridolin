# Instructions for coding assistants

You are probably here because someone cloned this repository and asked you
to set this machine up. This file says exactly how. Explain each step to
the person in a sentence, do the work yourself, and stop only where a step
needs their decision, their account or their phone.

## What this is

A Go program that links a WhatsApp account (as a companion device, like
WhatsApp Web) to Claude Code or Codex. Each incoming message runs `claude -p` (default)
or `codex exec` inside a
*brain*: a folder with its own prompt, memory and history. Everything after
installation — pairing WhatsApp, adding brains, allowing chats, Google Drive
backups — is done in a local web dashboard.

The person's data lives in brain folders they choose and in
`~/.fridolin/`, never in this repository. A fridolin can grow into a
*core* (the bridge, usually in a container: docs/CORE.md) and *nodes*
(other machines of the owner's that lend it files and devices:
docs/NODES.md), all on the owner's tailnet.

## What do you want to do?

Ask, unless the request already says it:

| The person says | Path |
| --- | --- |
| "install fridolin", "get it running", "set it up" (no core yet) | [A. Install a new fridolin](#a-install-a-new-fridolin) |
| "set this machine up as a node of my fridolin core" | [B. Join this machine as a node](#b-join-this-machine-as-a-node) |
| "move the core to this machine", "test a takeover" | [C. Make this machine the core](#c-make-this-machine-the-core) |
| "set up FrIDE on this node", "let fridolin code on this machine" | [D. Set up FrIDE on this node](#d-set-up-fride-on-this-node) |

### How you work on every path

- **Install the tools yourself.** List what is missing and the commands,
  ask once, then run them: Windows `winget` (Chocolatey where winget is
  missing), macOS Homebrew (install it first when `brew` is missing),
  Linux `apt`/`dnf` and Tailscale's install script. After winget, reload
  PATH in the same PowerShell:
  `$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')`.
  Say when a restart is due (WSL, Docker Desktop) and resume after it.
- **Open the pages the person must act on**, then ask them to finish and
  wait: `Start-Process URL` (Windows), `open URL` (macOS), `xdg-open URL`
  (Linux). Tailscale's login link (`tailscale up` prints it), Docker
  Desktop's first run, the core dashboard's Nodes page
  (`https://fridolin-core.<tailnet>.ts.net/#/nodes`, or `http://` when
  the tailnet has HTTPS off; `fridolin doctor` names it). Afterwards check it
  yourself (`tailscale status`, `docker info`, `fridolin nodes`).
- **Never ask for a secret in chat**, and never print one. Secrets are
  typed by the person into a prompt without echo (`fridolin core secrets
  set NAME`), or move machine to machine (`fridolin core receive` /
  `send`). A dashboard sign-in takes the token in the browser's sign-in
  page, from the person's password manager.
- **Elevation.** Say when an administrator PowerShell (UAC prompt) is
  needed — installing the node service, WSL, copying into Program Files —
  and run that one step elevated: `Start-Process PROGRAM -Verb RunAs -Wait
  -ArgumentList …`. Everything else runs as the person.
- **`fridolin setup-machine --node|--core`** shows what is done and the
  next step; it only looks and is safe to run again. `fridolin doctor`
  (`--node` on a node machine) explains each check.
- **End with a report**: what you installed and changed, the checklist of
  the path with each item's result, and what is left for the person.

## A. Install a new fridolin

1. **Prerequisites.** Check, and install what is missing with the person's OK:
   - Go 1.26+ (`go version`); macOS: `brew install go`.
   - make; macOS: `xcode-select --install` brings it. SQLite is pure Go,
     so the build needs no C compiler; `make check`'s race tests do, and
     the Command Line Tools bring one.
   - Claude Code 2.1.302 or newer, logged in (`claude --version`; `claude
     update` updates it; if `claude -p hi` says it is not logged in, ask the
     person to run `claude` once and log in). On macOS brains run in the
     sandbox, where they log in with a token instead: once the service is
     installed, ask the person to run `claude setup-token` and add the
     token as `CLAUDE_CODE_OAUTH_TOKEN` to the service's environment
     (docs/CONFIGURATION.md, "Where to set them"). Never ask for the token
     in chat.
   - Optional, for Codex mode: Codex CLI 0.163.0+, with a separate sign-in
     for each brain under `<data_dir>/brains/<id>/codex` (see
     docs/CONFIGURATION.md, "AI mode"). Settings → AI mode selects it;
     its defaults are `gpt-6.1-sol` and high effort. Never print Codex
     credentials or disable the sandbox to make a login work.
   - Optional, for backups: rclone (`brew install rclone` or
     https://rclone.org/install/).
   - Optional, for transcribing voice notes: python3 and ffmpeg (`brew
     install ffmpeg`); the engine is set up later (README, Transcription).

   A native bridge runs on macOS and Linux. On Windows the bridge runs
   only as the container core (path C with a fresh volume, docs/CORE.md).

2. **Build, test, install the service, open the dashboard:**

   ```bash
   ./setup.sh --yes
   ```

   This builds the bridge, runs the tests on the build, writes a starter
   config to `~/.fridolin/config.json`, installs the build to
   `~/.local/lib/fridolin/` with a launchd agent (macOS) or systemd user
   unit (Linux), restarts it, waits for the new version to answer and opens
   the dashboard. Use `./setup.sh --no-service` if the person does not want
   it to start at login; it then builds `bin/fridolin`, which you run with
   `bin/fridolin --open` (it runs in the foreground — start it in the
   background or a separate terminal). Below, `fridolin` means
   `~/.local/lib/fridolin/fridolin` (or `bin/fridolin` without the service).

3. **Hand over to the dashboard.** Tell the person to:
   - **Pair WhatsApp**: scan the QR code shown on the Overview page with the
     phone that holds the bridge's WhatsApp account (Settings → Linked Devices
     → Link a Device). Recommend a second number, not their personal one —
     the bridge answers as whichever account it is linked to.
   - **Add a brain** (Brains → Add brain): pick or create a folder; decide
     whether it handles direct messages.
   - **Allow their own number** (Chats → Allowed numbers): with the
     country code; a `+`, spaces and dashes are fine.
   - Optional: **Backup → Connect Google Drive**. Needs the same Google
     OAuth client as the calendar, with the Google Drive API enabled and
     the `drive.file` scope on its consent screen (docs/BACKUP.md, "Use
     your own Google client").
   - Optional: **Google Calendar** per brain. Needs a one-time Google OAuth
     client (docs/CALENDAR.md walks through it); the person creates it in
     Google Cloud Console and pastes it under Settings → Google sign-in.
   - Optional: **Integrations → Plaud** for a Plaud recorder (needs
     `npm install -g @plaud-ai/cli`; see docs/PLAUD.md).

   You cannot scan QR codes or approve Google or Plaud sign-ins for them.

4. **Verify:** `curl -s http://127.0.0.1:8765/health` prints `ok`;
   `curl -s http://127.0.0.1:8765/ready` shows `"whatsapp":"connected"` once
   the phone is paired; `fridolin service status` shows what is installed
   and running; `fridolin check` shows the brains and routing. Ask the
   person to send the bridge a DM and confirm a reply arrives.

Reopen the dashboard with `fridolin open`. To upgrade later: `git pull &&
./setup.sh`, then do what README's "Upgrading and rolling back" says for
the version you came from (scripts and brains that call `/send` or
`/ingest` without a token, rotating the dashboard token, who is heard in
the allowed groups). To go back to the version before: `fridolin service
rollback`.

## B. Join this machine as a node

**A box by default** (docs/NODES.md, "Node in a box"): Docker and one
line, no Go, no Tailscale on the host, no service of its own.

1. **Docker**: Windows `winget install --id Docker.DockerDesktop -e`
   (UAC prompt; open Docker Desktop once for its terms, and turn on
   "Start when you sign in"); macOS `brew install --cask orbstack` or
   Docker Desktop; Linux Docker Engine with the compose plugin.
   `docker info` must answer.
2. **The line**: ask the person to send `!join` to Fridolin in their
   own WhatsApp chat and paste the line for a box it replies with (sh,
   or PowerShell on Windows). Run it in a terminal the person sees: it
   asks before it starts, prints a Tailscale sign-in link (open it for
   them; they sign in with the core's tailnet account), runs `claude
   setup-token` in the box (open its link; they paste the token into a
   prompt without echo, never into chat), and asks the git name and
   email FrIDE's agents commit with. A code that ran out while they
   signed in: `!join` again, and the same line with the new code.
3. **Approve**: they reply `!approve ID` when Fridolin says the box
   joined.
4. **Verify**: `docker compose ps` in `~/.fridolin-box`
   (`%LOCALAPPDATA%\fridolin-box`) shows `node` and `fride` running;
   `docker compose exec node fridolin node fride status --state-dir
   /node` says FrIDE answers; the core lists the node online with
   `host`, `backup` and `fride`.

Rollback: `!revoke ID`, then `docker compose down -v` in the box's
folder (it deletes the node's key, the backups in the volume and FrIDE's
workspaces: ask first).

**Natively** instead, when the machine needs what a container cannot do
(Apple builds, USB phones, notifications, the workstation mode on host
folders), or the `!join` reply has no box line. The full runbook, with
every command per OS, is docs/NODES.md, "Joining a machine". In short:

1. **Tools**: Git, Go, Tailscale. Windows:
   `winget install --id Git.Git -e`, `winget install --id GoLang.Go -e`,
   `winget install --id Tailscale.Tailscale -e` (each with
   `--accept-source-agreements --accept-package-agreements`). macOS:
   `brew install go git && brew install --cask tailscale`. Linux: `apt`/`dnf`
   `golang git`, `curl -fsSL https://tailscale.com/install.sh | sh`.
2. **Tailscale**: `tailscale up`, open its login link for the person, who
   signs in with the account of the core's tailnet; wait; then
   `tailscale status` lists `fridolin-core`. The core's address is
   `fridolin-core.<MagicDNSSuffix of tailscale status --json>:8766`.
3. **Build**: `go build -o bin/fridolin ./cmd/fridolin` (Windows
   `bin\fridolin.exe`; set `CGO_ENABLED=0`). On Windows copy it,
   elevated, to `%ProgramFiles%\fridolin\fridolin.exe`: the service runs
   as LocalSystem from there.
4. **Join code** (default, nothing on the core machine and no token
   needed): ask the person to send `!join` to Fridolin in their own
   WhatsApp chat with it and paste the code (or the line for this OS) it
   replies with here. The code is single-use, valid 10 minutes, and
   grants nothing until they approve. Alternatives: they send
   `!dashboard`, open the sign-in link it sends on this machine (it works
   once, within 10 minutes, at the core's tailnet address), and click
   **Nodes → New join code**; or on the core machine `docker exec
   fridolin-core fridolin nodes join-code` (native core: `fridolin nodes
   join-code`). Never ask for the dashboard token.
5. **Join** with the folder the person chooses and no commands unless
   they name some: `fridolin node join CODE --core HOST:8766 --no-exec
   --root PATH:rw`. Add `--workstation` only when the person wants the
   core to run Claude Code sessions and a terminal in those folders
   (docs/NODES.md, "Workstation"); never with their home folder as a
   root, unless they ask for it and accept what it gives away
   (`--allow-home`). On Windows add `--no-service`, then `node install`
   and `node start` elevated, all three with one `--state-dir`:
   `"$env:LOCALAPPDATA\fridolin-node"` in a PowerShell the person started
   from the Start menu, but `"$env:USERPROFILE\.fridolin-node"` when you
   run inside the Claude desktop app or another MSIX-packaged app. There
   Windows keeps what you write under AppData, elevated too, in the app's
   own copy, which the LocalSystem service never sees, and the service
   then fails with only error 1053; `join` refuses such a state dir and
   names the one to use (docs/NODES.md, "Inside an app package"). On
   Linux, ask to turn on lingering, so the node runs without a login and
   after a reboot (docs/NODES.md, step 5).
6. **Approve**: once the node joined, Fridolin tells the person in their
   WhatsApp chat; they reply `!approve ID` (device and what it offers in
   one go), or click **Approve device** and **Approve commands** on the
   Nodes page (or `fridolin nodes approve ID` and `fridolin nodes approve
   ID --commands` on the core).
7. **Verify**: `fridolin node status` (service running), `fridolin doctor
   --node`, `fridolin setup-machine --node` (exit 0); the core's Nodes page
   shows the node online and approved.

Rollback: `!revoke ID` in the person's chat, or `fridolin nodes revoke ID` on the core; `fridolin node stop` and
`fridolin node uninstall` here (elevated on Windows), then remove the state
dir.

## C. Make this machine the core

The full runbook is docs/CORE.md, "Moving the core to another machine".
The core is the container image plus the `fridolin-data` volume; on
Windows it runs in Docker Desktop with WSL2. In short:

1. **Tools**: Git, Go, Tailscale (as in B), and Docker. Windows:
   `wsl --install --no-distribution` elevated, restart, then `winget
   install --id Docker.DockerDesktop -e`; open Docker Desktop for its
   first run and let the person accept its terms; `docker info` must
   answer. macOS: `brew install --cask orbstack`; Linux: Docker Engine, and
   on Ubuntu (AppArmor) `sudo bin/fridolin core apparmor` once after the build.
2. **Build** fridolin as in B, then the image of the same version:
   `fridolin core build` in the clone.
3. **Secrets**, machine to machine: here `fridolin core receive` (the
   person's normal shell); on the current core's host, in a terminal the
   person sees, `fridolin core send --to ADDRESS --code CODE` as receive
   printed it. The person compares the four words on both screens and
   types `yes` there. Fallback: the person types each with `fridolin core
   secrets set NAME`. Check with `fridolin core secrets`.
4. **Move**, in this order: on the old host `fridolin core stop`, then
   `fridolin core run core export`; here `fridolin core run core import
   --latest`, `fridolin core run core promote`, `fridolin core start`.
5. **Verify**: `curl -s http://127.0.0.1:8765/ready` (WhatsApp connected,
   lease held), `docker exec fridolin-core fridolin doctor`, `fridolin core
   run core status`, `fridolin setup-machine --core` (exit 0); the person
   sends a WhatsApp message and gets an answer. `fridolin core start` on
   the old host now exits with status 3.
6. **Way back**: the same in reverse: stop and export here; on the old
   host `fridolin core run core import --latest --force`, `core run core
   promote`, `core start`.

Brain folders bound in from the Mac's home move with the export, less what
each brain's backup settings keep home, and come back on the way back
(docs/CORE.md, "Brain folders on another machine"). The WhatsApp session moves in
`bridge.db`: never run the old and the new core at once.

## D. Set up FrIDE on this node

FrIDE is where fridolin's code changes happen (docs/NODES.md, "FrIDE").
This path gives a node its own companion FrIDE. The full runbook, with
every command, is docs/NODES.md, "Setting up FrIDE on a node". The
machine must already be a node (path B). In short:

1. **Where FrIDE runs.** On macOS and Linux it runs natively. On
   Windows it runs in WSL2 (Ubuntu), because FrIDE does not build for
   Windows yet (`unix.Flock`, a Unix PTY). To get WSL2, run
   `wsl --install -d Ubuntu` elevated and restart if asked; the owner
   then creates the Linux user in the Ubuntu window. On Windows 11, ask
   to set `networkingMode=mirrored` in `%UserProfile%\.wslconfig` (then
   `wsl --shutdown`), so FrIDE's news reaches the node agent on
   `127.0.0.1`. Run Linux commands as
   `wsl -d Ubuntu --exec bash -lc '…'`, adding `-u root` for apt and Go.
2. **Tools**: git, the Go version FrIDE's `go.mod` names (from go.dev),
   Node 18+, and Claude Code (`npm i -g @anthropic-ai/claude-code`).
   The owner signs in to `claude` in the browser; in WSL the browser
   opens through `wslview`, otherwise open the printed URL with
   `Start-Process`.
3. **Build and lay out**: clone FrIDE's repository (ask the owner for its URL)
   into `~/fride-companion/src/fride` and build
   `~/fride-companion/bin/fride`. Clone `fridolin` and `fride` into
   `~/fride-companion/repos` (in WSL's file system, never `/mnt/c`).
   Clone the ponytail plugin, pinned, into
   `~/fride-companion/plugins/ponytail` (NODES.md, "Ponytail for every
   agent"); FrIDE's agents load it through `companion.claudePlugins`.
   Write the config from NODES.md into `~/fride-companion/home`
   (`FRIDE_HOME`), and run `fride config check` and `fride doctor`.
   Ask the owner for the git name and email FrIDE's agents commit
   with, and set them (`git config --global`); without them the first
   commit stops at a question the chat does not show.
   Keep it separate from any FrIDE the owner runs himself.
4. **Trust**: the owner runs `claude` once in
   `~/fride-companion/workspaces` and chooses "Yes, I trust this folder".
5. **Service**: `fride serve --under-fridolin --port 17840`, run by a
   launchd agent `ai.fride.companion` (macOS), a systemd user unit
   (Linux), or, on Windows, a Scheduled Task at logon that runs it
   through `conhost --headless wsl.exe`. A systemd unit inside WSL is
   not enough on Windows, because WSL stops the distribution once no
   `wsl.exe` holds it.
6. **Pair**:
   - macOS/Linux: `fridolin node fride pair --url http://127.0.0.1:17840
     --fride ~/fride-companion/bin/fride --fride-home ~/fride-companion/home
     --base ~/fride-companion/repos`.
   - Windows: take the code from `fride pair-companion --json` in WSL
     straight into `--code` without printing it, and leave out `--base`.
     The agent cannot reach a Linux path, so `fride_repo_add` does not
     work there yet; clone further repositories in WSL.
7. **Approve**: the owner clicks **Approve commands** on the core's
   Nodes page (or runs `fridolin nodes approve ID --commands` on the
   core, or uses a chat command such as `!approve` where one exists).
8. **Verify**: `fride doctor` passes; `fridolin node fride status` says
   FrIDE answers; the core lists the node with `fride`; and a tiny test
   task from the core comes back `done` with a branch, after which the
   core closes it.

## Rules

- **Never delete or recreate `~/.fridolin/bridge.db`.** It holds
  the WhatsApp pairing; losing it means re-pairing and losing poll history
  and the replies still waiting to go out.
  If a repair seems to need it, stop and ask; move it aside rather than delete.
- **Never run two cores, or two bridges on one data directory or WhatsApp
  session.** Moving the core stops the old one first (path C). The
  binary refuses (lock file + port), but do not try to work around it. To
  experiment, use dev mode with a scratch config:
  `bin/fridolin --no-whatsapp --config /tmp/try/config.json`
  (set a different `http_listen` port and `data_dir` in that config).
- **`./setup.sh` and `fridolin service install` restart the running
  bridge**, which gives a reply in progress 15 seconds and cuts it off
  after that. Ask before running them on a machine where the bridge is in
  use.
- **Never print or paste** `~/.fridolin/dashboard.token`,
  `~/.fridolin/scripts.token`, the rclone config in
  `~/.fridolin/rclone/`, or anything that looks like a credential into
  chat or logs. `fridolin open` signs the browser in with a link that
  works once, so the token never goes into a URL.
- **Host secrets** (the age identity, B2 keys, vault and restic passwords,
  the Claude token, a node's `node.key`) are never read out, printed or
  typed by you: the person types them into `fridolin core secrets set`, or
  they move with `core send`/`core receive`, whose four words the person
  compares on both screens; never answer that prompt for them.
- Do not commit the person's data. `config.json`, brain folders, databases,
  media and tokens are not part of this repository.
- Do not weaken security defaults (sandbox, dashboard token, allowlists)
  without the person explicitly asking and understanding the trade-off.

## Working on the code

- `make check` runs what CI's build, lint and test jobs run: gofmt, both
  `go vet` runs, `go mod tidy -diff`, the race tests, staticcheck, exhaustive,
  shellcheck, ruff, actionlint, the action-pin check, ESLint on the
  dashboard, the Python, web, personal-data-check, action-pin-check and
  Makefile tests and a dev-mode smoke test (`make help` lists the
  targets).
  CI also runs the site's tests and the fuzz targets (both below),
  govulncheck, CodeQL and the personal-data scan of the pushed commits.
  `make check` builds only into temp dirs; `make build` writes
  `bin/fridolin`, and `make install` also copies it to `~/.local/bin`.
  None of them touches the service, which runs the copy in
  `~/.local/lib/fridolin/` that `./setup.sh` installs. While iterating,
  `go vet ./... && go test -race ./...` is enough; `gofmt` everything.
  exhaustive (`make lint-go`) wants a switch on an enum to name every
  constant, also when it has a default: a new constant then shows each
  switch that must decide about it. A switch meant to cover only some
  gets `//exhaustive:ignore` under a comment that says why.
  The tests run in an empty temp directory, with an empty temp `HOME` and
  the variables the bridge reads (`FRIDOLIN_CONFIG`, `BRAIN_MODE`) unset,
  so they never see a `config.json` in the checkout or a real data dir;
  read a repository file through `repoDir` (the checkout's root) and the
  package's own `testdata` through `pkgDir`. Tests set up brains as the
  bridge runs them, in folders with a `brain.json` listed in `brain_dirs`
  (`brainFolder`, `addFolderBrain`); a config with inline `"brains"` no
  longer loads. The tests of `internal/app` run in parallel: each calls
  `t.Parallel()` first and has an environment of its own, in which
  `setenv` sets `HOME` and the fake CLIs' `FRIDOLIN_FAKE_*`; the bridges,
  brain managers and configs it makes read that one (`envOf`,
  `app.Options.Env`), not the process's. A test that must change what the
  whole process shares (its environment, working directory, standard
  logger or a package variable) says why in a `// Serial:` comment first
  instead, and runs before the parallel ones;
  `TestEveryTestSaysWhetherItRunsInParallel` holds every test to one or
  the other.
- The golden files in `cmd/fridolin/testdata/golden` pin what the bridge says and writes
  today: every GET of the API, config writes and their errors, the files the
  dashboard serves, sandbox profiles, prompts and the Claude CLI's
  arguments, `fridolin check` and `init`, a new brain's folder, backup files,
  the service's launchd agent and systemd unit, the replies of `/send` and
  `/ingest`, and every message the bridge itself sends into chats, which
  live in `internal/outbound/messages.go`. Times, temp paths, PIDs and the version are
  masked, so a second run writes the same bytes. A change meant to change
  one rewrites it with `go test -run 'TestName' -update ./internal/app`, and
  the pull request says why. A new setting also goes into
  `cmd/fridolin/testdata/config-v1.json`, which sets every one, and into
  `docs/schemas/config.schema.json` (a field of brain.json into
  `brain.schema.json`), which `TestSchemasFollowTheGoTypes` holds to the
  Go types, and `TestSchemaEnumsAreTheBridgesValues` to the values the
  bridge takes for a setting with a fixed set of them (`validEfforts`
  and the like); a new route goes into `routeInventory`
  (`routes_test.go`) with the guards it needs. A setting renamed or
  removed, or one that comes to mean something else, raises
  `currentConfigVersion` and adds the step to `configMigrations`
  (`config.go`), so that a file written before keeps its meaning.
  End-to-end tests drive a dev-mode bridge
  through what it offers from outside with `newHarness`
  (`internal/app/harness_test.go`) instead of reaching into the `App`, so
  they keep working while code moves into packages; `knownbugs_test.go` pins
  bugs that stay as they are for now, and the fix that changes one rewrites
  its test. `cmd/fridolin/testdata/coverage-baseline.txt` is each file's statement
  coverage, which code that moves keeps. `TestSmoke` builds the bridge and
  runs it in dev mode; `go test -short` skips it.
- The parsers of untrusted input (chunking, poll blocks, triggers, JIDs and
  the DM allowlist, the identity map, brain.json, stream-json, attachment
  names, the junk filter) and the prompt that carries it to a run have fuzz
  targets beside their tests. Fuzz one with
  `go test -run='^$' -fuzz='^FuzzExtractPolls$' -fuzztime=30s ./internal/outbound`;
  a failing input is saved under the package's `testdata/fuzz/` (make
  `testdata` first in a package that has none), where plain `go test`
  runs it from then on, so commit it with the fix. `make fuzz-short` runs
  every target for 10 seconds (set `FUZZTIME`); CI fuzzes every target for
  20 seconds on each push to main. A run on main that fails opens or
  updates an issue labelled `ci-main` per failing fuzz target or test
  ("CI on main: FuzzX failing", `scripts/ci-report.sh`), with the
  failure, the run's link and a fuzz target's failing input from the
  run's `fuzz-failing-inputs` artifact; a later run that passes closes
  it. Fix those before new work: a red main teaches everyone to ignore red.
- The Python helpers in `tools/` have stdlib-only tests that never touch the
  network: `python3 -m unittest discover -s tools/tests` (`make test-py`).
  Keep them passing on Python 3.9. `make lint-py` runs ruff
  (`pip install -r tools/requirements-dev.txt`).
- The site process in `site/` (Node, TypeScript run by tsx; docs/SITE.md) has
  its own tests: `cd site && npm install && npm test`. The Go end-to-end
  tests of the site run it too and skip without its `node_modules`.
- Build with `make build` (into `bin/`, which git ignores) and try changes in
  dev mode. A build in the checkout never reaches the service; `./setup.sh`
  installs one. The entry point is the `main` package in `cmd/fridolin`; `internal/cli`
  handles commands and `internal/app` owns the runtime. A
  build by hand is `go build -o bin/fridolin ./cmd/fridolin`. The
  repository's root has no Go files: `go build .` there fails with "no Go
  files" instead of building, and leaves a `./fridolin` that an older
  `setup.sh` set a service up to run alone (`make root-check`).
- The dashboard is plain HTML/CSS/JS in `web/` (no build step), embedded into
  the binary — rebuild after changing it. It runs under a strict CSP: no
  inline scripts or styles, and Trusted Types without a policy, so the
  browser refuses `innerHTML` and every other way of turning a string
  into HTML or script; build elements with `h()` from `lib/dom.js`. Its
  ES modules live in `web/assets`: `app/` (the entry point and the
  plumbing every page shares), `pages/` (one module per
  page), `components/` (shared pieces of UI) and `lib/` (the element builder
  and pure helpers, which know nothing about the app). Pages import from
  `app/`, `components/` and `lib/`, never from each other; `lib/` imports
  nothing outside itself. Pages load on first visit (the route table is
  `app/routes.js`), so once the bridge runs another version an open tab
  asks for a reload instead of mixing two builds. A page follows the data
  it shows through `app/resources.js` (one poll per endpoint, shared with
  the shell and other parts of the page, which sends back the ETag of the
  answer it has, so that an endpoint answering with `writeJSONIfChanged`
  sends a bodyless 304 while nothing changed) instead of polling it itself,
  repaints with `paint()` and `reconcile()` from `lib/dom.js`, which keep
  the nodes of what did not change (and with them focus, typed text and
  open panels), and builds an editable form with `components/live-form.js`,
  which keeps edits across refreshes and says when the data changed
  elsewhere. The router (`app/navigation.js`) renders the page the
  address names and, after a link, moves the keyboard focus to its h1 or
  to the part it names with `ctx.focusAt`. Leaving a page with unsaved
  changes asks first (`app/nav-guard.js`). Its styles are in
  `web/assets/css`, one cascade layer per file, which `app.css` puts in
  order: `tokens.css` (every colour, each as `light-dark(light, dark)`,
  so the dark theme is no second list), `base.css` (elements and
  utilities), `components.css` (the shell and shared pieces) and
  `pages.css`. A later layer wins over an earlier one whatever the
  selectors, so change a rule where it lives rather than overriding it
  further down. Its tests need Node 22+ and nothing installed:
  `make test-web` syntax-checks every module and runs `node --test` on
  `web/**/*.test.js` files (`web/test`). They cover the pure helpers, run
  pages against a stubbed bridge in a fake DOM (`web/test/serve.js`,
  `web/test/fake-dom.js`), check that every import resolves and keeps
  those layers and that every export is imported, and hold the
  stylesheet to its layout. `make lint-web` runs ESLint with
  `web/eslint.config.js` (names that are not defined, code that is never
  used), which needs Node 22.13+: npx fetches the version the Makefile
  pins the first time, and nothing is installed into the checkout.
- The project was called whatsapp-claude-bridge before. Names from then
  that are on disk or that others rely on stay, each marked `compat:` in
  the code: the data dir `~/.whatsapp-claude-bridge`, which the bridge
  still uses when `~/.fridolin` does not exist (`defaultDataDir`, which
  `setup.sh` asks through `fridolin paths`), the launchd label
  `com.whatsapp-claude-bridge`, whose agent `fridolin service install`
  retires, the sandbox's denies of the old `~/.config` and
  `~/Library/Logs` folders, and the personal-data check's old denylist
  place, `~/.config/whatsapp-claude-bridge/personal-denylist`, which it
  reads with a note to move it.
  *Bridge* is still the product's own word: `bridge.db`, `bridge.lock`,
  the `X-Bridge-Token` header and the `BRIDGE_*` variables brains read
  keep it. Mark a new shim like these the same way, and list it in
  `CHANGELOG.md`, which says what changes for people who run Fridolin.
- Run `scripts/install-hooks.sh` before committing. The hooks refuse commits
  and pushes containing phone numbers, WhatsApp JIDs, emails, home paths or
  tokens. Use placeholders: `15551234567`, `120363000000000000@g.us`,
  `you@example.com`, `/Users/you`.
- The code's layout, the rules between its packages, which archtest
  (`internal/archtest`) enforces, and how far the move into packages has
  got are in `docs/ARCHITECTURE.md`; what each file does is in the README
  ("How it works"); configuration in `docs/CONFIGURATION.md`; brains in
  `docs/BRAINS.md`; backups in `docs/BACKUP.md`; Google Calendar in
  `docs/CALENDAR.md`; the Plaud recorder in `docs/PLAUD.md`; the site in
  `docs/SITE.md`; what stays compatible between versions, and how something
  is retired, in `docs/COMPATIBILITY.md`.
