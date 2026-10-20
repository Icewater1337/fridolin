# Decisions

One dated line per decision taken while implementing [PLAN.md](PLAN.md), so
the next session does not decide it again. Newest last.

- 2026-10-20 — Secrets for the core live in the host's macOS Keychain, account `fridolin`: `fridolin.tailscale.authkey`, `fridolin.b2.vault.key`/`.keyid`, `fridolin.b2.snapshots.key`/`.keyid`. `fridolin core start` (M1) reads them from there.
- 2026-10-20 — Tailscale: the policy file owns `tag:fridolin-core` and `tag:fridolin-node` (`autogroup:admin`). The core's auth key is reusable, not ephemeral, tagged `tag:fridolin-core`, 90 days; the node key waits for M2.
- 2026-10-20 — B2: buckets `fridolin-vault` and `fridolin-snapshots` in eu-central-003 (endpoint `s3.eu-central-003.backblazeb2.com`), private, SSE-B2, no object lock; both keep prior versions 30 days. One application key per bucket, restricted to it.
- 2026-10-20 — SQLite driver is `modernc.org/sqlite`. The DSN keeps the cgo driver's behaviour: `foreign_keys(1)`, `busy_timeout(5000)` (mattn's default; modernc has none) and `_time_format=sqlite` (mattn's text format, so old and new rows compare). whatsmeow's dialect name stays `"sqlite3"`.
- 2026-10-20 — The config version is `config.Version` in `internal/config/config.go` (PLAN.md calls it `currentConfigVersion`); the M0 data-layout step raises it to 3.
