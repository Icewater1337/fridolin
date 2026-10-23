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

You can also edit `config.json` by hand, or put back one from a backup,
while the bridge runs. The bridge does not notice at once: it reads the file
again before every change it saves (from the dashboard, a calendar sign-in,
a Plaud sync and so on), and when the file changed since it last read or
wrote it, it logs so and runs on the file as it is now, as if the edit had
been made in the dashboard. The change being saved then goes on top of your
edit, so both stay; only a save of the whole Settings page, made from the
config as it was before, is refused, and the page asks you to reload, which
shows your edit. A file that does not load, with a typo say, makes every
save fail, and nothing is written over it, until you fix it (`fridolin
check` says what is wrong). To apply a hand edit straight away, restart the
bridge (`fridolin service restart`).

Validate at any time:

```bash
fridolin check
```

It lists the brains and the issues of those it leaves out, and exits with
status 3 when a brain you approved will not run (its folder is missing or
cannot hold a brain, or it clashes with another), 1 when the config does
not load. `setup.sh` shows those issues and stops before it installs in
either case.

`check`, like `fridolin open` and `fridolin rotate-token`, is about a
bridge that is set up: with no config file where it looks, a mistyped
`--config` say, it says so and exits with status 1, and makes nothing,
instead of checking the defaults (`fridolin init` writes a starter
config).

For a bug report, `fridolin check --redact` prints the same with phone
numbers, group JIDs, LIDs and email addresses masked and `~` for your home
folder, its log lines and any `--print-sandbox` profile included.

