# Your own site

Fridolin can serve a website of your own: people sign in with Google or a
username and password, and see a page for each part of the site they may
open. Every part is a **module** — a folder in a private repository of yours,
with a `module.json` that says where it lives and who may open it. Nothing in
that repository reaches the site until you approve it in the dashboard.

```
your repository (private)          Fridolin (this machine)                  the internet
┌──────────────────────┐   fetch   ┌────────────────────────────────┐       ┌──────────┐
│ modules/             │ ────────► │ dashboard → Site               │       │ browser  │
│   welcome/           │           │  approve a commit, people,     │       └────┬─────┘
│   finance/           │  push     │  domain, sign-in               │            │ https
│     module.json      │ ◄──────── │                                │       ┌────┴─────┐
│     server.ts        │ (starter) │ site process (Node) ◄──────────┼───────┤ tunnel   │
│     spa/             │           │  serves the approved commit    │       └──────────┘
└──────────────────────┘           └────────────────────────────────┘
```

The site runs as its own process. It reads only what Fridolin writes for it
(the approved checkout, the account list, its session key) and gets none of
the bridge's environment, config or dashboard token. Accounts, roles, the
domain and which commit is live are set only in Fridolin's local dashboard —
nothing reachable from the internet can change them.

## Setting it up

Dashboard → **Site**. In order:

1. **Node.js 22 or newer** must be installed (`brew install node`). Fridolin
   unpacks the site process into `<data_dir>/site/shell` and installs its
   dependencies on first start.
2. **Repository.** A private repository on GitHub (or any git host). With the
   GitHub CLI (`gh`) logged in, *Create a private GitHub repository* makes one.
   *Create a deploy key* makes a key just for this repository; add it with
   **write access** (*Add it to GitHub with gh*, or the repository's
   Settings → Deploy keys). Then *Connect* with the SSH address
   (`git@github.com:you/my-site.git`), the branch, and — if the modules do not
   sit at the top — the folder that holds `modules/`.
3. **Starter.** For an empty repository, *Add the starter files* commits a
   welcome module, a `package.json`, types for module code and a `CLAUDE.md`
   for the site's conventions, and pushes them.
4. **People.** Add yourself: your Google address, role `admin`. Admins always
   sign in with Google. Others can use Google or a username and password
   (at least 10 characters). Roles besides `admin` are yours to name
   (`member` to start with); a module lists the roles that may open it.
   Removing someone or changing their role takes effect at once.
5. **Deploy.** *Check for changes* lists the new commits and, per module, what
   an approver must look at (below). *Approve and deploy* checks the commit out
   into its own folder, runs `npm install` and every module's build, and only
   then switches the site to it.
6. **Turn the site on.** It listens on `127.0.0.1:8780` by default — on
   this machine only.
7. **Domain.** Enter the host name (e.g. `example.com`); the site is then
   served as `https://example.com`. To reach it from the internet without
   opening a port, use a [Cloudflare
   Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/):
   create a tunnel in Cloudflare Zero Trust, give it a public host name for
   your domain pointing at `http://127.0.0.1:8780`, paste the tunnel's token
   under *Cloudflare tunnel*, and turn on *Run the tunnel* (needs
   `brew install cloudflared`). Fridolin runs `cloudflared` while the site is on.
8. **Google sign-in.** The site uses Fridolin's Google client (Settings →
   Google sign-in) or one of its own. Google lets a website sign people in
   only with a **Web application** client: if yours is a Desktop app client,
   create a Web application client and paste it under *Use a separate Google
   client for the site*. Either way, add the redirect URI the page shows
   (`https://example.com/auth/callback`) to the client's *Authorized redirect
   URIs* in Google Cloud Console.

## Approving changes

For every module that changes, the dashboard lists the fields of
`module.json` that decide what the module can do, old value and new:

| Field | Why it matters |
| --- | --- |
| `route` | where it is served |
| `access` | who may open it |
| `server`, `server.mount`, `server.access` | code that runs on this machine, where it answers, and who may call it |
| `capabilities.files`, `.commands`, `.secrets` | what that code touches |
| `app.public` | files anyone can fetch without signing in |
| `app.build` | a command Fridolin runs when deploying |

Only commits on the connected branch can be approved. Earlier deploys stay
listed; *Roll back to this* makes one live again (the last few are kept built,
so a rollback is instant). A deploy that fails to build leaves the live site
as it was.

## Building with a brain

Site → **Builders** lets a brain build the site. That brain then gets, in its
own folder under `.fridolin-site/`:

- the **site-builder plugin** — skills for the workflow, the module format and
  how Fridolin runs the site (`site/builder` in this repository);
- a **workspace** — a clone of the site repository on the branch
  `builder/<brain>`;
