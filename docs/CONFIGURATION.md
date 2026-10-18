# Configuration

The dashboard edits all of this for you (`fridolin open`).
This page is the reference behind it.

The configuration is `~/.fridolin/config.json`, or wherever `--config` or
the `FRIDOLIN_CONFIG` environment variable points (`--config` wins). A
`config.json` in the current directory is not used. On first start a safe
starter config is written there. Changes made in the dashboard apply
immediately; a few runtime settings (marked *restart*) need a restart, which
the dashboard offers: once one differs from the value the bridge started
with, every dashboard tab shows a "Restart the bridge to apply" banner until
the bridge restarts or the setting is changed back.

Validate at any time:

```bash
fridolin check
```

For a bug report, `fridolin check --redact` prints the same with phone
numbers, group JIDs, LIDs and email addresses masked and `~` for your home
folder, its log lines and any `--print-sandbox` profile included.

`fridolin paths` prints the config file and the data dir the other
commands use, and where each came from (`--json` for a program to read).
The bridge's log names both, and where each came from, at every start.

Paths may start with `~`, which expands to your home directory.

---

## Access control

### `allowed_numbers`

Phone numbers permitted to DM the bridge, with the country code. The bridge
compares digits only, so `"+1 555 123-4567"` written into the file by hand
lets 15551234567 in; an entry that is not 6 to 15 digits, or a `@lid` JID,
lets nobody in, and the Overview and the log at startup name it.

```json
"allowed_numbers": ["15551234567", "15559876543"]
```

In the dashboard both allow lists are edited on the **Chats** page (Settings
only counts them). It takes a number the way you would write it, with a `+`,
spaces, dashes or brackets, stores its digits, and refuses anything that is
not 6 to 15 of them, or a `@lid` JID (WhatsApp's internal ID for a sender,
not a number); a group must be a group JID.

An empty list means nobody may DM until you add a number, unless
`allow_any_dm` (below) is `true`. `allowed_groups` entries are compared
exactly as they are written, so one written by hand with a typo or spaces
matches no group; the log names such entries when the bridge starts, with
the form to write them in.

