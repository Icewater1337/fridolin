# Decisions

One dated line per decision taken while implementing [PLAN.md](PLAN.md), so
the next session does not decide it again.

- 2026-10-20 — `fridolin doctor` (M0): checks are `DoctorCheck` values in `app.DoctorRegistry`, run against a `DoctorEnv` of injected lookups (LookPath, Run, Stat, ReadFile, FreeBytes, WriteProbe), so tests never touch the system; statuses are ok / missing / warn / fail, and only a `fail` of a check with `Required` set makes it exit 1. Required today: config loads, data dir writable, free disk (fails under 2 GB, warns under 20 GB), Claude CLI and the sandboxed brains' login (Codex CLI in Codex mode). Every other check carries the milestone that needs it (`Needed`) and is flipped to required in that milestone's pull request. Host secrets are checked for presence only (`security find-generic-password` without `-w`, exit 44 = missing; a 0600 file under `$XDG_CONFIG_HOME/fridolin/secrets/<service>` off macOS); doctor switches to `hostsecrets.Store.Has` once that package lands. The login check also finds `CLAUDE_CODE_OAUTH_TOKEN` in the launchd agent or systemd unit, by name only, since doctor runs in a shell whose environment is not the service's.
