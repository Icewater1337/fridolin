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

## Without Plaud's cloud: the phone app

Plaud's own app syncs a NotePin only while it is open, so a recording can
take hours to reach Plaud's cloud, and Fridolin with it. The Plaud Sync app
in [`plaud-android/`](../plaud-android/README.md) replaces it on an Android
phone: it checks in with the pin in the background (every 10 minutes and on
events, or permanently connected: see [The pin's battery](#the-pins-battery)),
pulls every recording off it, has Plaud transcribe it, and saves
`YYYY-MM-DD_HH-MM-SS.mp3` and `.txt` in the phone's `Documents/PlaudSync`. Recordings no longer go to
your Plaud account then, and Plaud makes no AI summary of them. The
transcripts reach Fridolin through your site, or through a folder you sync.

### Through your site

With [your site](SITE.md) running under a domain, the app uploads each
transcript there as soon as Plaud has made it, and Fridolin builds the app
for you:

1. Dashboard → **Integrations → Plaud**: turn on **Recordings come from the
   Plaud Sync phone app, through your site** (or, before Plaud is
   connected, **Use the Plaud Sync phone app instead**), pick the brain and
   save.
2. Enter your Plaud developer app's **client ID**, **client secret** and
   **API key** ([portal.plaud.ai](https://portal.plaud.ai) → App Settings;
   the API key is under API Keys). Fridolin keeps them in
   `<data_dir>/plaud/developer.json` and never shows them again.
3. **Build phone app**. Fridolin builds `plaud-android/` of the checkout it
   was installed from with Gradle, which needs a JDK 17 or newer and the
   Android SDK on this computer (`brew install openjdk@21`, `brew install
   --cask android-commandlinetools`, then `sdkmanager "platforms;android-35"
   "build-tools;35.0.0"`). It finds them through `JAVA_HOME` and
   `ANDROID_HOME` in the service's environment, else where Homebrew and
   Android Studio put them; of several JDKs it takes the newest that the
   app's Gradle runs on (up to 23). The first build downloads Gradle and
   Plaud's SDK and takes a few minutes.

   Where this computer has neither, as the fridolin-core container
   ([CORE.md](CORE.md)) has not, a [node](NODES.md#android-build) that
   has them builds it: the card sends it the app's source (the image keeps
   it in `/opt/fridolin/plaud-android`), the site's address, a new pairing
   code and the developer keys, and gets the APK back. The card says which
   node builds it, shows the build's progress, and, when no node can, how
   to add one. `fridolin doctor` checks that something can build the app
   (`plaud-app-build`) while `phone_upload` is on; the app already on a
   phone keeps working either way.
4. **Download app (APK)**, copy it to the phone and install it. Remove the
   NotePin from Plaud's app, then tap **Pair NotePin** in Plaud Sync.

The app carries your site's address and a pairing code that works once,
within 24 hours; build again for another phone, or after one was unpaired.
On its first start the app makes a key pair in the phone's keystore, whose
private key never leaves the phone, and pairs: it sends the public key and
the code, signed with that key, to `https://<domain>/_fridolin/devices/pair`.
Each upload then goes to `/_fridolin/devices/plaud/transcripts` with a JWT
(ES256) the phone signed, which names the recording and the transcript's
SHA-256, so it is good for that one transcript and for ten minutes at most.
The site checks the signature to turn junk away, and Fridolin checks it
again, since module code runs in the site's process; it keeps only the
phones' public keys. A transcript that passes goes into
`<data_dir>/plaud/inbox/` and from there to the brain at once, as described
below. A phone offline meanwhile keeps its transcripts and sends them later.

The card lists the paired phones; **Unpair** refuses a phone's uploads from
then on. The developer keys are inside the app too, so keep the APK to
yourself; the key a phone pairs with is not.

### Voice mode

With uploads through the site, the app can also be a voice line to
Fridolin: you ask by voice and the phone reads the answer aloud. The answer
still reaches WhatsApp unless you turn that off.

1. Dashboard → **Integrations → Plaud**: turn on **Voice mode** (`voice`),
   pick the **Brain for voice questions** (default: the brain that gets your
   DMs) and save. Build the app again and install it over the old one: it
   keeps its pairing.
2. In the app, turn on **Voice mode**. Its notification says so, and offers
   **Ask**, **Stop** and **Skip**.

While it is on:

- **A NotePin recording is a question.** The app checks in with the pin
  at least every 2 minutes then, and **Fetch now** in the app or in the
  notification gets a question off the pin at once
  ([The pin's battery](#the-pins-battery)). It is transcribed by Plaud as
  usual and saved to `Documents/PlaudSync`, but it is sent as a question
  instead of a transcript: the speaker labels and timestamps are dropped,
  and it is not handed to the Plaud brain as a recording.
- **Ask** (in the app and in the notification) opens the phone's own speech
  recognizer, which needs no microphone permission for the app.
- The question goes to the voice brain as a hand-off in its private chat,
  as recordings do: never a group, and never a command. The brain is told
  to answer for the ear: one to three short spoken sentences, the most
  important point first, no lists, tables, links, code or emoji, numbers
  and times as they are said, in the language of the question. When the
  answer needs more, the brain writes the spoken part in a
  `[SPOKEN]…[/SPOKEN]` block and the details after it, saying the details
  are in WhatsApp. Only the block is read aloud, and the details go to the
  chat without it. An answer that comes back long anyway is read only up
  to the last whole sentence within 60 words.
- **The question itself never goes to WhatsApp.** The brain is told to
  keep it with its notes, as a voice question with the day's date, the way
  its `skills/plaud.md` files recordings (without that skill, wherever its
  notes go), and not to mention that in its answer. WhatsApp gets the
  answer in full, unless `voice_quiet` is on. Then nothing goes there, and
  only the spoken part reaches the phone.
- The phone reads the answer with Android's text-to-speech, with markdown
  stripped, links read as "a link to example.com" and polls read as a list.
  It picks German or English for each answer, where the phone has that
  voice. Music ducks meanwhile. A call stops it, and it goes on from the
  same sentence after the call. **Stop** silences it and drops what waits;
  **Skip** goes on to the next answer.
- **On the lock screen** an answer waits until the phone is unlocked. The
  notification then says only *Answer ready, unlock to hear it*, never the
  text. The app's **Speak answers while the phone is locked** allows
  speaking anyway.

How it works: the app sends the question to
`/_fridolin/devices/voice/ask`, signed like a transcript (aud
`fridolin:voice`). The JWT names the question's id and SHA-256, and the
public half of a second keystore key, which can only agree keys. Fridolin
seals the answer for that key: ECDH P-256 with a fresh key, HKDF-SHA256,
AES-256-GCM, with the question's id as associated data. The app fetches it
from `/_fridolin/devices/voice/reply` with a JWT naming the question (aud
`fridolin:voice-reply`). The site holds that request up to 15 seconds
until the answer is there, so an answer is spoken within a second or two of
being ready. The site, where module code runs, passes on ciphertext only.
Only the phone that asked can fetch the answer, and only its keystore opens
it. Answers wait in Fridolin's memory for 30 minutes at most, never on disk:
after a restart the app hears that the question is unknown, and the answer
is in WhatsApp. A question sent again with the same id, after its answer got
lost, asks nothing new. A phone offline keeps its questions and sends them
later.

### Through a synced folder

To hand them to a brain without the site, sync that folder to a folder on
this computer —
[Syncthing](https://syncthing.net) does it directly, without a cloud
(Syncthing-Fork on the phone, `brew install syncthing` here) — and set
that folder in `config.json`:

```json
"plaud": { "enabled": true, "phone_dir": "~/PlaudSync" }
```

then `fridolin service restart`. Plaud's CLI and its login are not used
while `phone_dir` is set; the card says **Phone folder** instead of who is
signed in. Everything else is as below: the first check only remembers the
transcripts already there, each new one goes to the brain as a
`[Plaud] New recording "Phone recording" (<when>, <length>)…` message with
the transcript attached, and the card lists what was handed over or given
up. A transcript is in the folder only once Plaud finished it, so it is
handed over at the next check. Only the app's own file names count: a sync
tool's temporary and conflict files are left alone, and so are the MP3s.
Fridolin only reads the folder; delete old recordings on the phone.

### The pin's battery

A NotePin connected to the phone cannot sleep. Up to version 0.3 the app
kept the Bluetooth link up all the time, and the pin's battery ran out
fast. Plaud's own app connects only while it is open. The app's **Sync**
setting now chooses:

| Sync | Connects | A recording reaches the phone | Pin battery |
| --- | --- | --- | --- |
| **Smart** (default) | every 10 min, and when the app is opened, the phone is plugged in or unlocked (at most every 5 min), or the pin comes back in range | at the next check-in: usually minutes, at most 10 | sleeps between check-ins |
| **Every N minutes** (2–240, default 30) | every N min, and when the app is opened | within N minutes | sleeps longest |
| **Instant** | always | seconds after it stops | runs down fast, as before |

A check-in connects, fetches what is new, deletes it on the pin, and
disconnects a few seconds later. If the pin is recording at that moment,
the link stays until the recording stops, so its end is seen at once: the
pin is awake while it records anyway. **Fetch now**, in the app and in its
notification, checks in at once in any mode. With voice mode on, smart
and every N minutes check in at least every 2 minutes. **Unpair** connects
to the pin first, so it needs the pin close by, not already connected.

A check-in that misses the pin (out of range, phone in another room) waits
longer each time. In smart and every-N-minutes modes the wait starts at
the interval and doubles up to an hour (never shorter than the interval;
with voice mode on, up to 10 minutes). In instant mode it starts at 30
seconds and doubles up to 10 minutes. Meanwhile, after a miss that did
not see the pin at all, a low-power scan notices it coming back and checks
in then. The timer is an Android alarm that Doze lets through
(inexactly: on a phone left still for a long time, Android may stretch
the 10 minutes).

**What the pin and Plaud's SDK offer**, which this design follows (SDK
commit `d532d5a`, read from its public README, its template app and the
AAR's classes):

- **No "recording stopped" or "new file" signal without a connection.**
  The SDK decodes from the pin's advertisement only who it is: the serial
  number, the model code, the firmware version and whether it is bound.
  Recording start and stop, and the file list, arrive only as callbacks
  over a live, handshaken link. Plaud's background wake-up, an iBeacon the
  pin sends while disconnected, is documented for iOS and the Note Pro
  only. The Android SDK has no API for it.
- **No control of the connection interval.** The SDK connects with
  `connectGatt(autoConnect=false, TRANSPORT_LE)`, asks for a larger MTU,
  and never calls `requestConnectionPriority`. It does not hand out its
  `BluetoothGatt`, so the app cannot ask for
  `CONNECTION_PRIORITY_LOW_POWER`. Keeping the link at low power (the last
  option) is therefore not available, and instant mode is the old link
  unchanged.
- **The SDK's scan has no filter** (`SCAN_MODE_BALANCED`, empty filter
  list). Android pauses such scans while the screen is off, so the app
  uses it only to pair. Version 0.4 connected straight to the pin's last
  known address instead. On the owner's phone that failed all night
  until the app was opened, so the pin's address changes, or a saved
  device record does not connect.
- **The app's own scan is filtered** by the Bluetooth company id in
  Plaud's advertisement (what the SDK calls the manufacturer code), not
  by an address. It turns each sighting into the SDK's device record with
  the SDK's own parser (the pinned AAR's `com.plaud.sdk.proto.u4.a`,
  which the SDK's scan calls too), and picks the pin by its serial number.
  A check-in looks with it for up to 20 seconds
  (`SCAN_MODE_LOW_LATENCY`) and connects to the address the pin uses
  right then. The log says when that address differs from the last one.
  Between check-ins it runs at `SCAN_MODE_LOW_POWER`, batched every 30
  seconds where the chip can. Android lets it run with the screen off,
  and it costs the pin nothing, since the pin advertises anyway. The app
  runs it after a miss, to see the pin come back, and in smart mode
  between check-ins, to see whether
  the pin's raw advertisement (its manufacturer data, beyond the fields
  the SDK reads) changes when a recording starts or stops. Plaud does
  not document that, so the app finds out. A change starts a check-in
  (at most one a minute). The check-in counts as a hit when it finds a
  new recording or one in progress. After three tries, changes count
  only while at least half of them were hits. Otherwise the app stops
  using them for that pin, until a pin is paired again. The log says
  which bytes changed and how it went, so **Share log** tells whether
  this pin has such a signal.

**The pin's battery** is asked each time a recording is saved off the pin
(`getChargingState`). The pin also reports changes while connected. The
app logs `NotePin battery N%` and shows the last reading on its screen,
so the log is a history of the battery against the recordings.

What to watch after switching: the pin's battery over a day or two, and
in the app's log how long recordings waited on the pin (`N new
recording(s) on device` against when they were made).

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
transcript to the `plaud` folder of that brain's media inbox,
`~/.fridolin/media/<brain id>/plaud/` (as
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

The transcript sits in the picked brain's media inbox
(`<media_dir>/<brain id>/plaud/`, by default
`~/.fridolin/media/<brain id>/plaud/`) until the brain files it; under the
sandbox no other brain can read it. Older versions wrote every brain's
transcripts to `media/plaud/`. Before the first inbox, the bridge moves
that folder to `media/.old/plaud/`, where no brain can read it, not even
one called `plaud`, whose inbox `media/plaud/` would otherwise have been,
and `/ingest` takes nothing from it. **Disconnect** signs out and stops
the checks; the transcripts already written, the brains' filed copies and
the list of recordings Fridolin has seen (`~/.fridolin/plaud/state.json`)
stay, so connecting the same account again sends nothing twice.

If Plaud's login expires, that private chat gets one notice a day until you
connect again or disconnect (the card offers both); without one, only the
dashboard shows it.
Plaud's tool deletes a login once it has expired. Fridolin remembers that it
signed in, so the card says **Login expired** rather than *Not connected*.
It says so too while Plaud refuses a login the tool still has (revoked on
Plaud's side). **Connect again** then signs that login out first, since the
tool's sign-in would otherwise just keep it ("Already logged in"). A
sign-in that still finds that login, because the sign-out could not
remove it (Plaud unreachable, say), fails, and so does one whose new login
Plaud refuses as well; the card and the notices then stay as they were.
A first sign-in whose login Plaud refuses right away fails too, and the
card shows **Login expired**.
A login that goes because you disconnected, or changed where it lives (see
below), while a check ran gets no notice: it did not expire.

## Where the login lives

Plaud's tool stores its login in `<home>/.plaud/tokens.json`. By default
Fridolin runs it with a private home in `~/.fridolin/plaud/home/`, which the
sandbox denies to brains, and denies brains `~/.plaud/` as well. It runs
the tool in that home folder, never in its own working folder (the tool
would read a `.env` there), and stops it, with anything it started, when a
check is cancelled. The tool's `#!/usr/bin/env node` finds `node` on a
`PATH` of Fridolin's: the tool's own folder first when that holds a
`node` (as with nvm), then only the folders of the service's `PATH` that
Fridolin takes its helpers from (see
[CONFIGURATION.md](CONFIGURATION.md#helper-programs)), and last the tool's
folder when it holds none. Fridolin runs the tool itself outside the
sandbox, but brains can write nothing outside their own places (see
[the sandbox](CONFIGURATION.md#writing)), so they can neither change the
tool, its npm package or the `node` that runs it, wherever they are
installed, nor add a newer node version to nvm or a `plaud` or `node` to a
`PATH` folder, where Fridolin would find it next.

**Let brains use the Plaud CLI** (`plaud.brain_access`) keeps the login in
`~/.plaud/` instead, so brains can run `plaud search`, `plaud transcript` and
so on themselves. Their `PATH` then has the tool's folder as well as the
service's whole `PATH`, so they find the tool also where the service's
`PATH` does not have it: first when the folder holds the `node` the tool
was installed with (as with nvm), so that they get that one, and
otherwise last, so that nothing in it runs in place of a program on the
service's `PATH`, such as `claude` for another brain's run. A brain could
then also read the token — turn it on only if you want brains to look
things up in Plaud directly. A brain's `plaud` also refreshes the login
when it is about to expire and saves it, as yours does: the sandbox lets
brains rewrite `~/.plaud/tokens.json` in place and set its mode, and
nothing else in `~/.plaud/`. Without that, a brain's command failed as not
logged in once the login had expired, and a refresh token Plaud replaced
was lost, which signed Plaud out. Brains cannot remove, rename or replace
the file: one that put a symlink in its place would have had Fridolin's
own `plaud`, which runs outside the sandbox, write your next sign-in
wherever the link points. Older versions' sandbox did not stop that, so
Fridolin also checks before each run of the tool: if `tokens.json` is a
symlink or has a second name (a hard link), or `~/.plaud` is a symlink, it
does not run it (a sign-out aside, which removes only the link), and the
Plaud card says to remove the link and connect again. A
brain's `plaud logout` still revokes the login at Plaud, as anything
holding the token can, though the file stays: Fridolin's next check then
finds the login refused.

Changing it does not move a login. Fridolin stops checking until you
connect again (a check that is running ends there, and the recordings it
has not handed over wait), and then uses the login the tool has in its new
home: your terminal's `plaud` may already be signed in to `~/.plaud/`,
maybe with another account, and Fridolin does not take that over unasked.
Connecting with another account starts over, as above. **Disconnect**
meanwhile signs out only the login in Fridolin's data folder: whatever
`~/.plaud/` holds stays for your terminal. Fridolin runs its own Plaud
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
| `phone_dir` | unset | a folder the phone app's files are synced to; recordings come from there instead of Plaud's cloud (see above) |
| `phone_upload` | `false` | recordings come from the phone app, which uploads them through the site (see above); they go into `phone_dir` when that is set, else `<data_dir>/plaud/inbox` |
| `voice` | `false` | voice mode for the phone app (see [Voice mode](#voice-mode)); needs `phone_upload` |
| `voice_brain` | the DM brain, else the default brain | brain id that answers voice questions |
| `voice_quiet` | `false` | answers to voice questions go to the phone only, not also to WhatsApp |
| `cli_path` | found in the folders of `PATH` Fridolin takes its helpers from (see [CONFIGURATION.md](CONFIGURATION.md#helper-programs)), else in the newest nvm node version, else in `/opt/homebrew/bin` or `/usr/local/bin` | path to `plaud` |
