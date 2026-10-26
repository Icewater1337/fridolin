# Privacy

This project is software you run yourself. There is no service behind it:
no server operated by the authors, no accounts, no analytics, no telemetry.
Whoever installs the bridge is the only operator of their copy, and this
policy describes what that copy does with data.

## What the bridge handles

- **WhatsApp messages** sent to the linked account, and files attached to
  them, from chats you allow in the dashboard.
- **Google Calendar data**, if you connect a brain to a Google account: the
  account's calendar list, and the events a brain reads or creates when you
  ask it to.
- **Google Drive**, if you turn on backups: the bridge uploads copies of your
  brain folders to your own Drive, and can only see the files it created there
  (the `drive.file` permission).
- **Plaud recordings**, if you connect a Plaud account: the name, date and
  length of new recordings, Plaud's transcript and AI summary, and the
  account's email or nickname, which the dashboard shows. Fridolin fetches
  them with Plaud's own command-line tool, with its usage statistics turned off.
  Brains get the same setting, for when they may run the tool themselves;
  runs you start in your own terminal follow your own settings. With the
  Plaud Sync phone app (`plaud.phone_dir`), the transcripts come instead
  from the folder the app's files are synced to, which Fridolin only reads.
  With `plaud.phone_upload`, a paired phone sends signed transcripts
  through your site, and Fridolin writes them into that folder or its
  private upload inbox (`<data_dir>/plaud/inbox`).
- **Your site's visitors**, if you turn on [your own site](docs/SITE.md):
  the accounts you give them (a Google address, or a username and a
  password, with a name and a role) and whatever they enter in its modules.

## Where it goes

- Everything is stored **on your own computer**: messages, history, memories,
  downloaded files, and the sign-in tokens for Google. The bridge holds a
  brain's Google token and makes its calendar calls: with the macOS sandbox
  on, no brain can read a token, its own included; on Linux, or with the
  sandbox off, brains run with your user's rights and can read every
  brain's token. A one-time code the OTP guard catches is kept in
  `<data_dir>/otp/` for the local script that asked for it, readable only by
  your user, for ten minutes, or until the script takes it with the scripts
  token through `POST /otp/take`, which deletes it. [What is stored where](#what-is-stored-where)
  lists it all.
- To answer a message, the bridge runs **Claude Code or Codex CLI**, as selected
  by `agent_provider`, on your computer. The selected CLI sends the prompt —
  which can include the message, recent chat history and any calendar details
  the brain looked up — to its model provider under your account and that
  provider's terms: **Anthropic** in Claude mode (or your configured Claude
  provider), or **OpenAI** in Codex mode.
- Backups go to **your own Google Drive**.
- Plaud transcripts are saved as text files on your computer and handed to the
  brain you chose, so they reach the selected model provider like any message
  the brain answers.
  Each is written to that brain's media inbox first
  (`<media_dir>/<brain id>/plaud/`), which no other brain may read under the
  sandbox, until the brain files it in its own folder; from there it goes
  into that brain's backups too.
- WhatsApp messages that no brain had started on when the bridge stopped
  are kept in `bridge.db`, readable only by your user, until the next start
  takes them up: it answers them, or drops those sent more than an hour
  before.
- A message deleted for everyone on WhatsApp is taken out of the brain's
  chat history (`conversation-history.jsonl`), and is not answered if it was
  still waiting; a file it carried stays in the media inbox, or where the
  brain filed it. Backups made before the deletion keep their copy. View-once
  photos, videos and voice notes are never downloaded. Messages in chats with
  disappearing messages turned on are kept in the history like any others.
- Nothing is sent to the authors of this project. Who else gets data, and
  when, is below.

## Who else gets data

