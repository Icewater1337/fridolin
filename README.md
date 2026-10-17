<p align="center">
  <img src="docs/logo.webp" alt="Fridolin: a fox in a headset at a desk, wiring WhatsApp to Claude Code" width="360">
</p>

# Fridolin

Talk to [Claude Code](https://claude.com/claude-code) over WhatsApp.

Message it like you'd message a person. It reads and writes files, runs
commands, and remembers things between conversations. Send it a PDF and it
files it. Ask what's on your calendar. Add it to a group and it follows along,
answering when addressed and staying quiet otherwise.

```
WhatsApp  ──►  bridge (Go)  ──►  claude -p   ──►  your files
   ▲              │                              your tools
   └──────────────┘                              your calendar
     replies, polls
```

It runs on your own machine, linked to a WhatsApp account as a companion
device — the same mechanism as WhatsApp Web. No third-party server sees your
messages.

> **This gives an AI assistant real access to your computer, driven by
> messages from a chat app.** Read [Security](#security) before you run it.

---

## What it does

**Conversations that continue.** Each chat keeps its own session. The
assistant sees the recent history of that chat and can search the full log.

**A dashboard.** Everything is set up in a local web page: pair WhatsApp by
scanning a QR code, add brains, allow chats, watch messages arrive and runs
finish, change settings, read the log. Nothing to edit by hand.

**Brains.** A brain is one isolated assistant: its own folder, prompt,
memory, and history. Give your DMs a private brain and a group chat a shared
one — they can't read each other's files. Add as many as you like, any time:
pick a folder in the dashboard and it becomes a brain, no restart needed.

**Your data stays yours.** Brains live in ordinary folders on your machine,
not in this repository. Link Google Drive in the dashboard and each brain is
backed up incrementally every night, with old versions kept.

**Files.** Photos, PDFs, documents, voice notes, and video are downloaded and
handed over as file paths. The assistant opens them and does whatever you asked.

**Real polls.** The assistant can send native WhatsApp polls and read the votes
back:

```
[POLL]
Question: When should we meet?
- Monday 2pm
- Tuesday 3pm
[/POLL]
```

**Group awareness.** In a group it reads everything but replies only when
addressed — by keyword, by @-mention, or when you send it a file.

**Orderly.** Messages that arrive while the assistant is busy wait their turn
(or, if you prefer, interrupt the current run). A photo album or a file plus
"what is this?" sent in quick succession becomes one request. `!stop` stops
whatever it is doing.

**Calendar.** Each brain can sign in to a Google account from the dashboard
and use the calendars you pick: list events, find free time, create events.

**Scriptable.** Localhost endpoints let your own scripts send messages through
the bridge — nightly summaries, alerts — or hand a brain files that arrived
some other way.

---

## Requirements

- **macOS or Linux.** The sandbox layer is macOS-only; everything else is portable.
- **[Claude Code](https://claude.com/claude-code)**, installed and logged in.
- **Go 1.25+** and a C compiler to build (`xcode-select --install` on macOS).
- **Python 3.9+** for the helper tools.
- **[rclone](https://rclone.org/install/)** — only for Google Drive backups.
- **A WhatsApp account for the bridge.** Use a second number, not your personal
  one — see below.

### Use a second phone number

The bridge answers automatically as whichever account it is linked to. Linking
your personal account means your own chats become the assistant's inbox and
your contacts get replies from "you".

Get a cheap prepaid SIM or a second number, register WhatsApp on it, and link
the bridge to that. Then add that number to your groups, or DM it from your own
phone.

---

## Setup

```bash
git clone https://github.com/YOUR_USERNAME/fridolin.git
cd fridolin
./setup.sh
```

Or open the folder in Claude Code (or any coding assistant) and say *"set this
up"* — [AGENTS.md](AGENTS.md) tells it exactly what to do.

`setup.sh` checks the prerequisites, builds the bridge, runs the tests,
installs a background service (launchd on macOS, systemd on Linux) and opens
the dashboard. Re-run it any time; it leaves your configuration and brains
alone.

Then, in the dashboard:

1. **Pair WhatsApp.** A QR code is shown. On the phone holding the bridge's
   account: **Settings → Linked Devices → Link a Device**, and scan it. The
   pairing is stored, so this is a one-time step.
2. **Add a brain.** Choose a folder (an empty one, or one you already keep
   notes in). The bridge writes a `brain.json` and a starter prompt into it and
   never overwrites existing files. Decide whether it answers your DMs.
3. **Allow your number** under *Settings → Access*, then DM the bridge.
4. **Optional:** *Backup → Connect Google Drive*.

Reopen the dashboard any time with:

```bash
./fridolin open
```

### Adding a group

1. Add the bridge's account to the group and send any message there.
2. The group appears in the dashboard under **Chats → waiting for access**.
3. Click **Allow**, pick the brain that should handle it, and say
   `@claude hello` in the group.

### Brain folders

A brain is a folder with a `brain.json`:

```json
{
  "version": 1,
  "id": "family",
  "name": "Family",
  "routing": { "handles_dms": false, "default": false, "groups": ["120363000000000000@g.us"] },
  "effort": "medium",
  "backup": { "enabled": true, "exclude": ["node_modules/", "*.log"] }
}
```

Copy a brain folder to another machine (or restore it from Drive), add it in
the dashboard, and it comes back with its settings. The bridge runs the
version you approved in the dashboard: if a brain edits its own `brain.json`,
the dashboard shows the change and waits for you. See
[docs/BRAINS.md](docs/BRAINS.md).

### Configuring by hand

The dashboard writes `~/.fridolin/config.json`; you can edit it
too. Every option is documented in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

```bash
./fridolin check                         # validate and show the routing
./fridolin check --route 15551234567@s.whatsapp.net
./fridolin migrate                       # move inline brains into brain.json files
```

### Trying it without WhatsApp

```bash
./fridolin --no-whatsapp --config /tmp/try/config.json --open
```

Dev mode runs the dashboard and brains with a simulated chat: inject messages
on the **Dev** page and read the replies there.

---

## Using it

**Direct message** — just write. Attach files, ask questions, give it work.

**In a group** — start with your trigger keyword (`@claude` by default),
@-mention the bridge's account, or send a file.

**Commands**

| Command  | Effect |
| -------- | ------ |
| `!help`  | Show available commands |
| `!stop`  | Stop the current run and drop anything queued |
| `!reset` | Forget the current session and start fresh |

### Shaping the assistant

Each brain's personality lives in `brain-prompt.md` in its folder. Adding a
brain writes a starting version from
[examples/brain-prompt.example.md](examples/brain-prompt.example.md). Edit it —
tell the assistant who you are, how to write, what it looks after. This is the
file that matters most for how good the thing feels.

### Memory

The assistant does not remember previous runs. Each message is a fresh Claude
process that gets the recent chat history and nothing else. Continuity comes
from files it writes itself:

```
<brain folder>/
├── brain.json                    routing and settings (managed by the dashboard)
├── brain-prompt.md               the system prompt — edit this
├── conversation-history.jsonl    every message, appended automatically
├── skills/                       procedures for recurring work
└── memories/                     notes the assistant writes for itself
    └── people/
        └── whatsapp-identity-map.md   phone numbers → names
```

Anything older than the recent window is searchable:

```bash
python3 tools/history_search.py --brain-dir ~/assistant/personal --since 30d "invoice"
```

### Skills

A skill is a markdown file in a brain's `skills/` directory telling the
assistant when a piece of recurring work applies and exactly how to do it —
which command, which flags, which mistakes not to make. The assistant reads
them before starting routine work, and writes new ones as it finds patterns.

Three ship with the bridge and are copied into each new brain:

- **[transcription](examples/skills/transcription.md)** — audio and video to
  text, including the reasons not to hand-roll it
- **[media-management](examples/skills/media-management.md)** — filing photos,
  clips, and voice memos out of the staging directory
- **[plaud](examples/skills/plaud.md)** — filing recordings from a Plaud
  recorder ([docs/PLAUD.md](docs/PLAUD.md))

They're starting points. Edit them, and add your own.

### Transcription

`tools/transcribe.py` handles voice notes and recordings of any length: it
pulls audio out of video, splits long files into chunks, runs a local Whisper
model or the Groq API, and retries with backoff on rate limits.

```bash
python3 tools/transcribe.py voice-note.ogg --output-dir ~/assistant/personal/transcripts
```

Neither engine is installed by default — pick one:

```bash
pip install -r tools/requirements-transcription.txt   # local: private, ~1.5 GB model
pip install groq && export GROQ_API_KEY=...           # API: light, needs a key
```

`ffmpeg` is required either way. `--model` accepts any Whisper-compatible model
ID, so a fine-tune matching your language or dialect usually beats the default
by a wide margin.

### Sending messages from a script

```bash
curl -X POST http://127.0.0.1:8765/send \
  -H 'Content-Type: application/json' \
  -d '{"jid":"120363000000000000@g.us","text":"Deploy finished ✅"}'
```

To hand a brain files that arrived some other way (a web upload, a recorder
sync), write them under `~/.fridolin/media/` and turn on
`ingest.enabled`:

```bash
curl -X POST http://127.0.0.1:8765/ingest \
  -H 'Content-Type: application/json' \
  -d '{"brain":"personal","paths":["/Users/you/.fridolin/media/upload/scan.pdf"],"text":"File this"}'
```

The brain handles it like a WhatsApp attachment and replies in its chat. Both
endpoints only listen on localhost and refuse requests a web page sends.

### Google Calendar

Dashboard → *Brains → brain → Calendar → Connect Google Calendar*: sign in
with the account for that brain and tick the calendars it may use. It needs a
Google OAuth client, which you create once in Google Cloud Console and paste
under *Settings → Google sign-in*. Step by step:
[docs/CALENDAR.md](docs/CALENDAR.md).

### Plaud recorder

Record conversations with a Plaud? Dashboard → *Integrations → Plaud* signs in
with Plaud's own login, and every new recording reaches a brain as a finished
transcript with Plaud's summary. See [docs/PLAUD.md](docs/PLAUD.md).

### Backups

*Backup → Connect Google Drive* links a Drive account through
[rclone](https://rclone.org). The bridge keeps its own rclone configuration
(your personal rclone setup is untouched) and asks only for the `drive.file`
permission, so it can see nothing in your Drive except the files it creates.

Every night (or when you click *Back up now*) each brain folder is synced
incrementally to `fridolin-backup/<brain>/current/`; files that
changed or were deleted move to `versions/<timestamp>/` and are kept for 30
days. Exclude patterns per brain keep out what you can rebuild. The WhatsApp
session, tokens and `.env` files are never uploaded. Restores go into a new
folder and never overwrite a brain. Details: [docs/BACKUP.md](docs/BACKUP.md).

### Running in the background

`setup.sh` installs a launchd agent (macOS) or a systemd user unit (Linux)
from the templates in [deploy/](deploy/). Pairing happens in the dashboard, so
the service can be started before WhatsApp is linked.

---

## Security

The bridge runs Claude Code with `--dangerously-skip-permissions`, because
nobody is at the keyboard to approve tool calls. The assistant can read and
write files and run commands without asking.

That means **anyone who can message the bridge can influence what it does on
your machine**, and so can text inside any file they send it. Treat it that way.

What the bridge does about it:

- **DM allowlist.** Only numbers in `allowed_numbers` are answered. Groups are
  deny-by-default: unlisted groups are ignored entirely.
- **No Status or channels.** WhatsApp Status updates and channels are ignored,
  and the bridge refuses to send to Status, broadcast lists or channels, so
  nothing the assistant writes can end up in your public Status.
- **Sandbox (macOS).** Each Claude run is wrapped in `sandbox-exec`. Brains
  can neither read nor write SSH keys, cloud credentials, shell history,
  browser and mail data, your Documents/Downloads/Desktop, the bridge's own
  database — or any other brain's directory — and cannot rename the folders
  above them to get around that. Other Claude sessions' transcripts are
  unreadable. Files that run outside the sandbox — Claude Code's settings,
  hooks and `CLAUDE.md` (also those above brain folders), its auto-memory,
  `~/Library/LaunchAgents`, shell startup files, the bridge's own binary and
  checkout, the Claude CLI and the other tools the bridge runs — are
  readable but never writable. Add your own paths under
  `sandbox.deny_paths`. When the sandbox is on but cannot be set up, the
  brain refuses the message with an error instead of running without it.
- **Brain isolation.** Brains have separate directories, sessions, histories,
  and calendar credentials, and they do not load your own Claude Code
  setup: your user settings and their hooks, skills, plugins and MCP
  servers (see [what a brain loads](docs/BRAINS.md#what-a-brain-loads)).
- **A guarded dashboard.** It only listens on localhost, rejects requests
  from web pages on other sites (and DNS-rebinding tricks), and requires a
  token from `~/.fridolin/dashboard.token` — a file the sandbox
  keeps away from brains, so a manipulated brain cannot reconfigure the bridge.
- **You approve brain settings.** A brain that edits its own `brain.json` to
  claim your DMs or another group changes nothing until you approve it.
- **OTP guard.** A message that is only a numeric code, arriving right after
  the assistant asked for one, is dropped before it reaches history or the model.

What it does not do:

- The sandbox is containment, not a jail. It raises the cost of a prompt
  injection; it does not stop code execution. It denies writing only where
  listed, so a brain can still write elsewhere in your home folder,
  including the Claude Code state all brains and your own sessions share
  (`~/.claude.json` and its MCP servers) and the files your shell startup
  files load (such as `~/.nvm/nvm.sh`), and it can ask launchd, `crontab`,
  `at`, `open`, `osascript` or a running tmux server (the activity viewer's,
  or your own) to start a program outside the sandbox. See
  [what the sandbox does not cover yet](docs/CONFIGURATION.md#what-the-sandbox-does-not-cover-yet).
- Classic mode (`brain_mode` off) runs Claude without the sandbox.
- On Linux there is no sandbox layer at all (the bridge refuses to start with
  `sandbox.enabled` on). Run the bridge as a dedicated user.
- Every file sent to the bridge lands in your data directory, and the assistant
  reads it.

Sensible precautions: a dedicated WhatsApp number, a tight allowlist, a work
directory that is not your home directory, and `sandbox.deny_paths` covering
anything sensitive.

---

## How it works

`main.go` loads config; `bridge.go` wires everything together.

**`whatsapp.go`** — [whatsmeow](https://github.com/tulir/whatsmeow) client.
Handles pairing, allowlists, trigger matching, attachment downloads, and poll
vote decryption (WhatsApp sends SHA-256 hashes of option names, which are
matched against the stored options).

**`debounce.go`** — merges a burst of messages from one person into one run.

**`brain.go`** — the multi-brain runner. Each brain has a queue and one
worker; each message spawns `claude -p` with `--output-format stream-json`,
seeded with the system prompt and recent chat history. Events are parsed live,
so an `AskUserQuestion` reaches WhatsApp immediately and a run over 10 seconds
gets a "working on it" notice. `BrainManager.Apply` swaps the brain set at
runtime without touching brains that did not change.

**`manifest.go`, `brainreg.go`** — `brain.json`, the brain registry, path
checks and scaffolding. **`configstore.go`** — the live configuration.

**`dashboard.go`, `dashboard_api.go`, `web/`** — the dashboard, its guards and
API; the page is plain HTML/JS compiled into the binary.

**`backup.go`** — Google Drive backups through rclone.

**`plaud.go`** — the Plaud recorder integration.

**`claude.go`** — the classic single-session runner used when `brain_mode` is
off: one Claude session per chat, resumed with `--resume`, retried without it
if the session has expired.

**`sandbox.go`** — generates a per-brain `sandbox-exec` profile, passed to
`sandbox-exec -p` on each run (never written to a file).

**`store.go`** — SQLite: the whatsmeow device store, session IDs, active PIDs,
polls, and votes.

**`chunks.go`** — splits long replies at paragraph, line, then sentence
boundaries so messages break somewhere readable.

**`server.go`** — the localhost `/send` and `/ingest` endpoints.

---

## Troubleshooting

**"claude: command not found"** — set `claude_path` to the CLI's absolute path.
On macOS the desktop app keeps it at
`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`.

**No reply in a group** — look under *Chats* in the dashboard: an unknown group
shows up there with an *Allow* button. Otherwise check which brain the group
routes to, and that you used a trigger keyword.

**No reply to a DM** — the sender's number must be allowed (*Settings →
Access*), with country code and no `+`. Blocked senders appear under *Chats*.

**"Not logged in" from the CLI** — run `claude` once in a terminal to
authenticate. Under launchd, check that `HOME` is set correctly in the plist.

**Logged out of WhatsApp** — remove the device under *Linked Devices* on the
phone, stop the bridge, move `bridge.db` out of your data directory (keep it
until the new pairing works), start the bridge and pair again from the
dashboard. This also clears sessions and poll history.

**"another bridge is already running"** — only one bridge may use a data
directory; two bridges on one WhatsApp session knock each other offline.

**Everything times out with the sandbox on** — set `sandbox.enabled` to `false`
to confirm it's the cause, then add the path it needs to `sandbox.allow_paths`.

**"the sandbox could not be set up"** — the sandbox is on, but the bridge
could not build the profile or find `/usr/bin/sandbox-exec`, so it did not run
the message at all. The bridge log names the cause, and the dashboard's
Overview shows it too, with the brains it affects.

---

## Contributing

Issues and pull requests welcome. Please don't include real phone numbers,
group JIDs, or chat content in bug reports — redact them.

Run `scripts/install-hooks.sh` once: it blocks commits and pushes that contain
phone numbers, WhatsApp JIDs, email addresses, home-directory paths or tokens,
plus anything in your personal denylist
(`~/.config/fridolin/personal-denylist`, one string per line —
your name, your numbers, your company). CI runs the same check.

## License

MIT — see [LICENSE](LICENSE). Privacy: [PRIVACY.md](PRIVACY.md).

Built on [whatsmeow](https://github.com/tulir/whatsmeow). Not affiliated with
or endorsed by WhatsApp or Meta. Automating a WhatsApp account may conflict
with WhatsApp's Terms of Service; you are responsible for how you use it.
