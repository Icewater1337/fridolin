The Plaud Sync app no longer updates itself in the background. Only when the owner opens the app does it ask Fridolin for a newer build and fetch it; the screen then shows an **Install update** button, and the tap hands the build to Android's PackageInstaller (Android asks to confirm). Background check-ins and Fetch now neither fetch nor install; no notification starts an install; `UPDATE_PACKAGES_WITHOUT_USER_ACTION` is gone.

- app: Updater/SyncService/MainActivity/AppUpdate, versionCode 15 / 0.11, UpdatePolicyTest updated
- core: `plaud_app_update` text, `plaud_app_status` wording, Plaud card text
- docs: PLAUD.md, plaud-android/README.md, CHANGELOG.md

Not run: the Android build/tests (no Gradle in the sandbox).
Deploy: core redeploy, then "Build and update phones". Phones on 0.10 or older still take this build the old way once.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