- **Anthropic**, for Claude runs, or the provider your Claude Code login
  names instead (Amazon Bedrock, Google Vertex AI, a gateway at
  `ANTHROPIC_BASE_URL`): the prompt (the message, the chat's recent
  history, the brain's instructions) and whatever the run reads with its
  tools, files and command output included, under that account and its
  provider's terms.
- **OpenAI**, for Codex runs: the prompt and whatever the run reads with its
  tools, under the account signed in to that brain's Codex home or supplied
  through `CODEX_API_KEY`. Switching providers keeps the same brain files and
  chat history, so earlier conversation context can reach the newly selected
  provider. The site's separate search helper still uses Claude.
- **Meta (WhatsApp)** carries every message the bridge receives and sends,
  as for any linked device, under WhatsApp's terms. Messages are end-to-end
  encrypted, so Meta does not see what they say, but it sees who the
  account talks to and when, as it does for any account.
- **Google**, only for what you turn on: the calendar calls a brain makes,
  and your backups in your Drive. Backups are copies of your brain folders
  and the bridge's settings, stored as they are, not encrypted.
- **Plaud**, only if you connect it: the bridge's requests for your own
  recordings and transcripts. The Plaud Sync phone app sends Plaud each
  recording to transcribe instead, and the tool you sync its folder to
  this computer with (Syncthing, say) carries the recordings and
  transcripts as that tool does. When the phone uploads through your
  site instead, its transcripts also pass through the site's tunnel,
  including Cloudflare while that tunnel is on.
- **OpenAI and ElevenLabs**, through Fridolin's connections, only while
  a key is set ([docs/CONNECTIONS.md](docs/CONNECTIONS.md)): the
  recordings and texts a site module or a granted brain sends with
  `ctx.openai`, `ctx.elevenlabs` or the connections' tools, under the
  account of the key and that provider's terms. The bridge passes them
  on and keeps nothing of them but the counts of its usage log.
- **Groq**, only when `GROQ_API_KEY` reaches a brain: `tools/transcribe.py`
  sends the audio it transcribes to Groq's API with `--engine groq`, and
  with the default engine whenever the local model is not installed or
  fails.
- **Hugging Face and PyPI**, for downloads: the local transcription engine
  fetches its model from Hugging Face the first time, and `uv run --script
  tools/transcribe.py` fetches the script's packages from PyPI. They get
  the requests for those files, not your audio.
- **For your own site**, only if you turn it on
  ([docs/SITE.md](docs/SITE.md)):
  - **Cloudflare**, while *Run the tunnel* is on, carries every request to
    the site and every page it serves, sign-ins and what visitors enter
    included. A visitor's HTTPS connection ends at Cloudflare, which
    forwards the request through the tunnel, so Cloudflare sees it
    unencrypted, under its own terms.
  - The **git host** of the site's repository (GitHub, through `gh` and
    the deploy key) holds the modules' code: Fridolin fetches it from
    there, and pushes the starter files and the builders' merged work.
  - **Google** signs in the visitors who use their Google account, and so
    learns that they sign in to your site.
  - The **npm registry** serves the packages the site process installs
    on its first start, and those each deploy installs.
- **Any website a run reaches.** Claude's web tools (WebFetch, WebSearch)
  and Codex's web tool fetch and search what a run asks for. A run that may
  use network commands can reach any address: Fridolin's macOS sandbox does
  not limit the network. What a run sends there is whatever the model puts in the
  request; `disallowed_tools` can take the web tools away.

## What is stored where

`~` is the home folder of the user the bridge runs as, and the data folder
is `~/.fridolin/` unless `data_dir` says otherwise. *Brains* below means
Claude or Codex runs. On Linux or with the Fridolin sandbox off, they can
read what your user can; with the macOS sandbox on, they read of these
only what the last column says.

