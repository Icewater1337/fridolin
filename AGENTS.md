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
   - A C compiler (SQLite needs cgo); macOS: `xcode-select --install`.
   - Claude Code 2.1.302 or newer, logged in (`claude --version`; `claude
     update` updates it; if `claude -p hi` says it is not logged in, ask the
     person to run `claude` once and log in).
   - Optional, for backups: rclone (`brew install rclone` or
     https://rclone.org/install/).

2. **Build, test, install the service, open the dashboard:**

   ```bash
   ./setup.sh --yes
   ```

   This builds `./fridolin`, runs the tests, writes a starter
   config to `~/.fridolin/config.json`, installs a launchd agent
   (macOS) or systemd user unit (Linux), starts it, and opens the dashboard.
   Use `./setup.sh --no-service` if the person does not want it to start at
   login; then run `./fridolin --open` yourself (it runs in the
   foreground — start it in the background or a separate terminal).

3. **Hand over to the dashboard.** Tell the person to:
   - **Pair WhatsApp**: scan the QR code shown on the Overview page with the
     phone that holds the bridge's WhatsApp account (Settings → Linked Devices
     → Link a Device). Recommend a second number, not their personal one —
     the bridge answers as whichever account it is linked to.
   - **Add a brain** (Brains → Add brain): pick or create a folder; decide
     whether it handles direct messages.
   - **Allow their own number** (Settings → Access): digits with country
     code, no `+`.
   - Optional: **Backup → Connect Google Drive**.
   - Optional: **Google Calendar** per brain. Needs a one-time Google OAuth
     client (docs/CALENDAR.md walks through it); the person creates it in
     Google Cloud Console and pastes it under Settings → Google sign-in.
   - Optional: **Integrations → Plaud** for a Plaud recorder (needs
     `npm install -g @plaud-ai/cli`; see docs/PLAUD.md).

   You cannot scan QR codes or approve Google or Plaud sign-ins for them.

4. **Verify:** `curl -s http://127.0.0.1:8765/health` prints `ok`;
   `curl -s http://127.0.0.1:8765/ready` shows `"whatsapp":"connected"` once
   the phone is paired; `./fridolin check` shows the brains and routing. Ask
   the person to send the bridge a DM and confirm a reply arrives.

Reopen the dashboard with `./fridolin open`.

## Rules

- **Never delete or recreate `~/.fridolin/bridge.db`.** It holds
  the WhatsApp pairing; losing it means re-pairing and losing poll history.
  If a repair seems to need it, stop and ask; move it aside rather than delete.
- **Never run two bridges on one data directory or WhatsApp session.** The
  binary refuses (lock file + port), but do not try to work around it. To
  experiment, use dev mode with a scratch config:
  `./fridolin --no-whatsapp --config /tmp/try/config.json`
  (set a different `http_listen` port and `data_dir` in that config).
- **Never print or paste** `~/.fridolin/dashboard.token`, the
  rclone config in `~/.fridolin/rclone/`, or anything that looks
  like a credential into chat or logs. `fridolin open` passes
  the token to the browser directly.
- Do not commit the person's data. `config.json`, brain folders, databases,
  media and tokens are not part of this repository.
- Do not weaken security defaults (sandbox, dashboard token, allowlists)
  without the person explicitly asking and understanding the trade-off.

## Working on the code

- `make check` runs what CI's build, lint and test jobs run: gofmt, both
  `go vet` runs, `go mod tidy -diff`, the race tests, staticcheck,
  shellcheck, ruff, actionlint, the action-pin check, the Python, web,
  personal-data-check, action-pin-check and Makefile tests and a dev-mode
  smoke test (`make help` lists the targets).
  CI also runs the site's tests and the fuzz targets (both below),
  govulncheck, CodeQL and the personal-data scan of the pushed commits.
  `make check` never rewrites `./fridolin`, which the installed service
  runs; `make build` and `make install` do. While iterating,
  `go vet ./... && go test -race ./...` is enough; `gofmt` everything.
  The tests run in an empty temp directory, with an empty temp `HOME` and
  the variables the bridge reads (`FRIDOLIN_CONFIG`, `BRAIN_MODE`) unset,
  so they never see a `config.json` in the checkout or a real data dir;
  read a repository file through `repoDir`. Tests set up brains as the
  bridge runs them, in folders with a `brain.json` listed in `brain_dirs`
  (`brainFolder`, `addFolderBrain`); a config with inline `"brains"` no
  longer loads.
- The parsers of untrusted input (chunking, poll blocks, triggers, JIDs and
  the DM allowlist, the identity map, brain.json, stream-json, attachment
  names, the junk filter) have fuzz targets beside their tests. Fuzz one
  with `go test -run='^$' -fuzz='^FuzzExtractPolls$' -fuzztime=30s .`;
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
- The dashboard is plain HTML/CSS/JS in `web/` (no build step), embedded into
  the binary — rebuild after changing it. It runs under a strict CSP: no
  inline scripts or styles. Its ES modules live in `web/assets`: `app/` (the
  entry point and the plumbing every page shares), `pages/` (one module per
  page), `components/` (shared pieces of UI) and `lib/` (the element builder
  and pure helpers, which know nothing about the app). Pages import from
  `app/`, `components/` and `lib/`, never from each other; `lib/` imports
  nothing outside itself. Pages load on first visit (the route table is
  `app/routes.js`), so once the bridge runs another version an open tab
  asks for a reload instead of mixing two builds. Its tests need Node 22+
  and nothing installed: `make test-web` syntax-checks every module and
  runs `node --test` on `web/**/*.test.js` files (`web/test`). They cover
  the pure helpers and check that every import resolves and keeps those
  layers.
- Run `scripts/install-hooks.sh` before committing. The hooks refuse commits
  and pushes containing phone numbers, WhatsApp JIDs, emails, home paths or
  tokens. Use placeholders: `15551234567`, `120363000000000000@g.us`,
  `you@example.com`, `/Users/you`.
- Architecture notes are in the README ("How it works"); configuration in
  `docs/CONFIGURATION.md`; brains in `docs/BRAINS.md`; backups in
  `docs/BACKUP.md`.
