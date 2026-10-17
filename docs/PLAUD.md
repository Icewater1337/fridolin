# Plaud recorder

If you record conversations with a [Plaud](https://www.plaud.ai) recorder,
Fridolin can hand every new recording to a brain as soon as Plaud has
transcribed it — with Plaud's AI summary — so the brain files it, keeps what
matters and tells you the action items.

## Setup

1. Install Plaud's official command-line tool (needs Node.js):

   ```bash
   npm install -g @plaud-ai/cli
   ```

2. Dashboard → **Integrations → Plaud → Connect Plaud**. Plaud's sign-in opens
   in your browser; approve access.
3. Turn on **Send new recordings to a brain** and pick the brain (by default
   the one that handles your direct messages).

The first check only remembers the recordings that already exist; from then
on, each new one is sent once. If that first check fails (Plaud is
unreachable, or the login has already expired), the next one starts over,
so recordings from before you connected are still not sent. Signing in
with a different Plaud account starts over the same way. Signing in again
with the same account, after the login expired say, picks up where it left
off: what you recorded in between still arrives (up to the latest 20).
Turning **Send new recordings to a brain** off and on again does the same:
nothing is checked while it is off, so what you recorded meanwhile is sent
once it is back on (up to the latest 20).

## What happens

Every minute (`plaud.interval_seconds`) Fridolin asks Plaud for recent
recordings. A new one waits until its transcript is ready (up to three days);
if fetching it fails, the next check tries again and the Plaud card shows
the error meanwhile. (A recording deleted in Plaud before its transcript was
ready keeps failing like this until those three days are up.) Then Fridolin
writes a `.txt` with the details, Plaud's summary and the timestamped
transcript to `~/.fridolin/media/plaud/` (as
`DD.MM.YYYY - <name> - <Plaud ID>.txt`; a recording handed over again
rewrites its file rather than adding a copy), and gives it to the brain
like a WhatsApp attachment, with an instruction (`plaud.message`; `{name}`,
`{when}` and `{duration}` are filled in). It runs in the brain you picked, with
that brain's memory and history.

Recordings are private, so the brain's answer never goes to a group: it goes
to the brain's `reply_chat` when that is a direct chat, otherwise to your own
DM (your first allowed number) — also for a brain that serves a group. When
that chat belongs to another brain (your DM, for a brain that is not the DM
brain), what you write back there goes to that other brain, although `!stop`
or `!reset` there still stops the picked brain's work on a recording. Without
any private chat (no allowed number and no direct `reply_chat`) recordings are not
handed over: they wait, and the dashboard says what to set. The same goes
for a picked brain that was removed or is not approved: the card names it,
and recordings wait until you pick another one. The Plaud card shows where
summaries go, and warns when that chat belongs to another brain.

New brains get a `skills/plaud.md` that tells them how to file recordings.

If Plaud's login expires, that private chat gets one notice a day until you
connect again; without one, only the dashboard shows it.

## Where the login lives

Plaud's tool stores its login in `<home>/.plaud/tokens.json`. By default
Fridolin runs it with a private home in `~/.fridolin/plaud/home/`, which the
sandbox denies to brains, and denies brains `~/.plaud/` as well. Fridolin
runs the tool itself outside the sandbox, so when it is installed in your
home folder (with nvm, for example) its whole npm package and the `node`
that runs it are read-only to brains. Unless `cli_path` is set, Fridolin
takes the tool from whichever nvm node version sorts last, so brains cannot
write anywhere in `~/.nvm/versions/node`, and they cannot add a `plaud` or
`node` to a `PATH` folder in your home folder either. One in
`/opt/homebrew` or `/usr/local` is not protected yet; see
[what the sandbox does not cover yet](CONFIGURATION.md#what-the-sandbox-does-not-cover-yet).

**Let brains use the Plaud CLI** (`plaud.brain_access`) keeps the login in
`~/.plaud/` instead, so brains can run `plaud search`, `plaud transcript` and
so on themselves. A brain could then also read the token — turn it on only if
you want brains to look things up in Plaud directly.

Fridolin runs Plaud's tool with its usage statistics turned off
(`DO_NOT_TRACK`), and with only a few variables of its own environment,
such as the locale and the proxy settings, and while it signs you in also
your desktop session's, so that it can open your browser
([details](CONFIGURATION.md#what-reaches-brains)).

## Settings

| `plaud.` | Default | |
| --- | --- | --- |
| `enabled` | `false` | check for new recordings and hand them over; **Check now** needs it on |
| `brain` | DM brain | brain id that receives recordings |
| `interval_seconds` | `60` | at least 30 |
| `polished` | `false` | use Plaud's AI-cleaned transcript; when Plaud has none an hour after the recording showed up, the raw one is sent, with a note saying so |
| `message` | built in | instruction sent with each recording |
| `brain_access` | `false` | share the login with brains (see above) |
| `cli_path` | found on PATH | path to `plaud` |