| What | Where | How long | Who can read it |
| --- | --- | --- | --- |
| The WhatsApp session: the linked device's keys, and the contact and push names and chat settings WhatsApp sends a linked device | `bridge.db` in the data folder, or `db_path` | Until you unlink the device or delete the file | Your user; not brains |
| Polls the bridge sent (chat, question, options) and every vote (who, what, when) | `bridge.db` | Kept | Your user; brains only through `fridolin polls`, which asks the bridge |
| Chats that wrote without being allowed: the JID, a group's name, the sender's number and WhatsApp name, how many messages came when | `bridge.db` | Until you allow the chat on the Chats page; *Dismiss* only hides it | Your user (the Chats page shows them); not brains |
| Replies on their way to WhatsApp | `bridge.db` | Until delivered; one given up on keeps only its error, for 7 days | Your user; not brains |
| One-time codes the OTP guard caught, for the local script that asked for one | `otp/` in the data folder | Ten minutes | Your user; not brains |
| The settings: allowed numbers and groups, brain folders | `config.json` in the data folder | Until you change them | Your user; not brains |
| Each brain's chat history: every message in and out of its chats, with sender and time | `conversation-history.jsonl` in the brain's folder | Until you delete it; a message deleted for everyone is taken out | Your user and that brain |
| What each run did: its chat, sender and model, what the model wrote, the tools it used, its reply and errors | `claude-stream.log` in the brain's folder | Up to 5 MB, and one older file | Your user and that brain |
| Memories, notes and whatever else a brain writes | The brain's folder | Until you delete them | Your user and that brain |
| Files sent to the bridge (photos, documents, voice notes, video) and Plaud transcripts | The inbox of the brain that takes them, `<brain id>/` in the media folder (`media/` in the data folder, or `media_dir`), and where that brain files them; what older versions left in the media folder is in `media/.old/` | Until you or the brain delete them; an inbox stays when its brain is removed, and a brain added later under the same id gets it | Your user and that brain; `media/.old/` not brains |
| Recordings (MP3) and transcripts from the Plaud Sync phone app | The folder `plaud.phone_dir` names, which your sync tool fills | Until you delete them on the phone: Fridolin only reads the folder | Your user, and brains unless the folder is in one the sandbox closes (such as `~/Documents`) or in `sandbox.deny_paths` |
| Transcripts uploaded by paired phones | `plaud.phone_dir`, or `plaud/inbox/` in the data folder | Until you delete them; deleting a recording on the phone does not delete an uploaded copy | Your user; brains only if the configured folder permits it, or through their own media inbox after hand-off |
| Paired phones' public keys and pairing-code hashes; Plaud developer keys and the built phone app | `plaud/devices.json`, `plaud/developer.json` and `plaud/app/` in the data folder, with a public-key copy in `site/devices.json`; copies of the APK wherever you download it | Until unpaired or deleted; used and expired pairing codes are dropped on the next device update | Your user; the site reads public keys; the APK contains developer keys, so anyone with a copy can extract them |
| Sign-ins: each brain's Google token, the OAuth client, the Drive link (rclone), the Plaud login, the dashboard and scripts tokens | The data folder, or `sandbox.credentials_dir` for the Google tokens; the Plaud login is in `~/.plaud/` while "Let brains use the Plaud CLI" is on | Until you disconnect, or delete them | Your user and the bridge; not brains, except that Plaud login |
| The OpenAI and ElevenLabs keys of the connections, and their usage per month and caller (requests, audio seconds, tokens, characters, estimated cost; never what was sent) | `connections/` in the data folder | A key until you remove it; the usage for 13 months | Your user and the bridge; not brains, nor the site under the macOS sandbox |
| The keys you put in the service's environment, such as the Claude login sandboxed brains use (`CLAUDE_CODE_OAUTH_TOKEN`), the Codex API key (`CODEX_API_KEY`), or `GROQ_API_KEY` | macOS: `~/Library/LaunchAgents/ai.fridolin.bridge.plist`; Linux: the drop-in `systemctl --user edit fridolin` made | Until you remove them | Your user; the selected provider gets its login, and brains get the keys `child_env_passthrough` names, in their environment, and can read the service file |
| The bridge's log: connections, routing, runs and errors, naming chats, senders' numbers, group names, poll questions and votes, but not what messages say | macOS: `~/Library/Logs/fridolin/`; Linux: your user journal (`journalctl --user -u fridolin`) | macOS: up to 10 MB a file, and one older file; Linux: as journald is set to keep it | Your user; not brains |
| Claude Code's transcript of every run: the prompt, every tool call and its output, the reply | With the macOS sandbox, the brain's own Claude Code config folder, `brains/<brain id>/claude/` in the data folder; without it, or with `inherit_claude_user_config`, `~/.claude/projects/` | As Claude Code's `cleanupPeriodDays` says (30 days by default) | Your user and that brain; with `inherit_claude_user_config`, your user only: the macOS sandbox keeps every brain out of `~/.claude/projects/`, its own transcripts included |
| Each brain's Codex login and CLI state | `brains/<brain id>/codex/` in the data folder, in both sandboxed and unsandboxed mode | Until you delete them; runs use `--ephemeral`, so the bridge does not ask Codex to save sessions | Your user and that brain; the personal `~/.codex/` is not copied or used |
| A brain's temporary files and caches | With the macOS sandbox, or in Codex mode, `brains/<brain id>/tmp/` and `cache/` in the data folder | Until you delete them; they stay when the brain is removed, and a brain added later under the same id gets them | Your user and that brain |
| Your site, if you turn it on: its visitors' accounts (`users.json`: Google addresses or usernames, names, roles, password hashes), the key that signs their sign-ins (`session.key`), the repository's deploy key, the tunnel token, the site's own Google client, the modules' secrets, what modules keep (`data/<module>/`) and the files they hand to a brain (`spool/`), the clone of the repository and its deploys | `site/` in the data folder | An account until you remove it; module data until a module or you delete it; a file for a brain until the bridge takes it, within a second; the live deploy and the last few | Your user and the site process: under the macOS sandbox it reads its own files (the accounts, the session key, the Google client, the live deploy) and the modules' data, but not the deploy key, the tunnel token or the secrets, which it gets once through a pipe (on Linux it has no sandbox and reads what your user can); not brains |
| Backups of the brain folders and the settings | `fridolin-backup/` in your Google Drive | Until you delete them; the earlier copies of changed and deleted files for 30 days (`keep_versions_days`) | You, and Google under its terms |

