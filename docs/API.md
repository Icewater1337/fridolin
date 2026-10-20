# The script API

The bridge's HTTP server (`http_listen`, default `127.0.0.1:8765`) has four
endpoints for programs on this computer: `GET /health`, `GET /ready`,
`POST /send` and `POST /ingest`. This page is their contract; tests pin
every part of it (`cmd/fridolin/server_test.go`, `cmd/fridolin/script_contract_test.go`). The JSON
of `/send` and `/ingest` has schemas, which the tests check every answer,
and every request the bridge takes, against:
[`send-request`](schemas/send-request.schema.json),
[`send-response`](schemas/send-response.schema.json),
[`ingest-request`](schemas/ingest-request.schema.json) and
[`ingest-response`](schemas/ingest-response.schema.json). Keys are added
to them, never renamed or removed. The response schemas describe exactly
the answers this version gives, and refuse any other key, which keeps
them to the code: a client that checks answers against them should let
unknown keys through, since later versions add them. The dashboard's own
API under `/api/` is not a contract and changes with the dashboard.

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

`whatsapp` is the connection state (`pairing`, `qr-expired`, `connecting`,
`connected`, `disconnected`, `logged-out`, `replaced`, `outdated`,
`banned`, `failed`, `disabled` in dev mode), so a script can tell a bridge
that still needs pairing from one that was logged out.

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
out in its chat; a code request sent this way arms the OTP guard in that
chat for ten minutes (see
[`otp_guard`](CONFIGURATION.md#otp_guard-and-otp_markers)). The line is
marked with how it came in (`"via"`: `"script"`, `"brain"` or `"api"` for no
token), and the brain's chat history shows it as sent by a script or the
brain that sent it, not as the brain's own words.

| Status | Body | When |
| --- | --- | --- |
| `200` | `{"status": "sent"}` | Every part went out. A message that asks for a one-time code the OTP guard would catch (`otp_guard` on, one of `otp_markers` in `text`, a brain that answers the chat) also gets `"otp_request"`, an id that the [hand-off of the code](CONFIGURATION.md#otp_guard-and-otp_markers) repeats in its `request`. |
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
{"brain": "personal", "paths": ["/Users/you/.fridolin/media/scan.pdf"], "text": "File this", "sender": "upload"}
```

- `brain`: the brain that handles it. `scope`, its old name, is refused
  with `400` unless it is empty, so a script that still sends it learns
  the name to use. A brain calling with its run's token may leave it out.
- `paths`: files already written at the top of the media directory
  (`<data_dir>/media` by default), or already in the named brain's own
  inbox (`media/<brain id>/`); the brain takes them into its inbox. A file
  in any other folder of the media directory is refused, since those hold
  files for brains (see
  [`ingest.enabled`](CONFIGURATION.md#ingestenabled)). Each must be an
  absolute path to a regular file there, also after symlinks are resolved.
  A brain calling with its run's token hands in only files in its own
  inbox: it cannot write the top of the media directory, so a file there
  is one a script left for some brain. A symlink in its inbox counts only
  when it leads to a file in that inbox; one that leads out is refused
  without being followed, with the same answer as a file that is not
  there.
- `text`: the message that goes with them; without one the brain is told
  "New files were uploaded." `paths` or `text` must be given.
- `sender`: who the brain is told it is from. Default `upload`.

The brain answers in its reply chat (`reply_chat`, or the default
[BRAINS.md](BRAINS.md) describes), even when that chat belongs to another
brain. The request returns at once; the run happens afterwards.

| Status | Body | When |
| --- | --- | --- |
| `202` | `{"status": "accepted", "jid": …}` | Queued; `jid` is the chat the answer goes to. |
| `400` | `{"error": …}` | The body is not JSON, it has a non-empty `scope`, nothing to hand over, an unknown brain, a path outside the media directory, in another folder of it or not a regular file, or a brain with no chat to answer in. |
| `401` | `{"error": …}` | No token, or one the bridge did not give out. |
| `403` | `{"error": …}` | A brain named another brain. |
| `404` | `{"error": …}` | `ingest.enabled` is off. |

## The run's MCP server: `GET /brain/mcp/tools`, `POST /brain/mcp/call`

Every run of Claude Code (and of Codex) is started with an MCP server of
the bridge's own, `fridolin mcp --run <run id>`, which the CLI runs over
stdio from the run's `--mcp-config` file in `run-tokens/<brain>/` (Codex:
`-c mcp_servers.fridolin.*`). That server knows no tools itself: it asks
the bridge on these two routes, with the run's token from
`FRIDOLIN_BRAIN_TOKEN_FILE` and the run id in an `X-Fridolin-Run` header,
which must be the token's run. The bridge decides from the token which
brain, run and chat is asking, and from that brain's approved
`brain.json` which tools the run gets ([BRAINS.md](BRAINS.md#the-core-brain-and-its-grants)):
nothing the model sends can change that. The scripts token is refused
here, since it is no run's.

`GET /brain/mcp/tools` answers `{"tools": [...]}`, each as MCP's
`tools/list` has it (`name`, `description`, `inputSchema`).
`POST /brain/mcp/call` takes `{"name": "<tool>", "arguments": {…}}` and
answers as MCP's `tools/call` does: `{"content": [{"type": "text",
"text": …}], "isError": …}`, `isError` for a tool that ran and failed.
A `delegate` call waits for the sub-brain's answer, up to its
`timeout_seconds`; `request_approval`, `ask_user` and `check_approval`
wait for the owner's answer up to their `wait_seconds` (at most 600), then
answer `pending` with the approval's id. Every call is a `tool_call` line
of the audit log, its arguments left out.

| Status | When |
| --- | --- |
| `200` | The tools, or the call's result (which can be a failure the model reads). |
| `400` | The body is not `{"name": …, "arguments": …}`. |
| `401` | No token, or not a token of a run in progress. |
| `404` | The run has no tool of that name: one it is not granted is as absent as one that does not exist. |
| `412` | `X-Fridolin-Run` names another run than the token's. |

These two are not a contract for scripts: they serve `fridolin mcp`, and
change with it.

With the [vault](VAULT.md) running, every run also gets `vault_ls`,
`vault_get`, `vault_put` and `vault_search`, scoped to its brain's vault
paths and, for a core run, those of the brains it is granted `files`; a
path outside them is answered as a failed call the model reads, not with
an HTTP error.

## The vault's dashboard routes

`fridolin vault` and the dashboard's Vault page use these, with the
dashboard token or session like the rest of `/api/` (not a contract
either):

| Route | Does |
| --- | --- |
| `GET /api/vault/status` | the vault's state, numbers, last upload, snapshots, checks, Drive copy, Litestream, last restore drill and the latest 50 evictions; ETag-cached |
| `POST /api/vault/sync`, `/evict`, `/rebuild`, `/restore-drill` | start that job: `202 {"status": "started"}`, or `409` while another job runs and `412` while the vault is off or not running |
| `POST /api/vault/snapshot` | a snapshot now, `{"kind": "nightly"}` (default) or `"hourly"`; `202` |
| `POST /api/vault/check` | `restic check` now, `{"read_data_subset": "5%"}` to read data too; `202` |
| `POST /api/vault/prune/allow` | lift a deletion hold: pruning goes on; `200` |
| `POST /api/vault/get` | `{"path": "<vault path>", "allow_over_budget": false}`: bring the file back and answer `{"path": "<absolute path>"}`; `404` for a path the vault does not hold, `412` when it would take the hot tier over its budget |

## The policy's dashboard routes

Like the rest of `/api/`, these serve the dashboard (its token or session,
and for writes its header and JSON) and are not a contract; they are
listed here because they decide what runs may do
([BRAINS.md](BRAINS.md#skills-risk-and-approvals)).

| Route | What |
| --- | --- |
| `GET /api/policy` | the lockdown (`lockdown`, `lockdown_by`, `lockdown_since`), the limits, and how many approvals and questions wait |
| `POST /api/policy/lockdown` | `{"on": true}` locks down, `{"on": false}` unlocks, as the dashboard |
| `GET /api/approvals` | `{"pending": [...], "history": [...]}`: what runs asked, waiting oldest first, decided newest first (100) |
| `POST /api/approvals/{id}` | `{"decision": "approve" \| "deny"}`, which counts as the owner's vote; `409` once it is decided |
| `GET /api/threads?limit=` | the runs of the audit log grouped by thread, newest first |
| `GET /api/audit?kind=&brain=&thread=&run=&id=&q=&limit=&months=` | the audit log's lines, newest first, of the last `months` (2) monthly files |
| `GET /api/brains/{id}/skills` | the brain's skills, each with its frontmatter, `status`, the `hash` of its declared policy and the policy that applies (`effective`) |
| `POST /api/brains/{id}/skills/approve` | `{"path", "hash"}` approves the declared policy if it is still the one with that hash (`409` otherwise) |
| `PUT /api/brains/{id}/skills/policy` | `{"path", "risk", "approve", "vault", "triggers"}` writes them into the file and approves them |
| `POST /api/dev/vote` | dev mode only: `{"poll", "voter", "options"}` votes in a poll the dev chat shows, as a vote from WhatsApp arrives |

## Limits

A request body may be 1 MiB at most; a larger one is refused with `400`.
Reading a request (headers and body) may take 30 seconds, and an idle
connection is closed after two minutes.

## Nodes

The fleet's routes are part of the dashboard's API, not this contract,
and are guarded like the rest of it: the dashboard token (`X-Bridge-Token`)
or a signed-in session, and on writes the `X-Bridge-Dashboard: 1` header
and a JSON body. `fridolin nodes` calls them with the token from the data
dir. [NODES.md](NODES.md) describes nodes, pairing and the protocol the
nodes speak on their own port.

| Route | Does |
| --- | --- |
| `GET /api/nodes` | `{"enabled", "listen", "nodes": [...]}`: every node of `nodes/nodes.json` with `device`, `commands`, `declared`, `approved`, `effective`, `online`, `last_seen`, `resources`, `tags` |
| `POST /api/nodes/join-code` | `{"code", "expires_at", "command"}`: a single-use code for 10 minutes and the line to paste on the new machine; `409` while `nodes.enabled` is off |
| `POST /api/nodes/{id}/approve` | approves the device (first stage) |
| `POST /api/nodes/{id}/approve-commands` | approves what the node declares now (second stage); `409` before the device is approved |
| `POST /api/nodes/{id}/revoke` | revokes it and closes its connection |
| `PUT /api/nodes/{id}/tags` | `{"tags": [...]}` replaces its tags; `422` for a tag that is not lower-case letters, digits and `._:-` |
| `POST /api/nodes/{id}/call` | `{"method", "params"}` calls a capability (`exec`, `fs.list`, `fs.read`, `fs.write`, `host.info`, `notify`) and answers `{"result": …}`; a refusal is `{"error", "code"}` with `403` (`denied`, `outside-root`), `404`, `400`, `503` (not connected) or `504` |

An unknown node is `404`; one that was revoked cannot be approved again
(`409`).
