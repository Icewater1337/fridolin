# Skill: Transcription

## When to use

Any time you receive an **audio or video** file whose content matters as text —
voice notes, recorded calls, meeting recordings, interviews, video clips with
speech. Formats: `.ogg`, `.mp3`, `.m4a`, `.wav`, `.opus`, `.mp4`, `.mov`,
`.webm`, `.3gp`, `.mkv`, `.avi`.

Length is not a consideration. The tool chunks long recordings internally; a
90-minute meeting is one command, same as a 10-second voice note.

If the file is audio or video but the *content* doesn't matter as text — a song,
a clip someone sent for fun, a voice memo being kept as a memento — use the
**media-management** skill instead and leave it untranscribed.

## Where things go

Both the transcript and the original media land in the transcripts folder of
your working directory:

```
<work_dir>/transcripts/
├── 2026-01-15 - Notes from the planning call.txt
└── 2026-01-15 - Notes from the planning call.ogg
```

The tool produces both, with a shared stem so they stay paired. Nothing to copy
elsewhere afterwards.

## Do not improvise

**Always call the transcription tool.** Never:

- import `whisper` or `transformers` and run a model yourself
- call a transcription API directly with `curl` or an SDK
- split the file with `ffmpeg` and transcribe the pieces yourself
- re-encode the file first "to make it work"

The tool already handles video-to-audio extraction, duration-based chunking,
loading the model once and reusing it across chunks, API fallback per chunk,
and rate-limit backoff. Each of those exists because the naive version failed
in a specific way — a hand-rolled pipeline re-encodes into formats that fail to
decode at segment boundaries, trips rate limits, and hallucinates confident
nonsense on silent chunks. It looks like it worked. It didn't.

If the tool errors, read the error and fix the cause. Don't route around it.

## How

One command, whatever the format or length:

```bash
python3 <bridge-repo>/tools/transcribe.py <file> --output-dir <work_dir>/transcripts
```

Useful flags:

| Flag | When |
| ---- | ---- |
| `--language de` | You know the language. Skips detection and improves accuracy. |
| `--prompt "..."` | Seed names, jargon, or the dialect being spoken — the single biggest accuracy win on domain-specific audio. |
| `--engine groq` | The local model isn't installed, or you want speed over privacy. |
| `--no-keep-original` | Don't copy the source media next to the transcript. |

A long recording can take several minutes. **Let it run.** Do not interrupt it
and reimplement it in shell.

The tool prints JSON: `transcript`, `transcript_file`, `audio_file`,
`language`, `engine`, `duration_seconds`.

## Afterwards

1. **Share the transcript** in the chat. For a long one, lead with a summary
   and the key points, then offer the full text rather than dumping thousands
   of words into WhatsApp.
2. **Mention where it was saved** — one line, just the path.
3. **Surface action items.** If the recording contains decisions, deadlines,
   commitments, or questions aimed at someone, call them out explicitly. This
   is usually the reason the recording was sent.
4. **Write down what's durable.** If the transcript establishes something worth
   remembering — a new person and their role, a decision and its reasoning, a
   preference, a recurring commitment — add a short note under `memories/`,
   with a line pointing back at the transcript filename. The transcript is the
   record; `memories/` is what you'll actually find later.

Don't write a memory for every transcript. Most are ephemeral. Write one when
you'd be annoyed to have forgotten it in a month.

## Example

Someone sends a voice note. You receive the file path in the message:

```bash
python3 <bridge-repo>/tools/transcribe.py \
  ~/.fridolin/media/3EB0A1B2C3.ogg \
  --output-dir <work_dir>/transcripts \
  --language en
```

Read the JSON, share the text, flag anything actionable.

## Setup note

Transcription needs extra dependencies — they are not installed by default:

```bash
# Local model: private, no API key, large download
pip install -r <bridge-repo>/tools/requirements-transcription.txt

# Or the API: light, needs a key
pip install groq && export GROQ_API_KEY=...
```

`ffmpeg` is required either way. If neither engine is available, the tool says
so clearly — pass that message to the user rather than trying to work around it.