- the **fridolin-site MCP server** (`fridolin mcp-site`) with five tools:
  `site_context` (the live site, roles, the workspace), `site_check` (loads the
  workspace's modules as the site will and lists what you will review),
  `site_preview` (runs the workspace), `site_sync` (rebases onto the newest
  branch) and `site_propose`.

Ask it for a page — on WhatsApp, or on the dashboard's **Build** page — and it
builds the module, checks it, previews it and proposes it. The Build page is a
chat with the builder brain next to its live preview; it runs in a chat of its
own (a Claude session of its own, *New conversation* starts another), with the
same sandbox, skills and tools as on WhatsApp. Proposals appear under Site →
**Proposals** with the same review list as a deploy; *Merge and deploy* merges
the branch, pushes it and deploys it. The brain cannot deploy, merge, or change
people, roles or the domain.

What keeps a builder contained:

- Everything about the workspace runs **inside the brain's sandbox**: the clone,
  `npm install`, checks and previews. Fridolin never runs git or npm on the
  workspace itself (its `.git` and `package.json` are the brain's to write), so
  history moves in and out only as git bundles.
- A **preview** knows no accounts: it gets no session key and no user list. It
  opens with its own link (the dashboard has it), or, for a signed-in admin,
  through the real domain at `/_fridolin/preview/<brain>`, where the live site
  forwards the admin's requests to it.
- Each brain's MCP server has a **token of its own**, valid only while the brain
  is a builder and only for its own workspace.
- Turning a builder off revokes its token and stops its preview; its workspace
  stays, so no unproposed work is lost.

## Modules

```
modules/
└── finance/
    ├── module.json     the manifest (required)
    ├── server.ts       server code, if the manifest has "server"
    └── spa/            the app's source; spa/dist is what gets served
```

The folder name is the module's `id`.

```json
{
  "version": 1,
  "id": "finance",
  "title": "Finance",
  "description": "Spending by month, vendors and categories",
  "icon": "💳",
  "color": "#ec4899",
  "order": 30,
  "route": "/finance",
  "access": { "roles": ["admin"], "emails": ["you@example.com"] },
  "app": {
    "dist": "spa/dist",
    "base": "built",
    "build": { "cwd": "spa", "run": ["npx", "vite", "build"] }
  },
  "server": { "entry": "server.ts" },
  "capabilities": {
    "files": {
      "db": { "path": "~/finance/card.db", "access": "write" }
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `version` | Always `1`. |
| `id` | Lowercase letters, digits and `-`. Must equal the folder name. |
| `title`, `description`, `icon`, `color` | The tile on the home page. |
| `order` | Tile position; lower comes first (default 100). |
| `route` | Where the module is served: one path segment, e.g. `/finance`. `/auth` and `/api` belong to the site, and no two modules share a route. |
| `access.roles` | Roles that may open the module, listed explicitly; `"*"` means any signed-in user. Required. |
| `access.emails` | Optional. Also require one of these addresses, **signed in with Google** (a password account's "email" is only its username and never matches). |
| `app.dist` | Folder with the built `index.html`: relative to the module folder, absolute, or `~/…`. |
| `app.base` | `"built"`: the app was built for its route (Vite `base: '/finance/'`). `"inject"`: the app uses relative asset paths and gets `<base href="<route>/">`. |
| `app.public` | Files in `dist` served without sign-in (a PWA's `manifest.webmanifest`, `sw.js`). Plain file names. |
| `app.build` | How the deploy builds `dist`: a folder (`cwd`, same path rules) and a command as a list. Leave it out for an app whose `dist` is committed. |
| `server.entry` | A file in the module folder whose default export returns an Express router. |
| `server.mount` | Where the router answers. Default `<route>/api`; must be under the route or `/api/<id>`. |
| `server.access` | Who may call it. Default: `access`. |
| `capabilities.files` | Named paths the server code uses, `{ "path", "access": "read" \| "write" }`. |
| `capabilities.commands` | Programs the server code runs. |
| `capabilities.secrets` | Secrets it needs, by name. |

A module needs `app`, `server` or both. Everything is behind sign-in and the
module's `access`; there is no public module, only public files.

`capabilities` are **declared, not enforced** yet: server code gets its paths
through `ctx.file('db')`, which refuses a name the manifest does not list, but
nothing stops it from opening other paths. They are the module's account of
what it touches, for whoever approves it.

### Server code

```ts
import express from 'express'
import type { ModuleContext } from '../../types/fridolin-site'

export default function finance(ctx: ModuleContext): express.Router {
  const db = ctx.file('db')          // from capabilities.files
  const api = express.Router()
  api.get('/hello', (req, res) => res.json({ module: ctx.id, user: (req as any).user.email }))
  return api                         // mounted at server.mount, behind sign-in + access
}
```

`ctx` has `id`, `dir` (the module folder), `file(name)` and `log(...)`.
`req.user` is the signed-in account: `{ id, email, name, role, authType }`
(for a password account, `email` is the username). TypeScript runs as is. The
site repository's `package.json` must say `"type": "module"` and list what
modules import (`express`, …); the deploy runs `npm install`.

A module that fails to load — an invalid manifest, server code that throws —
is skipped: the site's log says why, admins see it on the home page, and the
rest of the site keeps running.

## Files

Everything lives in `<data_dir>/site/` (`~/.fridolin/site/`), readable only by you:

| Path | What |
| --- | --- |
| `settings.json` | repository, domain, roles, the approved commit and earlier deploys |
| `users.json` | accounts; password accounts as PBKDF2-SHA256 hashes |
| `session.key` | signs the site's sign-in cookies; replacing it signs everyone out |
| `deploy_key`, `deploy_key.pub`, `known_hosts` | the repository's deploy key |
| `tunnel.token`, `google_client.json` | the Cloudflare tunnel token; the site's own Google client, if any |
| `repo/` | Fridolin's clone of the repository |
| `deploys/<commit>/` | one checkout per deployed commit (the live one and the last few) |
| `shell/` | the site process, unpacked from the Fridolin binary |
| `site.runtime.json` | what the site process is told when it starts |
