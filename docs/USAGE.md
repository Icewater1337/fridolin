# Usage

Use the local dashboard to pair WhatsApp, create brains, allow chats and monitor
runs. [The README usage guide](../README.md#using-it) explains conversation,
memory, skills and attachments. Each brain is a folder containing a manifest,
prompt and history; [Brains](BRAINS.md) describes routing and inheritance.

A group message must satisfy its sender policy and trigger rules. A direct
message must satisfy the DM allowlist. The fallback brain also handles DMs when
no brain explicitly claims them. The dashboard shows effective routing; use
`fridolin check --route JID` to inspect a route without starting the service.

| Task | Reference |
| --- | --- |
| Change runtime and startup settings | [Configuration](CONFIGURATION.md) |
| Send or ingest from a script | [API](API.md) |
| Give a brain Google Calendar access | [Calendar](CALENDAR.md) |
| Receive recorder transcripts | [Plaud](PLAUD.md) |
| Back up or restore brain data | [Backup](BACKUP.md) |
| Build and publish a site | [Site](SITE.md) |
| Inspect helper commands | [Tools](TOOLS.md) |
| Diagnose missing replies or startup failures | [Troubleshooting](TROUBLESHOOTING.md) |

Do not expose the dashboard to the public internet. Its loopback listener and
request guards are part of the access boundary; see [Security model](SECURITY-MODEL.md).