In an allowed group without a [`group_members`](#group_members) entry,
these numbers are also the only members whose messages are read: adding
or removing one changes who is heard there too.

Newer WhatsApp clients identify senders by an internal ID (a LID) rather than
a phone number; the bridge resolves that back to the number before checking,
so plain numbers here and in `group_members` are correct. It knows each
person by their number everywhere: a DM's history is kept under the number's
JID however WhatsApp addressed it (older lines under the LID still count as
the same chat), group members and the authors of quoted messages appear by
number, so the identity map names them, and a poll vote counts once per
person.

A message someone sends to a broadcast list you are on counts as a DM from
them and is answered in your direct chat with them, which is where WhatsApp
shows it. WhatsApp Status updates and channel posts are ignored, whoever
posts them.

### `allow_any_dm`

What an empty `allowed_numbers` means. Unset or `false` (the default, and what
new configs write): nobody may DM. `true`: **anyone** who can message the
account may use the assistant — with every permission the brains have. It
only matters while the list is empty; with numbers in it, only they are
answered. It counts in groups too: with an empty `allowed_numbers` and
`allow_any_dm` `true`, every member of an allowed group without a
[`group_members`](#group_members) entry is read.

```json
"allow_any_dm": false
```

Older versions treated an unset value as `true`. A config that relied on that
now refuses every DM: add the numbers, or set `"allow_any_dm": true` on
purpose. While anyone may DM, the dashboard says so and the bridge logs a
warning at startup and whenever an edit opens DMs to everyone.
`fridolin check` prints who may DM on its `DM allow:` line: the numbers,
`nobody` or `anyone`.

### `allowed_groups`

Group JIDs the bridge will respond in. Groups are deny-by-default — an empty
list means no groups at all.

```json
"allowed_groups": ["120363000000000000@g.us"]
```

To find a JID: add the bridge to the group and send a message. The group shows
up in the dashboard under **Chats → waiting for access**, with an *Allow*
button that also assigns it to a brain.

Allowing a group does not let everyone in it use the bridge: which of its
members get through is up to `group_members` (below). Without an entry
there, only the numbers in `allowed_numbers` do.

### `group_members`

Who in each allowed group reaches its brain: the group's JID and the phone
numbers of the members whose messages are read, with the country code, like
`allowed_numbers`. `["*"]` lets every member in.

```json
"group_members": {
  "120363000000000000@g.us": ["15551234567", "15559876543"],
  "120363000000000001@g.us": ["*"]
}
```

A message from a member a group's entry does not admit is not read at all:
it starts no run, a `!stop` or `!reset` from them does nothing, a file they
send is not downloaded, their edits and deletions change nothing, and it is
not even kept in the brain's history, so later runs see it only where an
admitted member quotes or forwards it. The log notes each one, the sender
masked; a reaction or a sticker, which the bridge does not read from
anyone, only at [`log_level`](#log_level-restart) `debug`. The group is not
offered on the dashboard again, since it is allowed. Votes in the bridge's
own polls still count from everyone in the group: a vote only picks one of
the poll's options.

- **No entry** for an allowed group: the senders `allowed_numbers` admits,
  and no one else. With an empty `allowed_numbers` and `allow_any_dm`
  `true`, that is anyone. The bridge logs each allowed group without an
  entry once at startup, and when an edit leaves one without, with the
  entry that lets everyone in again.
- **A list of numbers**: those members, and no one else. `allowed_numbers`
  does not count here, so list your own number too. An empty list lets
  nobody in.
- **`["*"]`**: every member, as an allowed group did before
  `group_members` existed. `"*"` stands on its own; next to numbers it is
  refused.

Whatever the entry, messages from the bridge's own account, sent from its
phone or any other device linked to it, always get through.

WhatsApp often names group members by LID instead of their number (see
above). The bridge looks up the number that comes with the message, or the
one WhatsApp told it before; a member whose number it cannot learn, which
happens in groups that hide members' numbers, matches no number on a list,
and only `["*"]` (or an open `allow_any_dm`, for a group without an entry)
lets them in.

The bridge refuses to start, and the dashboard to save, a `group_members`
with a key that is no group JID, an entry for a group that is not in
`allowed_groups`, a number that is none (6 to 15 digits; a `+`, spaces and
dashes are fine and dropped), or `"*"` together with numbers. The error
names a number that is none by its place in the group's list ("entry 2")
and does not repeat it, nor a key that is no group JID, so that it can go
into a bug report as `fridolin check --redact` prints it. Revoking a group
on the **Chats** page removes its entry too. The dashboard has no form for
the lists yet, and keeps them as they are when it saves: write them into
`config.json` and restart the bridge (`fridolin service restart`).
`fridolin check` tells you whether the file loads.

### `triggers`

Keywords that address the bridge in a group. Matched case-insensitively and
stripped before the text reaches Claude. A plain string matches anywhere in the
message; an object can restrict it:

```json
"triggers": ["@claude", {"text": "@c", "match": "start"}, {"text": "bot", "match": "word"}]
```

| `match` | Fires when |
| --- | --- |
| `anywhere` (default) | the keyword appears anywhere |
| `start` | the message starts with it — `@c hi` yes, `@carla` no, `hi @c` no |
| `word` | it appears as a whole word — `hey bot` yes, `robotics` no |

An @-mention of the bridge's own account always counts, whatever this list
says, and is stripped the same way: `@Fridolin !stop` is the command `!stop`.
So does a reply to any message the bridge's account sent. Sending a file
(photo, document, voice note, video; a GIF is none) and forwarding a message
count too, unless the group's brain turns them off with `routing.answer_files`
or `routing.answer_forwards` (docs/BRAINS.md, or the brain's page in the
dashboard): then a file is chatter whose caption the brain reads, and it is not
downloaded, and a forward is chatter like any other.

Untagged group messages, from the members `group_members` admits, go into the
brain's history, so that its next run in the group sees the conversation, and
produce no reply. They cost no Claude run
unless the brain's `observe` setting is `run` (see
[BRAINS.md](BRAINS.md#brainjson)), which gives each one an observe-only run
of its own; very short ones ("ok", "👍") never get one. An untagged message is
never a command: in a group, `!stop` needs the trigger (`@claude !stop`).

DMs never need a trigger.

---

## Claude

### `claude_path`

Path to the Claude Code CLI. `"claude"` uses your PATH.

On macOS the desktop app keeps it at:

```
~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude
```

That path contains a version number and changes when Claude updates. Either
point `claude_path` at a stable `claude` on your PATH, or re-run `setup.sh`
after an update.

Brains need Claude Code 2.1.302 or newer, the oldest release the bridge was
tested with: they rely on its flags and on the shape of its `stream-json`
output. The bridge logs the CLI's path and version when it starts, and the
log, `fridolin check` and the dashboard warn when it is older or missing. The
CLI normally updates itself; `claude update` does it by hand.

### `brain_model`

Model brains use — `"sonnet"`, `"opus"`, `"haiku"`, or a full model ID. An
individual brain can override it with its own `model` field. Empty means the
`model` in the brain folder's `.claude/settings.json`, or Claude Code's own
default, never the one in your `~/.claude/settings.json` (see
[`inherit_claude_user_config`](#inherit_claude_user_config)). The top-level
`model` key older versions had is [retired](#retired-keys): while
`brain_model` is empty, its value moves there.

### `brain_effort`

Passed as `--effort`: `low`, `medium`, `high`, `xhigh` or `max`. Empty uses
the CLI default. Brains can override it.

### `disallowed_tools`

Tools brains may not use, passed as `--disallowed-tools`, e.g.
`["WebFetch", "WebSearch"]`. Brains can override the list.

### `dangerously_skip_permissions`

Passes `--dangerously-skip-permissions`, so Claude uses tools without asking.

Effectively required: nobody is at the keyboard to approve a prompt, so
without it every tool call hangs. Set to `false` only if you want a
conversation-only assistant with no tool access.

Read [Security](../README.md#security) before running with this on. Without
a sandbox in effect (on Linux, or with `sandbox.enabled` off) the dashboard
warns that brains can read and change the bridge's own data; see
[`dashboard`](#dashboard).

### `run_timeout_minutes`

How long one brain run may take, in minutes. Default 30, at most 1440 (a
day); `0` lets a run take as long as it takes. A brain answers one message
at a time, so a run that hangs (a tool that waits for input, a network
stall) would otherwise hold up every later message of that brain until
someone sends `!stop`. At the limit the bridge stops the run, with every
command it started, and the chat gets an error that says so, also when
the run had asked the chat a question before it hung; the next message
runs. Long agentic tasks can take a while, so raise it rather than turn
it off if yours do.

Once a run has given its answer, the bridge waits five seconds for it to
end. A command it left running in the background that still holds the
CLI's output open is stopped then, rather than keeping the brain busy for
as long as it runs. The answer goes out either way, also when the limit
above comes within those five seconds.

### `max_concurrent_runs`

How many Claude runs may go at once, all brains together. Default 2, at
most 64; `0` lets every brain run whenever it has work, as before this
setting. A brain runs one message at a time anyway, so this matters once
more than two brains are busy at the same time: each run is a full Claude
Code agent with tools, on your CPU and on your account's rate limits, and
the next request needs them as much. A brain whose turn it is waits for
a free run, and the log says so; the sender of a request that has to
wait for one is told "⏳ Queued (#1)" (with [`queue`](#queue)'s
`notify`), as when it waits behind the brain's own work. Work addressed
to a brain gets the next free run before untagged chatter (an
observe-only run, see [`observe`](BRAINS.md)), and among them the brain
that asked first, so a brain that just had a run asks again behind the
others. A request that finds every run taken by observe-only runs stops
one of them, as it would in its own brain: the chatter is already in the
history. A change applies at once; with a lower limit, runs going on
finish and the next waits.

### `inherit_claude_user_config`

The dashboard has no field for it, but its overview warns while it is on.
To change it, stop the bridge, edit `config.json` and start the bridge
again: the bridge reads that file only when it starts, and a dashboard save
while it runs would write back the old value.

Off by default: a brain loads only its own folder's Claude Code setup, never
yours. Every brain runs with `--setting-sources project,local` and
`--strict-mcp-config`, plus `--mcp-config <brain folder>/.mcp.json` when the
folder has that file. So a brain gets the `CLAUDE.md`, `.claude/` settings,
skills, agents and commands and the `.mcp.json` servers of its own folder,
but not your user settings in `~/.claude/settings.json` (their hooks,
plugins, permissions, `env`, `apiKeyHelper` and `model`), the skills,
agents and commands in `~/.claude`, the MCP servers in `~/.claude.json`, or
those of plugins and of your claude.ai account. Given with `--mcp-config`, a
`.mcp.json` the CLI cannot use stops it from starting at all, where it used
to be ignored: one that is not a JSON object with an `mcpServers` object
(`{}` or a misspelt key included). The bridge therefore leaves out such a
file, and a symlink, anything but a regular file, or a file over 256 KiB,
and its log says why.

A server the folder's `.claude/settings.json` or `settings.local.json` turns
off with `disabledMcpjsonServers` (where Claude Code records a project
server you decline) stays off: `--mcp-config` would start it, so the bridge
passes the folder's other servers itself, as JSON on the command line. Any
process of your user, another brain's included, can read that with `ps`, as
it can the prompt, so a token in a server's `env` shows there too; to avoid
that, take a server you do not want out of `.mcp.json` instead, and the
bridge passes the file as it is. When it cannot read one of those two
files, it passes no server at all, and its log says why; that includes a
`.claude` folder that is a symlink out of the brain's folder, which the
bridge, reading outside the sandbox, does not follow. Every other
server in `.mcp.json` starts, whatever `enabledMcpjsonServers` says, as it
always did for brains; to stop one, take it out of `.mcp.json` or list it
in `disabledMcpjsonServers`.

These flags do not skip the `CLAUDE.md` and `CLAUDE.local.md` files and the
`.claude/` folders (their `CLAUDE.md`, rules, skills, agents and commands)
in the folders above a brain's folder: Claude Code reads them as part of
the project. A brain in your home folder would therefore load `~/CLAUDE.md`,
`~/.claude/CLAUDE.md` and `~/.claude/rules`, your personal instructions. So
every brain also runs with `--settings` holding a `claudeMdExcludes` list:
the `CLAUDE.md`, `CLAUDE.local.md`, `.claude/CLAUDE.md` and `.claude/rules`
of every folder above its folder, up to `/`, and `~/.claude/CLAUDE.md` and
`~/.claude/rules`, your user memory, wherever the brain lives. It adds to
the brain's own `claudeMdExcludes`, if it has any. It does not cover the
skills, agents and commands in those `.claude` folders, nor keep a brain
from reading any of these files with its tools. The
[sandbox](#sandbox-macos) does both: it keeps brains from reading or
writing any of them, up to `/`; in your home folder it denies only
`.claude/CLAUDE.md` and `.claude/rules`, since the rest of `~/.claude` is
the CLI's own state. Without the sandbox (Linux, or `sandbox.enabled` off),
brains still get the skills, agents and commands above their folder and
can read your instructions, so keep brain folders out from under folders
that hold either.

`true` brings back the old behaviour: brains load all of your setup, like a
Claude Code session you start yourself in the brain's folder, and the
sandbox lets them read those instruction files again (it still denies
writing them, as listed under [sandbox](#sandbox-macos)).

Before you upgrade to a version with this setting, check whether your
brains rely on any of this:

- Instructions brains got from `~/CLAUDE.md`, `~/.claude/CLAUDE.md`,
  `~/.claude/rules` or a `CLAUDE.md`, `CLAUDE.local.md` or `.claude/rules`
  above their folder no longer reach them, with or without the sandbox.
  `~/CLAUDE.md` reached every brain under your home folder. Copy what each
  brain needs into its own `CLAUDE.md` or `brain-prompt.md`.
- `apiKeyHelper`, `env` and `model` in `~/.claude/settings.json` no longer
  apply to brains, so neither does the API key, provider
  (`CLAUDE_CODE_USE_BEDROCK`, say) or model they choose. Move what a brain
  needs into its folder's `.claude/settings.json`, or set `brain_model`.
- Permission rules and hooks in `~/.claude/settings.json` no longer apply
  to brains, the `deny` rules and `PreToolUse` guards among them. Copy the
  ones a brain should keep into its folder's `.claude/settings.json`.
- MCP servers from `~/.claude.json`, from plugins and from your claude.ai
  account no longer load. Add the ones a brain needs to its `.mcp.json`.
- A `.mcp.json` server you declined that only `~/.claude.json` records as
  off for the brain's folder (`disabledMcpjsonServers` under `projects`)
  starts on the brain's first run: the bridge reads only the folder's
  settings, and Claude Code moves that record into the folder's
  `.claude/settings.local.json` only during that run. Copy it there first.
- With the sandbox, a `CLAUDE.md`, skill or rule in a brain's folder that is
  a symlink to one of the instruction files it denies above the folder
  (`~/CLAUDE.md`, say) no longer loads, and nothing says so: the sandbox
  goes by the real path. Copy the file into the brain's folder instead.
- Then send each brain a test message. Its reply shows that it can still log
  in; ask it which MCP tools it has and something only its `CLAUDE.md`
  answers.

> **Warning:** with this on, every brain runs your hooks, sees your personal
> instructions and can call your MCP servers' tools without asking (see
> `dangerously_skip_permissions`), on behalf of anyone who may message it,
> and every brain gets the same ones. Turn it on only if your brains rely on
> a user-level skill or MCP server. Better: copy what a brain needs into its
> own folder (`.claude/skills/`, `.mcp.json`, `CLAUDE.md`), and an `env`
> or `apiKeyHelper` it needs from `~/.claude/settings.json` into its
> folder's `.claude/settings.json`.

### `child_env_passthrough`

Names of variables in the bridge's environment that brains get besides
[the ones they always get](#what-reaches-brains). Empty by default; the
dashboard has no field for it.

```json
"child_env_passthrough": ["GROQ_API_KEY"]
```

Each entry is one exact name of letters, digits and `_`, not starting with
a digit: no prefixes such as `AWS_*`, no `NAME=value`. A name the bridge's
environment does not have is skipped. Names a setup typically needs:

- `GROQ_API_KEY` for transcription with the Groq API
- `SSH_AUTH_SOCK` for a brain that pushes over ssh with your ssh-agent
- Amazon Bedrock: `AWS_REGION`, and `AWS_PROFILE`, or `AWS_ACCESS_KEY_ID`,
  `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN`, or
  `AWS_BEARER_TOKEN_BEDROCK` (`CLAUDE_CODE_USE_BEDROCK` is on the list
  already)
- Google Vertex AI: `CLOUD_ML_REGION`, `GOOGLE_APPLICATION_CREDENTIALS`
  for a key file, and each `VERTEX_REGION_CLAUDE_...` you set
  (`CLAUDE_CODE_USE_VERTEX` and `ANTHROPIC_VERTEX_PROJECT_ID` are on the
  list already)
- the Claude CLI's settings whose names start with neither `CLAUDE_CODE_`
  nor `ANTHROPIC_`, such as `MAX_THINKING_TOKENS`,
  `BASH_DEFAULT_TIMEOUT_MS`, `BASH_MAX_TIMEOUT_MS`, `API_TIMEOUT_MS`,
  `MCP_TIMEOUT`, `MAX_MCP_OUTPUT_TOKENS` and `DISABLE_PROMPT_CACHING`
- a cache you moved, such as `HF_HOME` for the local model of
  `transcribe.py` (about 1.5 GB), which a brain otherwise downloads again
  into the default cache

A brain's run gets them, and so the scripts and tools it runs, and so do
the dashboard's `claude --version` check and the tmux server of the
activity [`viewer`](#viewer), which brains can read anyway. Anyone who may
message a brain can ask it for these values, so list only what a brain
needs, and set the variables themselves where the bridge's environment
comes from (see [Environment variables](#environment-variables)). A change
applies from the next run; an open activity viewer keeps what it started
with.

A setting that is no secret, such as the CLI's or a cache's, can go into
a brain folder's `.claude/settings.json` instead, under `env`. The CLI sets
it for that brain's runs and the tools they start:

```json
{ "env": { "MAX_THINKING_TOKENS": "16000" } }
```

---

## Brains

Brains are configured in their own folders (`brain.json`) and added in the
dashboard; see [BRAINS.md](BRAINS.md). In `config.json` they appear as:

```json
"brain_dirs": [
  { "path": "~/assistant/personal", "approved": { "id": "personal", "name": "Personal", "routing": { "handles_dms": true } } }
]
```

`approved` is the manifest you accepted — the version that runs. An entry
without one (written by hand) does not run; the Brains page shows it with a
Remove button, and a new brain cannot be added in, around or on its folder
until it is gone.

A brain whose settings clash with another (same id, a group claimed twice, two
DM brains, the same folder or a folder inside another brain's) is left out
and reported on the dashboard; the others keep running.

### Inline brains

Older configs listed brains inline, under `"brains"`. The bridge refuses to
start while that list has a brain in it; an empty list is a
[retired key](#retired-keys). To move them into their folders, see
[Configs with inline brains](BRAINS.md#configs-with-inline-brains).

### Retired keys

Older versions wrote a few top-level keys this one has no setting for. A
config that still has them loads; the log says once per start what became
of each, and the next dashboard save leaves them out:

| Key | What became of it |
|---|---|
| `model` | The model brains fell back to while `brain_model` was empty. Its value becomes `brain_model` then, so brains keep their model; otherwise it is unused. |
| `work_dir` | The folder classic mode ran in. Brains run in their own folders. |
| `brains` | Inline brains, when the list is empty or `null`. One that lists brains is refused, see [Inline brains](#inline-brains). |

Any other key the bridge does not know is logged as an unknown setting and
dropped the same way.

### `brain_mode`

Every chat goes to a brain. Classic mode, one resumable Claude session per
chat in one shared folder without the sandbox, was removed. The bridge
refuses to start with `"brain_mode": false`, or with `BRAIN_MODE` set to
anything but `true`, `1`, `yes` or `on`: set the key to `true` (or unset the
variable), start the bridge, and add a brain under *Brains → Add brain*. One
that handles direct messages and is the default answers every allowed chat,
as classic mode did.

Keep the key rather than delete it. The bridge itself writes
`"brain_mode": true` on every save, so that an older version you go back to
runs brains too: without the key, that one would run classic mode.

---

## Sandbox (macOS)

```json
"sandbox": {
  "enabled": true,
  "deny_paths": ["~/clients", "~/Projects/secret"],
  "allow_paths": []
}
```

When enabled, each Claude run is wrapped in `sandbox-exec` under a generated
profile. Changes (including added or removed brains) apply to the next run of
every brain. If the profile cannot be built or `/usr/bin/sandbox-exec` is
missing, the run is refused: the chat gets an error, the bridge log names the
cause, and Claude never starts unsandboxed. The profile is allow-by-default.
Brains can neither read nor write:

- every **other brain's** working directory
- `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`,
  `~/.config/rclone`
- `~/.git-credentials`, `~/.netrc`, shell history files
- `~/Documents`, `~/Downloads`, `~/Desktop`, `~/Movies`, `~/Music`, `~/Pictures`
- `~/Library/Mail`, `Messages`, `Calendars`, `Containers`,
  `Group Containers`, Contacts, Notes
- the profiles of Safari, Chrome (all of `Application Support/Google`),
  Firefox, Brave, Edge, Arc, Chromium, Vivaldi and Opera, with the release
  channels kept beside them (Edge Beta, Vivaldi Snapshot, Opera GX, …)
- the bridge's data directory — except `media/`, and a brain's own
  `run-tokens/<id>/` to read — under both names it has had, `~/.fridolin`
  and `~/.whatsapp-claude-bridge`, whichever is in use (an old one left
  behind can still hold the WhatsApp keys and tokens)
- the database (`db_path`, with its `-wal`, `-shm` and `-journal` files)
  wherever it is, and its folder when that lies outside the data directory,
  unless the folder is your home folder or a temp folder
- the config file (with its `.bak` and the temp file each save writes next
  to it) when `--config` puts it outside the data directory, and the `.env`
  files older versions read settings from, which may still hold some: the
  one next to the config and the one in the directory the bridge was
  started in
- the bridge's logs in `~/Library/Logs/fridolin`
- `~/.config/fridolin`, which holds the personal-data denylist the commit
  hooks read
- your Claude Code instructions above a brain's folder: the `CLAUDE.md` and
  `CLAUDE.local.md` in every folder above it, up to `/`, the `.claude`
  folder in each of them (in your home folder only `.claude/CLAUDE.md` and
  `.claude/rules`), and `~/.claude/CLAUDE.md` and `~/.claude/rules` for a
  brain outside your home folder too; unless `inherit_claude_user_config`
  is on, which makes them readable again

Other Claude Code sessions' records (`~/.claude/projects`,
`~/.claude/sessions`, yours included) are unreadable, but the CLI writes its
own run's files there, so writing stays allowed. Transcripts cannot be moved
or deleted; the small per-process files in `sessions/` can (the CLI deletes
its own on exit). Nor can a brain plant a symlink in either folder, such as
a new project's folder linked into its own, through which a later session's
transcript would reach it.

Brains can read but never write the files that are loaded or run outside
their sandbox, so a manipulated brain cannot plant code there:

- Claude Code's own configuration: `~/.claude/settings.json`,
  `settings.local.json`, `CLAUDE.md` and the `hooks`, `rules`, `agents`,
  `commands`, `skills`, `plugins` and `output-styles` folders (`CLAUDE.md`
  and `rules` are not even readable, see above). Your own Claude Code
  sessions load them, and with `inherit_claude_user_config` so does every
  brain's next run. Brains therefore cannot change Claude Code settings or
  install plugins; do that yourself.
- the `CLAUDE.md`, `CLAUDE.local.md` and `.mcp.json` in every folder above a
  brain's folder, up to and including your home folder (or up to `/` for a
  brain outside it), and the `.claude` folder in each of them below your
  home folder. Your own sessions load them. So would the brains below
  them, but the sandbox keeps the `CLAUDE.md` files and `.claude` folders
  from brains (see above), and a brain loads no `.mcp.json` but its own,
  unless `inherit_claude_user_config` is on. A brain's own folder is its
  own.
- Claude Code's auto-memory, `~/.claude/projects/<project>/memory`, which
  every new session of that project loads, yours included
- `~/Library/LaunchAgents`
- shell startup files: `~/.zshenv`, `.zprofile`, `.zshrc`, `.zlogin`,
  `.zlogout`, `.bash_profile`, `.bash_login`, `.bashrc`, `.profile`,
  `~/.config/fish`
- `~/.tmux.conf` and `~/.config/tmux`, read by the activity viewer's tmux
- `~/.gitconfig` and the files it includes (see `git_config` below)
- the bridge's own binary and, when it runs from one, its source checkout
  (launchd runs it outside any sandbox)
- the programs the bridge runs unsandboxed, when they live in your home
  folder: the Claude CLI (as found on `PATH` and the file it links to),
  rclone, the Plaud CLI with its whole npm package and the `node` that runs
  it, and tmux when the viewer is on. Brain runs therefore cannot update the
  Claude CLI or these tools, so the bridge turns the CLI's auto-updater off
  for them (`DISABLE_AUTOUPDATER=1`); your own sessions still update it.
- where the bridge would find such a program next, so that a brain cannot
  plant one: in every folder of the bridge's `PATH` (and the Plaud CLI's)
  that lies in your home folder, such as `~/.local/bin`, the names it runs
  by name (`claude`, `rclone`, `plaud`, `node`, `tmux`, `sh`, `tail`,
  `touch`, `osascript`, `open`), whether they exist there yet or not;
  `~/.local/bin/rclone`, where it looks when rclone is not in the
  [helper folders](#helper-programs) of `PATH`; and,
  unless `plaud.cli_path` is set, all of `~/.nvm/versions/node`, because
  the bridge takes the Plaud CLI of the newest node version that has one.
  Brains therefore cannot install node versions or global npm packages with
  nvm.

Of its `PATH`, the bridge looks up tmux, rclone and the Plaud CLI only in
those folders in your home folder and in a few system and package-manager
folders, which the sandbox does not protect (see
[helper programs](#helper-programs)).

Renaming counts as writing. The sandbox matches paths, not files, so a brain
that renamed `~/.ssh`, or the folder that holds another brain, used to read
it under the new name. Nothing denied can be renamed or deleted, apart from
the per-process files in `~/.claude/sessions`, and neither can any folder
above it, up to your home folder (or up to `/` for a path outside it).
Brains can still create and change files inside those folders. Nor can a
brain create such a folder while it does not exist yet, for example a
missing `~/.config` or `PATH` folder: it could create it as a symlink into
its own folder, or move a folder of its own there, and so redirect what
lies below it. The exception is `~/.claude` and the folders in it, which
the Claude CLI creates on its first run: brains can create them too, though
not as a symlink, and from the next run on they are pinned too.

`~/Library/Keychains` is deliberately **not** denied: the Claude CLI reads its
credentials from the keychain and fails with "Not logged in" without it.

`deny_paths` adds your own (denied for reading and writing). `allow_paths`
re-opens reading something the defaults closed; writing there stays denied.
It cannot re-open another brain's folder, nor the bridge's own data
directory, database, config file or credentials folder, nor your Claude
Code instructions above a brain's folder.
Entries must be absolute or start with `~/`; the bridge refuses a config with
a relative one, which would protect nothing.

The sandbox compares real paths, so every deny, brain folders included,
covers both the path as configured and the path it reaches through
symlinks (a home on another volume, a cloud-synced folder, `/var` →
`/private/var`). Symlinks are resolved again on every run, and a folder
that does not exist yet is still covered once it is created. Re-allowed
paths (`allow_paths`, media, calendar files) are resolved only where your
home, data or credentials folder itself is a symlink, never through any
other symlink, because a brain could plant one to reach something else.
If an `allow_paths` entry is a symlink or goes through one, list its real
path. A brain's own folder is allowed as configured and at the real path
the bridge found when it loaded the brains, the one the brain runs in.

A brain's own folder is always readable and writable, even inside a denied
directory. The brain cannot rename, delete or replace the folder itself,
though (with a symlink to another brain's folder, say), only what is inside
it, and the folder re-opens nothing denied inside it: another brain, the
data directory, a `db_path` folder, the credentials folder, the bridge's
checkout or a `deny_paths` entry inside a brain's folder stay as closed to
that brain as to every other, and so does the `CLAUDE.md` above a brain
further down. The sandbox refuses a brain whose folder is your home folder
or holds it (that fails every brain's runs, since each profile names every
brain's folder), and one whose folder is itself something the sandbox
protects: another brain's folder, the data, database or credentials folder,
the checkout, a default-denied folder such as `~/.ssh`. Its runs fail until
you move it. A `deny_paths` entry may be a brain's folder, which keeps that
folder from every other brain. Other tools a brain runs may still need
paths the defaults deny, so keeping brains somewhere like
`~/assistant/<name>` is simplest.

### `sandbox.git_config`

`"read"` (default): brains may read `~/.gitconfig` and the files it includes,
so they can commit in their own folder, but never write them. `"deny"` hides
git configuration entirely: those files and `~/.config/git`.
`~/.git-credentials` is denied either way.

### `sandbox.credentials_dir`

A directory of per-brain credential files kept outside the data directory
(absolute or starting with `~/`): the calendar tokens,
`google_calendar_token_<id>.json`. Brains are denied all of it, their own
token included; the bridge makes their calendar calls (see
[CALENDAR.md](CALENDAR.md#what-the-brain-gets)). Older versions let each
brain read its own token, and copied the OAuth client, secret and all,
next to it as `google_calendar_credentials_<id>.json`, which the bridge no
longer reads. It names the files of that name that hold a client secret
when it starts and on every brain's page, but deletes none itself, since
it cannot tell such a copy from a file of yours: delete the copies by
hand, unless something else of yours uses them. That matters most on
Linux or with the sandbox off, where brains can read this folder.

This is containment, not a jail — it raises the cost of a prompt injection
rather than eliminating it. Linux has no `sandbox-exec`: the bridge refuses a
config with `sandbox.enabled` on there (the Linux default is off). Running
the bridge as a dedicated user keeps brains out of your own files, but not
out of the bridge's data dir: brains run as that user too, so they can read
the dashboard token and change any setting.

### What the sandbox does not cover yet

The profile allows everything it does not deny, and writing is denied only
for the paths listed above. A manipulated brain can therefore still:

- write anywhere else your user can: other projects in your home folder
  (their `.claude/settings.json`, `.mcp.json`, `CLAUDE.md`, `.git/hooks`),
  scripts your Claude Code settings run from outside `~/.claude/hooks`,
  files a protected `CLAUDE.md` imports with `@path`, what your protected
  shell startup files source or run (such as `~/.nvm/nvm.sh`,
  `~/.oh-my-zsh`, `~/.p10k.zsh`, or a `ZDOTDIR` like `~/.config/zsh`), the
  scripts and programs a `claude_path` wrapper script runs or sources
  (only the wrapper itself is protected, and the bridge runs it outside
  the sandbox, with a brain's environment, for `claude --version`), a new
  program in a folder on your `PATH` that shadows another one (other than
  the names the bridge runs, in folders under your home folder), and
  programs outside your home folder such as `/opt/homebrew` (user-writable
  on Apple Silicon) and `/usr/local`, including a `plaud`, `rclone`,
  `tmux` or `node` in `/opt/homebrew/bin` or `/usr/local/bin` that the
  bridge would run next (it looks up its helpers there, see
  [helper programs](#helper-programs)), and a `claude`, `osascript`,
  `zenity`, `kdialog` or `open` in any folder of the service's `PATH`,
  which the bridge still runs by name outside the sandbox
  (`claude --version` for the dashboard, the dashboard's folder picker,
  `--open`)
- leave files in its own folder that run or load when you work there
  yourself, outside the sandbox: git hooks and settings in `.git/`
  (`hooks/`, `core.fsmonitor`, `core.hooksPath`), a `.envrc` for direnv,
  and its own `.claude/settings.json` (hooks), `.mcp.json` and `CLAUDE.md`
  for a Claude Code session you start there. Check them before you open a
  shell, git or Claude Code in a brain's folder.
- change the Claude Code state the CLI writes on every run, which all brains
  and your own sessions share: `~/.claude.json` (including the MCP servers
  your own sessions start), other sessions' transcripts in `~/.claude/projects`
  (it can add to them, not read or move them), `~/.claude/shell-snapshots`
  and `~/.claude/session-env` (sourced into running sessions' shell
  commands), and the small per-process files in `~/.claude/sessions`,
  which it can also move and then read
- move a prepared folder into `~/.claude/projects` for a project that has
  none yet (a symlink there is refused), which gives that project's
  sessions an auto-memory the brain wrote; with a `memory` symlink into its
  own folder inside, the brain also reads what those sessions save there
- create `~/.claude`, or a folder in it such as `~/.claude/projects`, while
  it is missing (on a home where the Claude CLI has not created it yet), by
  moving a prepared folder there (a symlink there is refused). What it put
  inside stays after the next run pins the folder: settings, hooks or a
  `CLAUDE.md` that the CLI and your own sessions load, or symlinks into its
  own folder, through which what they write there, transcripts included,
  reaches the brain
- ask launchd (`launchctl`), cron (`crontab`), `at`, LaunchServices
  (`open`), another app (`osascript`) or a tmux server that runs outside
  the sandbox (the activity viewer's, or your own) to start a program
  (`tmux run-shell`), which then runs outside the sandbox
- read what the bridge was started with, although a brain's run is handed
  only [part of it](#what-reaches-brains): the variables in the service
  file (`~/Library/LaunchAgents/ai.fridolin.bridge.plist`, which the
  profile leaves readable), what `launchctl print` shows of the job, and
  the bridge's own process environment (`ps eww`); and use your ssh-agent,
  through the socket that `launchctl getenv SSH_AUTH_SOCK` names
- write to the shared media inbox, and reach the network

Later changes give each brain its own Claude Code state folder and make
writes deny-by-default.

---

## Runtime

### `data_dir` *(restart)*

Database (including the WhatsApp session), downloaded media, the dashboard
token, the scripts token for `/send` and `/ingest` (`scripts.token`),
backup state, the bridge's rclone config and calendar credentials.
Default `~/.fridolin`; an install from before the project was renamed keeps
`~/.whatsapp-claude-bridge` while `~/.fridolin` does not exist, and says so
in the log at every start: set `data_dir` to it to keep it for good. A new
config, from `fridolin init`, `setup.sh` or the first start, gets the folder
chosen then (written with `~`), so later starts do not choose again.
`db_path` and `media_dir` override individually. Brains cannot read it,
except the `media/` inbox and their own runs' brain API tokens in
`run-tokens/<id>/`; a `db_path` elsewhere is denied to them too.

`runs/` in it lists the brain runs going on. Each run has a process group
of its own, which launchd's or systemd's cleanup does not reach, so when
the bridge crashes or is killed, the next start stops what is left of the
runs listed there, with the commands they started, before any brain runs.
It checks that each group's leader is still the process that ran, by its
start time, so a process that got the same PID since is left alone.

The lock that keeps a second bridge off the same WhatsApp session is
`bridge.lock` in `data_dir`. It does not cover a `db_path` outside
`data_dir`, so never point `db_path` at another bridge's `bridge.db`: a
second bridge with its own `data_dir` would open it anyway.

The database holds the keys of the linked WhatsApp device, so only your user
may read it: the bridge creates `bridge.db` with mode 0600, and at startup
takes group and other access away from an existing one (and from leftover
`-journal`, `-wal` and `-shm` files next to it), logging each file it changes.
The data dir gets mode 0700 at startup the same way, also when it already
existed; saving a different folder in the dashboard leaves its mode alone
until the restart. Three kinds of folder are left as they are, with a warning
in the log if other users can open them: a data dir that is your home folder
or one above it (`"~"`, or `"."` when the bridge starts in your home folder),
a data dir that belongs to another user, and the folder of a `db_path`
outside the data dir, which you may share on purpose.

### `http_listen` *(restart)*

Address of the dashboard and the local APIs. Default `"127.0.0.1:8765"`;
`"off"` disables all of it. Only loopback addresses are accepted. The bridge
binds it before connecting to WhatsApp and exits if it is taken — usually a
sign another bridge is running.

`POST /send {"jid","text"}` sends a message; it is appended to the owning
brain's history, so the assistant knows what went out under its name.
`GET /health` returns `ok` while the bridge runs, paired or not; `GET /ready`
says whether `/send` can deliver. None of these need the dashboard token, and
all of them refuse requests sent by web pages. [API.md](API.md) has their
contract and that of `/ingest`: request bodies, answers, every status code.

Brains reach `/brain/` there, which `fridolin polls` asks for the brain's
poll results and `fridolin calendar` for its calendar calls. It takes no
dashboard token but one the bridge makes for each run, in a file in the
data directory's `run-tokens/<id>/` that the sandbox lets only that
brain read; the run's `FRIDOLIN_BRAIN_TOKEN_FILE` names it. The token
stops working, and its file goes, when the run ends, and the bridge
answers with that brain's things only. The same token lets the run use
`/send` for the brain's own chats and `/ingest` for itself
([API.md](API.md#who-may-call)). The token is not in the run's
environment, since macOS shows a process's environment to every program
of the same user, other brains included. With `http_listen` off, those
commands do not work.

### `ingest.enabled`

Enables `POST /ingest {"brain","paths","text","sender"}`. A request that
still sends `scope`, the old name of `brain`, is refused with 400 and told
to use `brain`. Paths must be files inside the media directory. The
named brain receives them like WhatsApp attachments and answers in its
`reply_chat`, even when that chat belongs to another brain (your DM, say).
Off by default; see [API.md](API.md#post-ingest). It always needs a token:
yours in `<data_dir>/scripts.token`, or a brain's run's, which hands files
only to that brain.

### `script_api.tokenless_send`

```json
"script_api": { "tokenless_send": false }
```

Whether `POST /send` works without a token, as it did before tokens. Without
one it reaches only the chats `allowed_numbers` and `allowed_groups` name.
Unset means `true`, so scripts written before tokens keep working; new
configs set it to `false`. While it is on, any program on this computer, a
brain among them, can message those chats, and the dashboard says so. Have
your scripts send the token in `<data_dir>/scripts.token` as
`Authorization: Bearer …`, then turn it off (Settings → Advanced). Brains
send their run's token; see [API.md](API.md#who-may-call).

### `queue`

```json
"queue": { "mode": "queue", "max_pending": 10, "notify": true }
```

`mode: "queue"` (default) runs a brain's messages one after another and, with
`notify`, tells the sender "⏳ Queued (#2)". Each chat's messages run in the
order they came, and the chats take turns: the chat whose last run started
longest ago goes next, so a chat that sends five messages does not keep
another chat's one waiting behind all five. `mode: "interrupt"` lets a newer
request replace the sender's own: it stops their run in flight in that chat (or
a run on the chat's untagged chatter) and drops their earlier waiting request.
The newer request then waits behind the requests already queued (though ahead
of queued chatter, see below), so someone who keeps correcting themselves
cannot push other chats back. Requests from other chats, and other people's in
a group, still wait their turn (with the same notice). Untagged group chatter
never interrupts a run. Files sent through `/ingest` never interrupt a request
and never replace one another, each being a job of its own, though like any
other message they stop an observe-only run (see below).
`max_pending` applies in both modes. [Plaud](PLAUD.md) recordings take at
most five places, and half of a smaller queue (at least one), so your own
messages still get in.

Untagged group chatter waiting in the queue (in a brain whose `observe` is
`run`) is dropped when a newer message from that chat arrives — it is already
in the history the next run reads. Any other message, from any chat (one
addressed to the brain, a file through `/ingest`, a [Plaud](PLAUD.md)
recording), stops the observe-only run in flight and goes ahead of those
waiting, in both modes.
`!stop` stops the run for the chat it was sent in and drops that chat's
waiting messages; other chats keep theirs. It also stops files handed to
another brain that answers in that chat (its `reply_chat`).

### `debounce`

```json
"debounce": { "quiet_ms": 2000, "max_wait_ms": 10000 }
```

Messages from the same person in the same chat that arrive less than
`quiet_ms` apart are merged into one run (an album, a file and its question).
Files handed to different brains through `/ingest` stay apart even when those
brains answer in the same chat. A batch never waits longer than
`max_wait_ms`. `quiet_ms: 0` turns it off.

### `startup_checks`

```json
"startup_checks": {
  "required_files": ["~/.config/my-assistant/calendar-credentials.json"],
  "notify_whatsapp": true,
  "hint": "Restore them from your password manager."
}
```

Files the assistant's tools need. They are checked while the bridge runs, not
only at startup: the dashboard shows the ones missing when it asks, and the
bridge looks every five minutes. It logs each change loudly and, with
`notify_whatsapp`, sends the list to `notify_jid` (default: the first allowed
number) whenever the files missing change, once WhatsApp is connected; a file
restored and lost again is reported again. Edits to the list apply at once.

### `backup`

Google Drive backups; see [BACKUP.md](BACKUP.md).

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | run the daily backup |
| `daily_at` | `"03:30"` | local time, `HH:MM` |
| `root` | `"fridolin-backup"` | folder in Drive |
| `remote` | `"brain-gdrive"` | rclone remote name in the bridge's own rclone config |
| `keep_versions_days` | `30` | days a version folder is kept after the run that made it; `0` keeps them forever |
| `include_bridge_config` | `true` | also back up `config.json` and the brain list |
| `rclone_path` | found in the [helper folders](#helper-programs) of PATH | the rclone to run; when set, only that one: one that is missing or not executable stops backups with an error naming it. Unset, rclone is looked for in the folders of PATH the bridge takes its helpers from (see [helper programs](#helper-programs)), then in `/opt/homebrew/bin`, `/usr/local/bin` and `~/.local/bin`; a name without a folder is looked for in those PATH folders only |

### `plaud`

The Plaud recorder integration; see [PLAUD.md](PLAUD.md) for every setting.

### `dashboard`

`dashboard.disabled` turns the web dashboard off (*restart*).

Browsers sign in rather than keep the token. `fridolin open`, and the line
the bridge prints when it starts in a terminal, give the browser a link with
a login code that works once, within a minute; the page trades it (or a
token you paste) for a session that lasts 30 days, which replaces the one
the browser had. A bridge older than the `fridolin` you run `open` from
has no login codes: `open` then says so and how to open its dashboard,
rather than putting the token in the link. A session is two
halves: an HttpOnly, SameSite=Strict cookie, and a binding the page keeps
in the browser's storage for the dashboard's address and sends with every
call (`X-Bridge-Session`). The bridge takes neither alone. Cookies are not
kept apart by port, so the browser sends the cookie to every server on
`127.0.0.1`, a brain's [site preview](SITE.md) among them; that server
cannot read the dashboard's storage, which belongs to its own port.

The binding is in the browser's files on disk too, so this keeps a session
out of a brain's reach only in a browser whose profile the macOS sandbox
denies to brains: Safari, Chrome (every channel), Chromium, Brave, Arc,
Edge, Vivaldi, Opera and Firefox. In another Chromium browser (Thorium,
Yandex, Comet and others) a brain can read the binding, and gets the cookie
once you open a server it runs on `127.0.0.1` in that browser, such as its
site preview, which the Build page shows too; Chromium keeps cookies
encrypted on disk, so it needs that request. Firefox forks (Zen, LibreWolf,
Waterfox, Floorp) keep cookies unencrypted, so there a brain can read both
halves without one. Use one of the listed browsers for the dashboard, or at
least open no brain's server, the Build page included, in the one you sign
in with.

Sessions are kept, as hashes, in `<data_dir>/dashboard-sessions.json`, so a
restart keeps you signed in. *Settings → Dashboard sign-in* signs this
browser out, or every browser, which also replaces the token: scripts that
send the old one in `X-Bridge-Token` are refused from then on.
`fridolin rotate-token` does the same from a terminal, also while the
bridge is stopped. A bridge that runs with `dashboard.disabled` has no
dashboard API to replace the token through: run it while the bridge is
stopped, or once the dashboard is back on.

Versions before sign-in put the token in the link `fridolin open` opened
and kept it in the browser's storage, so after upgrading from one,
copies of the token may still be in the browser's history and in the
leftovers of that storage on disk, which a brain could read in a browser
the sandbox does not deny. They work until the token changes: run
`fridolin rotate-token` (or *Sign out everywhere*) once the new build runs,
and give scripts that send `X-Bridge-Token` the new one. The upgrade does
not do it for you, since that would cut those scripts off without warning.

`dashboard.require_token` (default `true`) requires the token from
`<data_dir>/dashboard.token` on every API call. Brains run on this machine and
could otherwise reach the dashboard with `curl`; leave it on. Turning it off
takes effect only after a restart, and the dashboard warns about it until
then and for as long as it stays off; turning it on applies at once.

The token keeps brains out only while they cannot read the data dir. The
macOS sandbox denies it to them. On Linux, or on macOS with the sandbox off,
a brain that runs with `dangerously_skip_permissions` can read the token,
`config.json` and the WhatsApp session like any other file of yours, and so
change every setting; the dashboard shows a warning whenever that is the
case.

### `otp_guard` and `otp_markers`

Catches a one-time code in a chat that was just asked for one. A chat was
asked when, in the last ten minutes, something went out there that holds
one of `otp_markers`: the assistant's own reply, or a message sent in the
chat through `/send` by a script or a brain. The markers are by default
`🔐`, `OTP`, `2FA`, `one-time code` and `verification code`, matched in
any letter case and as whole words (`OTP` is not found in "hotpot"). Add
your own (a bank's name in its code prompt, say); setting the list
replaces the defaults, so include them if you still want them. A message
in that chat, from anyone in it, is caught when it is nothing but a code:
4 to 8 digits, also in groups such as `483 920` or `483-920`, or 6 to 10
upper-case letters and digits with at least two digits, such as
`G-483920`. A number in another chat of the brain, or one that comes
later than ten minutes after the request, is a message like any other.
Only what was typed counts: a code sent as a reply to the request is
caught too, and so is a corrected code, when its sender edits the message.
Only messages from a chat are caught: text handed to a brain through
`/ingest` or by Plaud comes from a program, and the brain reads it.
Whoever sent the code is told that it was kept out of the conversation,
unless it was untagged group chatter, which the bridge never answers.

The code is never written to history or sent to the model — the assistant
never sees it. It is kept for local scripts instead:
`<data_dir>/otp/<brain>.json` holds the latest code the guard caught for
that brain, readable only by your user (in a folder only you can open) and
denied to brains by the sandbox:

```json
{"brain": "main", "chat": "15551234567@s.whatsapp.net", "sender": "15551234567",
 "code": "483920", "typed": "483 920",
 "received": "2026-10-18T09:30:12+02:00", "received_unix_ms": 1792308612345,
 "request": "9f3c2a71d04b6e58", "requested_unix_ms": 1792308590000,
 "expires_unix_ms": 1792309212345}
```

`code` is the code without spaces and dashes, `typed` the message as it
was sent. `request` is the id `/send` answered the request with, in its
`otp_request` (see [API.md](API.md#post-send)): that of the newest such
request in the chat in the last ten minutes, also when the assistant's
own reply held a marker after it, and `""` when only the brain asked, in
its own words. `requested_unix_ms` is when the request went out. The unix
times need no time zone, unlike `received`, which ends in `Z` on a host
set to UTC.

A script that asked for a code through `/send` (with one of the
`otp_markers` in its message) polls the file until its `request` is the
id it got, then takes the code and deletes the file, or renames it away
first if another program might read it too. One that asked a group also
checks `chat` and `sender`: for ten minutes anything in the group that
reads as a code is caught for its request, a year such as `2024` from
another member too, so only the code from the person it asked is its
code. The bridge deletes the file itself ten minutes after the code came
(`expires_unix_ms`), and at the next start one that expired while it was
stopped. A newer code for the same brain replaces the file: a corrected
code from the same chat, another member's number in a group, or one for
another request, which the `request`, `chat` and `sender` of the file
then tell apart.

### `max_chunk_size`

Longest single outgoing message, in characters, the `[1/3] ` numbering
included. Characters are Unicode code points, so most emoji count as one but
one with a skin tone or a family emoji counts as several. 0 or unset means the
default, 3900, which stays under the 4096 characters commonly given as
WhatsApp's limit. Anything else must be at least 100, so the numbering always
leaves room for text; the bridge refuses to start with a smaller value.

Longer replies are split and numbered `[1/3]`, `[2/3]`, … They are cut at
paragraphs first, then lines, sentence ends and spaces, each finer level only
for a part that is still too long; a word longer than a whole message is cut
between characters. Only the whitespace at a cut is dropped. The Build page
has no such limit and shows a builder brain's answer whole.

### `max_media_mb`

Largest attachment the bridge downloads, in megabytes (1 MB = 1,048,576
bytes). Default 100, at most 2048 (WhatsApp's own limit is 2 GB). A larger
file is skipped without being downloaded, and the sender gets one line
saying so; a caption sent with it still reaches the brain. The limit holds
even when a message understates its file's size: the download stops as soon
as it passes the limit. A download also has two minutes to finish, so a
high limit needs a fast connection; when one fails or runs out of time, the
sender is told to send the file again.

### `viewer`

Opens a tmux session per brain, tailing its activity log, in a Terminal window
(macOS), on the brain's first run. Off by default. Handy while tuning a
prompt; noisy otherwise. Turning it off, or removing the brain, ends the
session and closes the window; one left open by a restart is replaced on
the brain's next run. The viewer starts in the background, so a run never
waits for tmux or Terminal (each of their calls gets five seconds), and
Terminal does not come to the front. Requires tmux 3.0 or newer, in one of
the [helper folders](#helper-programs) of the bridge's `PATH` — without
tmux the bridge logs a warning and carries on. Nor does a viewer
start for a brain whose `claude-stream.log` the bridge refuses, a symlink say
(see [BRAINS.md](BRAINS.md#files-the-bridge-reads-and-writes)). The bridge
starts these sessions on tmux's default server, outside the sandbox, and any
brain can send that server commands (`tmux run-shell`), which then run
unsandboxed. The same goes for a tmux you run yourself; see
[what the sandbox does not cover yet](#what-the-sandbox-does-not-cover-yet).
A server the bridge starts keeps the environment it started with, which
any brain can read, so it gets only a few of the bridge's variables,
never the Claude CLI's login (see
[what reaches brains](#what-reaches-brains)). tmux, and the `sh` and `tail`
it runs, get `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, and Terminal is opened
with `/usr/bin/osascript`, so that no program a brain put in a folder on
the bridge's `PATH` runs in their place. A server the bridge starts keeps
that `PATH` for the windows you open in it; your shell's startup files set
your own.

### `log_level` *(restart)*

`debug`, `info`, `warn`, or `error`. `debug` is very verbose — it includes the
whatsmeow protocol chatter.

---

## Environment variables

Every setting comes from `config.json`: no environment variable overrides
one, and the bridge reads no `.env` file. Older versions did both. If a
`.env` lies next to the config, the bridge says in its log that it is no
longer read; move what it set into `config.json` (or the dashboard), and
whatever the Claude CLI or a brain's tools need into the bridge's process
environment (below). The sandbox still keeps brains from reading it.

The bridge itself reads two variables:

| Variable | What it does |
| -------- | ------------ |
| `FRIDOLIN_CONFIG` | the config file when `--config` is not given; `setup.sh` sets the service up with it |
| `BRAIN_MODE` | set to anything but `true`, `1`, `yes` or `on`, the bridge refuses to start, see [`brain_mode`](#brain_mode) |

`CONFIG_FILE`, the old name of `FRIDOLIN_CONFIG`, is ignored: the bridge
logs a warning when it is set, and `setup.sh` stops rather than set the
service up with another config. If it points at Fridolin's config, re-run
`setup.sh` with `FRIDOLIN_CONFIG` set to it; if it belongs to other
software, re-run it as `env -u CONFIG_FILE ./setup.sh`.

`setup.sh` also reads `FRIDOLIN_DATA_DIR`, the data folder it writes into
a new config (whose file is then `config.json` in that folder, unless
`FRIDOLIN_CONFIG` names another), and `FRIDOLIN_INSTALL_DIR`, the folder it
installs to (default `~/.local/lib/fridolin`). It asks the build for the
config file and the data folder (`fridolin paths`) rather than work them
out itself. `DATA_DIR` and `INSTALL_DIR`, their old names, are ignored with
a warning, since other software exports them too; when one names the
config or the install this would leave behind, `setup.sh` stops and says
how to re-run, as for `CONFIG_FILE`.

### What reaches brains

Brains are not handed the bridge's whole environment: anyone who may
message one can ask it to run `env`, so a key in the service file, or in
the shell that started the bridge, would otherwise reach every brain. A
brain's run, and so the scripts and tools it runs, gets only these
variables of the bridge's environment:

| Variables | For |
| --------- | --- |
| `HOME`, `USER`, `LOGNAME`, `PATH`, `SHELL`, `TMPDIR`, `LANG`, `LC_*`, `TERM`, `TZ` | what any program expects |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` (also in lower case), `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `SSL_CERT_DIR` | a proxy, or a firewall that inspects TLS |
| `DO_NOT_TRACK`, `TMUX_TMPDIR` | the telemetry opt-out many tools honour; where tmux keeps its socket |
| `CLAUDE_CODE_*`, `ANTHROPIC_*`, `CLAUDE_CONFIG_DIR` | the Claude CLI's login (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`), provider and gateway (`ANTHROPIC_BASE_URL`) |
| `DISABLE_AUTOUPDATER`, `DISABLE_TELEMETRY`, `DISABLE_ERROR_REPORTING` | the CLI's update check and reports |
| `CALENDAR_TZ`, `TRANSCRIBE_MODEL`, `TRANSCRIBE_LANGUAGE` | the tools brains run: the timezone of `fridolin calendar`, and the model and language of [tools/transcribe.py](../tools/transcribe.py) |

On top, the bridge sets `FRIDOLIN_BRAIN_ID`, the brain's id; `FRIDOLIN_BIN`
and `FRIDOLIN_TOOLS_DIR`, its own binary and the folder of its Python
helpers ([BRAINS.md](BRAINS.md#what-a-brain-loads)); `FRIDOLIN_API` and
`FRIDOLIN_BRAIN_TOKEN_FILE`, where the brain API is and the file with the
run's token for it (see [`http_listen`](#http_listen-restart)), which
also opens [`/send` and `/ingest`](API.md#who-may-call) to the brain's own
chats and itself;
`PLAUD_TELEMETRY_DISABLED=1` and `PLAUD_NO_UPDATE_NOTIFIER=1` for
[Plaud](PLAUD.md)'s CLI, with `plaud.brain_access` a `PATH` that also
has that CLI's folder; and `DISABLE_AUTOUPDATER=1` in the sandbox. It
also sets `BRIDGE_DATA_DIR` and `BRIDGE_DB_PATH`, the data directory and
database it runs with (absolute, wherever `data_dir` and `db_path` put
them), for a brain's own scripts; and, for one release, `BRIDGE_BRAIN_ID`,
`BRIDGE_CALENDARS` and `BRIDGE_CALENDAR_DEFAULT`, which the calendar tool
used to read. Nothing in `tools/` reads any of these any more.
`BRIDGE_CREDENTIALS_DIR`, which named the folder of the calendar tokens,
is not set any more. What the bridge's own environment has under these
names does not reach brains. The list lives in one place,
`childEnvAllowlist` in `childenv.go`.

Brains are not handed anything else, for example `GROQ_API_KEY`,
`SSH_AUTH_SOCK` (your ssh-agent), the variables of a Bedrock or Vertex
setup such as `AWS_REGION` and `CLOUD_ML_REGION`, the Claude CLI's
settings outside `CLAUDE_CODE_*` and `ANTHROPIC_*` such as
`MAX_THINKING_TOKENS`, or `HF_HOME`. Name the ones a brain needs in
[`child_env_passthrough`](#child_env_passthrough).

This only keeps variables from being handed over unasked; it is not a
secret store. A brain that goes looking can still read the bridge's
environment: from the bridge's process (`ps eww`, or `/proc/<pid>/environ`
on Linux, where brains run without a sandbox), from the service file and,
on macOS, from launchd (see [what the sandbox does not cover
yet](#what-the-sandbox-does-not-cover-yet)). Keep anything a brain must not
see out of the bridge's environment and its service file.

These programs the bridge starts for itself get the first three rows
only:

- rclone for [backups](BACKUP.md), which never gets an `RCLONE_*`
  variable but `RCLONE_CONFIG`, the bridge's own rclone config
- the [Plaud](PLAUD.md) CLI, whose `HOME`, `PATH` and `DO_NOT_TRACK` the
  bridge sets itself
- tmux and `osascript` for the [`viewer`](#viewer), which also get the
  names in `child_env_passthrough` (brains can read the viewer's tmux
  server anyway), and the system's `PATH` instead of the bridge's

rclone and the Plaud CLI never get those names, which are for brains.

While rclone connects Google Drive, and while the Plaud CLI signs you in,
they open your browser, so they also get the variables of your desktop
session that Linux needs to open one (macOS needs none of them):
`DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`, `XDG_RUNTIME_DIR`,
`DBUS_SESSION_BUS_ADDRESS`, `XDG_CURRENT_DESKTOP`, `KDE_SESSION_VERSION`,
`XDG_DATA_DIRS` (where snap and Flatpak browsers are found),
`XDG_CONFIG_DIRS` and `BROWSER`.

### Helper programs

The bridge runs tmux for the [`viewer`](#viewer), rclone for
[backups](BACKUP.md) and the [Plaud](PLAUD.md) CLI itself, outside any
sandbox. Unless `backup.rclone_path` or `plaud.cli_path` gives a path, it
looks them up only in the folders of its `PATH` that lie in your home
folder or are one of `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`,
`/usr/local/bin`, `/opt/homebrew/bin` and `/opt/local/bin`, in `PATH`'s
order. It leaves out every other folder of the service's `PATH`, which
starts with the one you installed it from and may name a folder such as
`/opt/homebrew/opt/go/bin` ahead of `/usr/bin`: a program a brain planted
there would run outside the sandbox. On macOS the
[sandbox](#sandbox-macos) keeps brains from planting these programs in the
folders in your home folder; it does not protect the others it keeps (see
[what the sandbox does not cover yet](#what-the-sandbox-does-not-cover-yet)).
Brains themselves still get the whole `PATH`.

The lookup is the same on Linux and with `sandbox.enabled` off, where it
protects nothing. A helper installed in another folder, such as snap's
`/snap/bin`, Homebrew's on Linux or nix's `/run/current-system/sw/bin`,
is not found: set `backup.rclone_path` or `plaud.cli_path` to it, and
link tmux, which has no such setting, from a folder of the service's
`PATH` in your home folder, such as `~/.local/bin`.

The Plaud CLI's `#!/usr/bin/env node` finds `node` the same way, or in
the CLI's own folder when that holds one (as nvm installs them). A `node`
that is only in another folder, such as a keg-only Homebrew `node@22` in
`/opt/homebrew/opt/node@22/bin`, snap's or nix's, is not found, and every
Plaud command fails with "env: node: No such file or directory"; setting
`plaud.cli_path` does not help then. Link `node` into `~/.local/bin` or
`/opt/homebrew/bin` (`brew link --force node@22`), or install the CLI
with the `node` it should use, as nvm does.

### Where to set them

Set what the Claude CLI and brains need in the bridge's process
environment:

- The Claude CLI's own login and network settings, if you set them rather
  than log in once with `claude`: `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN`, `HTTPS_PROXY`, `NODE_EXTRA_CA_CERTS`. Left in a
  `.env`, they do not reach `claude`, and every brain fails with "Not logged
  in" (or cannot get through the proxy).
- Keys a brain's tools need, such as `GROQ_API_KEY` for transcription,
  together with their names in `child_env_passthrough`.

Every `fridolin service install` (which `setup.sh` runs) rewrites the
launchd agent or the systemd unit. Keep these variables where it does not
drop them:

- **Linux (systemd):** `systemctl --user edit fridolin` opens a drop-in that
  the install leaves alone. Add the keys under `[Service]`, one
  `Environment=GROQ_API_KEY=...` line each, then run
  `fridolin service restart`.
- **macOS (launchd):** add the keys under `EnvironmentVariables` in
  `~/Library/LaunchAgents/ai.fridolin.bridge.plist` and run
  `fridolin service restart`, which reloads the agent. Later installs keep
  every variable there except `HOME` and `PATH`, which they set (and name
  the ones they kept; `--dry-run` does not show their values). An agent
  saved as a binary plist is converted with `plutil`; one that cannot be
  read stops the install, which leaves it as it is. The file is readable
  by you only.
- **Started by hand:** export them in the shell that runs `fridolin`.

The service's `PATH` is the one of the shell that ran the install, plus
`~/.local/bin` and the usual system folders, so brains find the same `claude`,
`uv`, `node` or `ffmpeg` you do. Install again after you change it.
