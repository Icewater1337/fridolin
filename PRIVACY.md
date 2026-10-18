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
  runs you start in your own terminal follow your own settings.

## Where it goes

- Everything is stored **on your own computer**: messages, history, memories,
  downloaded files, and the sign-in tokens for Google (readable only by the
  brain they belong to). A one-time code the OTP guard catches is kept in
  `<data_dir>/otp/` for the local script that asked for it, readable only by
  your user, until the next code replaces it.
- To answer a message, the bridge runs **Claude Code** on your computer. Claude
  Code sends the prompt — which can include the message, recent chat history
  and any calendar details the brain looked up — to **Anthropic** to generate
  the reply, under your own Claude account and Anthropic's terms.
- Backups go to **your own Google Drive**.
- Plaud transcripts are saved as text files on your computer and handed to the
  brain you chose, so they reach Anthropic like any message the brain answers.
  Each is written to the media inbox first (`<media_dir>/plaud/`), which every
  brain may read under the sandbox, until the brain files it in its own
  folder; from there it goes into that brain's backups too.
- A message deleted for everyone on WhatsApp is taken out of the brain's
  chat history (`conversation-history.jsonl`), and is not answered if it was
  still waiting; a file it carried stays in the media inbox, or where the
  brain filed it. Backups made before the deletion keep their copy. View-once
  photos, videos and voice notes are never downloaded. Messages in chats with
  disappearing messages turned on are kept in the history like any others.
- Nothing is sent to the authors of this project or to anyone else.

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
- Deleting a brain folder and `~/.fridolin/` removes everything
  the bridge stored locally, except a Plaud login in `~/.plaud/` (with "Let
  brains use the Plaud CLI" on), which belongs to Plaud's tool.

## Contact

Questions about this policy: open an issue in this repository.