`fridolin doctor` looks at the machine rather than the routing: the config
loads, the data dir is writable and has room (a warning under 20 GB free,
a failure under 2 GB), the Claude CLI is there and recent enough (the
Codex CLI in Codex mode), and sandboxed brains have a login
(`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` in your shell or the
service's environment). It also checks what the coming parts of
[the plan](PLAN.md) need — Tailscale running, its auth key and the four
Backblaze B2 keys in the host secrets (the Keychain, account `fridolin`,
on macOS; 0600 files under `$XDG_CONFIG_HOME/fridolin/secrets/` on Linux),
Docker with its daemon up, and `restic`, `rclone`, `ffmpeg`, `litestream`
and `age` — and reports those as `missing` or `warn` without failing. It
asks whether each secret is there and never reads one. Every line that is
not `ok` has the command that fixes it. It exits with status 1 when a
required check fails, 0 otherwise; `--json` prints the checks for scripts
and `--redact` masks the output as check's does.

`fridolin paths` prints the config file and the data dir the other
commands use, and where each came from (`--json` for a program to read).
The bridge's log names both, and where each came from, at every start.

Paths may start with `~`, which expands to your home directory.

[`schemas/config.schema.json`](schemas/config.schema.json) is a JSON Schema
of the file, for an editor or a script to check it against; tests keep it
to what the bridge reads. It refuses what stops the bridge from starting,
but not what the bridge only warns about, such as a group JID with a typo.
It also refuses keys the bridge does not know, which the bridge logs and
leaves out, and a few values the bridge takes anyway: a `log_level` in
capitals, and an `http_listen`
on loopback written other than as `127.x.x.x`, `localhost` or `[::1]`
with a port number, such as `[::ffff:127.0.0.1]:8765`. The other way
round, most editors take a whole number written as `1.0`, which the
bridge refuses.

---

The optional `_comment` field holds a note in the config file and has no effect
on the bridge.

## `version`

`config.json` says which version of the file it is: `"version": 2` today.
The bridge writes it on every save, and you never set it. A file without
it is version 1, as every config was before the key existed. The bridge
reads one as version 2, folding in the [retired keys](#retired-keys) of
version 1, and its next save writes it as version 2; starting does not
rewrite the file. The version goes up only when a key is renamed, removed
or comes to mean something else, never for a new key, which an older file
simply lacks.

A bridge refuses to start with a `config.json` of a newer version than it
reads, and the dashboard refuses to save one, since a save would drop the
settings it does not know. That happens when you go back to an older
build (`fridolin service rollback`) after a newer one has saved the
config: run the newer one again, or put back a copy of `config.json` from
before it (each save keeps the file before it as `config.json.bak`). A
build from before the key exists reads a version 2 file as it is, logs
`version` as an unknown setting and leaves it out when it saves.

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
members get through is up to `group_members` (below), which **Members**
next to the group on the **Chats** page sets. Without an entry there,
only the numbers in `allowed_numbers` do.

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
into a bug report as `fridolin check --redact` prints it.

In the dashboard, **Members** next to each group under **Chats → Allowed
groups** edits its entry, which applies at once: *Whom the allowed
numbers admit* (no entry), *Every member of the group* (`["*"]`) or *Only
these numbers*. It checks each number as the bridge does, keeps it as
digits, once, and says when the bridge refuses the list. Revoking a group
on the same page removes its entry too. Written into `config.json` by
hand, an entry applies once the bridge restarts (`fridolin service
restart`). `fridolin check` tells you whether the file loads, and its
`heard in` line for each allowed group says whose messages there are
read.

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

## AI mode

### `agent_provider`

`"claude"` (default) or `"codex"`. Change it in **Settings → AI mode →
Mode**, then Save. It applies to the next run of every brain without a
restart. A running turn keeps its provider, model and effort. Both modes
use the same brain prompt, files, chat history, routing and delivery queue.

### `codex_path`, `codex_model`, `codex_effort`

The Codex CLI executable (default `"codex"` on PATH), model (default
`"gpt-6.1-sol"`) and reasoning effort (default `"high"`). Effort can be
`low`, `medium`, `high`, `xhigh` or `max`. Empty fields use these defaults.
Install Codex CLI 0.163.0 or newer and use a model your account can access.
See the [Codex CLI guide](https://learn.chatgpt.com/docs/codex/cli).

These are separate from Claude's `brain_model`, `brain_effort`, and the
`model` / `effort` overrides in `brain.json`. Those settings apply only in
Claude mode and are preserved when switching. Codex uses the global Codex
model and effort for every brain.

Each brain has its own `CODEX_HOME` at `<data_dir>/brains/<id>/codex`, even
when Fridolin's sandbox is off. Sign in once for each brain from Terminal,
replacing the data folder and brain ID below as appropriate:

```sh
mkdir -p ~/.fridolin/brains/main/codex
CODEX_HOME=~/.fridolin/brains/main/codex codex -c 'cli_auth_credentials_store="file"' login
```

The dashboard's Overview shows the sign-in command when that brain has no
login file. The normal `codex login` in your personal home does not sign in
these isolated brains. The bridge never copies your personal credentials,
configuration, plugins or chats. An alternative is `CODEX_API_KEY` in the
service environment; it is passed only to Codex runs. Anyone allowed to
message a brain can use its account and read the credentials given to it.
Never paste credentials into chat or commit them.

Codex runs `codex exec --json` with its prompt on stdin, without saved
sessions or inherited user configuration, rules, or ancestor AGENTS.md
instructions. Put shared instructions in the brain's prompt file. Its
final completed answer becomes the WhatsApp reply; progress stays in the
activity log. Failed or incomplete turns do not send a partial answer.

Fridolin's macOS sandbox wraps Codex and every child process just as it
wraps Claude. When tools are enabled and that outer sandbox is active,
Codex's own sandbox is bypassed to avoid nested macOS sandboxes. Otherwise
Codex uses `workspace-write` when tools are enabled, or `read-only` when
they are not, always without interactive approvals. Fridolin refuses to
run when a configured outer sandbox cannot be applied. The timeout,
concurrency limit and `!stop` work in both modes.

Blocking `WebFetch` or `WebSearch` disables Codex's combined web tool
(`web_search = "disabled"`). Other Claude blocked-tool names have no safe
equivalent: a brain with any other effective `disallowed_tools` entry refuses
Codex runs rather than silently dropping that restriction. User Claude
plugins and `.mcp.json` are not imported. Fridolin's built-in site builder supplies its own skills
and MCP tools in Codex format. The site's separate read-only search helper
continues to use Claude's Read/Grep/Glob tools.

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

In Claude mode, passes `--dangerously-skip-permissions`, so Claude uses
tools without asking. New configs turn it on. In Codex mode, this selects
writable execution instead of the CLI's read-only sandbox, always without
interactive approvals; see [AI mode](#agent_provider).

Without it, nobody is there to approve a tool: a run is `claude -p`, which
asks no one, so Claude Code refuses every tool that needs approval, such
as running a command, editing or writing a file, or fetching a web page,
and the run goes on without it. Tools that need no approval still work:
reading and searching the files in the brain's folder, and whatever a
`permissions.allow` rule in the brain folder's `.claude/settings.json`
allows (or one in your own settings, with
[`inherit_claude_user_config`](#inherit_claude_user_config)). A brain then
answers and reads its files, but cannot write its memories, run its
skills' scripts or look anything up on the web.

The [sandbox](#sandbox-macos) does not depend on this setting: it limits
what every run can read and write either way. With this on, it is what
keeps a brain out of the rest of your computer.

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

For both Claude and Codex, once a run has given its answer or its error,
the bridge waits five seconds for it to end. A command it left running in the background that
still holds the CLI's output open is stopped then, rather than keeping the
brain busy for as long as it runs. One that left the run's process group
(a daemon, or a command started with `setsid`) is out of the bridge's
reach, but it no longer holds the brain either: the bridge stops waiting
for the output it holds open five seconds after the CLI exited, also when
the run was stopped or reached the limit above. The answer goes out
either way, also when a stop or the limit above comes within those five
seconds. The first result is final: a CLI error already reported is kept
in the same way, and a result with no answer is still an empty-output
error. Later output is drained without replacing that result.

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
writing any of them, up to `/`, and all of `~/.claude`, since a sandboxed
brain keeps its Claude Code state in a config folder of its own. Without
the sandbox (Linux, or `sandbox.enabled` off),
brains still get the skills, agents and commands above their folder and
can read your instructions, so keep brain folders out from under folders
that hold either.

`true` brings back the old behaviour: brains load all of your setup, like a
Claude Code session you start yourself in the brain's folder, and share
your Claude Code config folder rather than keep one of their own. The
sandbox then lets them read those instruction files and your setup again,
and write the CLI's state in your config folder, never the configuration
itself (see [with `inherit_claude_user_config`](#with-inherit_claude_user_config)).

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

`approved` is the manifest you accepted — the version that runs.
`moved_from`, which a core move sets, is where the folder lives on its home
machine while the move keeps it in the volume (`/data/brains-folders/…`):
the move back brings the folder home and points `path` at it again
([CORE.md](CORE.md#brain-folders-on-another-machine)). An entry
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

Version 1 configs, from older versions, may have a few top-level keys
that version 2 has no setting for. A version 1 config that still has them
loads; the log says once per start what became of each, and the next
dashboard save, which writes version 2, leaves them out:

| Key | What became of it |
|---|---|
| `model` | The model brains fell back to while `brain_model` was empty. Its value becomes `brain_model` then, so brains keep their model; otherwise it is unused. |
| `work_dir` | The folder classic mode ran in. Brains run in their own folders. |
| `brains` | Inline brains, when the list is empty or `null`. One that lists brains is refused, see [Inline brains](#inline-brains). |

In a version 2 config they are unknown keys. Any key the bridge does not
know, at the top or inside a section, is logged as an unknown setting by
its path (`sandbox.enable`, a typo of `sandbox.enabled`, say) and dropped
the same way.

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
cause, and Claude never starts unsandboxed. `fridolin check --print-sandbox
<brain id>` prints the profile a run would get.

### Writing

**Writing is denied everywhere**, except in a brain's own places:

- its own folder;
- its media inbox, `<brain id>/` in the media folder (`media/` in the
  data directory unless `media_dir` puts it elsewhere, see
  [`data_dir`](#data_dir-restart)), where the files sent to its chats land;
- its own state, `brains/<brain id>/` in the data directory, which the
  bridge creates before each run: `claude/`, the brain's own Claude Code
  config folder (`CLAUDE_CONFIG_DIR`: its settings, transcripts, todos and
  `.claude.json`), `tmp/` (`TMPDIR`, the CLI's `CLAUDE_CODE_TMPDIR`, and
  `TMPPREFIX` for zsh, which ignores `TMPDIR`) and `cache/`
  (`XDG_CACHE_HOME`, and `npm_config_cache` for npm). It stays, with the
  inbox, when the brain is removed, and a brain added later under the same
  id takes both over (see [BRAINS.md](BRAINS.md#removing-a-brain));
- with [`plaud.brain_access`](PLAUD.md#where-the-login-lives), the Plaud
  login it shares, `~/.plaud/tokens.json`, in place: the Plaud CLI
  rewrites it when it refreshes the login, after it sets the modes of the
  file and of `~/.plaud`. A brain cannot remove, rename or replace the
  file (with a symlink, through which the bridge's own Plaud CLI would
  write your next sign-in), nor write anything else in `~/.plaud`;
- `/dev/null`, `/dev/zero`, `/dev/dtracehelper`, `/dev/fd`.

Writing preferences by their domain (`defaults write com.apple.Terminal
…`) is denied too: cfprefsd writes them outside the sandbox, and some apps
run what their preferences say, such as the command a Terminal profile
starts when you open a window. A program may still read preferences, and
write a preferences file by its path in the brain's own places.

So a manipulated brain cannot leave anything that runs or loads outside its
sandbox: not in `~/.claude` (settings and their hooks, `CLAUDE.md`, skills,
plugins, auto-memory), which your own sessions load, nor in another
project's `.claude/settings.json` or `.mcp.json`, a `CLAUDE.md` above any
brain's folder, `~/Library/LaunchAgents`, a shell startup file, the files
those load (`~/.nvm/nvm.sh`, `~/.oh-my-zsh`), `~/.gitconfig`, a program in
a `PATH` folder (`~/.local/bin`, or `/opt/homebrew` and `/usr/local`,
which your user may write), the bridge's own binary, install folder and
checkout, or the programs the bridge runs unsandboxed (the Claude CLI,
rclone, the Plaud CLI with its npm package and node, tmux for the viewer,
node for the site, and git, `gh`, npm and the rest it finds on its
`PATH`). Nor can it change another brain's state or inbox. These stay
unwritable even where a brain's own folder would cover one: a brain folder
may not be or hold any of them, nor a folder the Plaud CLI's `PATH` names
or one of `/opt/homebrew/bin`, `/usr/local/bin` and `~/.local/bin`, where
the bridge also looks, nor lie inside such a program's npm package or
nvm's `~/.nvm/versions/node` (see [what wins](#what-wins)).

A folder of the bridge's own `PATH` inside a brain's folder, such as a
`bin/` with the scripts that brain's prompt runs by name, is no reason to
refuse the brain: the bridge takes it off its own `PATH` when it loads
the brains, and says so in its log, so it runs nothing from it, and hands
it to that brain's runs only, first on their `PATH`. `fridolin service
install` leaves such a folder out of the service's `PATH` too, and writes
it to `FRIDOLIN_BRAIN_PATH` in the service file instead, from where the
bridge hands it on the same way, and its log says which brain gets each
of those folders (`PATH: <folder>, from FRIDOLIN_BRAIN_PATH, goes to
brain <id>'s runs only`). A folder taken off stays off until the
bridge restarts, also when its brain is removed: what that brain left
there is still its own.

That also means brains cannot update the Claude CLI (the bridge turns its
auto-updater off for them, `DISABLE_AUTOUPDATER=1`; your own sessions
still update it), install programs, node versions or global npm packages,
or write a cache shared with you, such as `~/.cache` or
`~/Library/Caches`. Tools that follow `XDG_CACHE_HOME` (uv, Hugging Face
models, as `transcribe.py` downloads them) use the brain's own `cache/`, so
each brain downloads such a model once for itself. Point a tool that does
not at its own folder, for example in the brain folder's
`.claude/settings.json`: `{ "env": { "HF_HOME": "/Users/you/assistant/a/.cache/hf" } }`.

**Temporary files.** A brain's `TMPDIR` is its own `tmp/`, and writing
`/tmp`, `/var/tmp` or your own temp folder (`/var/folders/…`) is denied.
Python's `tempfile`, Node's `os.tmpdir()`, Go's `os.TempDir` and shell
here-documents follow `TMPDIR`, the CLI's Bash tool `CLAUDE_CODE_TMPDIR`
and zsh `TMPPREFIX`, which the bridge sets to the same folder. macOS's
`mktemp` does not: without a template (`mktemp`, `mktemp -d`, `mktemp -t
name`) it takes your user's temp folder even when `TMPDIR` is set, and fails
with "Operation not permitted", as does any fixed path in `/tmp`. Name the
folder instead: `mktemp "$TMPDIR/name.XXXXXX"`, `mktemp -d
"$TMPDIR/name.XXXXXX"` or `mktemp -p "$TMPDIR" name.XXXXXX`. A new brain's
starter `brain-prompt.md` says so; add the same to the prompt of a brain
you made before. What a brain leaves in its `tmp/` stays there until it
or you remove it.

**Logging in.** The login you made with `claude` lives in your keychain,
which the sandbox closes to brains (see [reading](#reading)), with
`inherit_claude_user_config` too. So a sandboxed brain needs a login from
the bridge's environment: run `claude setup-token` and set the token it
prints as `CLAUDE_CODE_OAUTH_TOKEN` there, or set `ANTHROPIC_API_KEY`, or
a Bedrock or Vertex setup (see [where to set them](#where-to-set-them)).
The dashboard warns while none of them is set. A brain with a config
folder of its own starts with empty Claude Code state; the transcripts of
its earlier runs stay in `~/.claude/projects`, which it can no longer
read.

### Reading

Reading is allowed by default. Brains cannot read:

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
- your Claude Code setup and state: all of `~/.claude` (and of the
  bridge's own `CLAUDE_CONFIG_DIR`, if it has one), with your settings,
  plugins, skills and every session's transcript, and `~/.claude.json`,
  with its MCP servers, and its backups; unless
  `inherit_claude_user_config` is on (see below)
- the bridge's data directory — except the brain's own media inbox
  (`media/<brain id>/`), its own state (`brains/<brain id>/`) and its own
  `run-tokens/<id>/` to read — under both names it has had, `~/.fridolin`
  and `~/.whatsapp-claude-bridge`, whichever is in use (an old one left
  behind can still hold the WhatsApp keys and tokens)
- the media folder, wherever `media_dir` puts it, except the brain's own
  inbox in it: attachments are downloaded into `media/.incoming/`, and the
  brain that takes the message moves them into its inbox, so no brain sees
  the files sent to another brain's chats. A brain cannot rename or
  replace its inbox folder itself, only what is inside it. Before the
  first inbox, the bridge moves every folder older versions left in the
  media folder, such as `media/plaud/` with every brain's Plaud
  transcripts, into `media/.old/`, so that none becomes the inbox of a
  brain with its name, and with them every file at its top from before
  the bridge started: older versions downloaded every chat's attachments
  there, where `/ingest` would hand them to any brain that guessed a
  name. A folder `media/.inboxes/` says it has done so. What it cannot
  move (a folder without write permission, an immutable file) stays where
  it is, the log says so, and `media/.inboxes/stuck` lists it: a brain
  with its name gets no inbox, and no run, until you move it out of the
  media folder; every other brain runs.
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
  `CLAUDE.local.md` in every folder above it, up to `/`, and the `.claude`
  folder in each of them below your home folder; unless
  `inherit_claude_user_config` is on, which makes them readable again
- `~/Library/Keychains`, even for an `allow_paths` entry that covers it.
  An item whose access list trusts a program a brain can run is readable
  without a prompt: the tokens `gh` and the Claude CLI itself store
  through `/usr/bin/security`, what git's credential helper keeps. The
  program that asks reads the keychain file, so this closes them to every
  program, copies included. It is also why brains log in with a token
  (see [writing](#writing)).

The folders above a brain's own places may still be looked at, though not
listed, as configured and by their real paths: programs check every folder
on the way to a path (`mkdir -p`, `realpath`), and would fail in a brain's
state below the data directory, or in a brain folder below `~/Documents`
(also one reached through a symlink, such as `~/assistant` →
`~/Documents/assistant`).

### Starting code elsewhere

A brain could also hand a command to something that runs it outside the
sandbox. It cannot run `launchctl` (launchd would start the program),
`open` (LaunchServices; a `.command` file runs in Terminal) or `osascript`
(another app, such as Terminal's `do script`), and sending Apple events or
opening anything through LaunchServices is denied whichever program
tries. `crontab` and `at` are setuid programs, which no sandboxed process
may run. Nor can it connect to a unix socket in your home folder (a Docker
daemon's: `docker run -v ~:/h`, under `~/.docker`, Colima or OrbStack; the
sandbox goes by the real path, so `/var/run/docker.sock` counts), in your
own temp folder (an editor's command socket) or in tmux's folder
(`tmux run-shell` on the activity viewer's server or your own), except
the sockets in its own folder and state, where the CLI keeps one. Nor can
it type into the terminal you started the bridge from (dev mode, or
`fridolin` in the foreground): every run starts in a session of its own,
without a controlling terminal, so it cannot push keystrokes into your
shell (`TIOCSTI`), and `/dev/tty` is not writable either.

### What wins

The sandbox applies the last rule that matches, so the profile is written
in this order, weakest first:

1. Writing is denied everywhere, but for the devices above, and so is
   writing preferences.
2. The home-folder defaults above and `deny_paths` entries outside the
   brain's folder deny reading.
3. The brain's own folder (reading and writing) and `allow_paths`
   (reading) open what is above.
4. Other brains' folders, the bridge's data directory, media folder,
   database, config file and credentials folder, the keychains, your
   instructions above the brain's folder and `~/.claude/CLAUDE.md` and
   `rules` are denied again, whatever 3 opened, and so is writing the
   bridge's binary, install folder and checkout, the programs it runs and
   the folders it finds them in (a `PATH` folder that holds the brain's
   own folder excepted, which the brain could not write otherwise, and
   one inside a brain's folder, which the bridge does not run from).
5. What the bridge gives the brain inside those: its media inbox, its
   state and, to read, its runs' brain API tokens; with
   `plaud.brain_access` the shared Plaud login; and with
   `inherit_claude_user_config` your Claude Code config folder, though
   the denies of 4 inside it (a Claude CLI installed in `~/.claude/local`,
   say) come again after it.
6. What stays closed inside the brain's own places: a `deny_paths` entry
   inside its folder, the database, and the folders themselves.
7. Starting code elsewhere, as above.

So `deny_paths` adds your own read denies, and `allow_paths` re-opens
reading something the defaults or `deny_paths` closed; writing there stays
denied. Neither the brain's folder nor an `allow_paths` entry can open
another brain's folder, the bridge's own files, the keychains or your
instructions above the folder. Entries must be absolute or start with `~/`; the bridge refuses
a config with a relative one, which would protect nothing.

The sandbox compares real paths, so every deny, brain folders included,
covers both the path as configured and the path it reaches through
symlinks (a home on another volume, a cloud-synced folder, `/var` →
`/private/var`). Symlinks are resolved again on every run, and a folder
that does not exist yet is still covered once it is created. Re-allowed
paths (`allow_paths`, the inbox, the state, the brain API tokens) are resolved
only where your home, data, media or credentials folder itself is a
symlink, never through any other symlink, because a brain could plant one
to reach something else. If an `allow_paths` entry is a symlink or goes
through one, list its real path. A brain's own folder is allowed as
configured and at the real path the bridge found when it loaded the
brains, the one the brain runs in.

A brain's own folder is always readable and writable, even inside a
default-denied folder such as `~/Documents`. The brain cannot rename,
delete or replace the folder itself, though (with a symlink to another
brain's folder, say), only what is inside it; the same goes for its inbox
and its state folders. A `deny_paths` entry inside a brain's folder wins
over the folder: the brain can neither read nor write it, nor move it or a
folder between it and the brain's folder, while an `allow_paths` entry
inside it opens reading again. A `deny_paths` entry may also be a brain's
folder, or hold one, which keeps that folder from every other brain. The
sandbox refuses a brain whose folder is your home folder or holds it (that
fails every brain's runs, since each profile names every brain's folder),
one whose folder is or holds something it protects (another brain's
folder, a default-denied folder such as `~/.ssh`, `~/.claude`,
`~/.gitconfig`, a program the bridge runs or a folder it finds programs
in, as listed under [writing](#writing)), and one whose folder lies inside
what the bridge keeps for itself (the data, media, database or credentials
folder, `~/.claude`, `~/Library/LaunchAgents`, the keychains, the bridge's
install folder or checkout, the npm package of a program it runs, nvm's
node versions). The dashboard refuses such a folder, and the bridge leaves a
configured one out, with an issue on the Brains page, until you move it.
Other tools a brain runs may still need paths the defaults deny, so keeping
brains somewhere like `~/assistant/<name>` is simplest.

### With `inherit_claude_user_config`

Brains then share your Claude Code config folder (`~/.claude`, or the
bridge's `CLAUDE_CONFIG_DIR`) instead of keeping their own, so they load
your setup, and the sandbox lets them read it and write the state the CLI
keeps there and in `~/.claude.json`. Not your login, though: it lives in
your keychain, which stays closed, so brains need a token in the bridge's
environment as well (see [logging in](#writing)). Never the
configuration itself: `settings.json`, `settings.local.json`, `CLAUDE.md`
and the `hooks`, `rules`, `agents`, `commands`, `skills`, `plugins` and
`output-styles` folders stay unwritable, as do the programs the bridge
runs from there (a Claude CLI installed in `~/.claude/local`, and that
folder when it is on `PATH`), and so does every project's
auto-memory,
`projects/<project>/memory`, which every new session of that project
loads, yours included. Other sessions' records (`projects` and `sessions`,
yours included) stay unreadable; transcripts cannot be moved or deleted,
the small per-process files in `sessions` can (the CLI deletes its own on
exit), and no symlink can be planted in either folder, such as a new
project's folder linked into the brain's own, through which a later
session's transcript would reach it. The config folder and those two
cannot be renamed or removed; while they do not exist yet the CLI may
create them, though not as a symlink. A brain can still change the state
all brains and your own sessions share, such as `~/.claude.json` with the
MCP servers your own sessions start, and the shell snapshots sourced into
running sessions.

### `sandbox.git_config`

`"read"` (default): brains may read your git configuration, so they can
commit in their own folder: `~/.gitconfig`, `~/.config/git` and every file
they include (`[include]` and `[includeIf]`, whatever the condition,
followed as git follows them, up to ten levels deep). `"deny"` hides all of
it. In both modes brains can never write any of it, where `core.fsmonitor`
or an alias would run in your own git, also when an included file lies in
a brain's folder. `~/.git-credentials` and `~/.config/git/credentials` are
denied either way.

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
config with `sandbox.enabled` on there (the Linux default is off), except in
the `fridolin-core` container, where brains run under bubblewrap instead
(on by default there; see [The brains' sandbox in the container](CORE.md#the-brains-sandbox-in-the-container)).
Running the bridge as a dedicated user keeps brains out of your own files,
but not out of the bridge's data dir: brains run as that user too, so they
can read the dashboard token and change any setting.

### What the sandbox does not cover

Reading is denied only for the paths listed above, and the profile leaves
everything but files alone. A manipulated brain can therefore still:

- read whatever else your user can read: other projects in your home
  folder, configuration under `~/.config` and `~/Library` not listed above,
  and the files the bridge runs (its tools, the Claude CLI)
- find out whether a name it guesses is there in a folder it may not read
  (the top of the media folder, another brain's inbox, `~/.ssh`): the
  sandbox refuses a name that exists with "Operation not permitted" and one
  that does not with "No such file or directory", so it hides what a file
  holds, not that it is there
- leave files in its own folder that run or load when you work there
  yourself, outside the sandbox: git hooks and settings in `.git/`
  (`hooks/`, `core.fsmonitor`, `core.hooksPath`), a `.envrc` for direnv,
  and its own `.claude/settings.json` (hooks), `.mcp.json` and `CLAUDE.md`
  for a Claude Code session you start there. Check them before you open a
  shell, git or Claude Code in a brain's folder.
- with `inherit_claude_user_config`, change the Claude Code state it shares
  with your own sessions: `~/.claude.json` (including the MCP servers your
  own sessions start), other sessions' transcripts (it can add to them, not
  read or move them), `~/.claude/shell-snapshots` and
  `~/.claude/session-env` (sourced into running sessions' shell commands)
  and the small per-process files in `~/.claude/sessions`, which it can
  also move and then read; and move a prepared folder into
  `~/.claude/projects` for a project that has none yet, or into a missing
  `~/.claude` (a symlink there is refused), which then holds what the brain
  put there
- ask the system's keychain service for an item of the data-protection
  keychain (where apps keep items with `kSecUseDataProtectionKeychain`, and
  iCloud Keychain) that trusts a program it can run: the service reads that
  keychain, not the brain, so the file deny above does not apply
- run programs it wrote itself, in its own folder or `TMPDIR`: the sandbox
  refuses only `launchctl`, `open` and `osascript`, because an interpreter
  (`python3`, `sh`, `node`) runs any script a brain writes anyway, and a
  brain's folder holds programs it needs (`node_modules/.bin`, a virtual
  environment, the site builder's workspace)
- ask launchd to start a program through its own interface, the one
  `launchctl` uses, from a program of its own (Python's `ctypes`, say), or
  run a copy of `launchctl` where macOS still runs one; and talk to other
  system services, unix sockets outside the folders above, and servers on
  localhost (a notebook or dev server you run) that may do what they are
  told. Some of them write on the brain's behalf, outside the sandbox:
  from a program of its own a brain may still ask to add a login item
  (Service Management or the shared file lists) or an item to the
  data-protection keychain
- read what the bridge was started with, although a brain's run is handed
  only [part of it](#what-reaches-brains): the variables in the service
  file (`~/Library/LaunchAgents/ai.fridolin.bridge.plist`, which the
  profile leaves readable), what `launchctl print` shows of the job, and
  the bridge's own process environment (`ps eww`); and use your ssh-agent,
  through the socket that `launchctl getenv SSH_AUTH_SOCK` names
- reach the network, the bridge's own `/send` and `/ingest` among it

For isolation beyond this, run each brain, or the whole bridge, as a macOS
user of its own.

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
`db_path` and `media_dir` override individually, each also *(restart)*. The bridge makes the
data dir and the folders of both as it starts, and no sooner: a folder typed
into Settings is made by the restart that uses it. Until then backup and
Google files, Plaud state and its default phone inbox, one-time-code handoffs,
attachments, and brain/site state and tokens stay at their running paths.
The sandbox and folder browser keep protecting the running data directory.
An explicitly configured `sandbox.credentials_dir` or `plaud.phone_dir` still
changes immediately. A `media_dir` must be
the bridge's alone: on the first start of a version with media inboxes,
every folder in it and every file at its top moves to `.old/` in it (see
[reading](#reading)). Brains cannot read it,
except their own inbox in `media/` (`media/<brain id>/`), their own state
in `brains/<id>/`, which they also write (see [writing](#writing)), and
their own runs' brain API tokens in `run-tokens/<id>/`; a `db_path` or
`media_dir` elsewhere is denied to them too, but for the inbox.

`runs/` in it lists the brain runs going on. Each run has a session and
process group of its own, which launchd's or systemd's cleanup does not
reach, so when
the bridge crashes or is killed, the next start stops what is left of the
runs listed there, with the commands they started, before any brain runs.
It checks that each group's leader is still the process that ran, by its
start time, so a process that got the same PID since is left alone.

The [core, vault and nodes](#core-vault-and-nodes) keep their state in
folders of their own in the data dir: `secrets/` (age-encrypted files),
`audit/` (the audit log, JSONL), `vault.db` (the vault's index), `vault/`
(with `documents/` and `workspaces/`), `nodes/` (the node registry and
keys), `tsnet/` (the tailnet node's state) and `core/` (the lease's state
and exports being staged). Each is made, with mode 0700, by the feature
that uses it when it runs; the bridge does not make them as it starts, so
a data dir with all of these features off has none of them.

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
to use `brain`. Paths must be files at the top of the media directory, or
already in the named brain's own inbox. A file in any other folder of the
media directory is refused, since those hold files for brains, which a
brain could otherwise ask `/ingest` for itself: another brain's inbox, the
inbox a removed brain or one that is left out left behind,
`media/.incoming/`, where WhatsApp attachments wait for their brain, and
`media/.old/`, where the bridge moved the folders older versions kept in
the media directory (see [the sandbox](#reading)), such as
`media/plaud/` with every brain's Plaud transcripts. A script that wrote
to a folder of its own in the media directory must write to its top
instead; its folder is in `media/.old/` now. Older versions downloaded
every WhatsApp attachment to the top as well; the bridge moved those into
`media/.old/` too, with every other file that was at the top before it
first started with inboxes, so `/ingest` cannot hand them to a brain that
guesses a name. A file a script left there for `/ingest` before that
start is in `media/.old/` as well: write it again. The named brain takes
the files into its own inbox (`media/<brain id>/`), receives them like
WhatsApp attachments and answers in its `reply_chat`, even when that chat
belongs to another brain (your DM, say). Off by default; see
[API.md](API.md#post-ingest). It always needs a token: yours in
`<data_dir>/scripts.token`, or a brain's run's, which hands files only to
that brain, and only files in its own inbox (a symlink there only when it
leads to a file in the inbox).

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

When the bridge stops or restarts, a brain's run in progress gets 15
seconds to finish and send its answer. The WhatsApp messages still
waiting, in a queue or in a [`debounce`](#debounce) batch, are kept in
`bridge.db` and answered after the next start, unless they were sent more
than an hour before it. So are those that arrive while the bridge stops:
the next start handles them, and the edits and deletions that came with
them, as if they had just come in, commands included. A run the stop
cut short is not run again, since it may have done part of what it was
asked: its chat gets "🔄 I restarted while I was working on your message.
Send it again if you still need an answer." Files sent through `/ingest`
and messages from the dev page are not kept; a [Plaud](PLAUD.md)
recording is handed over again after the restart.

Shutdown waits for a finished run's answer to reach the outbox before
closing the database, including when its brain was removed or moved.
Edits and deletions saved during shutdown are applied before replayed
messages can start new runs. This protects an orderly stop and restart;
a forced exit, crash or power failure can still lose pending messages,
including during startup replay.

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
| `include_secret_files` | `false` | also upload `.env` files, private keys and tokens from brain folders (see [BACKUP.md](BACKUP.md)); config.json only |
| `rclone_path` | found in the [helper folders](#helper-programs) of PATH | the rclone to run; when set, only that one: one that is missing or not executable stops backups with an error naming it. Unset, rclone is looked for in the folders of PATH the bridge takes its helpers from (see [helper programs](#helper-programs)), then in `/opt/homebrew/bin`, `/usr/local/bin` and `~/.local/bin`; a name without a folder is looked for in those PATH folders only |

### `plaud`

The Plaud recorder integration; see [PLAUD.md](PLAUD.md) for every setting,
including `interval_seconds`, `polished`, `message`, `phone_dir` and
`phone_upload`.

### GitHub

GitHub has no setting in `config.json`. It is connected from the dashboard
(*Integrations → GitHub*), whose app keeps its key where the core keeps
its secrets, never here; see [GITHUB.md](GITHUB.md). The second choice,
a typed token, is the environment variable `FRIDOLIN_GITHUB_TOKEN` (in
the container, the host secret `fridolin.github.token`, which `core
start` passes). Brains never get it: a run gets only the token of its own
grant, as gh's login in `GH_CONFIG_DIR`.

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
`otp_markers` in its message) takes it with
[`POST /otp/take`](API.md#post-otptake) and the id it got: the bridge
answers with the hand-off and deletes it, and with `404` until the code
is there. That works also when the core runs in a container, whose data
folder the computer's scripts cannot read. A script on the bridge's own
machine may instead poll the file until its `request` is the id it got,
then take the code and delete the file, or rename it away first if
another program might read it too. One that asked a group also
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
leaves room for text. The bridge accepts at most 65536 characters per chunk
to bound individual messages, and refuses values outside that range.

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
sender is told to send the file again. At most three files download at
once, all chats together; a file whose message declares it over the limit
is refused at once, without waiting for them. A chat's messages after a
file wait for it, so they reach the brain in the order they were sent.

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
starts these sessions on tmux's default server, outside the sandbox. The
sandbox keeps brains off its socket, as off that of a tmux you run
yourself, so they cannot send it commands (`tmux run-shell`); see
[starting code elsewhere](#starting-code-elsewhere).
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

`debug`, `info`, `warn`, or `error` (case-insensitive). Unknown values are
rejected. `debug` is very verbose — it includes the
whatsmeow protocol chatter.

---

## Core, vault and nodes

The settings of the containerized core, the tiered vault in Backblaze B2,
nodes and the approval policy, which [PLAN.md](PLAN.md) describes. The
first three are off by default, so a `config.json` without them runs as
before; this version reads and checks them, and the parts that use them
follow. The policy applies with its defaults.
A value of `0` or `""`, like leaving the key out, means the default.

### `core`

| Field | Default | Meaning |
| --- | --- | --- |
| `tailnet.enabled` *(restart)* | `false` | join the tailnet as a device of its own (tsnet), with the auth key from the host's secrets, and serve the dashboard and the node port there |
| `tailnet.hostname` *(restart)* | `"fridolin-core"` | the core's name on the tailnet: one DNS label, lower-case letters, digits and `-` |
| `lease.enabled` *(restart)* | `false` | take and renew the core lease, `core/lease.json` in the vault bucket, so two copies of the core never run one WhatsApp session |
| `lease.renew_seconds` *(restart)* | `300` | how often the holder renews the lease, 1 to 86400 |
| `lease.stale_seconds` *(restart)* | `1200` | how long after its last renewal another host may take the lease over, 1 to 86400; must be more than `renew_seconds` |
| `memory_limit_gb` | `8` | the memory the core's container may use, in GB |

`core.tailnet.enabled`, `core.tailnet.hostname`, `core.lease.enabled`,
`core.lease.renew_seconds` and `core.lease.stale_seconds` need a restart:
the tailnet node joins and the lease is taken as the bridge starts.

[docs/CORE.md](CORE.md) says what the tailnet node and the lease do. With
`lease.enabled` the bridge finds the vault bucket as `fridolin core`
does: `$FRIDOLIN_CORE_STORE`, else `vault.bucket` *(restart)* with
`B2_VAULT_KEY_ID` and `B2_VAULT_KEY`; it does not start without one.

### `vault`

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | run the vault service |
| `hot_budget_gb` | `50` | the most the hot tier on this host may hold, in GB |
| `activity_days` | `60` | a file stays hot this many days after it was last used |
| `cold_after_days` | `90` | a binary goes cold after this many days even without a markdown companion |
| `headroom_percent` | `10` | share of the budget eviction keeps free, 1 to 90 |
| `bucket` | `"fridolin-vault"` | the B2 bucket of the cold tier, the core's exports and the lease |
| `snapshots_bucket` | `"fridolin-snapshots"` | the B2 bucket of the restic and Litestream snapshots |
| `endpoint` | `"s3.eu-central-003.backblazeb2.com"` | the buckets' S3 endpoint: a host name, without `https://` or a path |
| `b2_daily_budget` | `2000` | Class B and Class C transactions a day each that `fridolin doctor` and the dashboard measure the bridge's B2 use against: they warn at 70%. B2's free tier caps each class at 2,500 a day ([VAULT.md](VAULT.md#b2-transactions)) |
| `targets` | `[]` | backup targets besides the cloud: restic repositories in folders on nodes, below ([VAULT.md](VAULT.md#node-targets)) |

A bucket name is 6 to 63 lower-case letters, digits and `-`, starting and
ending with a letter or digit and not with `b2-`. The keys to the buckets
are never in `config.json`: they live in the host's secrets. A change to
the `vault` section starts the vault again with it, except
`b2_daily_budget`, which only changes what is warned about, and `targets`,
which run on their own. What the vault does with these is in
[VAULT.md](VAULT.md).

Each of `vault.targets` (the Vault page's *Add target…* writes them; up to
16) is:

```json
{"name": "nas", "kind": "node", "node": "a1b2c3d4e5f6", "folder": "D:\\Backups\\fridolin",
 "nightly_at": "03:00", "hourly": false, "include_data_dir": false,
 "retention": {"hourly": 24, "daily": 14, "weekly": 8, "monthly": 12},
 "check_days": 7, "read_data_days": 30, "window_hours": 26}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `name` | | 1 to 32 lower-case letters, digits and `-`; a brain chooses the target as `"node:<name>"` in its `backup.targets` |
| `kind` | | `"node"`, the only kind |
| `node` | | the node's id (`fridolin nodes`) |
| `folder` | | an absolute path on the node, in its own spelling |
| `nightly_at` | `"03:00"` | when the nightly snapshot runs, `HH:MM` local time |
| `hourly` | `false` | a snapshot each hour besides |
| `include_data_dir` | `false` | the nightly snapshot takes the data dir too, less the brain folders that do not choose the target |
| `retention` | 24, 14, 8, 12 | what pruning keeps: hourly, daily, weekly, monthly; one that keeps nothing is refused |
| `check_days` | `7` | how often `restic check` runs |
| `read_data_days` | `30` | how often a check reads 5% of the data back |
| `window_hours` | `26` | how old the last snapshot may grow before `fridolin doctor` warns |

### `nodes`

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | accept connections from nodes |
| `listen` | `":8766"` | the node port, `host:port`; an empty host is every interface, otherwise an IP address or a host name |

`nodes.enabled` and `nodes.listen` wait for a restart: the bridge opens the
node port as it starts, on the address it had then. The registry of nodes,
their approvals and the join codes are in `nodes/nodes.json` in the data
dir; [NODES.md](NODES.md) says how a machine joins and what it may do.

### `policy`

| Field | Default | Meaning |
| --- | --- | --- |
| `max_runs_per_hour` | `20` | the most runs one chat starts in an hour; the next is refused with a notice (files handed in, delegated tasks and untagged group chatter do not count) |
| `max_pending_irreversible` | `3` | the most irreversible actions waiting for approval at once; a run's `request_approval` beyond it is refused |
| `approval_timeout_hours` | `24` | how long an approval poll waits before it counts as no |

The policy applies from this version on; see
[Skills, risk and approvals](BRAINS.md#skills-risk-and-approvals).

A negative number stops the bridge, as does one above the maximum
[the schema](schemas/config.schema.json) gives it (a day of seconds for the
lease, 90 for `headroom_percent`, 30 days for `approval_timeout_hours`).

### `housekeeping`

A job that keeps the machine the bridge runs on clean: every
`every_hours` it looks for what the bridge and its children left
running — processes of brain runs that are over, run MCP servers
(`fridolin mcp --run`) whose run is over, the vault's `rclone rcd`,
Litestream and restic without a bridge as their parent, activity viewer
tmux sessions nobody watches, zombies — and for stale locks: restic locks
of a process of this host that is gone (removed with `restic unlock`,
which leaves the locks of runs still going), `bridge.lock` naming a dead
process, and run records whose process group is gone.

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | run on the schedule; `fridolin housekeeping run` and the dashboard run it either way |
| `every_hours` | `48` | how often it runs |
| `dry_run` | `true` | only report what it would stop or remove; set it to `false` once its reports look right |
| `cpu_percent` | `80` | CPU, in percent of one core, that makes a process heavy |
| `rss_mb` | `2048` | resident memory in MB that makes a process heavy |
| `min_age_minutes` | `60` | processes younger than this are left alone: a run that just ended may still be winding down |

It stops only what it can tell is the bridge's own: a run's processes
carry `FRIDOLIN_BRAIN_TOKEN_FILE`, naming a file in the data dir's
`run-tokens/` that exists exactly while the run lasts; the vault's
children run with the data dir's own paths. Each stop is SIGTERM, then
SIGKILL after five seconds, for the process and what it started, after
checking that each PID is still the process it saw. On a native install
and on a node, other processes are never touched and not even listed. In
the core's container, where everything is the core's, a process that is
not the bridge's and over a threshold is put to the owner as an approval
poll (no answer counts as no), and stopped only after a yes. Live
processes of the bridge over a threshold are reported. In a lockdown it
reports only.

Each action is a line of kind `housekeeping` in the audit log; the owner
gets a WhatsApp message only when the job stopped or removed something or
found a problem it cannot fix, never in a dry run. The Overview page shows
its last report, `fridolin housekeeping status` too, and `fridolin doctor`
checks that it ran.

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

With `vault.enabled`, the bridge reads the vault's keys and passwords
from its environment, `B2_VAULT_KEY_ID`, `B2_VAULT_KEY`,
`FRIDOLIN_VAULT_CRYPT_PASSWORD`, `FRIDOLIN_VAULT_CRYPT_PASSWORD2`,
`B2_SNAPSHOTS_KEY_ID`, `B2_SNAPSHOTS_KEY` and `RESTIC_PASSWORD`, else (on
a native install) from the host's secrets; and `FRIDOLIN_VAULT_LOCAL`, a
folder that stands in for Backblaze in dev mode ([VAULT.md](VAULT.md)).

The `fridolin core` commands ([CORE.md](CORE.md)) also read
`FRIDOLIN_HOME`, the data dir they work on (the image sets it to its
volume, `/data`), `FRIDOLIN_AGE_IDENTITY`, the key exports are encrypted
to, `FRIDOLIN_CORE_STORE`, a folder or rclone `remote:bucket` in place of
the vault bucket, and `B2_VAULT_KEY_ID` and `B2_VAULT_KEY`, the vault
bucket's key, which only the rclone they start gets. `fridolin core start`
passes them into the container from the keychain.

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
| `CLAUDE_CODE_*`, `ANTHROPIC_*`, `CLAUDE_CONFIG_DIR` | the Claude CLI's login (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`), provider and gateway (`ANTHROPIC_BASE_URL`); sandboxed brains get a `CLAUDE_CONFIG_DIR` of their own |
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
has that CLI's folder; and in the sandbox `DISABLE_AUTOUPDATER=1`, and
what points the run at the brain's own state (see [writing](#writing)):
`CLAUDE_CONFIG_DIR` (unless `inherit_claude_user_config` is on), `TMPDIR`,
`CLAUDE_CODE_TMPDIR`, `TMPPREFIX`, `XDG_CACHE_HOME` and
`npm_config_cache`. It also sets `BRIDGE_DATA_DIR` and `BRIDGE_DB_PATH`,
the data directory and database it runs with (absolute, wherever
`data_dir` and `db_path` put them), for a brain's own scripts; and, for one
release, `BRIDGE_BRAIN_ID`, `BRIDGE_CALENDARS` and
`BRIDGE_CALENDAR_DEFAULT`, which the calendar tool used to read. Nothing
in `tools/` reads any of these any more. `BRIDGE_CREDENTIALS_DIR`, which
named the folder of the calendar tokens, is not set any more. What the
bridge's own environment has under these names does not reach brains. The
list lives in one place, `childEnvAllowlist` in `cmd/fridolin/childenv.go`.

A brain whose folder holds folders of the bridge's `PATH` gets them first
on its `PATH` (see [writing](#writing)); no other brain's run, and nothing
the bridge runs, gets them.

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
on macOS, from launchd (see [what the sandbox does not
cover](#what-the-sandbox-does-not-cover)). Keep anything a brain must not
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

While the Plaud CLI signs you in, it opens your browser, so it also gets
the variables of your desktop session that Linux needs to open one (macOS needs none of them):
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
[sandbox](#sandbox-macos) keeps brains from writing in any of these
folders, as anywhere outside their own places (see [writing](#writing)),
so the lookup only adds to it there. Brains themselves still get the
whole `PATH`.

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

- The Claude CLI's own login and network settings: `CLAUDE_CODE_OAUTH_TOKEN`
  (which `claude setup-token` prints) or `ANTHROPIC_API_KEY`, and
  `HTTPS_PROXY`, `NODE_EXTRA_CA_CERTS`. Sandboxed brains need the login
  here: the sandbox closes your keychain to them, so the one you made with
  `claude` does not reach them. Without the sandbox, logging in
  once with `claude` is enough. Left in a `.env`, these do not reach
  `claude`, and every brain fails with "Not logged in" (or cannot get
  through the proxy).
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
  every variable there except `HOME`, `PATH` and `FRIDOLIN_BRAIN_PATH`,
  which they set (and name
  the ones they kept; `--dry-run` does not show their values). An agent
  saved as a binary plist is converted with `plutil`; one that cannot be
  read stops the install, which leaves it as it is. The file is readable
  by you only.
- **Started by hand:** export them in the shell that runs `fridolin`.

The service's `PATH` is the one of the shell that ran the install, plus
`~/.local/bin` and the usual system folders, so brains find the same `claude`,
`uv`, `node` or `ffmpeg` you do. Its folders inside a brain's folder go to
`FRIDOLIN_BRAIN_PATH` instead, for that brain's runs only (see
[writing](#writing)). Install again after you change it.
