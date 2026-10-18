# Instructions for coding assistants

You are probably here because someone cloned this repository and asked you to
"set it up" or "get it running". This file says exactly how. Follow it in
order, explain each step to the person in a sentence, and stop to ask them
whenever a step needs their decision or their phone.

## What this is

A Go program that links a WhatsApp account (as a companion device, like
WhatsApp Web) to Claude Code. Each incoming message runs `claude -p` inside a
*brain*: a folder with its own prompt, memory and history. Everything after
installation — pairing WhatsApp, adding brains, allowing chats, Google Drive
backups — is done in a local web dashboard.

The person's data lives in brain folders they choose and in
`~/.fridolin/`, never in this repository.

## Setup steps

1. **Prerequisites.** Check, and install what is missing with the person's OK:
   - Go 1.26+ (`go version`); macOS: `brew install go`.
   - A C compiler (SQLite needs cgo) and make; macOS: `xcode-select
     --install` brings both.
   - Claude Code 2.1.302 or newer, logged in (`claude --version`; `claude
     update` updates it; if `claude -p hi` says it is not logged in, ask the
     person to run `claude` once and log in). On macOS brains run in the
     sandbox, where they log in with a token instead: once the service is
     installed, ask the person to run `claude setup-token` and add the
     token as `CLAUDE_CODE_OAUTH_TOKEN` to the service's environment
     (docs/CONFIGURATION.md, "Where to set them"). Never ask for the token
     in chat.
   - Optional, for backups: rclone (`brew install rclone` or
     https://rclone.org/install/).
   - Optional, for transcribing voice notes: python3 and ffmpeg (`brew
     install ffmpeg`); the engine is set up later (README, Transcription).

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
   - Optional: **Backup → Connect Google Drive**.
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
`/ingest` without a token, rotating the dashboard token). To go back to
the version before: `fridolin service rollback`.

## Rules

- **Never delete or recreate `~/.fridolin/bridge.db`.** It holds
  the WhatsApp pairing; losing it means re-pairing and losing poll history
  and the replies still waiting to go out.
  If a repair seems to need it, stop and ask; move it aside rather than delete.
- **Never run two bridges on one data directory or WhatsApp session.** The
  binary refuses (lock file + port), but do not try to work around it. To
  experiment, use dev mode with a scratch config:
  `bin/fridolin --no-whatsapp --config /tmp/try/config.json`
  (set a different `http_listen` port and `data_dir` in that config).
- **`./setup.sh` and `fridolin service install` restart the running
  bridge**, cutting off replies in progress. Ask before running them on a
  machine where the bridge is in use.
- **Never print or paste** `~/.fridolin/dashboard.token`,
  `~/.fridolin/scripts.token`, the rclone config in
  `~/.fridolin/rclone/`, or anything that looks like a credential into
  chat or logs. `fridolin open` signs the browser in with a link that
  works once, so the token never goes into a URL.
- Do not commit the person's data. `config.json`, brain folders, databases,
  media and tokens are not part of this repository.
- Do not weaken security defaults (sandbox, dashboard token, allowlists)
  without the person explicitly asking and understanding the trade-off.

## Working on the code

- `make check` runs what CI's build, lint and test jobs run: gofmt, both
  `go vet` runs, `go mod tidy -diff`, the race tests, staticcheck,
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
  The tests run in an empty temp directory, with an empty temp `HOME` and
  the variables the bridge reads (`FRIDOLIN_CONFIG`, `BRAIN_MODE`) unset,
  so they never see a `config.json` in the checkout or a real data dir;
  read a repository file through `repoDir`. Tests set up brains as the
  bridge runs them, in folders with a `brain.json` listed in `brain_dirs`
  (`brainFolder`, `addFolderBrain`); a config with inline `"brains"` no
  longer loads.
- The golden files in `testdata/golden` pin what the bridge says and writes
  today: every GET of the API, config writes and their errors, the files the
  dashboard serves, sandbox profiles, prompts and the Claude CLI's
  arguments, `fridolin check` and `init`, a new brain's folder, backup files
  and the replies of `/send` and `/ingest`. Times, temp paths, PIDs and the
  version are masked, so a second run writes the same bytes. A change meant
  to change one rewrites it with `go test -run 'TestName' -update .`, and
  the pull request says why. A new setting also goes into
  `testdata/config-v1.json`, which sets every one, and into
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
  (`harness_test.go`) instead of reaching into the `Bridge`, so
  they keep working while code moves into packages; `knownbugs_test.go` pins
  bugs that stay as they are for now, and the fix that changes one rewrites
  its test. `testdata/coverage-baseline.txt` is each file's statement
  coverage, which code that moves keeps. `TestSmoke` builds the bridge and
  runs it in dev mode; `go test -short` skips it.
- The parsers of untrusted input (chunking, poll blocks, triggers, JIDs and
  the DM allowlist, the identity map, brain.json, stream-json, attachment
  names, the junk filter) and the prompt that carries it to a run have fuzz
  targets beside their tests. Fuzz one with
  `go test -run='^$' -fuzz='^FuzzExtractPolls$' -fuzztime=30s .`;
  a failing input is saved under the package's `testdata/fuzz/` (make
  `testdata` first in a package that has none), where plain `go test`
  runs it from then on, so commit it with the fix. `make fuzz-short` runs
  every target for 10 seconds (set `FUZZTIME`); CI fuzzes every target for
  20 seconds on each push to main.
- The Python helpers in `tools/` have stdlib-only tests that never touch the
  network: `python3 -m unittest discover -s tools/tests` (`make test-py`).
  Keep them passing on Python 3.9. `make lint-py` runs ruff
  (`pip install -r tools/requirements-dev.txt`).
- The site process in `site/` (Node, TypeScript run by tsx; docs/SITE.md) has
  its own tests: `cd site && npm install && npm test`. The Go end-to-end
  tests of the site run it too and skip without its `node_modules`.
- Build with `make build` (into `bin/`, which git ignores) and try changes in
  dev mode. A build in the checkout never reaches the service; `./setup.sh`
  installs one.
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
- Architecture notes are in the README ("How it works"); configuration in
  `docs/CONFIGURATION.md`; brains in `docs/BRAINS.md`; backups in
  `docs/BACKUP.md`; Google Calendar in `docs/CALENDAR.md`; the Plaud
  recorder in `docs/PLAUD.md`; the site in `docs/SITE.md`; what stays
  compatible between versions, and how something is retired, in
  `docs/COMPATIBILITY.md`.
