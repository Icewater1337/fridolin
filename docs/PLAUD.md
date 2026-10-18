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

   Fridolin reads what this tool prints, and was tested with version
   0.3.15. The Plaud card shows the installed version, and warns when it is
   older or a newer 0.x, which may print things differently; then
   `npm install -g @plaud-ai/cli@0.3.15` installs the tested one.

2. Dashboard → **Integrations → Plaud → Connect Plaud**. Plaud's sign-in opens
   in your browser; approve access.
3. Turn on **Send new recordings to a brain** and pick the brain (by default
   the one that handles your direct messages).

The first check only remembers the recordings that already exist; from then
on, each new one is sent until its brain has answered it. If that first
check fails (Plaud is unreachable, or the login has already expired), the
next one starts over, so recordings from before you connected are still not
sent. Signing in
with a different Plaud account starts over the same way. Signing in again
with the same account, after the login expired say, picks up where it left
off: what you recorded in between still arrives. Turning **Send new
recordings to a brain** off and on again does the same: nothing is checked
while it is off, so what you recorded meanwhile is sent once it is back on.
Each check reads Plaud's list back to the last recording it knows (up to
2,000 recordings back). Each recording is a brain run of its own, and at
most five are with the brain at once, waiting or running (fewer when
`queue.max_pending` is below 10: half of it, at least one). So a backlog
arrives oldest first, as the brain gets through it, and never fills the
brain's queue, which then would turn your own messages away.

## What happens

Every minute (`plaud.interval_seconds`) Fridolin asks Plaud for recent
recordings. A new one waits until its transcript is ready: Fridolin asks
about it at the next check, then less and less often, up to every 15
minutes. If fetching it fails, it is tried again the same way, and the
Plaud card shows the error meanwhile. After three days without a transcript,
or of failing, Fridolin gives up on it: the card lists it with the reason,
and the private chat summaries go to (see below) gets one notice. A
recording deleted in Plaud meanwhile is given up at once, without a notice.
Once it is ready, Fridolin
writes a `.txt` with the details, Plaud's summary and the timestamped
transcript to `~/.fridolin/media/plaud/` (as
`YYYY-MM-DD - <name> - <Plaud ID>.txt`, by the day it was recorded; a
recording handed over again rewrites its file rather than adding a copy),
and gives it to the brain like a WhatsApp attachment. The message with it
starts with a fixed line, `[Plaud] New recording "<name>" (<when>,
<duration>)…`, so skills can recognise it, and then gives an instruction:
`plaud.message` (`{name}`, `{when}` and `{duration}` are filled in), or a
built-in one that says to follow the brain's `skills/plaud.md`. How
recordings are filed is up to that skill, not to Fridolin. It runs in the
brain you picked, with that brain's memory and history.

A recording counts as handed over once the brain's run has answered it.
When the brain's queue is full, it waits for the next check. A run that
fails is repeated after 5 and then 10 minutes; after the third the
recording is given up (on the card, with one notice). A run you stop with
`!stop` or from the dashboard is not repeated: the card offers **Send
again**, as it does for recordings given up. When Fridolin restarts while
a recording waits for its run or is running, it is handed over again
after the restart, so a brain may now and then get the same recording
twice; its file is rewritten, not copied.

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

The transcript sits in the media inbox (`<media_dir>/plaud/`, by default
`~/.fridolin/media/plaud/`) until the brain files it; under the sandbox every
brain may read the inbox. **Disconnect** signs out and stops the checks; the
transcripts already written, the brains' filed copies and the list of
recordings Fridolin has seen (`~/.fridolin/plaud/state.json`) stay, so
connecting the same account again sends nothing twice.

If Plaud's login expires, that private chat gets one notice a day until you
connect again (or disconnect); without one, only the dashboard shows it.
Plaud's tool deletes a login once it has expired. Fridolin remembers that it
signed in, so the card says **Login expired** rather than *Not connected*.
A login that goes because you disconnected, or changed where it lives (see
below), while a check ran gets no notice: it did not expire.

## Where the login lives

Plaud's tool stores its login in `<home>/.plaud/tokens.json`. By default
Fridolin runs it with a private home in `~/.fridolin/plaud/home/`, which the
sandbox denies to brains, and denies brains `~/.plaud/` as well. It runs the
tool in that home folder, never in its own working folder (the tool would
read a `.env` there), and stops it, with anything it started, when a check
is cancelled. Fridolin runs the tool itself outside the sandbox, so when it
is installed in your home folder (with nvm, for example) its whole npm
package and the `node` that runs it are read-only to brains. Unless
`cli_path` is set, Fridolin takes the tool from the newest nvm node version
that has it, so brains cannot write anywhere in `~/.nvm/versions/node`, and
they cannot add a `plaud` or `node` to a `PATH` folder in your home folder
either. One in
`/opt/homebrew` or `/usr/local` is not protected yet; see
[what the sandbox does not cover yet](CONFIGURATION.md#what-the-sandbox-does-not-cover-yet).

**Let brains use the Plaud CLI** (`plaud.brain_access`) keeps the login in
`~/.plaud/` instead, so brains can run `plaud search`, `plaud transcript` and
so on themselves. Their `PATH` then starts with the tool's folder, as
Fridolin's own runs do, so they find it, and the `node` it was installed
with, also where the service's `PATH` does not have them. A brain could
then also read the token — turn it on only if you want brains to look
things up in Plaud directly.

Changing it does not move a login. Fridolin stops checking until you
connect again, and then uses the login the tool has in its new home: your
terminal's `plaud` may already be signed in to `~/.plaud/`, maybe with
another account, and Fridolin does not take that over unasked. Connecting
with another account starts over, as above. Fridolin runs its own Plaud
commands one at a time, since the tool rewrites its login without a lock
when it refreshes it; a brain running the tool at the same moment can
still collide with it, which at worst signs Plaud out (the card then says
the login expired). With it on, **Disconnect** signs out the login in
`~/.plaud/`, which your terminal's `plaud` uses too. If a login is still
there after signing out, Disconnect says so instead of reporting success.
Either way Fridolin uses no Plaud login after **Disconnect** until you
connect again: not one still there, nor one your terminal's `plaud` makes
in `~/.plaud/` meanwhile.

Fridolin runs Plaud's tool with its usage statistics turned off
(`DO_NOT_TRACK`), and with only a few variables of its own environment,
such as the locale and the proxy settings, and while it signs you in also
your desktop session's, so that it can open your browser
([details](CONFIGURATION.md#what-reaches-brains)). Brains' runs get
`PLAUD_TELEMETRY_DISABLED=1` and `PLAUD_NO_UPDATE_NOTIFIER=1`, so a brain
that runs the tool itself does not report to Plaud's analytics or check npm
for updates either; `plaud` in your own terminal follows your own settings.

## Settings

| `plaud.` | Default | |
| --- | --- | --- |
| `enabled` | `false` | check for new recordings and hand them over; **Check now** needs it on |
| `brain` | the DM brain, else the default brain | brain id that receives recordings |
| `interval_seconds` | `60` | at least 30 |
| `polished` | `false` | use Plaud's AI-cleaned transcript; when Plaud has none an hour after the recording showed up, the raw one is sent, with a note saying so |
| `message` | follow `skills/plaud.md` | instruction sent with each recording, after the fixed first line |
| `brain_access` | `false` | share the login with brains (see above) |
| `cli_path` | found on `PATH`, else in the newest nvm node version, else in `/opt/homebrew/bin` or `/usr/local/bin` | path to `plaud` |
