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
   - Claude Code, logged in (`claude --version`; if `claude -p hi` says it is
     not logged in, ask the person to run `claude` once and log in).
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
   `./fridolin check` shows the brains and routing. Ask the
   person to send the bridge a DM and confirm a reply arrives.

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

- `go vet ./... && go test -race ./...` must pass; `gofmt` everything.
  The tests run in an empty temp directory, with an empty temp `HOME` and
  the config override variables unset, so they never see a `config.json`
  or `.env` in the checkout or a real data dir; read a repository file
  through `repoDir`.
- The Python helpers in `tools/` have stdlib-only tests that never touch the
  network: `python3 -m unittest discover -s tools/tests`. Keep them passing
  on Python 3.9.
- The dashboard is plain HTML/CSS/JS in `web/` (no build step), embedded into
  the binary — rebuild after changing it. It runs under a strict CSP: no
  inline scripts or styles.
- Run `scripts/install-hooks.sh` before committing. The hooks refuse commits
  and pushes containing phone numbers, WhatsApp JIDs, emails, home paths or
  tokens. Use placeholders: `15551234567`, `120363000000000000@g.us`,
  `you@example.com`, `/Users/you`.
- Architecture notes are in the README ("How it works"); configuration in
  `docs/CONFIGURATION.md`; brains in `docs/BRAINS.md`; backups in
  `docs/BACKUP.md`.
