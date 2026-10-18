# Plaud recordings

When to use: a message from **plaud** that starts with `[Plaud] New recording`
arrives with a `.txt` attachment, or someone asks about a Plaud recording.
That first line says what arrived. What follows is the instruction for
recordings: one the owner set on the dashboard, which wins where it differs
from this skill, or Fridolin's own, which points here.

## A new recording arrived

Fridolin picked it up from the Plaud recorder (dashboard → Integrations →
Plaud) once Plaud had finished transcribing it. The attachment is already a
transcript:

```
Plaud recording: <name>
Recorded: YYYY-MM-DD HH:MM
Duration: …
Plaud ID: of_…

=== Plaud AI summary ===     (when Plaud made one)
…
=== Transcript ===
[00:00 - 00:12] Speaker 1: …
```

**Do not transcribe it again** — it is text, not audio.

1. Read it. Work out who was speaking (the identity map and memories help;
   "Speaker 1/2" are Plaud's guesses) and what the conversation was about.
2. Archive it: move the file from the media inbox into `transcripts/` as
   `YYYY-MM-DD - Short description.txt` (date of the recording, a few words
   that say what it was), like the transcripts `skills/transcription.md`
   makes, so the folder sorts by date. Do not leave it in the inbox.
3. Keep what matters: decisions, commitments, dates, people, numbers → the
   relevant notes in `memories/` (and the wiki, if this brain keeps one, with
   a link back to the transcript).
4. Reply with a short summary: what it was, the key points, and the action
   items with who owns them. If something should go in the calendar, offer it.

## Looking things up

Only when the dashboard's "Let brains use the Plaud CLI" setting is on — otherwise
the login is not available to you and these fail; use the transcripts you
archived instead.

```bash
plaud recent --days 7          # recordings of the last week
plaud search "keyword"         # by name
plaud transcript <of_id>       # a transcript
plaud summary <of_id>          # Plaud's AI summary
```

## When something is off

- A recording never arrived: Plaud may still be transcribing it (Fridolin waits
  up to three days), or the login expired — Fridolin then says so in this chat
  once a day. The fix is in the dashboard: Integrations → Plaud → Connect again.
- Recordings are waiting although Plaud is connected: Fridolin has no private
  chat to send summaries to (no allowed number and no direct reply chat for
  this brain), and the Plaud card says so. They arrive once one is set.
- Recordings are waiting and the Plaud card names a brain: the brain picked
  for Plaud was removed or is not approved. They arrive once another one is
  picked there.
- The Plaud card says the last check failed for a recording: it could not be
  fetched from Plaud (the card shows why). Fridolin tries again, less often
  over time, for up to three days, then gives up, lists it on the card and
  says so in this chat once; one deleted in Plaud is dropped at once.
- A recording was stopped with `!stop`: it is not sent again on its own.
  **Send again** on the Plaud card hands it over once more. One whose run
  failed is handed over again by itself up to twice more, then given up
  with a notice.
