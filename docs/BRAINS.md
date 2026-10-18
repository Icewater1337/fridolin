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
| `id` | Stable identifier (lowercase, digits, `-`, `_`). Used for history attribution, the sandbox profile and per-brain credential file names. It cannot be changed; remove the brain and add the folder again under a new id instead. |
| `name` | Display name. |
| `routing.handles_dms` | This brain answers direct messages. At most one brain. |
| `routing.default` | This brain answers allowed groups no brain claims. At most one; without one, the first brain is the fallback. |
| `routing.groups` | Group JIDs this brain answers. The group must also be allowed in the global settings. |
| `routing.answer_files` | In this brain's groups, a file someone sends (photo, document, voice note, video) addresses the bridge, which answers it. Default `true`; `false` makes it chatter: the brain reads its caption without answering, and the file is not downloaded. The trigger, an @-mention or a reply to the bridge still address it. |
| `routing.answer_forwards` | The same for a forwarded message. Default `true`. |
| `model`, `effort` | Override the global `brain_model` / `brain_effort`. |
| `disallowed_tools` | Override the global list; omit to inherit, `[]` to allow every tool. |
| `prompt_file` | The prompt sent ahead of every message, relative to the folder. It must stay inside the folder, and be a file rather than a symlink (see [Files the bridge reads and writes](#files-the-bridge-reads-and-writes)). |
| `reply_chat` | Where answers go for files handed in through `/ingest`. The files are handled by this brain even when the chat belongs to another one, so a brain without chats can answer in your DM. `!stop` and `!reset` in that chat also stop this brain's work on them. Default: the first allowed number for the brain that gets DMs (the DM brain, or the fallback brain when none handles DMs), else the brain's first group. |
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

The bridge itself, outside the sandbox, works on these files in a brain's
folder: it reads the prompt file, `brain.json` (to tell when it changed on
disk) and `memories/people/whatsapp-identity-map.md`, appends every message
to `conversation-history.jsonl` and reads it back, and logs what each run
did in `claude-stream.log`, each run under a line naming its chat, sender,
process and model, and after it the end of what the CLI wrote to stderr,
under a line naming the run. A history line has the type `in`, `out` for an
answer, written once all of it reached WhatsApp, or `event` for a vote in
one of the brain's polls or an edit of a message a run already read, and a
message from WhatsApp carries its `id`. A message deleted for everyone is
taken out of the history again, by rewriting the file. A run that finds the
log over 5 MB first moves it to `claude-stream.1.log`, replacing the one
before. A brain can change any file in its folder, and put a symlink to
`~/.ssh` or to another brain's history in place of one of them, so the
bridge never leaves the folder through a symlink, and takes only a regular
file there: not a symlink at the file itself, even one to another file in
the folder. Nor does it take a prompt file, `brain.json` or identity map
larger than 256 KiB. It treats such a file as missing and writes nothing
through it; its log says why, and the dashboard shows a `brain.json` it
cannot read. It opens the folder once when it loads the brain and goes
through that handle from then on (`brain.json` excepted, which it reads for
brains that are not running too), so a folder you put in a brain's place
takes a reload; see [Moving a brain](#moving-a-brain). Copy a file into the
folder rather than link it.

## Moving a brain

Brain folders are portable. To move one to another machine: copy the folder
(or restore it from a Drive backup), then *Add brain* on the new machine — the
dashboard offers to use the `brain.json` it finds. Keep the same `id` so
history and credentials still match.

To swap a brain's folder for another at the same path, a restored backup
say, move the old one aside, put the new one in its place, then restart the
bridge or save a setting in the dashboard. Until it loads its brains again,
the bridge goes on reading the prompt and writing the history in the folder
it opened, the one you moved aside, while runs start in the new one. The
reload stops the brain's run in progress, drops the messages waiting for
it, and starts it afresh in the new folder.

## Removing a brain

*Remove* unregisters the brain: its runs stop and its chats fall back to the
default brain. The folder is left exactly as it was.

## Configs with inline brains

Configs from before brain folders listed brains inline in `config.json`
(`"brains": [{"name": "...", "work_dir": "..."}]`). This version no longer
runs them, and refuses to start while that list has a brain in it; an
empty list is ignored. Move them into their folders first with
`fridolin migrate`. Only builds from before inline brains were removed
have it, and there are no releases yet, so build commit `2592376` from
your checkout and run its `migrate` on your config
(`~/.fridolin/config.json` unless you moved it):

```bash
git worktree add /tmp/fridolin-migrate 2592376
(cd /tmp/fridolin-migrate && go build -o fridolin .)
/tmp/fridolin-migrate/fridolin migrate --dry-run --config ~/.fridolin/config.json
/tmp/fridolin-migrate/fridolin migrate --config ~/.fridolin/config.json
git worktree remove /tmp/fridolin-migrate
```

`--dry-run` shows what would change. Without it, `migrate` writes a
`brain.json` into each work dir and moves the entry to `brain_dirs`,
keeping the same id, so the history carries over. Before you start this
version, go through what it printed:

- **conflict**: the folder already has a different `brain.json`, and the
  brain stays inline. Run `migrate` again with `--force` to overwrite that
  `brain.json`, or remove the inline entry and add the folder under Brains →
  Add brain, which offers the `brain.json` it finds.
- **skipped**: the work dir does not exist. Create it, or remove the entry.
- **warning: the prompt file is outside the brain folder**: the brain now
  reads `brain-prompt.md` in its folder, so copy the prompt file there.

Then run `fridolin check` with this version. It must load the config,
and a brain whose folder was in `brain_dirs` already may now be listed
twice, which it reports as two brains with the same id: remove one of the
two entries from `brain_dirs` in `config.json`. Until you do, the
dashboard refuses to save any brain's settings.

## What a brain loads

Each message is one `claude -p` run in the brain's folder. Its prompt is
the brain's prompt file (`brain-prompt.md`) followed by the recent chat
history, with the brain's answers that still wait to go out to WhatsApp,
and the new message; Claude Code adds its own system prompt. From
the brain's folder it also loads the Claude Code setup a session started
there would: `CLAUDE.md` and `CLAUDE.local.md`, the settings, skills,
agents, commands and rules in `.claude/`, and the MCP servers in
`.mcp.json`, except those its `.claude` settings turn off with
`disabledMcpjsonServers`.

Every run gets the bridge's environment (see [what reaches
brains](CONFIGURATION.md#what-reaches-brains)) plus variables of its own:
`FRIDOLIN_BIN`, the bridge's binary, which the brain runs as
`"$FRIDOLIN_BIN" history`, `"$FRIDOLIN_BIN" polls` and
`"$FRIDOLIN_BIN" calendar`; `FRIDOLIN_TOOLS_DIR`, the folder with the Python
helpers such as `transcribe.py`, which the bridge finds in `tools/` next to
its binary, or in the checkout above `bin/` for a development build
(without one, the log says so and the variable is not set);
`FRIDOLIN_API` with `FRIDOLIN_BRAIN_TOKEN_FILE`, how those commands reach
the bridge during this run; and `BRIDGE_DATA_DIR`, `BRIDGE_DB_PATH` and the
other `BRIDGE_*` variables the older Python tools read. The starter prompt
and the example skills name the commands and helpers by these variables,
never by a path, so a brain folder keeps working when it or the bridge
moves. Prompts written by older versions run `calendar_tool.py`,
`poll_results.py` and `history_search.py` from the checkout's `tools/` by
its full path. For one release those are wrappers that run the commands
above with the same arguments, so such a prompt keeps working while the
checkout is there; change it to the commands (the starter prompt in
`examples/brain-prompt.example.md` shows them).

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
the bridge's own binary, its helpers and source checkout, and the tools it
runs. It can still write most other
places your user can, including the Claude Code state all brains share;
[the sandbox section](CONFIGURATION.md#what-the-sandbox-does-not-cover-yet)
lists what that leaves open. Its own folder is always readable and
writable, even if it lives somewhere the defaults deny, but it cannot move
or replace the folder itself and it re-opens nothing denied inside it; a
brain whose folder is your home folder, or is itself something the sandbox
protects, is refused. A run that cannot be sandboxed is refused, never run
without it.

A brain also reaches the bridge's [script API](API.md) only as itself,
with its run's token, which `FRIDOLIN_BRAIN_TOKEN_FILE` names and which
stops working when the run ends. With it a brain can send a message to its
own chats (to tell you a job it runs finished, say) and hand files to
itself, but not message other chats or make another brain run:

```bash
curl -X POST "$FRIDOLIN_API/send" \
  -H "Authorization: Bearer $(cat "$FRIDOLIN_BRAIN_TOKEN_FILE")" \
  -H 'Content-Type: application/json' \
  -d '{"jid":"15551234567@s.whatsapp.net","text":"The report is ready"}'
```
