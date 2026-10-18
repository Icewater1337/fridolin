# The script API

The bridge's HTTP server (`http_listen`, default `127.0.0.1:8765`) has four
endpoints for programs on this computer: `GET /health`, `GET /ready`,
`POST /send` and `POST /ingest`. This page is their contract; tests pin every part of it
(`server_test.go`). The dashboard's own API under `/api/` is not a contract
and changes with the dashboard.

All four answer on loopback only. They refuse a request whose `Host` is not
the bridge's address (`421`, which stops DNS rebinding) and a write that a
web page in your browser sent for another site (`403`, from the `Origin` or
`Sec-Fetch-Site` header); those two answer in plain text. Programs such as
curl, Python or Node send neither header and pass.

## Who may call

Brains run on this computer too, and can reach the API with curl. So
`/send` and `/ingest` ask who is calling, from an `Authorization: Bearer
<token>` header (the scheme in any case, as HTTP allows):

| Token | Who | `/send` | `/ingest` |
| --- | --- | --- | --- |
| `<data_dir>/scripts.token` | your own scripts | any chat | any brain |
| the run's token, in the file `$FRIDOLIN_BRAIN_TOKEN_FILE` names | that brain, while the run lasts | its own chats | only itself |
| none | anyone on this computer | the allowed chats, while `script_api.tokenless_send` is on | refused |

- The bridge makes `scripts.token` on its first start. It is readable only
  by your user, and the macOS sandbox denies it to brains, like the rest of
  the data dir. On Linux, or with the sandbox off, a brain can read it.
- Every brain run gets `FRIDOLIN_API`, the API's address, and
  `FRIDOLIN_BRAIN_TOKEN_FILE`, a file with a token for that run: the
  token of the [brain API](CONFIGURATION.md#http_listen-restart), which
  stops working when the run ends. The macOS sandbox lets only that brain
  read the file. The token is not in the run's environment, since macOS
  shows a process's environment to every program of the same user, other
  brains included. A brain's own chats are its reply chat and the allowed
  chats that route to it: the groups it claims, for the fallback brain
  ([`routing.default`](BRAINS.md#brainjson)) also the allowed groups no
  brain claims, and, for the brain that gets DMs, the allowed numbers.
- Without a token, `/send` reaches only the chats `allowed_numbers` and
  `allowed_groups` name, and only while
  [`script_api.tokenless_send`](CONFIGURATION.md#script_apitokenless_send)
  is on, so scripts written before tokens keep working. A brain could use it
  to message chats that are not its own, which is why new configs turn it
  off.
- A token the bridge did not give out, one of a run that is over, or one
  of a brain that was removed, is refused with `401`; it never falls back
  to calling without one.

```bash
curl -X POST http://127.0.0.1:8765/send \
  -H "Authorization: Bearer $(cat ~/.fridolin/scripts.token)" \
  -H 'Content-Type: application/json' \
  -d '{"jid":"120363000000000000@g.us","text":"Deploy finished ✅"}'
```

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
  (`…@lid`) or a group (`…@g.us`), with something before the `@`, or a
  phone number with the country code, written any way
  (`"+1 555 123 4567"`), for the direct chat with it. A device part
  (`…:12@s.whatsapp.net`) is dropped, and a LID chat goes to the person's
  number when WhatsApp has told the bridge which it is.
- `text`: sent as it is, split into numbered parts when it is longer than
  `max_chunk_size`. A `[POLL]` block is not turned into a poll here.

The message is appended to the history of the brain that owns the chat,
under the chat's JID whatever form `jid` had, so the brain knows what went
out in its chat; a code request sent this way arms the OTP guard (see
[`otp_guard`](CONFIGURATION.md#otp_guard-and-otp_markers)). The line is
marked with how it came in (`"via"`: `"script"`, `"brain"` or `"api"` for no
token), and the brain's chat history shows it as sent by a script or the
brain that sent it, not as the brain's own words.

| Status | Body | When |
| --- | --- | --- |
| `200` | `{"status": "sent"}` | Every part went out. |
| `400` | `{"error": …}` | The body is not JSON, `jid` or `text` is missing or blank, `jid` is not a chat, or it is a Status, broadcast list or channel (`…@broadcast`, `…@newsletter`), which the bridge never posts to. |
| `401` | `{"error": …}` | No token while `script_api.tokenless_send` is off, or a token the bridge did not give out. |
| `403` | `{"error": …}` | The caller may not send to that chat (see [Who may call](#who-may-call)). |
| `503` | `{"error": …}` | WhatsApp is not connected or not paired. Try again later. |
| `500` | `{"error": …}` | Any other send failure. |

When a long message fails part of the way, the parts that went out are in
the chat and in the history, and the error says how many of them there were.
`/send` does not try again: the script decides. Brains' own replies go
through an outbox in `bridge.db` instead, which keeps trying for an hour.

## `POST /ingest`

Hands a brain files that arrived some other way (a web upload, a recorder
sync), as if they were sent in its chat. Off until
[`ingest.enabled`](CONFIGURATION.md#ingestenabled) is on.

```json
{"brain": "personal", "paths": ["/Users/you/.fridolin/media/upload/scan.pdf"], "text": "File this", "sender": "upload"}
```

- `brain`: the brain that handles it. `scope`, its old name, is refused
  with `400`, so a script that still sends it learns the name to use. A
  brain calling with its run's token may leave it out.
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
| `401` | `{"error": …}` | No token, or one the bridge did not give out. |
| `403` | `{"error": …}` | A brain named another brain. |
| `404` | `{"error": …}` | `ingest.enabled` is off. |

## Limits

A request body may be 1 MiB at most; a larger one is refused with `400`.
Reading a request (headers and body) may take 30 seconds, and an idle
connection is closed after two minutes.
