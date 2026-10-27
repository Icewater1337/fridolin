# GitHub

Fridolin reaches GitHub through a **GitHub App of its own**. You connect it
once from the dashboard; afterwards every run that needs GitHub gets a
token that:

- lasts at most an hour;
- covers only the repositories and permissions of that one use;
- is revoked when the run ends.

No personal access token is typed in or kept.

## Connect GitHub

Open the dashboard and go to **Integrations → GitHub → Connect GitHub**.

1. **Choose what the app may do.**
   - Tick *Let FrIDE open pull requests* if FrIDE should push and open
     pull requests (`pull_requests: write`); see [Later](#later).
   - Name an organization if it should own the app. Left empty, the app
     belongs to your own account.
2. **Continue to GitHub.** A small page of the bridge posts the app's
   *manifest* to GitHub (GitHub's [manifest
   flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest)).
   GitHub shows the new app before it makes it:
   - **Name:** `fridolin-<8 random hex>`. An app's name is unique on all
     of GitHub, so every install gets its own.
   - **Visibility:** private.
   - **Webhook:** none.
   - **Permissions:** contents read, issues write, metadata read, and
     pull requests write only when you ticked it.

   Click **Create GitHub App** there.
3. **GitHub sends the browser back** to `/github/created` on the
   dashboard, with a one-time code. The bridge exchanges the code for
   the app's id, private key and client secret
   (`POST /app-manifests/{code}/conversions`) and keeps them (see
   [Where the keys are](#where-the-keys-are)).
4. **Pick the repositories.** The bridge sends the browser straight on to
   the app's install page. Choose the repositories Fridolin may use and
   click **Install**. GitHub comes back to `/github/installed`, which
   returns to the Integrations page.

The card then shows:

- the app, where its key is kept, and each installation with its
  repositories;
- **Manage on GitHub**, **Install on more repositories** and **App
  settings**;
- **Test**: mints a token that may only read issues, for the first
  repository, reads its newest issue, and revokes the token;
- **Disconnect**.

### The local dashboard and the tailnet

GitHub never connects to the bridge. Every step back is a browser
redirect, so it works at both addresses:

- `http://127.0.0.1:8765` on the core's machine;
- `https://fridolin-core.<tailnet>.ts.net` from another device of the
  tailnet.

The app's redirect and setup addresses are those of the dashboard you
clicked Connect in. To change them later, edit *Callback URL* and *Setup
URL* in the app's settings on GitHub.

The hand-off page is the dashboard's only page with a form. It has a
policy of its own: no script at all, and `form-action https://github.com`.
The dashboard keeps `form-action 'none'`.

### The one-time state

Connect makes a state that works for one hour (as long as GitHub's code
does). It is used up when GitHub comes back.

- The hand-off page and `/github/created` need no dashboard token: the
  state is what ties them to your click.
- `/github/installed` does nothing but send the browser on. The bridge
  reads the installations from GitHub itself, never from what GitHub
  appends to the address.

## Where the keys are

The app's id, slug, owner, private key (PEM) and client secret are kept
where the bridge itself can write a secret:

| Where the bridge runs | Kept in |
| --- | --- |
| The core container | `secrets/github-app.age` in the data dir, encrypted to the core's post-quantum age identity (`FRIDOLIN_AGE_IDENTITY_PQ`), like a node target's password. It moves with `fridolin core export`, and opens on every host that has the identity, which `core send`/`receive` carry. |
| A native install | The host's secret store as `fridolin.github.app` (the keychain, the Windows Credential Manager, 0600 files on Linux), base64 of the JSON. |

They never go to `config.json`, a log, an API answer or the audit trail.
The dashboard shows where they are, never what they are.

The container cannot write the host's keychain, which `core start` only
reads from. That is why a connect made from the container's dashboard
goes into the data dir, under the identity the host already keeps.

## Tokens for runs

A run that needs GitHub carries a **grant**. Scheduled agents carry one.
In Go it is `chat.GitHubGrant` on the run's message:

```json
{"github": {"repos": ["you/fridolin"], "permissions": {"issues": "write", "contents": "read"}}}
```

At the run's start, the bridge mints an installation access token:

- from the installation on the repositories' owner;
- for exactly those repositories (`repositories`) and permissions
  (`permissions`);
- with `contents: read` and `metadata: read` when the grant names no
  permissions.

The run gets it as **gh's own login**, not as a variable:

- **Where it is:** `hosts.yml` (with a `config.yml` of version 1) in a
  folder of its own, `run-tokens/<brain>/run-*.gh` in the data dir. That
  is beside the run's brain API token, readable only by that brain (in
  the container, mounted read-only into its sandbox).
- **What the run's environment gets:** only `GH_CONFIG_DIR`, naming that
  folder, plus `GH_PROMPT_DISABLED` and `GH_NO_UPDATE_NOTIFIER`.
- **Why a file:** macOS shows a process's environment to the user's other
  processes, other brains' runs among them; a file in the brain's own
  folder stays with that brain. This is the same reason the brain API
  token is a file.
- **What works with it:**
  - `gh` as it is;
  - git through gh's credential helper: `git -c
    credential.helper='!gh auth git-credential' push`;
  - other programs, with `GH_TOKEN="$(gh auth token)"`.
- **What is not passed on:** the bridge's own variables,
  `FRIDOLIN_GITHUB_TOKEN` included.

When the run ends, the bridge revokes the token
(`DELETE /installation/token`) and removes the folder; a bridge that
stopped without doing so removes it at its next start. A token that cannot be minted leaves the
run without one, and the log says why: not connected, not installed on
that owner, or a repository the installation lacks.

All of a grant's repositories must belong to one owner: a token is of one
installation.

### A typed token (second choice)

Without a connected app, runs with a grant get a token you typed in:

- `FRIDOLIN_GITHUB_TOKEN` in the bridge's environment;
- on a host, the secret `fridolin.github.token`, which `core start`
  passes into the container:

```bash
fridolin core secrets set github.token
```

Such a token cannot be narrowed to one use, nor revoked after it. It is
only a fallback; an app is the better choice.

## Disconnect

**Disconnect** forgets the app's key here. From then on, runs get no
tokens from it.

The app stays on GitHub until you delete it there: the card links to the
app's settings → **Delete GitHub App**.

To connect again, click Connect GitHub. That makes a new app; delete the
old one on GitHub.

## FrIDE

The FrIDEs of the nodes get the app as their `github` identity
([NODES.md](NODES.md#git-identities-for-fride)): an installation token of
the app's installation on its owner (else its first one), for all its
repositories and permissions. It lasts an hour; each node asks for a new
one 25 minutes before it ends. The installation must let it write code:
set *Contents* to *Read and write* in the app's settings on GitHub, and
*Pull requests* to *Read and write* for pull requests (the app made with
*Let FrIDE open pull requests* has the second), then accept the change
on the installation. Until then FrIDE gets no GitHub token, and its
node's own git login keeps pushing. Without an app, FrIDE gets the typed
token as it is.

The GitHub card's optional commit name and email are what FrIDE commits
with in a GitHub workspace.

## Later

The same app is meant to serve this. It is not wired yet:

- **The site's repository deploy:** a `contents: read` token for the
  site's repository in place of its deploy key ([SITE.md](SITE.md)).

## Code

- `internal/ghapp` is the client: manifest, conversion, the app's JWT
  (RS256, ten minutes), installations, and minting and revoking tokens.
  `internal/ghapp/ghapptest` is a fake of GitHub for the tests, which
  never reach GitHub.
- `internal/app/github_runtime.go` holds the flow, the status and the
  runs' tokens; `github_secrets.go` holds where the keys are.
- `internal/httpapi/github.go` holds the routes and the hand-off page.
- The routes:
  - `GET /api/github`, and `POST /api/github/connect`, `/test` and
    `/disconnect` (dashboard token);
  - `GET /github/connect`, `/github/created` and `/github/installed`
    (the browser's, tied to the state).
