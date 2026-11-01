# Brains

A brain is one isolated assistant: a folder with its own prompt, memories,
skills and conversation history. Every chat the bridge answers is routed to
exactly one brain.

## Adding a brain

In the dashboard: **Brains → Add brain**, then pick a folder. You can

- click **Choose folder…** for your system's folder dialog,
- browse your home folder inside the dashboard (and name a new folder there),
- or type a path (`~/assistant/family`).

The bridge checks the folder, by where it really is (through any symlink on
the way, also for a folder it is about to create): it must be inside your
home folder, must not be inside or contain another brain, and must not be,
hold or lie inside what the sandbox keeps from brains, such as the
bridge's data, media or credentials folder, `~/.claude`,
`~/Library/LaunchAgents`, the keychains, the bridge's install folder or
checkout, or must not be or hold a folder like `~/.ssh` or `~/.config`, a
program the bridge runs or a folder it finds programs in (`~/.local/bin`).
A folder of your `PATH` inside it is fine: the bridge runs nothing from
it and hands it to this brain's runs only (see
[writing](CONFIGURATION.md#writing)).
The bridge checks configured brains the same way whenever it loads them,
also those you add to `brain_dirs` by hand, and leaves out one that fails,
with an issue on the Brains page; `fridolin check` then exits with status
3, and `setup.sh` stops before it installs. Until you fix the folder, the
chats such a brain would take go to no other brain: a DM brain left out
does not hand your DMs to the default brain, nor a default brain left out
its chats to the first one. The chat is told that its brain cannot run. Then it writes what is missing:

```
<folder>/
├── brain.json                       this brain's settings
├── brain-prompt.md                  sent ahead of each message — make it yours
├── memories/
│   ├── README.md
│   └── people/whatsapp-identity-map.md
└── skills/                          example skills, memory consolidation among them
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
  "observe": "history",
  "approvals": "ask",
  "live_session": "off",
  "backup": { "enabled": true, "exclude": ["node_modules/", "*.log", "/big-exports/"] }
}
```

| Field | Meaning |
| --- | --- |
| `version` | The version of `brain.json` the file is written in, `1` today; a file without it is version 1. A bridge refuses a `brain.json` of a newer version than it reads, from a newer Fridolin before you went back to an older one say: the Brains page shows it as changed on disk, with both versions, the approved copy keeps running, and no edit or approval writes over the file, which would drop the settings the older bridge does not know. |
| `id` | Stable identifier (lowercase, digits, `-`, `_`). Used for history attribution, the sandbox profile and per-brain credential file names. It cannot be changed; remove the brain and add the folder again under a new id instead. |
| `name` | Display name. |
| `routing.handles_dms` | This brain answers direct messages. At most one brain; without one, the [fallback brain](#the-fallback-brain) answers them. |
| `routing.default` | This brain is the [fallback brain](#the-fallback-brain): it answers the allowed groups no brain claims, and direct messages while no brain handles them. At most one; without one, the first brain is the fallback. A default brain that cannot run (see above) is replaced by no other. |
| `routing.groups` | Group JIDs this brain answers. The group must also be allowed in the global settings. |
| `routing.answer_files` | In this brain's groups, a file someone sends (photo, document, voice note, video) addresses the bridge, which answers it. Default `true`; `false` makes it chatter: the brain reads its caption without answering, and the file is not downloaded. The trigger, an @-mention or a reply to the bridge still address it. |
| `routing.answer_forwards` | The same for a forwarded message. Default `true`. |
| `model`, `effort` | Override the global `brain_model` / `brain_effort` in Claude mode. Codex uses the separate global `codex_model` / `codex_effort`. |
| `disallowed_tools` | Override the global list; omit to inherit, `[]` to allow every tool. |
| `prompt_file` | The prompt sent ahead of every message, relative to the folder. It must stay inside the folder, and be a file rather than a symlink (see [Files the bridge reads and writes](#files-the-bridge-reads-and-writes)). |
| `reply_chat` | Where answers go for files handed in through `/ingest`. The files are handled by this brain even when the chat belongs to another one, so a brain without chats can answer in your DM. `!stop` and `!reset` in that chat also stop this brain's work on them, and `!reset` starts the chat afresh for this brain too. Default: the first allowed number for the brain that gets DMs (the DM brain, or the fallback brain when none handles DMs), else the brain's first group. |
| `observe` | What the brain does with untagged group messages, the ones nobody addressed to it (see [triggers](CONFIGURATION.md#triggers)). `history`, the default, keeps them in its history, where its next run in that group reads them; they cost nothing. `run` also gives each one a run of its own (a bare "ok" or emoji excepted), for a brain that keeps notes as the conversation goes: its prompt marks it `<observe_only>`, nothing it writes is sent, and a message addressed to the brain stops it and goes first, but each costs a full Claude run, with tools, on whatever any member the group's `group_members` admits writes (see [group_members](CONFIGURATION.md#group_members)). Set in the dashboard under *Untagged group messages*. |
| `approvals` | Whether the brain asks you before an irreversible step. `ask`, the default: `request_approval` puts a poll in your chat and waits for your yes ([Skills, risk and approvals](#skills-risk-and-approvals)). `auto`: it answers approved at once, without a poll, and the brain's prompt says it may act without asking and must say in its reply what it did. **A security trade-off**: the brain then sends, posts, pays, submits and deletes on its own judgement, which a message, file or web page it reads can sway; set it only on a brain you trust that far, such as your own core brain in your DM. See [Acting without asking](#acting-without-asking). Set in the dashboard under *Approvals*; like every field here, a change the brain makes itself waits for your approval. |
| `live_session` | `off`, the default: every message runs `claude -p`. `owner_dm`: the brain keeps one Claude Code session running and wakes it with the messages you write to it in a direct chat; see [Live session](#live-session). Needs `dangerously_skip_permissions` and not `inherit_claude_user_config`; a change the brain makes itself waits for your approval, like every field here. |
| `backup` | Where this folder is backed up; see [BACKUP.md](BACKUP.md) and [below](#backups-cloud-and-node-targets). `backup.exclude` applies to the Google Drive backup and to the vault's snapshots (and its cold tier, for a folder in the data dir), and to the node targets; `backup.enabled: false` keeps the folder out of every backup ([VAULT.md](VAULT.md#brain-folders-and-their-backup-settings)). `backup.targets` and `backup.node_only` send it, or parts of it, to [node targets](VAULT.md#node-targets). |
| `calendar` | The Google account this brain signed in with and the calendars it may use; set by *Connect Google Calendar*, see [CALENDAR.md](CALENDAR.md). |
| `role` | `core` for the [core brain](#the-core-brain-and-its-grants), the one brain that may reach others; left out for every other brain. At most one brain is the core. |
| `grants` | Only on the core brain: what it may do with each other brain, by that brain's id, from `delegate`, `read` and `files` (`{"family": ["delegate", "read"]}`). See [below](#the-core-brain-and-its-grants). |
| `memory` | The [memory index](#memory): `inject` (default `false`) puts the owner's messages and the brain's own notes that match a new message into its prompt; `top_k` (default 5, at most 20) and `max_chars` (default 2000, at most 20000) bound how much; `consolidate` (`"weekly sun 18:00"`, `"daily 07:00"`; default none) runs `skills/memory-consolidation.md` on that schedule, as the [scheduled agent](#scheduled-agents) `memory-consolidation`. Left out, nothing changes for the brain. |
| `schedules` | The brain's [scheduled agents](#scheduled-agents): tasks it runs by itself on a schedule, or programs the bridge runs on a node. |
| `files` | `files.uploads`: the folders, with their subfolders, that take uploads from the site roles allowed to upload to the brain, each `{"folder": "documents", "to_brain": true}`; `to_brain` hands an upload to the brain, as a file sent on WhatsApp, instead of leaving it in the folder. Set on the Files page; see [FILES.md](FILES.md#uploads). |
| `github` | The GitHub access of the runs that answer *you*: your direct messages to the brain and your Plaud voice questions, never a group, a hand-off or another brain's task. `{"repos": ["you/project"], "permissions": {"issues": "write", "contents": "read"}}`; each such run is logged in to `gh` with a token for exactly these, which ends with the run. See [GitHub in your chats](#github-in-your-chats). |

[`schemas/brain.schema.json`](schemas/brain.schema.json) is a JSON Schema
of the file, version 1.

### Live session

By default every message starts a new `claude -p` run, which is told the
chat's recent history. With `"live_session": "owner_dm"` a brain keeps one
interactive Claude Code running instead and wakes it with each message *you*
write to it in a direct chat, so that it keeps what was said (and Claude
Code's own prompt cache) between messages. It is the idea of the
[whatsapp-claude-plugin](https://github.com/Rich627/whatsapp-claude-plugin):
a Claude Code *channel*.

- **Which messages.** Only a direct message from a number in
  `allowed_numbers`, from WhatsApp. Groups, other senders, untagged chatter,
  scheduled agents, delegated tasks, GitHub runs and the answers to approvals
  run `claude -p` as before. (Widening this to more chats is a later setting;
  `owner_dm` is the narrowest.) A session serves the direct chat it started
  for: with more than one number in `allowed_numbers`, a message from
  another runs `claude -p` meanwhile, so that no session mixes two chats.
  A message that waits behind another waits in
  the brain's queue as ever, and `!stop` and the run timeout stop a turn the
  way they stop a run, by ending the session.
- **How it starts.** The first message that could use a session runs as a
  one-off while the session starts in the background. The session is *ready*
  once it answered a check of its own, which proves that Claude Code accepted
  the channel and the login. Channels are a research preview: an organisation
  (`channelsEnabled`) or an account can have them off. A session that does
  not come up is left alone for 5 minutes, then 10 and 20, and every message
  runs `claude -p` meanwhile; the bridge log says what the CLI showed.
- **How it runs.** Claude Code runs in a terminal of its own, inside the
  brain's sandbox profile and with the brain's own Claude config, started with
  `--dangerously-load-development-channels server:fridolin`. That flag is how
  Claude Code loads a channel that is not on Anthropic's list; the bridge
  answers its consent dialog ("I am using this for local development") with
  its first choice. The channel is the bridge's own MCP server
  (`fridolin mcp --run … --live`), the one that also serves the brain's
  tools; it passes each message on as a `notifications/claude/channel`, and
  the session's Stop hook (`fridolin live-turn-end`) brings the turn's last
  text back, which is sent to the chat as a run's reply is: quiet mode, the
  one-time-code guard, history and policy are the same. Each turn is admitted
  by the policy as a run is (lockdown, risk, rate limit, audit `run_start` and
  `run_end`), and the session's token speaks for the turn's message and chat.
- **What it is told.** The first turn gets the whole prompt a one-off run
  gets (instructions, history, reply context, files), and a note that it is in
  a session. Later turns get the new message, what the bridge sent since, and
  the policy note. The `catch_up` tool gives the counts of what came in since
  the session last answered, and with `read: true` the last 10 lines of the
  chat.
- **When it ends.** After 50 turns, 12 hours, or 30 minutes idle (never in
  the middle of a turn), when the brain's settings or its sandbox change (a
  tool taken away, a path denied, another model or folder), and when the
  bridge stops; Claude Code compacts its context itself meanwhile. The
  next message runs as a one-off while a new session starts. A session that
  dies in the middle of a turn ends that message in an error, as a failed run
  would, and is not run again: it may have done something already.
- **GitHub.** A brain with a [`github`](#github-in-your-chats) grant of
  its own keeps its session: `gh` in it is logged in for each turn with a
  token of the grant minted for that turn, and logged out, the token
  revoked, once the turn is over, as a one-off run's would be. Between
  turns the session has no GitHub login: a background task that outlives
  its turn finds `gh` logged out. The login is in `live-tokens/<brain>`
  with the session's token (below).
- **What it does not do.** `AskUserQuestion` is off in a session (use
  `ask_user`). Activity shows in the brain's activity log as one line a turn,
  not as the tool calls of a run. Without the sandbox (macOS and Linux with
  `sandbox.enabled` off) the brain's folder must have been trusted once in an
  interactive `claude` for the session to start. A session holds the brain's
  login token in its environment as a run does, for as long as it lives.
- **Security.** A session reads the owner's direct messages with the brain's
  tools, as a run does. Its brain API token, which speaks for those messages
  for as long as the session lives, is in a folder of its own,
  `live-tokens/<brain>` in the data dir, which only the session's sandbox
  opens: the brain's other runs (a group's, a scheduled agent's) read
  `run-tokens/<brain>` and never see it. Nothing it can call changes who may message the
  bridge: the allowed numbers and groups, the nodes and the pairing are
  changed in the dashboard, the CLI, or by the owner's own chat commands,
  never by a tool.

### Backups: cloud and node targets

```json
"backup": {
  "enabled": true,
  "exclude": ["node_modules/", "*.log", "/work/"],
  "targets": {"cloud": true, "node:nas": true},
  "node_only": ["/work/"]
}
```

- `targets` says which targets get the folder: `"cloud"` (the Google Drive
  backup, the vault's snapshots and its cold tier) and `"node:<name>"` for
  each target of [`vault.targets`](CONFIGURATION.md#vault). Left out, the
  cloud gets the folder and no node target does: a `brain.json` without it
  means what it always meant. `"cloud": false` keeps the whole folder out
  of the cloud.
- `exclude` stays out of the cloud and out of the node targets.
- `node_only` lists paths, in `exclude`'s syntax, that never go to the
  cloud but do go to the node targets the brain chooses. A path in both
  `exclude` and `node_only` goes to the node targets: keep `/work/` in
  `exclude` too, and it stays out of the cloud even if `node_only` is
  removed later. A `node_only` path with no node target chosen goes
  nowhere.
- `enabled: false` keeps the folder out of every target.

A brain may edit its own `brain.json`, and the edit waits for approval
([Approval](#approval)): until then the bridge runs it with the approved
copy. For the cloud, whichever of the two leaves out more holds; a node
target gets the folder only when both choose it, and a `node_only` path
goes there only when both list it. So a brain cannot send more anywhere by
editing its file. The Vault page sets `targets` for the brains of each
target (`PUT /api/brains/{id}/backup` with `targets` and `node_only`), an
edit by the owner that needs no approval.

A field the bridge does not know, a typo such as `routing.handles_dm` or
one a newer version added, is logged by its path when the bridge reads the
file; the brain loads without it, and the next edit in the dashboard drops
it from the file.

### The fallback brain

The brain marked `routing.default`, or without one the first brain in
`brain_dirs` that runs, is the fallback brain. It answers the allowed
groups no brain claims, and your direct messages as well while no brain
has `routing.handles_dms`: with a single brain, that brain answers your
DMs whatever its `handles_dms` says. A brain that cannot run (see above)
keeps its chats from it: a DM brain left out keeps your DMs, and a
default brain left out leaves no fallback brain at all. The dashboard
names the fallback brain on the Overview, the Brains page ("Fallback for
other chats") and the Chats page (a group's *Default brain*), and *Add
brain* says so when the brain it adds becomes it.

### The core brain and its grants

One brain may be the *core brain* (`"role": "core"`): the one the outside
world talks to, which answers itself or hands work to the other brains,
the sub-brains (family, work, taxes), as far as you grant it. Routing
stays as it is: a chat goes to the brain it is bound to. What changes is
that the core can reach across, and nothing else can.

```json
{
  "id": "core",
  "role": "core",
  "grants": {
    "family": ["delegate", "read"],
    "taxes":  ["delegate", "read", "files"]
  }
}
```

| Grant | The core may |
| --- | --- |
| `delegate` | hand that brain a bounded task and get its answer |
| `read` | read that brain's `memories/**/*.md` and `skills/**` |
| `files` | fetch files from that brain's vault paths with `vault_ls`, `vault_get` and `vault_search` ([VAULT.md](VAULT.md#brains-and-the-vault)) |

The bridge checks them whenever it loads the brains, and refuses an edit
or approval that breaks a rule: at most one core brain; grants only on the
core brain (a sub-brain with a `grants` field is left out, with an issue
that says so); a grant names a configured brain, never the core itself;
only the three kinds above, each once. Removing a brain takes it out of
the core's grants too. You edit them on the Brains page, on the core
brain's settings (*Other brains*), and like every change to `brain.json`
a change a brain makes itself waits for your approval there.

Every run, of every brain, starts the bridge's own MCP server
(`fridolin mcp --run <id>`, see [API.md](API.md#the-runs-mcp-server-get-brainmcptools-post-brainmcpcall)),
whose tools the bridge picks for that run from the run's token, never from
what the model says:

| Tool | Core run | Sub-brain run |
| --- | --- | --- |
| `delegate(brain, task, timeout_seconds)` | the brains granted `delegate` | absent |
| `list_brains()` | the brains with any grant, their names and grants | absent |
| `read_memory(brain, path)` | its own, and the brains granted `read` | its own only |
| `request_approval(action, details, risk, step)`, `ask_user(question, options)`, `check_approval(id)` | ask you in WhatsApp and wait for the answer, see [Skills, risk and approvals](#skills-risk-and-approvals) | the same, in the core run's chat; not in an observe-only run |
| `node_update(node)` | with nodes on: updates a node's fridolin to the core's version, a write action ([NODES.md](NODES.md#updates)) | absent |
| `node_exec(argv, node, dir, timeout_seconds, approval_id, wait_seconds)`, `node_exec_rules(node)` | while a node whose `exec` you approved is online: runs one command its allowlist allows, a write action; a command whose rule has `approve` asks you in a poll each time ([NODES.md](NODES.md#the-core-brain-runs-commands)) | absent |
| `contact_person(number, name, goal, brief, message, days)`, `errands(id)`, `errand_answer(id, text)`, `errand_stop(id)` | with brain.json's `errands` set, in a run that may ask you: has Fridolin write to someone for you and carry the conversation, see [Errands](#errands) | absent |
| `plaud_app_update()`, `plaud_app_status()` | while the Plaud Sync app uploads through the site (`plaud.phone_upload`): builds the app as the Plaud card does, which the paired phones then install, a write action; and says what each phone runs and how its last update went ([PLAUD.md](PLAUD.md#updates)) | absent |

Any brain, core or not, also gets the tools of Fridolin's
[connections](CONNECTIONS.md#brains) while the connection has a key and
`connections.<id>.brains` lists the brain. The bridge makes the call with
the key, which the run never sees, and counts it against the brain's
`brain_monthly_usd`:

| Tool | What |
| --- | --- |
| `openai_transcribe(path, language, prompt, model)` | a recording's text, from OpenAI; a write action, since the audio leaves the machine |
| `elevenlabs_transcribe(path, language, model)` | a recording's text and language, from ElevenLabs; a write action |
| `elevenlabs_speak(text, voice, model, format)` | reads a text aloud into `speech/` in the run's temp folder and answers the file's path; a write action. The bridge cannot send it to WhatsApp yet |
| `elevenlabs_voices()` | the ElevenLabs account's voices |

A tool a run is not granted is absent, not refused: it is not in the
run's list, and a call of it is a call of a tool there is none of.

`delegate` puts the task into the sub-brain's queue as a message from the
core brain, in a chat of its own (`delegate:<run id>`, one per run of the
core), and the sub-brain's worker runs it like any message: with its own
prompt, memory, history and sandbox. Its answer goes back to the core's
waiting run, never to a WhatsApp chat; neither its working notice nor a
question it asks goes anywhere. The call waits up to `timeout_seconds`
(120 by default, at most 600); an answer that comes later is handed to
the core brain as a new message in the chat its run was for, and the
core answers it there. A delegated run never gets `delegate`, whatever
its brain, so delegation goes one level deep, and a sub-brain cannot
reach the core or a sibling. Each delegation is one line of kind
`delegation` in the [audit log](#the-audit-log): which run of which brain,
to whom and the task (shortened) as its detail, the outcome and how long
it took. The delegated run is in its core run's thread, with its risk
level.

**You are asked once, at the start.** Handing a task over is not an
irreversible step itself, and the core does not ask you for it. When the
task holds one (posting in a group, sending, paying), the core asks you
for that step with `request_approval` before it delegates, in the chat
you are talking to it in, and passes the approval's id as `delegate`'s
`approval_id`. The bridge checks it is an approved, unused request of the
core's own, and the delegated run is told, ahead of the task, that you
approved that step (with the id, as a scheduled agent is told the steps
approved with its schedule): it takes it without asking again, and an
irreversible tool of the bridge's takes the id from it, once. Any other
irreversible step it still asks you for, in the core run's chat. A task
handed over without an approval asks you from the delegated run, when it
comes to the step. Either way one poll, not two. The audit log's
`delegation` and `run_start` lines name the approval handed over. An
approval the core's own `approvals: auto` gave goes only to a brain set to
`auto` too: a brain that asks still asks you.

`read_memory` reads one file, or lists a folder, of `memories/` (only
its `.md` files) or `skills/`. The bridge reads it outside the sandbox,
so it follows no symlink on the way, not even one inside the folder.
What the core learns about a sub-brain goes into its own notes, never the
sub-brain's files.

## Errands

The core brain can have Fridolin write to people who are not in
`allowed_numbers`, for you, and carry the conversation with them until
it comes to an end: "Ask Alex when dinner next week suits them", "Tell
the plumber Tuesday works and agree on a time". Each is an *errand*.
Fridolin writes from its own number and introduces itself as your
assistant the first time it writes to someone. It is off until the core
brain's brain.json turns it on:

```json
"errands": {
  "owner_name": "Sam",
  "intro": "Hallo, hier ist Fridolin, der Assistent von Sam.",
  "model": "gpt-5"
}
```

| Field | What |
| --- | --- |
| `owner_name` | How Fridolin names you, to the people it writes to and to its errands' runs. Required. |
| `intro` | What Fridolin's first message to someone opens with, as the bridge sends it. Default: `Hi, this is Fridolin, <owner_name>'s assistant.` Write it in the language your contacts speak. |
| `model` | In Codex mode, the OpenAI model the errands' runs use. Default `gpt-5`. In Claude mode they run on the brain's model. |

**How one goes.** You ask the core brain; it calls `contact_person` with
the number, the name, the goal, a *brief* (everything Fridolin may tell
them and agree to) and the first message.

- **The first time** Fridolin writes to someone, you get the message as
  it will go out, the introduction ahead of it, with the goal and the
  brief, and a poll: ✅ Yes or ❌ No (a 👍 or `!approve` does it too).
  Nothing goes out before your yes.
- **Someone Fridolin wrote to before** gets the message at once, without
  the introduction, but only when you asked for it in your own direct
  chat (the first number in `allowed_numbers`, a message you typed). An
  errand that any other run opens, a scheduled agent's or one that
  answers a group, a file or another brain, asks you first as the first
  time does. The bridge tells which run it is from the message that
  started it, not from anything the model says, and what an errand
  reports to you never starts a run.
- **Their answers** never reach the brain. Each goes to the errand's own
  run, which reads the rules, the goal, the brief and the conversation,
  and nothing else, and answers with one of four things: write back,
  done (with a summary for you), ask you (with a question), or abort. A
  few messages that come close together are answered at once.
- **What the brief does not cover** (another date, money, a promise,
  details about you) goes to you: Fridolin tells them it will check, and
  you get the question in your chat. Tell the core brain the answer; it
  passes it on with `errand_answer`, which only a run that answers your
  own message may call.
- **The end.** When the goal is reached, or clearly cannot be, you get
  the summary. If they ask not to be written to again, Fridolin forgets
  them: the next errand with them asks you first. An errand also ends
  after its days (`days`, 1 to 14, default 3) or 20 messages of
  Fridolin's, and the brain's `errand_stop` ends one at once. What they
  write after the end is passed on to you, up to five messages a day,
  and not answered.

**Why a person cannot turn Fridolin against you.** The protection is the
bridge's, not a model's judgement:

- The errand's run has no tools at all: no shell, no files, no MCP
  server, no web, no memory. In Codex mode it is a request to OpenAI's
  Responses API through Fridolin's [OpenAI connection](CONNECTIONS.md),
  with no tools and its answer held to a schema, counted against the
  brain's `brain_monthly_usd`; Codex mode needs the connection's key for
  errands, since Codex's CLI cannot run without tools. In Claude mode it
  is `claude -p --tools ""` with no MCP server, the rules as its system
  prompt, in an empty folder of its own, under the brain's sandbox.
- It reads the conversation as JSON, so what they write stays their line
  and cannot pass for a rule or for you.
- The bridge sends what it writes to that one person only. A run they
  talked into anything can do no more than write back to them, and give
  away no more than the brief.
- **Abort.** When they try to make Fridolin do what it must not (ignore
  its rules or brief, tell more than the brief allows, write to someone
  else, run something, speak as you), the run answers abort. They get
  the bridge's own message, `🚫 I won't go along with that, so I'm
  ending our conversation here. <owner_name> has been told.`, never a
  text of the run's; you get why, with their last message; and Fridolin
  ignores them from then on: what they send is not read, not passed on
  and not answered. Only you lift that, with `!unblock <number>` in your
  own chat, after which writing to them asks you first again. Telling an
  attempt from an odd message is a model's judgement, so it can miss one
  or see one where there is none; the points above hold either way.
- Their voice notes, photos and files are not downloaded: the run reads
  a line that says what came instead.

The brain's tools:

| Tool | What |
| --- | --- |
| `contact_person(number, name, goal, brief, message, days)` | Opens an errand, or asks you for it. Refused for a number in `allowed_numbers` (write to them in their chat), for someone Fridolin ignores, and while an errand with them has not ended. A write action. |
| `errands(id)` | This brain's errands: those that have not ended, then the last ten others. With `id`, one errand with its conversation. |
| `errand_answer(id, text)` | Passes on what you said to an open errand. Only in a run that answers your own message in your direct chat. A write action. |
| `errand_stop(id)` | Ends an errand without a word to them. A write action. |

The conversations are in bridge.db (`errands`, `errand_lines`,
`errand_contacts`), never in a brain's folder or history. The audit log
has one `errand` line for each step (asked, opened, sent, answered,
asked the owner, ended, blocked), with the number masked and without
the messages' text.

## Skills, risk and approvals

A skill is a markdown file in the brain's `skills/` folder, either
`skills/<name>.md` or a folder `skills/<name>/SKILL.md` with what else it
needs beside it. Its frontmatter is the portable
[Agent Skills](https://agentskills.io) format, with Fridolin's own fields
under `metadata.fridolin`:

```markdown
---
name: taxes-de
description: File the German tax return through ELSTER, from the documents in the vault.
requires:
  env: [ELSTER_CERT]
  bins: [python3]
os: [darwin, linux]
install:
  - kind: brew
    formula: python
metadata:
  fridolin:
    risk: irreversible          # read | write | irreversible
    vault: ["documents/taxes/**"]
    approve: [submit]           # steps that stop for your yes
    triggers: [steuer, taxes, elster]
---
# Taxes
...
```

| Field | |
| --- | --- |
| `name` | lower-case letters, digits and single hyphens, at most 64; a `SKILL.md`'s is its folder's name |
| `description` | what it does and when to use it, at most 1024 bytes |
| `requires.env`, `requires.bins` | the variables and programs it needs |
| `os` | `darwin`, `linux`, `windows` (`win32` reads as `windows`) |
| `install` | the steps that install what it needs: commands, or mappings such as `{kind: brew, formula: python}` |
| `metadata.fridolin.risk` | the highest risk of any of its steps: `read` (search, summarise), `write` (files, a calendar event; undone by a revert) or `irreversible` (submit, send to someone else, pay, delete, publish); `read` when left out |
| `metadata.fridolin.approve` | the steps that stop for your yes |
| `metadata.fridolin.vault` | the vault paths it uses, relative, globs allowed |
| `metadata.fridolin.triggers` | words or phrases that make a message a run of this skill |

A file without a frontmatter, from before, is a skill too, read-only and
without triggers. The bridge reads these files itself, through the
folder and following no symlink, like `brain.json`.

**What applies is what you approved.** A brain writes its skills, and a
skill it wrote, or a frontmatter it changed, must not give its runs more
than you agreed to. So, as with `brain.json`, the bridge keeps the policy
you approved (in `bridge.db`), and the brain page's *Skills* section shows
each skill with what applies:

| Status | What applies |
| --- | --- |
| approved | the policy in the file, which you approved |
| starter | the policy of a starter skill the bridge wrote into the new brain, as long as it is unchanged |
| not approved | `read`, and its triggers count for nothing, until you approve it |
| changed | the policy you approved before; the new one waits for you |
| no frontmatter, invalid | `read`, no triggers (or what you approved before) |

*Approve as declared* accepts the file's policy, provided it is still the
one you looked at; *Edit policy* writes your risk, steps, vault paths and
triggers into the file's `metadata.fridolin` and approves them (comments
in the frontmatter go).

**Every run has a risk level.** Before a run starts, the bridge looks for
the triggers of the brain's skills in the message: the run is one of the
riskiest skill that matches, with that skill's risk as its level, and of
none, `write`. A delegated task's run has its core run's level and is in
its thread. The level shows in the audit log and the Threads page; the
bridge's own tools that write need `write`, and those that take an
irreversible step (none today; the vault's later) need an approval.

**Irreversible steps ask first.** Every run's prompt says: before any
irreversible step, and before each step a skill lists under `approve`,
call `request_approval` and go on only when it answers approved. That
sends a WhatsApp poll, *🔐 Approve? &lt;action&gt;* with *✅ Yes*, *❌ No* and
*📝 Show me the draft*, to the chat the run is for, or, for a run in no
WhatsApp chat (a delegated task whose core run was not in one), to your
DM, the first number in `allowed_numbers`. Only votes from the numbers in
`allowed_numbers` count; anyone else's is ignored and logged. *Show me
the draft* sends what the run gave as `details` and leaves the poll open.
A 👍 reaction on the poll from an allowed number counts as *Yes*, a 👎 as
*No*; other emoji and other people's reactions are ignored and logged.
The dashboard's Approvals page shows what waits, with *Approve* and
*Deny*, which count as yours. No answer within
`policy.approval_timeout_hours` (24) counts as no.

The call waits for your vote up to `wait_seconds` (5 minutes by default,
10 at most): a run cannot wait a day, and the bridge gives a tool call
of Codex no more. Unanswered by then, it answers *pending* with the
approval's id, which `check_approval` waits on again; once no call
waits, your answer comes to the brain as a new message in that chat,
which it answers there. Pending approvals live in `bridge.db`, so a
restart keeps them. At most `policy.max_pending_irreversible` (3)
irreversible ones wait at once. A tool the bridge marks irreversible takes
the id of an approved request as `approval_id`, once.

### Acting without asking

A brain whose `brain.json` says `"approvals": "auto"` (the brain page's
*Approvals*: *Act without asking*) is not asked about: its
`request_approval` answers approved at once, without a poll, and its
prompt says it may act without asking and must say in its reply what it
did. Each such approval is still a row of the Approvals page's history,
approved by `auto`, and two lines of the audit log
(`approval_requested`, `approval_decided` by `auto`); an irreversible
tool takes its id as any other. It covers the steps a skill lists under
`approve` too; the bridge's log names each such step it let through.
What still holds:

- a lockdown: `request_approval` is refused, as for every brain;
- the run's risk level: a run held to `read` (by its skill, or a
  lockdown) asks you with a poll as before;
- `policy.max_pending_irreversible`: with that many waiting, the call is
  refused;
- an observe-only run has no `request_approval` at all;
- a task the brain delegates follows the other brain's own setting: an
  `auto` approval does not travel to a brain that asks.

This is a security trade-off: the poll is what stops a brain that a
message, a file or a web page talked into something. Use it for a brain
that only you talk to, and keep `!lockdown` in mind.

`ask_user(question, options)` asks you a question: with options as a
poll whose vote is the answer, without as a message whose answer is the
next message you send in that chat.

**Lockdown.** `!lockdown` from your number (or *Lock down* on the
Approvals page) pauses everything but reading: a run of a skill that
writes or submits is refused, every other run runs at `read`, no approval
is asked and no write or irreversible tool goes ahead, until `!unlock` or
*Unlock*. The bridge enforces this for its own tools; what a brain does
with its shell is up to its sandbox and its instructions. It survives a
restart.

**Rate limit.** One chat starts at most `policy.max_runs_per_hour` (20)
runs in an hour; the next is refused with a notice. Files handed in,
delegated tasks and untagged group chatter do not count.

### The audit log

`<data_dir>/audit/YYYY-MM.jsonl` is one JSON object a line, a file a month,
appended: `ts`, `kind`, `thread` (the chat a run serves; a delegated task
is in its core run's thread), `chat`, `brain`, `run`, and as they apply
`id` (the approval's or delegation's), `skill`, `risk`, `by` (a voter's
number, `dashboard`, `timeout`), `detail`, `outcome` and `duration_ms`.
The kinds: `run_start`, `run_end`, `run_refused`, `tool_call` (the tool's
name and how it ended, never its arguments), `approval_requested`,
`approval_vote`, `approval_decided`, `question_asked`,
`question_answered`, `delegation`, `lockdown`, `unlock`,
`skill_approved`, the owner's node commands `node_join_code`
(`!join`), `dashboard_link` (`!dashboard`), `node_approved` (`!approve`)
and `node_revoked` (`!deny`, `!revoke`), never with the code or link, and
`owner_approval`: the owner's `!approve` and `!deny` of anything else, a
vote in the poll that lists them (each tick it newly makes), and the confirmation an unexpected one
asks for. Texts are shortened, and what looks like a password or
token is masked. The dashboard's Threads page groups the runs by thread,
and its Audit page filters the lines. `fridolin doctor` warns about an
approval that has waited more than an hour, and says when the bridge is
in lockdown.

## Approval

The bridge does not simply trust `brain.json`. Your accepted version is stored
in the bridge's config (`brain_dirs[].approved`), and that is what runs.

Brains run with broad permissions inside their own folder, so a brain could
edit its own `brain.json` — for instance after reading a document with
instructions hidden in it — to claim your direct messages or another brain's
group. Such a change has no effect: the dashboard marks the brain as
*changed on disk* and shows the difference, and you approve it or not.
Edits you make in the dashboard update both copies, together: `brain.json`
is written once the edit has passed its checks, just before the bridge's
config, and when the config then cannot be written, the save fails and
`brain.json` is put back as it was. An edit of a brain whose `brain.json`
is not a file the bridge reads (see below) fails rather than replace it.
An edit or approval is refused when it would make brains clash (two
brains for your DMs, one group for two brains and so on) or leave one that
cannot run; a brain that cannot run already, after a hand edit of
`config.json` say, keeps none of the others from being edited.

The brain's scheduled agents, in [`.brain/schedules/`](#the-brain-folder),
are approved the same way, each file on its own, on the Schedules page;
`brain.json`'s approval leaves them aside.

The bridge also tells you in your WhatsApp chat, within a minute, of a
changed `brain.json`, a new or changed scheduled agent and a new or
changed skill: what changed and what it would let the brain do ("run with
these settings changed: routing", "Runs: daily 07:00, on its own", "start
when a message says: steuer"), flagged *Unexpected*, since the brain wrote
it into its own folder rather than you in the dashboard. Reply `!approve`
twice (the second within 2 minutes) to approve it as it is on disk then,
or `!deny` to leave the approved version running and stop being asked
until the file changes again. With several waiting, `!approve` sends a
poll to pick one. A run's request for approval, which has its own poll,
is answered by `!approve` and `!deny` too
([NODES.md](NODES.md#approving-from-whatsapp) has the rules).

## Files the bridge reads and writes

The bridge itself, outside the sandbox, works on these files in a brain's
folder: it reads the prompt file, `brain.json` (to tell when it changed on
disk) and `memories/people/whatsapp-identity-map.md`, appends every message
to `conversation-history.jsonl` and reads it back, and logs what each run
did in `claude-stream.log`, each run under a line naming its chat, sender,
process and model, and after it the end of what the CLI wrote to stderr,
under a line naming the run. A line of the CLI's output longer than 4 MiB,
a tool's large result say, is not read; the log notes its size. For Claude,
it also names each kind of event in the output that the bridge does not read,
once a run, so that a new CLI that sends something new shows there. A
history line (see [below](#conversation-historyjsonl)) has the type `in`, `out` for an
answer, written once all of it reached WhatsApp as what the chat got (a
poll as its question and numbered options), `event` for a vote in one
of the brain's polls or an edit of a message a run already read, or
`reset`, without text, where someone sent `!reset` in the chat: a run's
prompt shows only the chat's messages after the newest one, while the
file keeps the ones before for `fridolin history`, which, like the
dashboard's feed, leaves the marker out. A message from WhatsApp carries
its `id`. An `in` line is written once the
brain took the message (not for one its full queue turned away), with
the time it was sent as its `ts`. A message deleted for everyone is
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

### `conversation-history.jsonl`

The history is one JSON object per line, in UTF-8, appended as things
happen. `fridolin history` searches it, and a brain's own scripts may read
it too. A line has these fields
([`schemas/history-line.schema.json`](schemas/history-line.schema.json)):

| Field | Meaning |
| --- | --- |
| `ts` | For an `in` line when the message was sent, for the others when the line was written: an RFC 3339 time to the second, with the offset of the bridge's time zone (`2026-01-02T09:00:00+01:00`), or `Z` when the offset is zero (UTC, or for example London in winter). Python before 3.11 reads one with `datetime.fromisoformat` only after a final `Z` is replaced by `+00:00`. |
| `from` | For `in`, `event` and `reset`, who wrote it, did it or sent `!reset`: their phone number, or the sender an `/ingest` named. For `out`, the brain's id; for a line sent through `/send`, the brain that sent it or `script`. |
| `chat` | The chat's JID: `<number>@s.whatsapp.net` for a person, `…@g.us` for a group. |
| `text` | What was said. A quoted message and the paths of attachments are lines of their own in it; for an `event`, what happened, said after the name in `from`; empty for a `reset`. |
| `type` | `in`, a message to the brain; `out`, one it sent or that was sent for it; `event`, something that happened in the chat without a message; `reset`, where `!reset` started the chat afresh. |
| `via` | Only on an `out` line that came in through [`/send`](API.md#post-send): `brain`, `script` or `api` (no token). |
| `id` | Only on a message from WhatsApp: its id, which an edit or a deletion names. |
| `otp_request` | Only on the `out` line of a `/send` that asked for a one-time code: the id `/send` answered with, which the [hand-off of the code](CONFIGURATION.md#otp_guard-and-otp_markers) repeats. |

Fields and types are added, never renamed or removed, so a reader ignores
the fields it does not know and the lines of a type it does not know.
Lines carry no version of their own. The schema describes exactly the
lines this version writes, and refuses any other field or type, which
keeps it to the code: a reader that checks lines against it should let
unknown fields and types through, since later versions add them.

## Memory

A brain's memory is its files: `memories/` (curated notes such as
`memories/core.md`, and daily notes `memories/YYYY-MM-DD.md`), its
`conversation-history.jsonl` and its `transcripts/`. The bridge keeps a
search index over them, `memory/<brain id>.db` in its data folder: a
SQLite FTS5 index, one row per section of a note (by its markdown
heading), per history line and per paragraph of a transcript. It is
derived from the files and kept up to date by size and time (the history,
which only grows, from where it stopped): before each run that recalls,
before each search, and every 30 seconds while `memory` is set. `fridolin
memory reindex [--brain ID]` builds it afresh; deleting the file loses
nothing.

Each row carries where its text came from, its provenance, which the
bridge sets from the file and the line, never from what the text says:

| Provenance | What |
| --- | --- |
| `owner` | A message an allowed number (`allowed_numbers`) typed in a direct chat with the bridge, on WhatsApp (its history line has a WhatsApp `id`; what `/ingest`, `/send` and the bridge's schedules write has none). A note whose `source` anchors all point at such messages. |
| `agent` | A note the brain wrote in `memories/`. |
| `untrusted` | Everyone else: group members (the owner in a group too), files and messages handed in, transcripts, and notes whose `source` is a URL, `web`, `untrusted`, `forwarded`, `attachment`, a transcript or a history line that is not the owner's. |
| `system` | What went out to a chat: the brain's own replies (which may repeat anything it read, a web page included), and what scripts and other brains sent through `/send`; and what the bridge's schedules hand the brain. |

The index lives in the data folder, which the sandbox keeps from brains:
a brain can change what its notes say, and lower their class by citing
where they came from, but cannot set the class itself.

With `memory.inject`, a run's prompt starts, after the brain's prompt
file, with a `<memory_recall>` block: the `owner` and `agent` rows that
match the new message best (BM25; a note's trigger phrases weigh four
times its text, its importance raises it), never `untrusted` or `system`
ones, each a line of JSON with its source anchor (`memories/core.md#health`,
`conversation-history.jsonl#3EB0C4F2`), when it was said and its text,
without what the chat history in the prompt shows anyway. Without it the
prompt is as it was.

A brain searches everything, labelled, with `"$FRIDOLIN_BIN" memory search
QUERY` (through the bridge, with its run's token; the index is not in its
sandbox). From a shell, `fridolin memory search --brain ID QUERY` does the
same, and `fridolin memory status` says what each index does not hold yet.
`fridolin doctor` checks, for brains that use the index, that the
provenance rule holds on the build and that each index is up to date.

### Notes comments

A note's section may end with a comment saying what should bring it to
mind, how much it matters and where it came from:

```
## Dentist
Appointment moved to Friday 10:00.
<!-- triggers: dentist, tooth; importance: 4; source: conversation-history.jsonl#3EB0C4F2 -->
```

`triggers` are phrases, comma-separated; `importance` is 0 to 5; `source`
lists anchors: `conversation-history.jsonl#ID` (or `history#ID`), a
transcript, a URL or `web`. A comment before a note's first heading applies
to the whole file. The starter prompt and `skills/memory-consolidation.md`
tell the brain to write them, and the consolidation promotes durable facts
from the daily notes into `memories/core.md` with their anchors.

### Standing intents

"Remind me on Friday at 9", "tell me when Sam mentions the boat": a brain
makes such a request a standing intent, which the bridge keeps and fires,
instead of a line in its notes.

```bash
"$FRIDOLIN_BIN" intent add --when 2026-01-09T09:00 "Remind the owner to call the dentist"
"$FRIDOLIN_BIN" intent add --when "weekly mon 08:00" "Send the owner the week's plan"
"$FRIDOLIN_BIN" intent add --match boat [--match harbour] [--repeat] "Tell the owner about the boat"
"$FRIDOLIN_BIN" intent list
"$FRIDOLIN_BIN" intent remove ID
```

`--when` is a time (once), `daily HH:MM` or `weekly DAY HH:MM`, in the
bridge's time zone; `--match` keywords fire on a message in any of the
brain's chats that holds one as a whole word (not on the owner's own
messages in the chat the intent was set in), once, or with `--repeat` at
most every 10 minutes. When one fires, the brain gets a message from
`intent`, in the direct chat the intent was set in, saying which and why;
the message that fired a keyword intent is quoted in it as someone's
words, not instructions. A brain has at most 50.

Only a run that answers the owner's own direct message (an allowed number,
in a direct chat, on WhatsApp or the dashboard's dev page) may add or
remove one: a group member, a file handed in or an intent that fired
cannot, and the command says so. The intents are kept in
`intents/<brain id>.json` in the data folder, out of the brain's reach.
The Brain page in the dashboard lists them and removes one; so does
`DELETE /api/brains/{id}/intents/{intent}`.

Standing intents with a time are listed on the dashboard's Schedules
page too, under *Reminders*, with when each fires next; they stay
intents, set and removed as above, and never go into `brain.json`. So
are the reminders a brain keeps itself, in `.brain/reminders.json` or
`skills/reminders.json` (a list of `{"id", "fire_at", "message", "sent",
"repeat_every_days", "jid"}`, as a brain's own remind skill writes it,
`fire_at` in the bridge's time zone): those not sent yet are listed with
the file they are in. The brain fires those itself; the bridge only
shows them.

`memory.consolidate` is the brain's own weekly job, the scheduled agent
`memory-consolidation` ([below](#scheduled-agents)): on its schedule the
brain is asked to follow `skills/memory-consolidation.md`, and its one-line
answer goes to its reply chat, as it always did. The first time the bridge
sees the setting it starts counting from then, so switching it on does not
run the job at once; a bridge that ran it before keeps counting from its
last run.

## The .brain folder

`.brain/` in a brain's folder holds what Fridolin manages for the brain
as definitions, one file per thing, each approved by you on its own: a
brain, or you, can add one without touching `brain.json`, and the review
of a change shows just that file. Today it holds the brain's
[scheduled agents](#scheduled-agents), in `.brain/schedules/`, and may
hold the brain's own [reminders](#standing-intents). Something new that
Fridolin reads from a brain's folder and that needs your approval goes
in a folder of its own there (`.brain/<kind>/<name>.json`), with a schema
in `docs/schemas`, and is approved per file the same way: the approved
copy lives in `config.json`, and what is on disk runs only once you
approve it. The folder is the brain's, so it is backed up and moved with
it; the bridge reads it as it reads a brain's other files (no symlinks
out of the folder, nothing larger than 256 KiB).

## Scheduled agents

A scheduled agent is a task a brain runs by itself on a schedule ("every
6 hours, check X and tell me if Y"), or a program the bridge runs on a
node on a schedule. Each agent is a file of its own in the brain's
[`.brain` folder](#the-brain-folder), `.brain/schedules/<name>.json`
([schema](schemas/schedule.schema.json)); the file's name is the
agent's:

```text
Personal/
  brain.json
  .brain/schedules/
    morning-briefing.json   {"when": "daily 06:00", "template": "morning-briefing"}
    inbox.json              {"when": "every 6h", "prompt": "Look in inbox/ for new files and tell me what needs me.", "report": "worth"}
    weekly-plan.json        {"when": "weekly mon 08:00", "report": "always"}
    fix-bugs.json           {"when": "daily 03:00", "prompt_file": "skills/fix-bugs.md", "effort": "max", "timeout_minutes": 180}
    scrape.json             {"when": "daily 21:00", "command": {"node": "laptop", "argv": ["/Users/you/bin/scrape.sh"]}, "report": "silent", "timeout_minutes": 15}
```

| Field | Meaning |
| --- | --- |
| `name` | lower-case letters, digits and `-`, unique in the brain: the file's name, which a `name` in the file must match; it may be left out |
| `when` | `every 6h` (`m`, `h`, `d`; at least 5 minutes), `daily 07:00`, `weekly mon 09:00`, or a cron expression, `cron 0 7 * * 1-5` (minute hour day month weekday; `*`, lists, ranges, steps, `mon`…`sun`), in your time zone ([`timezone`](CONFIGURATION.md#timezone), which the Schedules page names); across a daylight saving change it keeps the wall-clock time, a time the change skips runs once at the next minute that exists, and a repeated one once |
| `enabled` | `false` keeps it from running on its schedule; *Run now* still runs it |
| `description` | one line on what it is for |
| `prompt`, `prompt_file`, `template`, `command` | what it does, at most one: the task inline; a file in the brain's folder; a built-in template (below); or a program on a node. None is the file `schedules/<name>.md` in the brain's folder |
| `vars` | `{"name": "value"}`, filling `{{name}}` in the template or task file |
| `report` | where the result goes: `worth` (the default) sends the answer only when the run found something worth reporting (it answers `NOTHING_TO_REPORT` otherwise), `always` sends every answer, `silent` sends nothing. A failure is sent unless the agent is silent |
| `chat` | the chat results go to, a JID; default the brain's `reply_chat` |
| `risk` | the run's risk budget, `read`, `write` or `irreversible`; left out, what the task's skill triggers say, else `write` ([Skills, risk and approvals](#skills-risk-and-approvals)) |
| `disallowed_tools` | its own blocked tools; left out, the brain's |
| `effort` | its runs' reasoning effort, `low`, `medium`, `high`, `xhigh` or `max`, in place of the brain's (`brain.json`'s `effort`, else [`brain_effort`](CONFIGURATION.md)), so an agent can think harder than the brain's chats without making them slower; the runs FrIDE's news starts for the agent use it too. Left out, the brain's. In Codex mode it is ignored, as a brain's `effort` is: `codex_effort` applies |
| `timeout_minutes` | stops a run that takes longer, in place of `run_timeout_minutes`: shorter or longer, up to 1440 (24 hours); left out or 0, `run_timeout_minutes`. A long timeout with a high effort can cost a lot of usage when a run goes in circles (for a program, it is the node's exec timeout, which the node's own limit still caps) |
| `missed` | a run that fell due while the bridge was down: `run` (the default) runs it once when the bridge is back, however many times it fell due; `skip` waits for the next time |
| `jitter_minutes` | each run starts a fixed number of seconds within this many minutes after its time, the same for every run of the agent; left out, [`schedules.jitter_minutes`](CONFIGURATION.md#schedules) (2) |
| `preapproved` | steps the owner approved together with the schedule (`"file GitHub issues on example/repo"`): the run is told it may take them without `request_approval`; every other irreversible step still asks |
| `github` | `{"repos": ["owner/name"], "permissions": {"issues": "write", "contents": "read"}}`: the GitHub access its runs, and no other run, are granted: each run is logged in to `gh` with a token for exactly these repositories and permissions, which ends with the run ([below](#github-for-scheduled-agents)) |

A task file may start with a frontmatter of `description:` and
`vars: name, other=default` lines, as the templates do; the rest is the
task.

**Approval.** Each agent's file is approved on its own, like
`brain.json` is ([approval](#approval)): what runs is the copy you
approved, which `config.json` keeps with the rest of the brain's approved
manifest. A new file, a changed one or one that is gone shows on the
Schedules page as *Pending approval*, with the change and what the agent
may do (a program on a node, irreversible steps, GitHub write access,
every tool), and runs, or stops, only once you approve that version; the
version approved before runs on meanwhile. A brain asked in a chat to
"check X every morning" may write such a file itself. An agent you add
or edit on the Schedules page is yours: the bridge writes its file and
the approved copy at once, and it needs no approval after. A file the
bridge cannot read, or that is not a valid agent, is listed with why,
and cannot be approved until it is fixed. `brain.json`'s own approval
leaves the agents aside. The task file of an agent is the brain's own,
like its prompt file, and its runs have the sandbox and risk levels of
any run; a program on a node runs only what that node's exec allowlist
and your approval of its commands let it.

**Agents in brain.json** (compat). Before `.brain`, a brain kept its
agents in `brain.json`, under `"schedules": [...]`, each with its `name`.
The bridge still reads them there (a file of the same name wins), and
moves them into their files as it starts and whenever it writes the
brain's `brain.json`: the rest of `brain.json` stays as it was, and
nothing approved changes, so an approved agent runs on and a pending one
stays pending. A bridge from before `.brain` does not see the files: it
runs the agents you approved, from `config.json`, and shows the brain's
`brain.json` as changed on disk.

**How a run goes.** A brain run of an agent goes through the brain's
queue like a message: the same sandbox, prompt, memory, risk level and
irreversible-step approvals, which ask in your DM. It runs in a chat of
its own, `schedule:<name>`, whose history its next run sees, so it knows
what it reported before. Its message says the agent's name and schedule,
where its answer goes and the steps approved with it, then the task. A
question it asks mid-run has nobody to answer it and is not sent. An
answer it writes and then follows with tool calls only is still its
answer: the bridge reports the last text the run wrote when the CLI's
result carries none. A run that ends in an error keeps that text in its
history and log, and the chat is told of the failure only. An
agent never runs twice at once: a time that comes while its run goes on
is left out. A lockdown runs an agent at `read`, and refuses one whose
`risk` says more; it refuses every program on a node.

**FrIDE and workstation sessions from an agent.** A run of an agent
may hand work to FrIDE (`fride_task`). That work's news never goes to
WhatsApp: it stays in the agent's own chat (`schedule:<name>`) and on
the dashboard's FrIDE page. A question from FrIDE, or work done or
failed, starts a run of the agent out of its schedule, with FrIDE's text
and the work it was handed: that run answers FrIDE itself
(`fride_reply`) or follows up (`fride_close` once merged), and tells the
chat the agent reports to only when something merged, failed for good
or needs your decision on something destructive or security-relevant
(else it ends with `NOTHING_TO_REPORT`, whatever the agent's `report`;
a `silent` agent tells nobody). Progress, and FrIDE's notice that an
agent "has not moved" for a while, start none. One such run at a time,
and at most one an hour per agent: news that arrives while a run of the
agent goes on, or within the hour after the last such run, waits for one
more, together. Work an agent handed FrIDE before its news stayed with
it, which reported to your DM, goes back to the agent when the relay
starts: a thread that began during a run of exactly one agent of its
brain, or whose first line is in that agent's task.
A paused or switched-off agent, or one that runs a program, gets no run;
the news stays on the FrIDE page. A run of an agent that reports
somewhere may also start a session on a workstation node
(`node_agent_run`), whose questions and news go to the chat the agent
reports to (its `chat`, else the brain's reply chat, else your DM),
where you answer them as usual; a `silent` agent's runs may not.

A program runs on the node through `exec` ([NODES.md](NODES.md#what-a-node-offers)),
no shell: its `argv[0]` must be on the node's allowlist, the node's
commands approved, and the node online. `command.node` names the node
by its id or its name, in any case, as `fridolin nodes` does: a name
more than one node has runs nothing until you give the id, and a
revoked node steps aside for another with its name. A node whose `node.json` names
folders in `exec.accept_from_core` takes the program of an agent you
approve when it lies in one of them: approving the agent adds it to the
node's allowlist with exactly its arguments, and deleting the agent
removes it, unless another agent runs it
([NODES.md](NODES.md#programs-from-the-core)). The Schedules page and the
wizard say how each agent's program stands on its node. Its output and exit code go into
the run's log; `always` sends the end of its output, `worth` only a
failure. Each such run is an audit line of kind `scheduled_command`.

**Built-in templates.** The bridge ships these tasks
([`examples/schedules/`](../examples/schedules)); `vars` fills theirs:

| Template | What it does |
| --- | --- |
| `memory-consolidation` | follows `skills/memory-consolidation.md` (what `memory.consolidate` runs) |
| `morning-briefing` | each morning, today's appointments from the brain's calendar, "Good morning! You have N appointments today: …", and nothing on a day without any (`greeting` sets the first words) |
| `code-review-security` | reviews GitHub repositories (`repos`) against current secure-coding practice; files a public issue labelled `security` per hardening finding, and an exploitable one only as a private draft security advisory, or in its answer to you; at most `max_issues` (3) a run |
| `code-review-bugs` | hunts bugs and files a bug report (`bug`) per real, reproducible finding |
| `code-review-simplify` | questions every setting, line and dependency, and files concrete proposals (`simplify`) that lose no function |

The three reviews keep notes in the brain's `memories/reviews/`, look for
duplicates among the open and closed issues before they file, give
`file:line` evidence with a link to the reviewed commit, and post no
secrets or personal data. They need a `github` grant on the repositories
they review (the security review can add `repository_advisories: write`,
for private advisories, once the GitHub App has that permission too;
without it, an exploitable finding goes only into its answer to you), and since filing an issue publishes something,
`"risk": "irreversible"` and the filing as a `preapproved` step:

```json
{"name": "review-bugs", "when": "daily 03:30", "template": "code-review-bugs",
 "vars": {"repos": "you/project"},
 "github": {"repos": ["you/project"], "permissions": {"issues": "write", "contents": "read"}},
 "risk": "irreversible",
 "preapproved": ["file GitHub issues, labelled bug, on you/project"]}
```

### GitHub for scheduled agents

A run is logged in to GitHub only when its agent has a `github` grant:
the run's message carries it, and as the run starts the bridge gets a
token for exactly the grant's repositories and permissions and writes it
as `gh`'s login into a folder of the run's own (`GH_CONFIG_DIR`, in the
data dir's `run-tokens/`, which only that run's sandbox may read), never
into the run's environment, which macOS shows the user's other
processes. `gh` works as it is; `git` uses it with
`git -c credential.helper='!gh auth git-credential'`. When the run ends
the folder is removed and the token released. No other run or program
gets one.

The token comes from the bridge's GitHub App (Integrations → GitHub,
[GITHUB.md](GITHUB.md)): a short-lived installation token minted per run
for exactly the grant, and revoked when it ends. A grant's repositories
must all be of one owner, where the app is installed, and the app must
have each permission the grant asks for; a grant without permissions is
contents and metadata read. Without an app, the fallback is a token you
type in, the same for every grant, which the bridge cannot narrow: a
fine-grained personal access token for the reviewed repositories alone,
with *Issues: read and write*, *Contents: read* and *Metadata: read*. In
the core's container, store it with `fridolin core secrets set
github.token` (it becomes `FRIDOLIN_GITHUB_TOKEN` at the next `fridolin
core start`); on a native install, set `FRIDOLIN_GITHUB_TOKEN` in the
service's environment (CONFIGURATION.md, "Where to set them"). A run
can read its own gh login, so the token is the brain's while it runs.

### GitHub in your chats

Scheduled agents are not the only runs that may use GitHub: a brain's
own `github` in brain.json, with the same shape as an agent's, logs in
the runs that answer you, so "file that as an issue" in your DM or in a
voice question just works.

```json
"github": {"repos": ["you/project", "you/other"], "permissions": {"issues": "write", "contents": "read"}}
```

Only runs that answer the owner get it: a direct message from a number
in `allowed_numbers` to the brain, and what the Plaud Sync app's voice
mode sends, which only your paired phone can sign: a question, or a
recording from your pin. A pin recording can hold other people's voices
(a meeting), and the brain decides whether you spoke to it, so keep the
grant narrow. A run for a group, for someone else's DM, for a hand-off
(`/ingest`, a Plaud recording with voice mode off, an upload), for a
task the core brain delegated, or for untagged chatter gets none, so
what other people write cannot reach your GitHub through it. A scheduled agent keeps its own grant. The token is
minted per run from the GitHub App (or the typed fallback token) and
revoked when the run ends, as [above](#github-for-scheduled-agents).

**A trade-off.** A run that answers you also reads what it fetches,
files and web pages among them, so a page it reads could try to make it
use the token. Keep the grant to the repositories and permissions you
want filed from chat (`issues: write`, `contents: read` is enough for
issues). Like every field of brain.json, a change the brain makes
itself waits for your approval, which names the repositories and
permissions.

### The Schedules page and the CLI

The dashboard's Schedules page lists every agent of every brain, grouped
by brain and filtered by brain, state or text, each with a badge for its
state: *Pending approval*, *Active*, *Paused*, *Failing* (its last run
failed, or it cannot run as it is) or *Disabled* (`enabled: false`). A
card shows its schedule, its next run (in how long, and when), how its
last run went and how long it took, and what it may do: its risk budget
or node, its GitHub repositories and permissions, its preapproved steps
and where its results go. *Run now* starts any approved agent at once,
whatever its schedule, and *Pause*/*Resume* stop and restart it on its
schedule; an agent whose run goes on cannot start twice. The agents
waiting for approval come first, each with its review inline: what it
may do, flagged when risky, the change field by field, and *Approve*,
*Approve & run now* or *Delete the file*. The Overview page flags them
too. Below the agents, the reminders.

*Add agent* is a wizard: what it does (a built-in template, a custom
prompt, or a command on a node), which brain and name, when (every few
hours, daily, weekly, or a cron expression, with its next three runs),
its details (the template's vars, a repository picker from Connect
GitHub's installed repositories, the prompt or the program), what it may
do in plain words (risk level, preapproved steps, GitHub access, node,
where results go, and more settings), and the exact file it writes.
Saving writes the file and approves it. An agent's page shows every
field, its runs (start, how long, result, cost or tokens, and the
message sent) and each run's log: the CLI's output as the activity log
has it, its tool calls, and errors; *Edit* and *Duplicate* go through the
same wizard, *Delete* asks first. Pausing is the dashboard's, not the
file's: a paused agent does not make up the times it missed when it is
resumed.

```bash
fridolin schedules list
fridolin schedules run main/inbox --wait     # run now, then print its log
fridolin schedules pause inbox               # NAME alone when one brain has it
fridolin schedules resume inbox
fridolin schedules logs inbox [RUN] [--runs]
```

The history and logs are in `schedules/` in the data dir, within the
limits of [`schedules`](CONFIGURATION.md#schedules), and in the vault's
nightly snapshot like the audit log.

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
reload stops the brain's run in progress and starts it afresh in the new
folder, which then answers the messages that were waiting for it. Their
lines go into the new folder's history, unless it has them already: a
folder you moved, rather than swapped, keeps its history.

## Removing a brain

*Remove* unregisters the brain: its runs stop and its chats go to the
[fallback brain](#the-fallback-brain), which also answers the messages
that were waiting for it (files and recordings handed to the removed
brain are dropped). When the brain you remove is the fallback, the next
brain becomes it and takes the chats no brain claims, and your DMs while
no brain handles them. The dialog names the brain that takes over, and
warns when it would start answering your DMs. The folder is left exactly
as it was.

So are the brain's media inbox (`<brain id>/` in the media folder, which is
`media/` in the data folder unless `media_dir` puts it elsewhere) and its
state in the data folder (`brains/<brain id>/`: its Claude Code config
folder with the CLI's transcripts, todos and `.claude.json`, its `TMPDIR`
and its caches), with its memory index (`memory/<brain id>.db`) and
standing intents (`intents/<brain id>.json`). A brain added later under
the same id, from any folder, takes them over and can read what is in
them, the old brain's transcripts and files included; its intents fire
for it, and its index is built afresh from the new folder. To start such a brain afresh, move `brains/<brain id>/`
out of the data folder and `<brain id>/` out of the media folder while the
bridge is stopped, or give it another id.

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
two entries from `brain_dirs` in `config.json`. Until you do, the bridge
runs only the first of the two.

## What a brain loads

Each message is one `claude -p` run in the brain's folder. Its prompt is
the brain's prompt file (`brain-prompt.md`) followed by the messages of
the chat before the new one (the last 10 of a direct chat, 20 of a
group, looked for in the last 16 MB of the history, which the bridge
reads back from its end, skipping a line longer than 4 MB), what went
out there while the new one waited for its run or still waits to go
out to WhatsApp (the reply to the message before it, say), and the new
message; Claude Code adds its own system prompt. Each
message is one line of JSON with its time, sender and text, so that
nothing someone writes can pass for the prompt's own structure or for
another sender. The prompt reaches the CLI on stdin, not among its
arguments, which process listings show and the system limits in size
(about a megabyte in all on macOS, 128 KiB for one argument on Linux).
From the brain's folder it also loads the Claude Code setup a session
started there would: `CLAUDE.md` and `CLAUDE.local.md`, the settings,
skills, agents, commands and rules in `.claude/`, and the MCP servers in
`.mcp.json`, except those its `.claude` settings turn off with
`disabledMcpjsonServers`.

Every run gets the bridge's environment (see [what reaches
brains](CONFIGURATION.md#what-reaches-brains)) plus variables of its own:
`FRIDOLIN_BIN`, the bridge's binary, which the brain runs as
`"$FRIDOLIN_BIN" history`, `"$FRIDOLIN_BIN" polls`,
`"$FRIDOLIN_BIN" calendar`, `"$FRIDOLIN_BIN" memory search` and
`"$FRIDOLIN_BIN" intent`; `FRIDOLIN_TOOLS_DIR`, the folder with the Python
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

With `sandbox.enabled` (macOS), every run is wrapped in `sandbox-exec`. The
brain may write only its own folder, its own media inbox
(`media/<brain id>/`, where the files sent to its chats land) and its own
state in the bridge's data folder (`brains/<brain id>/`: a Claude Code
config folder, `TMPDIR` and caches), so nothing it writes runs or loads
outside its sandbox, in another brain or in your own Claude Code sessions.
It cannot read any other brain's folder or inbox, the rest of the bridge's
data folder, or the usual secrets (SSH keys, the keychains, cloud
credentials, browser and mail data, shell history). Nor can it read your
own Claude Code setup and instructions, unless
`inherit_claude_user_config` is on: all of `~/.claude` and
`~/.claude.json`, and the `CLAUDE.md` and `CLAUDE.local.md` files and the
`.claude` folders above its folder, up to `/`. Since your login lives in
the keychain, it logs in with a token in the bridge's environment
(`claude setup-token`; see [the sandbox section](CONFIGURATION.md#writing)),
with `inherit_claude_user_config` too.
[What the sandbox does not cover](CONFIGURATION.md#what-the-sandbox-does-not-cover)
lists what that leaves open. Its own folder is always readable and
writable, even if it lives somewhere the defaults deny, but it cannot move
or replace the folder itself, and the bridge's own files and other brains
stay closed inside it; a brain whose folder is your home folder, or is,
holds or lies inside something the sandbox protects, is refused. A run that
cannot be sandboxed is refused, never run without it.

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

### Codex mode

[Settings → AI mode](CONFIGURATION.md#ai-mode) selects the provider for new
runs of every brain. Both providers read this brain's prompt and chat
history. Codex keeps a separate login and state in
`<data_dir>/brains/<id>/codex`; sign-in instructions are in the configuration
guide. Claude-specific model overrides and user plugins do not transfer
to Codex. Blocking `WebFetch` or `WebSearch` disables Codex's web tool;
other blocked-tool names refuse Codex runs because they have no safe
equivalent.
