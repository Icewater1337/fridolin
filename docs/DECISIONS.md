# Decisions

One dated line per decision taken while implementing [PLAN.md](PLAN.md), so
the next session does not decide it again. Newest last.

- 2026-10-20 — The M0 data-layout step leaves `config.Version` at 2: it only adds the sections `core`, `vault`, `nodes` and `policy`, all off by default, and renames, removes or re-means no key, so a version 2 file keeps its meaning and needs no `configMigrations` step (PLAN.md assumed one; a bump would only make every older build refuse the file). A step comes with the first setting these sections supersede.
- 2026-10-20 — The new sections are live settings (`config.Live`), not restart (`Boot`) fields, while nothing runs on them; the code that starts the tailnet, the lease or the node port marks its fields `Boot` in `internal/config/fields.go`.
- 2026-10-20 — Data-dir paths come from `config.Layout` (`Boot.Layout()`); its folders are made lazily with 0700 by their feature (`config.EnsureDir`), never at startup. Packages that may not import config (platform, plaud) are handed the path: `platform.AcquireLock` takes the lock file's path, `plaud.Options.SiteDevices` the site's devices.json.
