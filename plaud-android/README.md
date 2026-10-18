# Plaud Sync (Android)

A small Android app that takes a Plaud NotePin's recordings off the pin as soon as they stop,
instead of waiting until Plaud's own app is opened (on Android, Plaud's app syncs a NotePin only
while it is open).

It keeps a Bluetooth link to the pin from a foreground service. When a recording stops it
downloads it as MP3 into the phone's `Documents/PlaudSync/`, deletes it on the pin once it is
saved, has Plaud's Transcription API transcribe it and saves the transcript next to it:

```
Documents/PlaudSync/2026-10-18_14-03-12.mp3
Documents/PlaudSync/2026-10-18_14-03-12.txt   [00:00 - 00:04] Speaker 1: …
```

The name is when the recording started. Built on Plaud's Embedded SDK
([Plaud-AI/plaud-sdk-public](https://github.com/Plaud-AI/plaud-sdk-public), Apache-2.0).

## To Fridolin

Sync `Documents/PlaudSync` to a folder on the computer Fridolin runs on, for example with
[Syncthing](https://syncthing.net) (Syncthing-Fork on the phone), and set that folder as
`plaud.phone_dir`. Each new transcript then reaches a brain like a recording from Plaud's cloud.
See [docs/PLAUD.md](../docs/PLAUD.md#without-plauds-cloud-the-phone-app).

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

## Build

```bash
cd plaud-android
ANDROID_HOME=$(brew --prefix)/share/android-commandlinetools ./gradlew assembleDebug
# → app/build/outputs/apk/debug/app-debug.apk
```

The first build fetches the SDK into `app/libs/` and checks its hash. The credentials are built
into the APK, so keep the APK to yourself. Builds on the same computer are signed with the same
debug key, so a new APK installs over the old one and keeps the pairing.

## Use

1. In Plaud's app, sync once, then remove the NotePin there: a pin belongs to one app at a time.
2. Copy the APK to the phone and install it (allow installing from that source).
3. Open it, allow **Nearby devices** and notifications, tap **Pair NotePin** with the pin close
   by, then **Allow running in background**.
4. Record something. It shows up in `Documents/PlaudSync` seconds after you stop it, and the
   transcript once Plaud is done.

**Share log** sends the app's log (connections, downloads, errors; never a recording's content).
**Unpair** gives the pin back to Plaud's app; it needs the pin connected.

## Limits

- Out of range, recordings wait on the pin. The app looks for the pin again, but with the screen
  off Android may hold the search until the screen is next on, as the SDK's scan has no filter.
- Plaud's Transcription API runs in the US; other regions need Plaud's sales team. Plaud keeps a
  transcription for 7 days.
- No Plaud AI summaries: the brain that gets the transcript writes its own.
