# Plaud Sync (Android)

A small Android app that takes a Plaud NotePin's recordings off the pin in the background,
instead of waiting until Plaud's own app is opened (on Android, Plaud's app syncs a NotePin only
while it is open).

From a foreground service it checks in with the pin over Bluetooth (see [Sync and the pin's
battery](#sync-and-the-pins-battery)). For each new recording it downloads it as MP3 into the phone's `Documents/PlaudSync/`, deletes it on the pin once it is
saved, has Plaud's Transcription API transcribe it and saves the transcript next to it:

```
Documents/PlaudSync/2026-10-18_14-03-12.mp3
Documents/PlaudSync/2026-10-18_14-03-12.txt   [00:00 - 00:04] Speaker 1: …
```

The name is when the recording started. Built on Plaud's Embedded SDK
([Plaud-AI/plaud-sdk-public](https://github.com/Plaud-AI/plaud-sdk-public), Apache-2.0).

## To Fridolin

Two ways, both in [docs/PLAUD.md](../docs/PLAUD.md#without-plauds-cloud-the-phone-app):

- **Through your site** (`plaud.phone_upload`): Fridolin's dashboard builds the app for you, with
  your site's address and a one-time pairing code (Integrations → Plaud → Build phone app). On
  its first start the app makes a key pair in the phone's keystore, pairs the public key with
  Fridolin through the site, and from then on uploads each transcript signed with that key
  (`Fridolin.kt`). The private key never leaves the phone.
- **Through a synced folder** (`plaud.phone_dir`): build the app yourself as below and sync
  `Documents/PlaudSync` to a folder on the computer Fridolin runs on, for example with
  [Syncthing](https://syncthing.net) (Syncthing-Fork on the phone).

## Voice mode

Built by Fridolin, with voice mode on in the dashboard, the app's **Voice mode** switch lets you
ask Fridolin by voice and hear the answer: a NotePin recording, or **Ask** (the phone's speech
recognizer), becomes a question, and the answer, encrypted to a key in the phone's keystore, is
read aloud with Android's on-device text-to-speech (`Voice.kt`, behind the small `SpeechVoice` interface so a better voice can replace it; `SpeechText.kt`, `VoiceCrypto.kt`). See
[docs/PLAUD.md](../docs/PLAUD.md#voice-mode).

## Sync and the pin's battery

A NotePin cannot sleep while it is connected, so a permanent link runs its battery down fast.
The **Sync** setting on the app's screen picks the trade-off:

- **Smart** (default): connects every 10 minutes, and when you open the app, plug the phone in
  or unlock it, or the pin comes back in range. It fetches what is new and disconnects. While the
  pin records it stays connected, so the end of the recording is seen at once.
- **Every N minutes** (2–240): only on that timer and when you open the app. The pin sleeps
  longest.
- **Instant**: stays connected, as up to version 0.3. Recordings arrive seconds after they stop,
  and the pin's battery runs down fast.

**Fetch now**, on the screen and in the notification, checks in at once. With voice mode on,
smart and every N minutes check in at least every 2 minutes. A pin out of reach is tried less
and less often (the interval doubling up to an hour; in instant mode 30 seconds up to 10
minutes), and a low-power scan for its address notices it coming back.

Why not event-driven: the pin's advertisement, as Plaud's SDK reads it, says who it is but not
whether it records or holds files. Recording events arrive only over a connection. The SDK
gives the app no say over the connection interval either. The app tests whether this pin's raw
advertisement changes with recordings anyway, and uses that if it does. The findings are in
[docs/PLAUD.md](../docs/PLAUD.md#the-pins-battery). The scheduling and backoff rules are in
`SyncPolicy.kt`, and the Android side is in `SyncService.kt` and `PinWatch.kt`.

## What you need

- A Plaud developer app at [portal.plaud.ai](https://portal.plaud.ai) (free tier: one device,
  300 hours of transcription). Put its credentials in `~/.plaud/plaud-secrets`:

  ```
  client_id=…
  client_secret=…
  api_key=…        # App Settings → API Keys; without it there are no transcripts
  ```

- JDK 17 or newer, and the Android SDK:

  ```bash
  brew install --cask android-commandlinetools
  sdkmanager "platforms;android-35" "build-tools;35.0.0"
  ```

## Build and test

Fridolin's dashboard runs this build itself for the site upload. By hand (for the synced
folder, or to work on the app):

```bash
cd plaud-android
ANDROID_HOME=$(brew --prefix)/share/android-commandlinetools ./gradlew assembleDebug
# → app/build/outputs/apk/debug/app-debug.apk
ANDROID_HOME=$(brew --prefix)/share/android-commandlinetools ./gradlew testDebugUnitTest
```

The unit tests run on the JVM: when to check in with the pin and how to back off
(`SyncPolicyTest`), the text made for speech, and opening an answer the bridge
sealed (the vector of `TestVoiceSealVector` in `internal/plaud/voice_test.go`).

The first build fetches the SDK into `app/libs/` and checks its hash. The credentials are built
into the APK, so keep the APK to yourself. Fridolin's build passes `-PfridolinConfig=<file>` (the
developer keys, the site's address and the pairing code) and `-PfridolinBuildDir=<folder>`
instead. Builds on the same computer are signed with the same
debug key, so a new APK installs over the old one and keeps the pairing.

## Use

1. In Plaud's app, sync once, then remove the NotePin there: a pin belongs to one app at a time.
2. Copy the APK to the phone and install it (allow installing from that source).
3. Open it, allow **Nearby devices** and notifications, tap **Pair NotePin** with the pin close
   by, then **Allow running in background**.
4. Record something. It shows up in `Documents/PlaudSync` at the next check-in (seconds after
   you stop it in instant mode, or tap **Fetch now**), and the transcript once Plaud is done. Built by Fridolin, the app then sends the transcript on; its
   screen says whether it is paired and how many transcripts wait to be sent (offline, say).

**Share log** sends the app's log (connections, downloads, errors; never a recording's content).
**Unpair** gives the pin back to Plaud's app; it connects to the pin first, so keep it close by.

## Limits

- Out of range, recordings wait on the pin until it is back. A check-in connects straight to
  the pin as last found and the app's own scan is filtered, so both work with the screen off.
  The SDK's unfiltered scan, which the app uses after pairing and now and then to find the pin
  afresh, may wait until the screen is next on.
- The check-in timer is an Android alarm that Doze lets through inexactly, so on a phone left
  still for a long time the 10 minutes can stretch.
- Plaud's Transcription API runs in the US; other regions need Plaud's sales team. Plaud keeps a
  transcription for 7 days.
- No Plaud AI summaries: the brain that gets the transcript writes its own.
