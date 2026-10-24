# Contributing

Issues and pull requests welcome; a security problem goes privately
through [SECURITY.md](SECURITY.md) instead. Please don't include real
phone numbers, group JIDs, or chat content in bug reports — redact them.
The issue forms ask for `fridolin version`, your OS and a redacted
`fridolin check`, and the pull request template has a short checklist.

Run `scripts/install-hooks.sh` once: it blocks commits and pushes that contain
phone numbers, WhatsApp JIDs, email addresses, home-directory paths or tokens,
plus anything in your personal denylist
(`~/.config/fridolin/personal-denylist`, one string per line —
your name, your numbers, your company). CI runs the same check.

`make check` runs what CI's build, lint and test jobs run: gofmt, `go vet`,
`go mod tidy -diff`, the race tests, staticcheck, exhaustive, shellcheck, ruff,
actionlint, a check that every workflow action is pinned to a commit SHA,
a check that `THIRD_PARTY_NOTICES` is current, ESLint and TypeScript's
checker on the dashboard,
the Python, dashboard, personal-data-check, action-pin-check, notices,
release-archive and Makefile tests, and a smoke test that starts a
race-enabled build in dev mode on a scratch config (`make help` lists every
target). staticcheck and exhaustive run through `go tool` and actionlint through `go run`, all pinned, and ESLint and TypeScript through `npx` at pinned versions, which it fetches
the first time; ruff comes from `pip install -r tools/requirements-dev.txt`,
shellcheck from your package manager. `make check` builds into a temp dir.
`make build` writes `bin/fridolin` (it builds `./cmd/fridolin`; the
repository's root has no Go files, so `go build .` there fails), and
`make install` also copies it to `~/.local/bin` (set `PREFIX` or `BINDIR`
to change that) to run by hand. Neither changes the service, which runs
its own copy in `~/.local/lib/fridolin/`: `./setup.sh` installs one.

CI also installs the site's npm packages and runs its own tests
(`cd site && npm install && npm test`; [docs/SITE.md](docs/SITE.md));
`make check` does not, and without those packages its Go tests of the site
skip. It runs [govulncheck](https://go.dev/doc/security/vuln/) on pushes to
main, every pull request and once a week; it fails when the code reaches a
known vulnerability. Run it yourself after changing dependencies:
`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`. CodeQL scans the
Go code, the dashboard JavaScript, the Python tools and the workflows on the
same schedule; its findings go to the repository's code scanning alerts.

On pushes to main, CI also fuzzes the parsers of untrusted input (WhatsApp
messages, model output, files a brain writes) for 20 seconds each;
[AGENTS.md](AGENTS.md) says how to run a fuzz target yourself, and
`make fuzz-short` runs them all for 10 seconds each.

## Development boundaries

Use a separate checkout and scratch config for development. Do not point a test
bridge at the installed service's data directory. `make build` builds the local
binary; `setup.sh` and service installation deploy and restart the service.

For an isolated smoke test, run `make smoke`. Its temporary HOME and fake provider
exercise the HTTP API without pairing WhatsApp or touching an installed service.
For a manual session, prepare the isolated config described in
[Setup](docs/SETUP.md#development-without-whatsapp).

## Changes and reviews

Keep package dependencies in the architecture allowlist. Add settings to the
persisted schema, runtime projection, field registry, schema/contract tests and
configuration documentation. A changed on-disk meaning needs a migration, not
only a renamed Go field. See [Architecture](docs/ARCHITECTURE.md) and
[Compatibility](docs/COMPATIBILITY.md).

Keep golden bytes unchanged during moves. If behavior intentionally changes,
explain the change and its regression test in the PR. Test at the owning package
boundary; use `app/apptest` for application lifetime and API integration. Existing
white-box integration fixtures live in `internal/app`.

Frontend pages use DOM construction, shared resource polling and the existing
CSS layers. Do not add inline scripts, HTML-string rendering or cross-page
imports. See [AGENTS.md](AGENTS.md) for the detailed module conventions.

