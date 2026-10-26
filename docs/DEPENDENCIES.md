# Dependency updates

Renovate proposes the updates; a dependency agent merges them once a day.
Nobody has to look at a dependency PR unless the agent says it could not
merge it.

## Renovate

The Mend Renovate GitHub app runs Renovate on this repository with
`.github/renovate.json5`. It sees:

| What | Where |
| --- | --- |
| Go modules | `go.mod` (not its `go` directive, which stays at oldstable) |
| Actions, pinned by SHA | `.github/workflows/*.yml` |
| The Python tools | `tools/requirements*.txt` |
| The site's npm packages | `site/package.json`, `site/template/package.json` |
| Base images | `container/Dockerfile`, `container/fride/Dockerfile` |
| Versions pinned by hand | the `ARG …_VERSION` lines of the Dockerfiles and `…_VERSION :=` of the Makefile, each under a `# renovate: datasource=… depName=…` comment |
| The Plaud phone app | `plaud-android/` (Gradle) |

A pin by hand without that comment is invisible to Renovate: give a new one
the comment. `container/python/requirements.lock` is not Renovate's:
`make container-python-lock` regenerates it from `requirements.in`.

How it proposes:

- A release must be **7 days old** first (`minimumReleaseAge`), which keeps
  a hijacked release that is yanked within days out. Security fixes come at
  once, labelled `security`.
- Minor and patch releases come **grouped**, one PR per ecosystem ("Go
  modules", "GitHub Actions", "container images and tools", …). A **major**
  release comes on its own.
- PRs open before 6am (Europe/Zurich), all labelled `dependencies`.
- The **Dependency Dashboard** issue lists every update, open PR and held
  one; ticking a box there makes Renovate act on it.

Holds that are deliberate live in `renovate.json5` with the reason
(`tailscale.com` below v1.104, the core image's Go at 1.26). Drop one when
its reason is gone.

## The daily dependency agent

A Claude Code routine runs every morning at 07:30 (Europe/Zurich) in the
cloud environment "fridolin", with this file as its instructions. What it
does, in order:

### 1. Look at Renovate itself

- An open issue titled like "Action Required: Fix Renovate Configuration"
  means Renovate stopped: fix `.github/renovate.json5` in a PR of its own
  (check it with `npx --yes --package renovate -- renovate-config-validator
  --strict`), merge it, and go on.
- Note the Dependency Dashboard's "errored" or "rate-limited" entries in
  the report at the end.

### 2. List the open Renovate PRs

```bash
gh pr list --author app/renovate --state open \
  --json number,title,headRefName,headRefOid,labels,mergeable,isDraft
```

Work through them one at a time, each merged before the next starts, since
one merge can change what the next needs (go.sum, a Dockerfile). Order:
`security` first, then GitHub Actions, Go modules, Python tools, site npm
packages, Makefile tools, container images and tools, the Plaud Android app,
and the major updates last.

Skip a PR labelled `dependencies-blocked` unless something changed since the
agent's comment there: its head commit is not the one the comment names
(`<!-- dependency-agent: blocked head=SHA -->`), or the reason it gives is
gone (Go's oldstable moved on, an upstream fix was released). Then remove
the label and try again.

### 3. Bring the PR up to date

When it is behind main or conflicts, merge `origin/main` into its branch
and push (or, while nobody else has pushed to it, tick the rebase box in
its description and wait for Renovate).

### 4. Wait for CI, merge when it is green

The checks that must pass are every check except those on macOS and
Windows (`go (macos-latest, …)`, `go-windows`, `python (macos-latest, …)`,
`scripts (macos-latest)`, `site (macos-latest)`). These run later and
the agent does not wait for them. A PR touching `container/` also has the
image builds, which must pass. Then:

```bash
gh pr merge NUMBER --merge --delete-branch
```

### 5. When a check fails: make the changes it needs

Check out the branch, find the cause and fix it on the branch, as a
commit of its own that says what changed and why. The usual ones:

- **A tool version in a Dockerfile**: Renovate moved `…_VERSION`, but not
  the SHA-256s beside it, so the image build fails. Take the new
  checksums from the release's own checksum file (`SHA256SUMS`,
  `checksums.txt`, the release notes), for both amd64 and arm64. Download
  the file and compare it when the release publishes no checksums. Never
  take a checksum from anywhere but the release.
- **An API change** (a major Go module, `typescript`, `express`): adapt
  the code. For a Go major, Renovate already rewrote the import paths.
- **Golden files** (`cmd/fridolin/testdata/golden`): rewrite with `go test
  -run 'TestName' -update ./internal/app` only when the change in what the
  bridge says is the update's and harmless. Say so in the commit.
- **`THIRD_PARTY_NOTICES`**: `make notices` after a Go module change.
- **`tools/requirements-transcription.txt`**: carry the change over to
  `tools/transcribe.py`'s script block, which `ScriptMetadataTest` holds
  to it. Also run `make container-python-lock` when the image's ranges
  moved.
- **The Plaud Android app**: no CI job builds it. Build it (`cd
  plaud-android && ./gradlew assembleDebug`) before merging. Without an
  Android SDK, that is a reason to stop (step 6).

Test locally what the change touches (`go vet ./... && go test -race
./...`, `make check` for anything broad), push and go back to step 4. Read
the release notes of a major update before merging it, even when CI is
green. A change people who run Fridolin will notice goes into
`CHANGELOG.md`.

Never make it pass by weakening something: no skipped or deleted tests, no
`--no-verify`, no lower `minimumReleaseAge`, no turned-off sandbox or
check, no push to main.

### 6. When it cannot be merged: say why on the PR

Stop when an update cannot be merged, even with changes. For example:

- it needs a decision (it changes behaviour on purpose, drops something
  Fridolin uses);
- it needs a newer Go than go.mod's oldstable;
- an upstream bug breaks it and no fixed release exists yet;
- the fix would be a redesign rather than an adaptation;
- it cannot be tested here (the Android app without an SDK);
- the release looks wrong (a new maintainer, unexpected install scripts,
  checksums that do not match).

Then push nothing half done. Instead:

1. Comment on the PR: what fails (the check and the error), what was
   tried, what it would take, and when it is worth trying again. End it
   with `<!-- dependency-agent: blocked head=HEAD_SHA -->`.
2. Add the label `dependencies-blocked`. Leave the PR open.

When only one member of a group is the problem, take that one back out of
the group's branch (its old version, in a commit that says so), merge the
rest and comment on the PR which update was held back and why. Renovate
then opens a PR of its own for it, which gets the same comment and label
on the next run.

### 7. Afterwards

Look at main's CI once after the last merge. When a merge broke it, fix
forward with a PR, or revert the merge when the fix is not quick. End
with a short report: merged (with the fixes made), blocked (with the
reason), skipped, and what Renovate itself said.
