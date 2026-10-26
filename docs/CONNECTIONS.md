# Connections: OpenAI and ElevenLabs

Fridolin can hold one OpenAI key and one ElevenLabs key. Site modules and
brains then use OpenAI and ElevenLabs through the bridge. The bridge makes
each call with the key. No module, brain run or site process ever sees a
key, so rotating one means changing it in one place.

For each call the bridge:

- checks what the caller declared (a module) or was granted (a brain),
- checks the caller's monthly budget,
- calls the provider with the key,
- counts what the call cost, estimated, per caller and month,
- writes a line to the audit log.

## Setting the keys

Dashboard → **Integrations → OpenAI** or **ElevenLabs**. Paste the key
and save. The page shows only whether a key is set and its last four
characters. **Check** asks the provider whether it takes the key.
**Remove** deletes it; the usage and the settings stay.

For OpenAI you can also set an organization and a project id. They go
out as the `OpenAI-Organization` and `OpenAI-Project` headers. A project
key with a budget of its own on OpenAI's side is the safest choice: OpenAI
enforces that budget, while Fridolin's is an estimate.

The bridge keeps the keys in `<data_dir>/connections/`, a folder only
your user may open:

| File | What |
| --- | --- |
| `openai.json` | the OpenAI key, organization and project |
| `elevenlabs.json` | the ElevenLabs key |
| `usage.json` | the usage per month, connection and caller, for 13 months |

They are never in `config.json`, the environment of a brain or the site,
a log, the audit log or anything the dashboard reads. `fridolin doctor`
checks each key that is set.

## Site modules

A module declares what it uses in its `module.json`, and the deploy review
lists it:

```json
"capabilities": {
  "openai":     { "use": ["transcribe", "responses"], "models": ["gpt-4o-transcribe"], "monthly_usd": 20 },
  "elevenlabs": { "use": ["tts", "voices"], "monthly_usd": 5 }
}
```

- `use`: the operations it may call (below).
- `models`: the models it may use. Left out, any model. The default model
  a call uses when it names none must be listed too.
- `monthly_usd`: its monthly limit in US dollars, estimated. Left out, no
  limit of Fridolin's.

Its server code calls `ctx.openai` and `ctx.elevenlabs`
([SITE.md](SITE.md)). The site passes each call to the bridge's site
listener with the module's id. The bridge checks it against the live
commit's manifest of that module. All modules run in one process, so a
module's code could name another module and get what that one declares:
approving a deploy trusts its code with what every module of it
declares.

## Brains

Dashboard → **Integrations**, on the connection's card: the brains that
may use it and each brain's monthly limit. Or in `config.json`:

```json
"connections": {
  "openai":     { "brains": ["personal"], "brain_monthly_usd": 10 },
  "elevenlabs": { "brains": ["personal"], "brain_monthly_usd": 5, "voice": "21m00Tcm4TlvDq8ikWAM" }
}
```

A brain's runs get the tools below only while the connection has a key
and the brain is in its `brains`. `brain_monthly_usd` is each brain's
limit. `voice` is the ElevenLabs voice text-to-speech uses when a caller
names none.

| Tool | Arguments | Answers |
| --- | --- | --- |
| `openai_transcribe` | `path`, `language`, `prompt`, `model` | `{"text"}` |
| `elevenlabs_transcribe` | `path`, `language`, `model` | `{"text", "language"}` |
| `elevenlabs_speak` | `text`, `voice`, `model`, `format` | `{"path", "content_type", "characters"}` |
| `elevenlabs_voices` | none | `{"voices": [{"id", "name"}]}` |

`path` is an absolute path in the brain's folder, its media inbox or its
temp folder, such as a voice note's path from the message. The bridge
reads it there itself, through `os.Root`, so a link cannot lead out.
`elevenlabs_speak` writes the audio to `speech/` in the brain's temp
folder. The bridge cannot send audio into WhatsApp yet; that comes later.

The tools that send something to the provider act outside the bridge.
In a lockdown they are refused, and so they are for a run whose risk
level is `read` ([BRAINS.md](BRAINS.md#skills-risk-and-approvals)).
Listing the voices only reads.

A brain whose data must not leave the machine keeps transcribing with the
local model of `tools/transcribe.py`: leave it out of `brains`.

## Operations and limits

| Connection | Operation | What it calls | Limits |
| --- | --- | --- | --- |
| OpenAI | `transcribe` | `POST /v1/audio/transcriptions` | audio up to 25 MiB; default model `gpt-4o-mini-transcribe`; 120 s |
| OpenAI | `responses` | `POST /v1/responses`, the request as it is | up to 20 MiB; `model` required; `stream` and `background` refused; 180 s |
| ElevenLabs | `tts` | `POST /v1/text-to-speech/{voice}` | up to 10,000 characters; default model `eleven_multilingual_v2`, format `mp3_44100_128`; 120 s |
| ElevenLabs | `transcribe` | `POST /v1/speech-to-text` | audio up to 25 MiB; default model `scribe_v1`; 180 s |
| ElevenLabs | `voices` | `GET /v1/voices` | 30 s |

A call that goes wrong answers an error the caller can act on: 400 for a
bad request, 403 for something not declared, 429 over the budget, 503
without a key, 502 when the provider refused (with its status and code),
504 when it took too long. A provider's message is scrubbed of anything
that looks like a key before it is passed on.

## Usage and budgets

The Integrations page shows this month's usage per caller: requests,
errors, audio seconds, tokens, characters and the estimated cost, with
each caller's limit. A month is a calendar month in UTC.

The cost is an **estimate** from a small price table in the bridge
(`internal/connections/prices.go`):

- OpenAI transcription: per minute when OpenAI says how long the audio
  was; else by its tokens; else from the file's size, about a minute per
  megabyte.
- OpenAI responses: by the input and output tokens OpenAI counts.
- ElevenLabs text-to-speech: $0.30 per 1,000 characters billed, as
  ElevenLabs reports them, else the text's length.
- ElevenLabs speech-to-text: per minute, by the transcript's last word,
  else from the file's size.
- A model not in the table costs the table's highest rate, so a budget
  errs on the safe side.

A caller whose estimate this month reached its limit is refused until the
next month. Calls that run at the same time are checked before either
has counted, so a limit can be passed by the last few calls. The
providers' own bills are what you pay; set a budget on OpenAI's project
for a hard limit.

## Audit

Each call writes a `connection_call` line: who asked (`module:<id>` or
`brain:<id>`), the connection, operation and model, and how it ended.
Setting or removing a key writes a `connection_key` line, by `dashboard`.
Neither ever holds a key, a text, a recording or an answer.

## What goes where

- The recordings and texts a caller sends go to OpenAI or ElevenLabs,
  under the account of the key and that provider's terms
  ([PRIVACY.md](../PRIVACY.md)).
- The keys stay in the data dir. They move with the core's export and the
  vault's snapshot of the data dir, like the dashboard token
  ([CORE.md](CORE.md)).
- The macOS sandbox keeps brains and the live site out of the data dir.
  On Linux without the container, brains and the site run without a
  sandbox and can read what your user can, the keys included
  ([SECURITY-MODEL.md](SECURITY-MODEL.md)).

## Not yet

- Streaming, and OpenAI's Realtime API for live transcription.
- Sending audio into WhatsApp: the bridge has no outbound media yet.
- A key per brain or team: there is one key a connection.
