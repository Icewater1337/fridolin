# Troubleshooting

**"claude: command not found"** — set `claude_path` to the CLI's absolute path.
On macOS the desktop app keeps it at
`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`.

**No reply in a group** — look under *Chats* in the dashboard: an unknown group
shows up there with an *Allow* button. Otherwise check which brain the group
routes to, and that you used a trigger keyword.

**No reply to a DM** — the sender's number must be allowed (*Chats →
Allowed numbers*), with the country code; a `+`, spaces and dashes are fine.
An empty list allows nobody unless `allow_any_dm` is `true`. Blocked senders
appear under *Chats*, and the Overview warns about an entry that is no
number.

**"Not logged in" from the CLI** — with the sandbox on, brains cannot read
your keychain, where the login you made with `claude` lives: run
`claude setup-token` and set `CLAUDE_CODE_OAUTH_TOKEN`
(or `ANTHROPIC_API_KEY`) in the bridge's process environment (the
dashboard warns while neither is there). Without the sandbox, run `claude`
once in a terminal to authenticate; under launchd, check that `HOME` is
set correctly in the plist. The bridge reads no `.env` file, and says so in
its log when one lies next to the config
([details](CONFIGURATION.md#environment-variables)).

**Logged out of WhatsApp** — pair again with *Start pairing* on the
dashboard's Overview; no restart is needed, and `bridge.db` can stay (the
bridge starts a new session in it and keeps the poll history). Remove the old
entry under *Linked Devices* on the phone if it is still there. Only if
pairing keeps failing, stop the bridge and move `bridge.db` out of your data
directory, keeping it until the new pairing works; that also clears the poll
history.

**"Fridolin needs an update"**, **"Temporarily banned"**, **"WhatsApp refused
the connection"** — the Overview says what WhatsApp answered and what to do:
update and rebuild (`git pull && ./setup.sh`) when WhatsApp no longer accepts
the bridge's WhatsApp library; wait for a ban to end, when the bridge
connects again by itself. After a refusal on WhatsApp's side it tries again
by itself too, waiting longer each time, up to half an hour; after any other
refusal, restart the bridge.

**"another bridge is already running"** — only one bridge may use a data
directory; two bridges on one WhatsApp session knock each other offline.

**Everything times out with the sandbox on** — set `sandbox.enabled` to `false`
to confirm it's the cause, then add the path it needs to read to
`sandbox.allow_paths`. That opens reading only: a brain writes nowhere but
its own folder, inbox and state. A tool that fails with "Operation not
permitted" writing to `/tmp`, or a bare `mktemp`, needs `$TMPDIR` named
([temporary files](CONFIGURATION.md#writing)).

**"the sandbox could not be set up"** — the sandbox is on, but the bridge
could not build the profile or find `/usr/bin/sandbox-exec`, so it did not run
the message at all. The bridge log names the cause, and the dashboard's
Overview shows it too, with the brains it affects.

---

See also [Setup](SETUP.md), [Configuration](CONFIGURATION.md) and the
[security reporting policy](../SECURITY.md).
