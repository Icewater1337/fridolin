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

## Where it goes

- Everything is stored **on your own computer**: messages, history, memories,
  downloaded files, and the sign-in tokens for Google (readable only by the
  brain they belong to).
- To answer a message, the bridge runs **Claude Code** on your computer. Claude
  Code sends the prompt — which can include the message, recent chat history
  and any calendar details the brain looked up — to **Anthropic** to generate
  the reply, under your own Claude account and Anthropic's terms.
- Backups go to **your own Google Drive**.
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
  token at Google and deletes it. You can also remove access at
  [myaccount.google.com/permissions](https://myaccount.google.com/permissions).
- Disconnect Google Drive on the dashboard's Backup page.
- Deleting a brain folder and `~/.whatsapp-claude-bridge/` removes everything
  the bridge stored locally.

## Contact

Questions about this policy: open an issue in this repository.
