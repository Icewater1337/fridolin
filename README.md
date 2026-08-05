# WhatsApp–Claude Bridge

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

**Brains.** A brain is one isolated assistant: its own directory, prompt,
memory, and history. Give your DMs a private brain and a group chat a shared
one — they can't read each other's files. Add as many as you like.

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

**Interruptible.** Send a follow-up while it's still thinking and it drops the
old run and starts on the new message.

**Calendar.** An included tool lists, creates, and finds gaps in Google
Calendar, with separate credentials per brain.

**Scriptable.** An optional localhost HTTP endpoint lets your own scripts send
messages through the bridge — nightly summaries, alerts, whatever.

---

## Requirements

- **macOS or Linux.** The sandbox layer is macOS-only; everything else is portable.
- **[Claude Code](https://claude.com/claude-code)**, installed and logged in.
- **Go 1.21+** to build.
- **Python 3.9+** for the helper tools.
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
git clone https://github.com/YOUR_USERNAME/whatsapp-claude-bridge.git
cd whatsapp-claude-bridge
./setup.sh
```

`setup.sh` checks your prerequisites, finds the Claude CLI, asks who's allowed
to talk to the bridge, sets up your brains, writes `config.json`, and builds
the binary. Re-run it any time; it backs up your config first.

Then pair with WhatsApp:

```bash
./whatsapp-claude-bridge
```

A QR code appears. On the phone holding the bridge's account:
**Settings → Linked Devices → Link a Device**. Scan it. The pairing is stored,
so this is a one-time step.

Send the bridge a DM from an allowed number. It should answer.

### Adding a group

Group chats are opt-in and the bridge ignores unknown groups, so you need the
group's JID:

1. Add the bridge's account to the group.
2. Send any message there.
3. The bridge logs it:
   `Group message from 120363000000000000@g.us ("Team") … ignored (not in allowed_groups)`
4. Put that JID in `allowed_groups` in `config.json`, and in the `groups` list
   of whichever brain should handle it.
5. Restart the bridge, and say `@claude hello` in the group.

### Configuring by hand

`setup.sh` is a convenience, not a requirement. Copy `config.example.json` to
`config.json` and edit it. Every option is documented in
[docs/CONFIGURATION.md](docs/CONFIGURATION.md).

```bash
./whatsapp-claude-bridge --check     # validate config and show the routing
```

---

## Using it

**Direct message** — just write. Attach files, ask questions, give it work.

**In a group** — start with your trigger keyword (`@claude` by default),
@-mention the bridge's account, or send a file.

**Commands**

| Command  | Effect |
| -------- | ------ |
| `!help`  | Show available commands |
| `!reset` | Forget the current session and start fresh |

### Shaping the assistant

Each brain's personality lives in `brain-prompt.md` in its working directory.
`setup.sh` writes a starting version from
[examples/brain-prompt.example.md](examples/brain-prompt.example.md). Edit it —
tell the assistant who you are, how to write, what it looks after. This is the
file that matters most for how good the thing feels.

### Memory

The assistant does not remember previous runs. Each message is a fresh Claude
process that gets the recent chat history and nothing else. Continuity comes
from files it writes itself:

```
<brain work_dir>/
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

Two ship with the bridge and are copied into each brain by `setup.sh`:

- **[transcription](examples/skills/transcription.md)** — audio and video to
  text, including the reasons not to hand-roll it
- **[media-management](examples/skills/media-management.md)** — filing photos,
  clips, and voice memos out of the staging directory

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

With `http_listen` enabled:

```bash
curl -X POST http://127.0.0.1:8765/send \
  -H 'Content-Type: application/json' \
  -d '{"jid":"120363000000000000@g.us","text":"Deploy finished ✅"}'
```

The endpoint is unauthenticated and refuses to bind to anything but localhost.

### Google Calendar

Optional. Install the deps and create OAuth credentials per brain:

```bash
pip3 install -r tools/requirements.txt
```

Save the downloaded OAuth JSON as
`~/.whatsapp-claude-bridge/google_calendar_credentials_<brain>.json`, then
authorise once:

```bash
python3 tools/calendar_tool.py --profile personal list
```

Full instructions are in the header of
[tools/calendar_tool.py](tools/calendar_tool.py).

### Running in the background

`setup.sh` can install a launchd agent (macOS) or print a systemd unit (Linux).
Templates are in [deploy/](deploy/). Pair with WhatsApp interactively first —
the QR scan needs a terminal.

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
- **Sandbox (macOS).** Each Claude run is wrapped in `sandbox-exec` with reads
  denied to SSH keys, cloud credentials, shell history, browser and mail data,
  your Documents/Downloads/Desktop, other Claude sessions' transcripts, the
  bridge's own database — and to every other brain's directory. Add your own
  paths under `sandbox.deny_paths`.
- **Brain isolation.** Brains have separate directories, sessions, histories,
  and calendar credentials.
- **Loopback-only HTTP API**, which refuses a non-loopback bind address.
- **OTP guard.** A message that is only a numeric code, arriving right after
  the assistant asked for one, is dropped before it reaches history or the model.

What it does not do:

- The sandbox is containment, not a jail. It raises the cost of a prompt
  injection; it does not stop code execution.
- On Linux there is no sandbox layer at all. Run the bridge as a dedicated user.
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

**`brain.go`** — the multi-brain runner. Each message spawns `claude -p` with
`--output-format stream-json`, seeded with the system prompt and recent chat
history. Events are parsed live, so an `AskUserQuestion` reaches WhatsApp
immediately and a run over 10 seconds gets a "working on it" notice. A newer
message cancels the run in flight.

**`claude.go`** — the classic single-session runner used when `brain_mode` is
off: one Claude session per chat, resumed with `--resume`, retried without it
if the session has expired.

**`sandbox.go`** — generates a per-brain `sandbox-exec` profile.

**`store.go`** — SQLite: the whatsmeow device store, session IDs, active PIDs,
polls, and votes.

**`chunks.go`** — splits long replies at paragraph, line, then sentence
boundaries so messages break somewhere readable.

**`server.go`** — the localhost send API.

---

## Troubleshooting

**"claude: command not found"** — set `claude_path` to the CLI's absolute path.
On macOS the desktop app keeps it at
`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`.

**No reply in a group** — is the group JID in `allowed_groups`? Is it in a
brain's `groups`? Did you use a trigger keyword? Check the log.

**No reply to a DM** — the sender's number must be in `allowed_numbers`, with
country code and no `+`.

**"Not logged in" from the CLI** — run `claude` once in a terminal to
authenticate. Under launchd, check that `HOME` is set correctly in the plist.

**Logged out of WhatsApp** — delete `bridge.db` from your data directory and
re-pair. (This also clears sessions and poll history.)

**Everything times out with the sandbox on** — set `sandbox.enabled` to `false`
to confirm it's the cause, then add the path it needs to `sandbox.allow_paths`.

---

## Contributing

Issues and pull requests welcome. Please don't include real phone numbers,
group JIDs, or chat content in bug reports — redact them.

## License

MIT — see [LICENSE](LICENSE).

Built on [whatsmeow](https://github.com/tulir/whatsmeow). Not affiliated with
or endorsed by WhatsApp or Meta. Automating a WhatsApp account may conflict
with WhatsApp's Terms of Service; you are responsible for how you use it.
