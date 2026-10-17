# Brains

A brain is one isolated assistant: a folder with its own system prompt,
memories, skills and conversation history. Every chat the bridge answers is
routed to exactly one brain.

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
├── brain-prompt.md                  starter system prompt — make it yours
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
| `prompt_file` | System prompt, relative to the folder. It must stay inside the folder. |
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

## Isolation

With `sandbox.enabled` (macOS), every run is wrapped in `sandbox-exec`: the
brain cannot read or write any other brain's folder, the bridge's data folder
(except the shared media inbox), or the usual secrets (SSH keys, cloud
credentials, browser and mail data, shell history), and it cannot rename or
delete the folders above them, which would move them out from under the
sandbox's rules. Its own folder is always readable and writable, even if it
lives somewhere the defaults deny. A run that cannot be sandboxed is refused,
never run without it.
