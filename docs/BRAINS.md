# Brains

A brain is one isolated assistant: a folder with its own prompt, memories,
skills and conversation history. Every chat the bridge answers is routed to
exactly one brain.

## Adding a brain

In the dashboard: **Brains → Add brain**, then pick a folder. You can

- click **Choose folder…** for your system's folder dialog,
- browse your home folder inside the dashboard (and name a new folder there),
- or type a path (`~/assistant/family`).

The bridge checks the folder (it must be inside your home folder, must not be
inside or contain another brain, and must not be the bridge's data folder),
then writes what is missing:

```
<folder>/
├── brain.json                       this brain's settings
├── brain-prompt.md                  sent ahead of each message — make it yours
├── memories/
│   ├── README.md
│   └── people/whatsapp-identity-map.md
└── skills/                          two example skills
```

Existing files are never overwritten, so you can turn a notes folder you
already have into a brain. The brain starts receiving messages immediately; no
restart.

## brain.json

```json
{
  "version": 1,
  "id": "family",
  "name": "Family",
  "routing": {
    "handles_dms": false,
    "default": false,
    "groups": ["120363000000000000@g.us"]
  },
  "model": "",
  "effort": "medium",
  "disallowed_tools": ["WebFetch"],
  "prompt_file": "brain-prompt.md",
  "reply_chat": "",
  "backup": { "enabled": true, "exclude": ["node_modules/", "*.log", "/big-exports/"] }
}
```

| Field | Meaning |
| --- | --- |
| `id` | Stable identifier (lowercase, digits, `-`, `_`). Used for the stored session, history attribution, the sandbox profile and per-brain credential file names. It cannot be changed; remove the brain and add the folder again under a new id instead. |
| `name` | Display name. |
| `routing.handles_dms` | This brain answers direct messages. At most one brain. |
| `routing.default` | This brain answers allowed groups no brain claims. At most one; without one, the first brain is the fallback. |
| `routing.groups` | Group JIDs this brain answers. The group must also be allowed in the global settings. |
| `model`, `effort` | Override the global `brain_model` / `brain_effort`. |
| `disallowed_tools` | Override the global list; omit to inherit, `[]` to allow every tool. |
| `prompt_file` | The prompt sent ahead of every message, relative to the folder. It must stay inside the folder, and be a file rather than a symlink (see [Files the bridge reads and writes](#files-the-bridge-reads-and-writes)). |
| `reply_chat` | Where answers go for files handed in through `/ingest`. Default: the first allowed number for the DM brain, else the brain's first group. |
| `backup` | Google Drive backup for this folder; see [BACKUP.md](BACKUP.md). |
| `calendar` | The Google account this brain signed in with and the calendars it may use; set by *Connect Google Calendar*, see [CALENDAR.md](CALENDAR.md). |

## Approval

The bridge does not simply trust `brain.json`. Your accepted version is stored
in the bridge's config (`brain_dirs[].approved`), and that is what runs.

Brains run with broad permissions inside their own folder, so a brain could
edit its own `brain.json` — for instance after reading a document with
instructions hidden in it — to claim your direct messages or another brain's
group. Such a change has no effect: the dashboard marks the brain as
*changed on disk* and shows the difference, and you approve it or not.
Edits you make in the dashboard update both copies.

## Files the bridge reads and writes

The bridge itself, outside the sandbox, reads these files in a brain's
folder: its prompt file and `conversation-history.jsonl`, which it also
appends every message to. A brain can change any file in its folder, and
put a symlink to `~/.ssh` or to another brain's history in place of one of
them, so the bridge opens the folder once when it loads the brain and goes
through that: it never leaves the folder through a symlink, and it takes
only a regular file, not a symlink at the file itself, even one to another
file in the folder. It treats such a file as missing, writes nothing
through it, and its log says why. Copy a file into the folder rather than
link it.

## Moving a brain

Brain folders are portable. To move one to another machine: copy the folder
(or restore it from a Drive backup), then *Add brain* on the new machine — the
dashboard offers to use the `brain.json` it finds. Keep the same `id` so
history and credentials still match.

## Removing a brain

*Remove* unregisters the brain: its runs stop and its chats fall back to the
default brain. The folder is left exactly as it was.

## Inline brains (older configs)

Configs from before brain folders define brains inline in `config.json`
(`"brains": [{"name": "...", "work_dir": "..."}]`). They keep working.
`fridolin migrate` (or the banner on the Brains page) writes a
`brain.json` into each work dir and moves the entry to `brain_dirs`, keeping
the same id — sessions and history carry over. Editing an inline brain in the
dashboard migrates that one brain.

## What a brain loads

Each message is one `claude -p` run in the brain's folder. Its prompt is
the brain's prompt file (`brain-prompt.md`) followed by the recent chat
history and the new message; Claude Code adds its own system prompt. From
the brain's folder it also loads the Claude Code setup a session started
there would: `CLAUDE.md` and `CLAUDE.local.md`, the settings, skills,
agents, commands and rules in `.claude/`, and the MCP servers in
`.mcp.json`, except those its `.claude` settings turn off with
`disabledMcpjsonServers`.

It does not load your own setup: not your user settings in
`~/.claude/settings.json` (their hooks, plugins and permissions, so neither
the `deny` rules nor the guard hooks there), nor the skills, agents and
commands in `~/.claude`, nor the MCP servers in `~/.claude.json`, of
plugins or of your claude.ai account. Those are yours, and a brain acts for
whoever may message it. What a brain needs belongs in its folder, the
permission rules and hooks that should hold for it too. Claude Code would
also load the `CLAUDE.md` files and `.claude/` folders above the brain's
folder, your `~/CLAUDE.md` and `~/.claude/CLAUDE.md` included, which
`--setting-sources` does not cover. The bridge tells it to skip the
`CLAUDE.md` files and rules there (`claudeMdExcludes`), and the sandbox
keeps brains from reading any of it, the skills, agents and commands in
those `.claude` folders too (see below). Without the sandbox, a brain still
gets those and can read your instructions with its tools, so keep brain
folders out from under folders that hold either. The sandbox goes by a
file's real path, so a `CLAUDE.md`, skill or rule in the brain's folder
that is a symlink to one of those files above it is denied as well, and
nothing says so: copy such a file into the brain's folder instead.

> **Before upgrading** from a version without
> [`inherit_claude_user_config`](CONFIGURATION.md#inherit_claude_user_config):
> instructions brains got from `~/CLAUDE.md`, `~/.claude/CLAUDE.md`,
> `~/.claude/rules` or a `CLAUDE.md` above their folder no longer reach
> them, with or without the sandbox, and neither do the `apiKeyHelper`,
> `env` and `model` in `~/.claude/settings.json`. Copy what each brain
> needs into its own `CLAUDE.md` or `brain-prompt.md` and its
> `.claude/settings.json`, then go through the upgrade checklist under that
> setting.

> **Warning:**
> [`inherit_claude_user_config`](CONFIGURATION.md#inherit_claude_user_config)
> brings back the old behaviour, in which every brain loads all of your
> setup: it runs your hooks, reads your personal instructions and can call
> your MCP servers' tools without asking, on behalf of anyone who may
> message it. Read its warning before you turn it on.

## Isolation

With `sandbox.enabled` (macOS), every run is wrapped in `sandbox-exec`: the
brain cannot read or write any other brain's folder, the bridge's data folder
(except the shared media inbox), or the usual secrets (SSH keys, cloud
credentials, browser and mail data, shell history), and it cannot rename or
delete the folders above them, which would move them out from under the
sandbox's rules. Nor can it read your own Claude Code instructions, unless
`inherit_claude_user_config` is on: the `CLAUDE.md` and `CLAUDE.local.md`
files and the `.claude` folders above its folder, up to `/`, and
`~/.claude/CLAUDE.md` and `~/.claude/rules` (the rest of `~/.claude` is
Claude Code's own state, which a brain's run needs). It can read but not
change the other files that run outside its sandbox: Claude Code's
settings, hooks, skills and plugins (which your own sessions load), Claude
Code's auto-memory, `~/Library/LaunchAgents`, your shell startup files, and
the bridge's own binary and the tools it runs. It can still write most other
places your user can, including the Claude Code state all brains share;
[the sandbox section](CONFIGURATION.md#what-the-sandbox-does-not-cover-yet)
lists what that leaves open. Its own folder is always readable and
writable, even if it lives somewhere the defaults deny, but it cannot move
or replace the folder itself and it re-opens nothing denied inside it; a
brain whose folder is your home folder, or is itself something the sandbox
protects, is refused. A run that cannot be sandboxed is refused, never run
without it.
