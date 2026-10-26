# Security policy

Fridolin lets people on WhatsApp start Claude Code or Codex CLI runs on
your computer, so a flaw in it can reach your files, your accounts and
your WhatsApp session. Please report one privately, as described below, and not in a
public issue, a pull request or a discussion.

## Supported versions

Fixes land on `main` and ship in the next release. Only the latest
release gets them, and none are backported to older ones; until the first
release, that is `main`. To get a fix, upgrade (README, *Upgrading and
rolling back*).

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: on the repository's
**Security** tab, choose **Report a vulnerability**. Only the maintainers
see the report, and you can discuss it with them there.

If that option is unavailable, open an issue asking the maintainers to
enable private vulnerability reporting. Include no vulnerability details
or sensitive data in that request; send the report once the private form
is available.

Please include what you can of:

- the version (`fridolin version`), the OS, and whether the sandbox was on;
- who can do it (someone who may message the bridge, a member of an
  allowed group, a brain, another program or user on the same computer, a
  web page the owner visits) and what they get;
- the steps to reproduce it, with placeholders such as `15551234567` and
  `120363000000000000@g.us` instead of real numbers and JIDs, and no real
  chat content, tokens or files from your data folder.

## Scope

In scope, for example:

- **Sandbox escapes** (macOS): a brain's run that reads or writes what its
  sandbox profile denies it (another brain's folder, the bridge's data
  folder, SSH keys, the keychains), or that starts a program outside the
  sandbox, other than in the ways the
  [sandbox section](docs/CONFIGURATION.md#sandbox-macos) lists as not
  covered.
- **Token exposure**: a way for a brain, a web page or another user of the
  computer to get the dashboard token or a dashboard session, the scripts
  token, another run's brain token, a Google token or the OAuth client's
  secret, the OpenAI or ElevenLabs key of the connections, or the WhatsApp
  session in `bridge.db`.
- **The connections** ([docs/CONNECTIONS.md](docs/CONNECTIONS.md)): a
  module calling OpenAI or ElevenLabs beyond what its live manifest
  declares, a brain using a connection it is not granted, or a call that
  gets past a caller's budget other than by calls at the same time.
- **`/send` and `/ingest`**: a token reaching chats or brains it does not
  allow ([docs/API.md](docs/API.md#who-may-call)), a file taken in from
  outside the media folder, or a request a web page sent getting through.
- **The dashboard**: getting past its token or session, a cross-site
  request or DNS rebinding that gets through, script injected into a page,
  or a brain approving its own `brain.json` change.
- **Who reaches a brain**: a message from a number or group that is not
  allowed, or from a member `group_members` does not admit, reaching a
  brain; a chat routed to a brain it does not belong to; a one-time code
  the OTP guard should have caught.
- A secret reaching a backup, a log or a brain it should not.

Out of scope:

- What a brain does with the permissions it has by design. With
  `dangerously_skip_permissions` on, anyone who may message a brain can
  have it run commands as you (README, *Security*): a prompt injection is
  a vulnerability here only when it crosses one of the boundaries above.
- A brain using the bridge user's filesystem or process permissions on
  Linux or with the sandbox turned off, where no sandbox boundary is
  promised, and the other limits README lists under *What it does not do*.
  Authentication, API authorization and chat routing remain in scope on
  every supported platform.
- Flaws in Claude Code, Codex CLI, whatsmeow, WhatsApp or another dependency:
  report those to their makers, and tell us if Fridolin has to change because of
  one.

## What to expect

Fridolin is a small project, looked after in spare time. You can expect:

- an answer to your report within a week;
- within 30 days, an assessment: a plan for a fix, or why it is not one;
- the fix on `main` and in a release, with a GitHub security advisory that
  credits you, unless you would rather it did not;
- coordinated disclosure: please keep the details to yourself until the
  advisory is published, or for 90 days after your report, whichever comes
  first.

For questions about privacy that are not about a flaw, see
[PRIVACY.md](PRIVACY.md).
