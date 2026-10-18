# The script API

The bridge's HTTP server (`http_listen`, default `127.0.0.1:8765`) has four
endpoints for programs on this computer: `GET /health`, `GET /ready`,
`POST /send` and `POST /ingest`. This page is their contract; tests pin every part of it
(`server_test.go`). The dashboard's own API under `/api/` is not a contract
and changes with the dashboard.

All four answer on loopback only. They refuse a request whose `Host` is not
the bridge's address (`421`, which stops DNS rebinding) and a write that a
web page in your browser sent for another site (`403`, from the `Origin` or
`Sec-Fetch-Site` header); those two answer in plain text. Programs such as curl, Python or Node send neither
header and pass.

## `GET /health`

`200`, body `ok` and a newline. Nothing else is checked: it says the bridge
is up and serving, paired or not.

## `GET /ready`

Whether `/send` can deliver: `200` once WhatsApp is connected (or in dev
mode), `503` otherwise, both with

```json
{"ready": true, "whatsapp": "connected", "version": "…", "started_at": "…"}
```

`whatsapp` is the connection state (`pairing`, `connecting`, `connected`,
`logged-out`, `replaced`, …), so a script can tell a bridge that still
needs pairing from one that was logged out.

## `POST /send`

Sends a WhatsApp message.

```json
{"jid": "120363000000000000@g.us", "text": "Deploy finished ✅"}
```

- `jid`: the chat. A phone-number chat (`…@s.whatsapp.net`), a LID chat
  (`…@lid`) or a group (`…@g.us`), with something before the `@`.
- `text`: sent as it is, split into numbered parts when it is longer than
  `max_chunk_size`. A `[POLL]` block is not turned into a poll here.

The message is appended to the history of the brain that owns the chat, so
the brain knows what went out in its chat; a code request sent this way arms
the OTP guard (see [`otp_guard`](CONFIGURATION.md#otp_guard-and-otp_markers)).

| Status | Body | When |
| --- | --- | --- |
| `200` | `{"status": "sent"}` | Every part went out. |
| `400` | `{"error": …}` | The body is not JSON, `jid` or `text` is missing or blank, `jid` is not a chat, or it is a Status, broadcast list or channel (`…@broadcast`, `…@newsletter`), which the bridge never posts to. |
| `503` | `{"error": …}` | WhatsApp is not connected or not paired. Try again later. |
| `500` | `{"error": …}` | Any other send failure. |

When a long message fails part of the way, the parts that went out are in
the chat and in the history, and the error says how many of them there were.

## `POST /ingest`

Hands a brain files that arrived some other way (a web upload, a recorder
sync), as if they were sent in its chat. Off until
[`ingest.enabled`](CONFIGURATION.md#ingestenabled) is on.

```json
{"brain": "personal", "paths": ["/Users/you/.fridolin/media/upload/scan.pdf"], "text": "File this", "sender": "upload"}
```

- `brain`: the brain that handles it. `scope`, its old name, is refused
  with `400`, so a script that still sends it learns the name to use.
- `paths`: files already written inside the media directory
  (`<data_dir>/media` by default). Each must be an absolute path to a
  regular file there, also after symlinks are resolved.
- `text`: the message that goes with them; without one the brain is told
  "New files were uploaded." `paths` or `text` must be given.
- `sender`: who the brain is told it is from. Default `upload`.

The brain answers in its reply chat (`reply_chat`, or the default
[BRAINS.md](BRAINS.md) describes), even when that chat belongs to another
brain. The request returns at once; the run happens afterwards.

| Status | Body | When |
| --- | --- | --- |
| `202` | `{"status": "accepted", "jid": …}` | Queued; `jid` is the chat the answer goes to. |
| `400` | `{"error": …}` | The body is not JSON, it has `scope`, nothing to hand over, an unknown brain, a path outside the media directory or not a regular file, or a brain with no chat to answer in. |
| `404` | `{"error": …}` | `ingest.enabled` is off. |

## Limits

A request body may be 1 MiB at most; a larger one is refused with `400`.
Reading a request (headers and body) may take 30 seconds, and an idle
connection is closed after two minutes.
