# Configuration

The dashboard edits all of this for you (`./fridolin open`).
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
./fridolin check
```

Paths may start with `~`, which expands to your home directory.

---

## Access control

### `allowed_numbers`

Phone numbers permitted to DM the bridge. Country code, no `+`, no spaces.

```json
"allowed_numbers": ["15551234567", "15559876543"]
```

An empty list means nobody may DM until you add a number, unless
`allow_any_dm` (below) is `true`.

Newer WhatsApp clients identify senders by an internal ID rather than a phone
number; the bridge resolves that back to the number before checking, so plain
numbers here are correct.

A message someone sends to a broadcast list you are on counts as a DM from
them and is answered in your direct chat with them, which is where WhatsApp
shows it. WhatsApp Status updates and channel posts are ignored, whoever
posts them.

### `allow_any_dm`

What an empty `allowed_numbers` means. Unset or `false` (the default, and what
new configs write): nobody may DM. `true`: **anyone** who can message the
account may use the assistant — with every permission the brains have. It
only matters while the list is empty; with numbers in it, only they are
answered.

```json
"allow_any_dm": false
```

Older versions treated an unset value as `true`. A config that relied on that
now refuses every DM: add the numbers, or set `"allow_any_dm": true` on
purpose. While anyone may DM, the dashboard says so and the bridge logs a
warning at startup and whenever an edit opens DMs to everyone.
`./fridolin check` prints who may DM on its `DM allow:` line: the numbers,
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
So does sending a file, and so does forwarding a message.

Untagged group messages still run through the brain — so it keeps up with the
conversation — but produce no reply. Very short messages ("ok", "👍") are
skipped entirely. An untagged message is never a command: in a group, `!stop`
needs the trigger (`@claude !stop`).

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

### `model` and `brain_model`

Model to use — `"sonnet"`, `"opus"`, `"haiku"`, or a full model ID. Empty uses
the CLI default. `brain_model` applies to brains, and an individual brain can
override it with its own `model` field. `model` is left from classic mode:
brains use it while `brain_model` is empty. For a brain, empty means the
`model` in its folder's `.claude/settings.json`, or Claude Code's own default,
never the one in your `~/.claude/settings.json` (see
[`inherit_claude_user_config`](#inherit_claude_user_config)).

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

Read [Security](../README.md#security) before running with this on.

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

`approved` is the manifest you accepted — the version that runs.

A brain whose settings clash with another (same id, a group claimed twice, two
DM brains, the same folder or a folder inside another brain's) is left out
and reported on the dashboard; the others keep running.

### Inline brains

Older configs listed brains inline, under `"brains"`. The bridge refuses to
start while that list has a brain in it; an empty list is ignored. To move
them into their folders, see
[Configs with inline brains](BRAINS.md#configs-with-inline-brains).

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
- `~/Library/Mail`, `Messages`, `Calendars`, `Safari`, Chrome and Firefox
  profiles, `Containers`, `Group Containers`, Contacts, Notes
- the bridge's data directory — except `media/` and the brain's own calendar
  credentials — under both names it has had, `~/.fridolin` and
  `~/.whatsapp-claude-bridge`, whichever is in use (an old one left behind
  can still hold the WhatsApp keys and tokens)
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
  `~/.local/bin/rclone`, where it looks when rclone is not on `PATH`; and,
  unless `plaud.cli_path` is set, all of `~/.nvm/versions/node`, because
  the bridge takes the Plaud CLI of whichever node version sorts last.
  Brains therefore cannot install node versions or global npm packages with
  nvm.

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
(absolute or starting with `~/`). Brains are denied all of it except their
own calendar pair (`google_calendar_credentials_<id>.json`,
`google_calendar_token_<id>.json`).

This is containment, not a jail — it raises the cost of a prompt injection
rather than eliminating it. Linux has no `sandbox-exec`: the bridge refuses a
config with `sandbox.enabled` on there (the Linux default is off). Run the
bridge as a dedicated user instead.

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
  the sandbox, with a brain's environment, for the dashboard's
  `claude --version`), a new program in a
  folder on your `PATH` that shadows another one (other than the names the
  bridge runs, in folders under your home folder), and programs outside
  your home folder such as `/opt/homebrew` (user-writable on Apple Silicon)
  and `/usr/local`, including a `plaud`, `rclone` or `node` there that the
  bridge would run next, since launchd's `PATH` lists them first
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
token, backup state, the bridge's rclone config and calendar credentials.
Default `~/.fridolin`. `db_path` and `media_dir` override
individually. Brains cannot read it, except the `media/` inbox and their own
calendar credentials; a `db_path` elsewhere is denied to them too.

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

`POST /send {"jid","text"}` sends a message (see the README); it is appended to
the owning brain's history, so the assistant knows what went out under its
name. Text that is empty or only whitespace is refused with 400, since there
would be nothing to send. Status, broadcast-list and channel JIDs
(`…@broadcast`, `…@newsletter`) are refused with `400 {"error": …}`.
`GET /health` returns `ok` while the bridge runs, paired or not. `GET /ready`
says whether `/send` can deliver: `200` once WhatsApp is connected (or in dev
mode), `503` otherwise, both with
`{"ready", "whatsapp", "version", "started_at"}`, where `whatsapp` is the
connection state (`pairing`, `connecting`, `connected`, `logged-out`,
`replaced`, …). None of these need the dashboard token, and all of them refuse
requests sent by web pages.

### `ingest.enabled`

Enables `POST /ingest {"brain","paths","text","sender"}` (`scope` is accepted
as an alias of `brain`). Paths must be files inside the media directory. The
named brain receives them like WhatsApp attachments and answers in its
`reply_chat`, even when that chat belongs to another brain (your DM, say).
Off by default.

### `queue`

```json
"queue": { "mode": "queue", "max_pending": 10, "notify": true }
```

`mode: "queue"` (default) runs a brain's messages one after another and, with
`notify`, tells the sender "⏳ Queued (#2)". `mode: "interrupt"` lets a newer
request replace the sender's own: it stops their run in flight in that chat (or
a run on the chat's untagged chatter) and drops their earlier waiting request.
The newer request then waits behind what is already queued, so someone who
keeps correcting themselves cannot push other chats back. Requests from other
chats, and other people's in a group, still wait their turn (with the same
notice). Untagged group chatter never interrupts a run, and files sent through
`/ingest` neither interrupt nor replace one another: each is a job of its own.
`max_pending` applies in both modes.

Untagged group chatter waiting in the queue is dropped when a newer message
from that chat arrives — it is already in the history the next run reads.
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

Files the assistant's tools need. Missing ones are logged loudly, shown on the
dashboard and, with `notify_whatsapp`, sent to `notify_jid` (default: the
first allowed number) once WhatsApp is connected.

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
| `rclone_path` | found on PATH | |

### `plaud`

The Plaud recorder integration; see [PLAUD.md](PLAUD.md) for every setting.

### `dashboard`

`dashboard.disabled` turns the web dashboard off (*restart*).
`dashboard.require_token` (default `true`) requires the token from
`<data_dir>/dashboard.token` on every API call. Brains run on this machine and
could otherwise reach the dashboard with `curl`; leave it on.

### `otp_guard` and `otp_markers`

Drops a message that is only a 4–8 digit number when one of the assistant's
last 50 outbound messages contains one of `otp_markers` — by default `🔐`,
`OTP`, `2FA`, `one-time code`, `verification code`. Add your own (a bank's
name in its code prompt, say); setting the list replaces the defaults, so
include them if you still want them. Only what was typed counts: a code sent
as a reply to the request is caught too.

The code is never written to history or sent to the model — the assistant
never sees it. It is handed to whatever local script asked for it instead:
`<data_dir>/otp/<brain>.json` holds the latest caught code
(`{"code", "chat", "sender", "received"}`), readable only by your user and
denied to brains by the sandbox. A script that requested a code through
`/send` (with one of the `otp_markers` in its message) polls that file for a
`received` time after its request.

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
between characters. Only the whitespace at a cut is dropped.

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
(macOS). Off by default. Handy while tuning a prompt; noisy otherwise. Requires
tmux — without it the bridge logs a warning and carries on. Nor does a viewer
start for a brain whose `claude-stream.log` the bridge refuses, a symlink say
(see [BRAINS.md](BRAINS.md#files-the-bridge-reads-and-writes)). The bridge
starts these sessions on tmux's default server, outside the sandbox, and any
brain can send that server commands (`tmux run-shell`), which then run
unsandboxed. The same goes for a tmux you run yourself; see
[what the sandbox does not cover yet](#what-the-sandbox-does-not-cover-yet).
A server the bridge starts keeps the environment it started with, which
any brain can read, so it gets only a few of the bridge's variables,
never the Claude CLI's login (see
[what reaches brains](#what-reaches-brains)).

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
| `BRIDGE_DATA_DIR`, `BRIDGE_DB_PATH`, `CALENDAR_TZ`, `TRANSCRIBE_MODEL`, `TRANSCRIBE_LANGUAGE` | the tools in [tools/](../tools/): the data directory and database if you moved them, the calendar's timezone, the transcription model and language |

On top, the bridge sets `BRIDGE_BRAIN_ID`, `BRIDGE_CREDENTIALS_DIR`,
`BRIDGE_CALENDARS` and `BRIDGE_CALENDAR_DEFAULT` for the calendar tool, and
`DISABLE_AUTOUPDATER=1` in the sandbox. The list lives in one place,
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
  names in `child_env_passthrough`: brains can read the viewer's tmux
  server anyway

rclone and the Plaud CLI never get those names, which are for brains.

While rclone connects Google Drive, and while the Plaud CLI signs you in,
they open your browser, so they also get the variables of your desktop
session that Linux needs to open one (macOS needs none of them):
`DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`, `XDG_RUNTIME_DIR`,
`DBUS_SESSION_BUS_ADDRESS`, `XDG_CURRENT_DESKTOP`, `KDE_SESSION_VERSION`,
`XDG_DATA_DIRS` (where snap and Flatpak browsers are found),
`XDG_CONFIG_DIRS` and `BROWSER`.

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

Re-running `setup.sh` rewrites the launchd plist and the systemd unit from the
templates in [deploy/](../deploy/), which drops anything added to them. Keep
these variables where it does not:

- **Linux (systemd):** `systemctl --user edit fridolin` opens a drop-in that
  `setup.sh` leaves alone. Add the keys under `[Service]`, one
  `Environment=GROQ_API_KEY=...` line each, then run
  `systemctl --user restart fridolin`.
- **macOS (launchd):** there is no drop-in. Add the keys under
  `EnvironmentVariables` in `~/Library/LaunchAgents/ai.fridolin.bridge.plist`,
  reload the agent with `launchctl bootout gui/$(id -u)/ai.fridolin.bridge`
  and `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ai.fridolin.bridge.plist`,
  and add them again after every `setup.sh` run.
- **Started by hand:** export them in the shell that runs `./fridolin`.