## Google user data

Data received from Google APIs is used only to provide the features you turned
on — showing and changing your calendars on request, and storing your backups
— and is not sold, shared, or used for advertising or model training.

The use and transfer of information received from Google APIs adheres to the
[Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
including the Limited Use requirements.

## Removing access and data

- **Disconnect** a brain's calendar in the dashboard: the bridge revokes the
  brain's access at Google and deletes its token. Google keeps one grant per
  app and account, so when another brain, or a token file left by a brain
  removed without a disconnect, signs in to the same account with the same
  OAuth client (or the bridge cannot tell), only this brain's token is
  deleted, and the page says that the access stayed and names that brain or
  file. When Google does not confirm a revocation, the token stays unless
  you choose to delete it here only. You can also remove access at
  [myaccount.google.com/permissions](https://myaccount.google.com/permissions).
- **Disconnect** Google Drive on the dashboard's Backup page: the bridge
  removes its own rclone link, and backups stop. What is already in your
  Drive stays there, and rclone keeps the access Google gave it until you
  remove it at
  [myaccount.google.com/permissions](https://myaccount.google.com/permissions).
- Disconnect Plaud under Integrations: Fridolin signs out of Plaud and stops
  fetching recordings. The transcripts already written (in the media inbox
  and where brains filed them) and the list of recordings it has seen
  (`~/.fridolin/plaud/state.json`) stay. With "Let brains use the Plaud CLI"
  on, the login is the one in `~/.plaud/`, and disconnecting also signs out
  the `plaud` command in your terminal (unless the card asks you to connect
  again after that setting changed: then `~/.plaud/` is left alone).
- To remove everything, after disconnecting the above:
  1. Unlink the bridge from WhatsApp on the phone: **Settings → Linked
     Devices**, the bridge, **Log out**.
  2. `fridolin service uninstall`: it stops the bridge and removes the
     LaunchAgent (`~/Library/LaunchAgents/ai.fridolin.bridge.plist`, with
     the keys in it) or the systemd unit
     (`~/.config/systemd/user/fridolin.service`) and the install in
     `~/.local/lib/fridolin/`. On Linux, also delete the drop-in with your
     keys, `~/.config/systemd/user/fridolin.service.d/`.
  3. Delete the data folder, `~/.fridolin/` (or your `data_dir`), and
     `db_path`, `media_dir` and `sandbox.credentials_dir` if you put them
     elsewhere. Each brain's Codex login and CLI state under `brains/` go
     with the data folder too. With it go your site's files in `site/`: its
     visitors' accounts, the modules' data and secrets, the deploy key and the
     tunnel token.
  4. Delete each brain folder, or its `conversation-history.jsonl`,
     `claude-stream.log` (and `claude-stream.1.log`) and memories if you
     keep the folder, and the site workspace `.fridolin-site/` of a brain
     that built your site.
  5. Delete the logs: on macOS `~/Library/Logs/fridolin/`; on Linux they
     age out of your user journal as journald is set to drop them.
  6. Delete Claude Code's transcripts of the runs that were not in the
     macOS sandbox, or ran with `inherit_claude_user_config` (those of
     sandboxed brains went with the data folder): the folders under
     `~/.claude/projects/` named after each brain folder's path (such as
     `-Users-you-assistant-personal`).
  7. Delete the backups: `fridolin-backup/` in your Google Drive (or the
     folder `backup.root` names), then empty Drive's trash.
  8. If you ran your site, remove what it set up elsewhere: the deploy key
     on the git host (on GitHub, the repository's **Settings → Deploy
     keys**) and the repository itself, unless you keep its code; the
     tunnel and its public host name in Cloudflare Zero Trust
     (**Networks → Tunnels**); and the site's redirect URI, or its own
     Google client, in Google Cloud Console.
  9. A Plaud login in `~/.plaud/` (with "Let brains use the Plaud CLI" on)
     belongs to Plaud's tool: sign out with it, or delete the folder. With
     the phone app, delete the recordings on the phone and the folder
     `plaud.phone_dir` names, and stop syncing it. For uploads, unpair the
     phones, delete downloaded APKs and uninstall the phone app; its
     uploaded copies in the data folder go with that folder, while any
     configured `phone_dir` must be removed separately. The transcription venv
     (`~/.local/share/fridolin/venv`) and Hugging Face's model cache
     (`~/.cache/huggingface/`) hold no messages.

## Contact

Questions about this policy: open an issue in this repository. A security
problem, such as a way to read data this page says stays private, goes
through [SECURITY.md](SECURITY.md) instead: it is reported privately, not
in a public issue.
