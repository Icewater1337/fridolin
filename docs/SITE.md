# Your own site

Fridolin can serve a website of your own: people sign in with Google or a
username and password, and see a page for each part of the site they may
open. Every site has two of its own, **Files** and **Wiki**: your brains'
folders and wikis, for the brains each person's role may see
([FILES.md](FILES.md)). Every other part is a **module** — a folder in a
private repository of yours, with a `module.json` that says where it lives
and who may open it. Nothing in that repository reaches the site until you
approve it in the dashboard.

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
the bridge's environment, config or dashboard token. On macOS it runs in a
sandbox built from the modules' manifests: module code reaches the files,
programs and secrets its `module.json` declares, and nothing else ([What a
module can reach](#what-a-module-can-reach)). Accounts, roles, the domain and
which commit is live are set only in Fridolin's local dashboard — nothing
reachable from the internet can change them.

## From zero to your domain

Dashboard → **Site**, from top to bottom; the page has the same steps.
You need a domain whose DNS is with Cloudflare (a free account does), and
on this machine **Node.js 22 or newer** (`brew install node`) and
**cloudflared** (`brew install cloudflared`; the core's image has both).
Fridolin unpacks the site process into `<data_dir>/site/shell` and installs
its dependencies on first start.

1. **Yourself.** Under *People*, add your Google address with the role
   `admin`. Admins always sign in with Google, so set up Google sign-in too
   (step 8 of [Adding modules](#adding-modules) explains the client; the
   page shows the redirect URI to add).
2. **Turn the site on** (the switch at the top). It serves Files and Wiki
   at once, with no repository: try it on this machine at the address the
   page shows (`http://127.0.0.1:8780` by default).
3. **The domain.** Under *Your domain*, enter the host name, e.g.
   `example.com`, and save. The site is then served as
   `https://example.com`.
4. **A Cloudflare tunnel** gives the domain to this machine without opening
   a port:
   1. If the domain's DNS is not with Cloudflare yet, [add the
      domain](https://dash.cloudflare.com/) there and change its name
      servers where you bought it.
   2. In [Cloudflare Zero Trust](https://one.dash.cloudflare.com/) →
      **Networks** → **Tunnels** → **Create a tunnel**, type
      **Cloudflared**, give it a name (e.g. `fridolin`).
   3. On **Install and run a connector**, copy the token: the long text
      after `--token` in any of the commands shown. Install nothing;
      Fridolin runs the tunnel. Paste it under *Cloudflare tunnel* and
      save. It is stored in `<data_dir>/site/tunnel.token`, never shown
      again.
   4. Next, add a **public host name**: your domain, no path, service
      **HTTP**, URL `127.0.0.1:8780` (the site's *Listen on*). Cloudflare
      adds the DNS record. You may add `wiki.example.com` the same way: it
      leads to `example.com/wiki`.
   5. Turn on **Run the tunnel**. Fridolin runs `cloudflared` while the
      site is on, and the page shows its state and output. In a core
      container it runs inside the container and moves with the core
      ([CORE.md](CORE.md#the-site)).
5. **Check that it answers**: *Check https://example.com* asks
   `https://example.com/_fridolin/health` through the internet, as anyone
   would, and says what is wrong if it does not answer: the name does not
   resolve yet, the tunnel does not reach the site, or another server
   answers.
6. **Files and Wiki.** Under *Files and Wiki*, give each role the brains it
   may see; `admin` sees all. Add people under *People* with those roles.
   What is never shown, and how every view is audited, is in
   [FILES.md](FILES.md).
7. **More pages**, optionally: modules from a repository of yours (below).
   A starter module to copy is in
   [`examples/site-module/`](../examples/site-module/).

## Adding modules

Dashboard → **Site** → *Modules from your repository*. In order:

1. **Node.js 22 or newer**, as above.
2. **Repository.** A private repository on GitHub (or any git host). With the
   GitHub CLI (`gh`) logged in, *Create a private GitHub repository* makes one
   and fills in its address. Enter the SSH address
   (`git@github.com:you/my-site.git`), the branch, and — if the modules do not
   sit at the top — the folder that holds `modules/`. *Create a deploy key*
   makes a key just for this repository; add it with **write access** before
   connecting (*Add it to GitHub with gh* adds it to the address you entered,
   or use the repository's Settings → Deploy keys). Then *Connect*.
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
   into its own folder, runs `npm install` (in every folder that holds a
   module's `package.json`, see "Where the `package.json` goes" below) and every
   module's build, and only then switches the site to it.
6. **The site** listens on `127.0.0.1:8780` by default — on this machine
   only. A core in a container publishes that port on the host's loopback
   ([CORE.md](CORE.md#the-site)).
7. **Domain and tunnel**: as in [From zero to your
   domain](#from-zero-to-your-domain).
8. **Google sign-in.** The site uses Fridolin's Google client (Settings →
   Google sign-in) or one of its own. Google lets a website sign people in
   only with a **Web application** client: if yours is a Desktop app client,
   create a Web application client and paste it under *Use a separate Google
   client for the site*. Either way, add the redirect URI the page shows
   (`https://example.com/auth/callback`) to the client's *Authorized redirect
   URIs* in Google Cloud Console. Saving or removing the site's own client
   restarts the site; a change to Fridolin's client reaches it without one.

### Bringing password accounts from another app

An account from an app that kept bcrypt hashes (Node's `bcrypt` or
`bcryptjs`, Python's `bcrypt`, PHP, `htpasswd -B`) can move over with its
password: its owner signs in as before, and nobody has to know or retype it.
The dashboard's API takes the hash instead of a password when adding the
account:

```bash
curl -s -X POST http://127.0.0.1:8765/api/site/users \
  -H "X-Bridge-Token: $(cat ~/.fridolin/dashboard.token)" -H "X-Bridge-Dashboard: 1" \
  -H "Content-Type: application/json" \
  -d '{"kind":"password","username":"sam","name":"Sam","role":"member","password_hash":"$2b$12$…"}'
```

The hash must be `$2a$`, `$2b$` or `$2y$` with a cost from 10 to 14. The
10-character minimum applies to passwords set in Fridolin, not to a password
brought over this way. Setting a new password later replaces the hash with
Fridolin's own.

## Approving changes

For every module that changes, the dashboard lists the fields of
`module.json` that decide what the module can do, old value and new:

| Field | Why it matters |
| --- | --- |
| `route` | where it is served |
| `access` | who may open it |
| `server`, `server.mount`, `server.access` | code that runs on this machine, where it answers, and who may call it |
| `server.public` | paths of that code anyone can call without signing in |
| `capabilities.files`, `.commands`, `.secrets` | what that code touches — on macOS, all it can touch |
| `capabilities.ingest` | brains it may hand files to |
| `capabilities.search` | brains whose folder it may search with a read-only Claude agent |
| `capabilities.openai`, `capabilities.elevenlabs` | the calls and models it may ask of OpenAI or ElevenLabs, and its monthly limit: the bridge makes them with Fridolin's key, at Fridolin's cost |
| `app.dist` | the files it serves |
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
| `route` | Where the module is served: one path segment, e.g. `/finance`. `/auth`, `/api`, `/files` and `/wiki` belong to the site, and no two modules share a route. |
| `access.roles` | Roles that may open the module, listed explicitly; `"*"` means any signed-in user. Required. |
| `access.emails` | Optional. Also require one of these addresses, **signed in with Google** (a password account's "email" is only its username and never matches). |
| `app.dist` | Folder with the built `index.html`: relative to the module folder, absolute, or `~/…`. |
| `app.base` | `"built"`: the app was built for its route (Vite `base: '/finance/'`). `"inject"`: the app uses relative asset paths and gets `<base href="<route>/">`. |
| `app.public` | Files in `dist` served without sign-in (a PWA's `manifest.webmanifest`, `sw.js`). Plain file names. |
| `app.build` | How the deploy builds `dist`: a folder (`cwd`, same path rules) and a command as a list. Leave it out for an app whose `dist` is committed. |
| `server.entry` | A file in the module folder whose default export returns an Express router. |
| `server.mount` | Where the router answers. Default `<route>/api`; must be under the route or `/api/<id>`. |
| `server.access` | Who may call it. Default: `access`. |
| `server.public` | Paths under `server.mount` that answer **without sign-in**, e.g. `["/intake"]`: that path and everything below it. Letters, digits, `.`, `_`, `-` and `/`; no wildcards. See *Public paths*. |
| `capabilities.files` | Named paths the server code uses, `{ "path", "access": "read" \| "write" }`, same path rules. A path ending in `/` (or one that is a folder) covers everything below it. |
| `capabilities.commands` | Programs the server code runs: a name on the site's `PATH`, an absolute or `~/…` path, or a path in the module folder. |
| `capabilities.secrets` | Secrets it needs, by name: a letter, then up to 63 letters, digits, `_` or `-`. |
| `capabilities.ingest` | Brains it may hand files to with `ctx.ingest`, by id (see *Handing files to a brain*). |
| `capabilities.search` | Brains whose folder it may search with `ctx.search`, by id (see *Searching a brain's folder*). |
| `capabilities.openai` | What it may ask of OpenAI with `ctx.openai`, through Fridolin's key: `{ "use", "models"?, "monthly_usd"? }` (see *OpenAI and ElevenLabs*). |
| `capabilities.elevenlabs` | The same for ElevenLabs, with `ctx.elevenlabs`. |

A module needs `app`, `server` or both. Everything is behind sign-in and the
module's `access`, except the files in `app.public` and the paths in
`server.public`.

Server code gets its paths through `ctx.file('db')` and its secrets through
`ctx.secret('API_KEY')`, which refuse a name the manifest does not list. On
macOS the sandbox holds the code to `capabilities` as well, whatever it opens
or runs; on Linux, which has no sandbox, they are the module's account of what
it touches, for whoever approves it ([What a module can
reach](#what-a-module-can-reach)).

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

`ctx` has `id`, `dir` (the module folder), `file(name)`, `secret(name)`,
`dataDir`, `ingest(...)`, `search(...)`, `openai`, `elevenlabs` and
`log(...)`. `dataDir` is a folder of the module's
own that survives deploys (`<data_dir>/site/data/<id>` on the live site, one in
the builder's workspace in a preview), for logs, state and uploads on their way
to a brain: nothing needs declaring for it.
`req.user` is the signed-in account: `{ id, email, name, role, authType }`
(for a password account, `email` is the username). TypeScript runs as is. The
site repository's `package.json` must say `"type": "module"` and list what
modules import (`express`, …); the deploy runs `npm install`.

### Public paths

Some pages must reach people without an account — a form a patient fills in
from a link in an email, a webhook. `server.public` lists the paths of the
module's server that answer without sign-in:

```json
"server": { "entry": "server.ts", "public": ["/intake"] }
```

A request to `<mount>/intake` or below goes to the router without the
sign-in check and without `req.user`; whatever the router does not answer
there falls through to sign-in as usual. The router decides what such a
request may do — a one-time token in the link, a signature from the sender —
and parses its own bodies with a size limit (`express.json({ limit: '64kb' })`).
The site allows 120 requests a minute per client address and module on public
paths and answers `429` above that. The deploy review lists `server.public`
for every module that adds or changes it.

The page that calls them is a public file (`app.public`), e.g. one
self-contained `intake.html` with its script and styles inline, since the
app's other files stay behind sign-in.

**Where the `package.json` goes.** Node looks for a package in
`node_modules` beside the file that imports it and in every folder above, so
a module uses the `package.json` nearest above its `module.json`: beside
`modules/` (`dashboard/package.json` for modules in `dashboard/modules`), at
the repository's root, or in the module's own folder. The deploy runs `npm
install` in each folder that is the nearest one for some module, and in the
site's folder when it has one (`npm ci` where there is a
`package-lock.json`); a builder's workspace gets the same installs, as the
brain, and a module that brings a `package.json` later gets its own before a
check or preview. A folder with a lock file is installed once per lock file:
`<data_dir>/site/npm-cache` keeps the last few installs by the hash of the
lock file, `package.json`, node and the platform, and a deploy whose
dependencies did not change copies them from there. When the site starts on
a deploy whose `node_modules` are gone (a data dir moved to another machine
leaves them out, [CORE.md](CORE.md)), it installs them first. A module that
imports a package it cannot find names the `package.json` Node went by, and
says when that one has no `node_modules` beside it.

A module that fails to load — an invalid manifest, server code that throws —
is skipped: the site's log says why, admins see it on the home page, and the
rest of the site keeps running.

## What a module can reach

On macOS the live site runs under `sandbox-exec`, with a profile Fridolin
builds from the live commit's manifests whenever it starts the site. Every
file that is not allowed is denied, so the list you review is also the limit.

**Files.** Module code reads what `capabilities.files` declares and writes what
is declared `"write"`; nothing else. A path is one file, or a folder with
everything below it when it ends in `/` or is a folder already. A file declared
`"write"` includes SQLite's `-journal`, `-wal` and `-shm` files next to it; to
replace a file by renaming a new one over it, declare its folder. When a
`"write"` folder (or the folder of a `"write"` file) does not exist yet,
Fridolin creates it as the site starts. The declared folder itself cannot be
renamed, removed or swapped for a symlink, nor a declared file replaced by one.
Allowed paths follow no symlinks of yours: declare the real path. `app.dist` is
readable too, since the site serves it.

**What the site itself needs** is readable, and no more: the system's libraries
and programs, Node.js, the software in Homebrew's folders (not its `var/` or
`etc/`), the site process (`shell/`), the live deploy and the files Fridolin
writes for it. The only other places it writes are its temp folder (`tmp/`, its
`TMPDIR`), the modules' data folders (`data/<id>`, `ctx.dataDir`) and the
handoff folder to brains (`spool/`, `ctx.ingest`).

**Programs.** Only node, the programs of its dependencies (in `node_modules`,
such as esbuild), a shell (`sh`, `bash`, `zsh`, `/usr/bin/env`) and
`capabilities.commands` may run. A command is a name looked up on the site's
`PATH` (node's folder, `/opt/homebrew/bin`, `/usr/local/bin`, `/usr/bin`,
`/bin`), an absolute or `~/…` path, or a path in the module folder. It runs
under the same profile, so it reaches no more files than the module: a program
that needs files of its own beyond the system's (a script's interpreter, a
tool's data folder) needs those declared too. Xcode's stand-ins in `/usr/bin`
(`git`, `python3`, …) start the real program from the developer tools, which
the sandbox does not open; use Homebrew's. Some programs hand work to programs
outside any sandbox (`open`, `osascript`, `launchctl`): declaring one gives the
module a way out, so approve those only when you mean it.

**Network.** Module code may connect anywhere but to the bridge's own HTTP
port: the dashboard, `/send`, `/ingest` and the brains' `/brain/`. A module
has no token for them, but `/send` still takes token-free sends to your
allowed chats while
[`script_api.tokenless_send`](CONFIGURATION.md#script_apitokenless_send) is
on. With the port closed, it sends no WhatsApp messages and hands brains
work only as [Handing files to a brain](#handing-files-to-a-brain) says.

**Never**, whatever a module declares:

- Fridolin's data folder, but for the site's own files: the shell, the live
  deploy, `users.json`, `session.key`, `previews.json`, the Google client and
  `tmp/`. The modules' secrets are in that folder too, out of reach.
- The bridge's config, database and credentials folder, and the paths in
  `sandbox.deny_paths`.
- The usual secrets: `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`,
  `~/.config/gcloud`, `~/.config/rclone` and the rest of what the brains'
  sandbox keeps from them, mail, messages, calendars, browser data,
  `~/Library/Keychains`, `~/.claude`, `~/CLAUDE.md`, `~/.netrc`,
  `~/.git-credentials`, `~/.*-token` and `~/.*-api-key`.
- For writing: what runs outside the sandbox — shell startup files,
  `~/Library/LaunchAgents`, Claude Code's settings, hooks and skills, the
  bridge's binary and its install folder or checkout.

Your own folders (`Documents`, `Desktop`, `Downloads`, `Movies`, `Music`,
`Pictures`) are closed like everything else, but a module may be given a path
in them by declaring it.

**One process.** All modules run in the one site process, so the profile allows
what any of them declares. `ctx.file` and `ctx.secret` hand each module only its
own, but within one process they are no wall: server code you approve can
reach every module's declared files, the site's own `users.json` (password
hashes) and `session.key` (which signs every sign-in), and, through the
token the site reads the built-in Files and Wiki with, whatever any role may
read there ([FILES.md](FILES.md#on-your-site)). The same token reaches
OpenAI and ElevenLabs through the bridge, which goes by the module the site
names in each call: server code could name another module and use what that
one declares. Approving server code is trusting it with all of that.

**Deploys are not sandboxed.** `npm install` and the modules' `app.build`
commands run as you when you approve a commit, outside the sandbox: a
dependency's install script or a build command reaches what you can.

**Linux** has no `sandbox-exec`: there the site runs without a sandbox, and
module code reaches every file the bridge's user can. The dashboard's Site page
says which applies (*Sandbox: On* or *Off*).

### Handing files to a brain

The site cannot reach Fridolin's media folder or the bridge's `/ingest`, so a
module that takes uploads hands them over with `ctx.ingest`, for the brains its
`module.json` lists in `capabilities.ingest`:

```ts
// files the module wrote, e.g. an upload saved into ctx.dataDir
await ctx.ingest({ brain: 'office', files: [path], text: 'File this under documents/', sender: user.email })
```

The site moves the files and a request into a folder of its own
(`<data_dir>/site/spool/`); the bridge picks it up within a second, checks
that the live commit's module lists that brain, moves the files into its media
folder and hands them to the brain like a WhatsApp attachment: the brain's
answer goes to its reply chat. `ctx.ingest` resolves once the brain has taken
them, and throws for a brain the module does not list, when the brain's queue
is full, when `ingest.enabled` is off, and in previews and checks.

### Phones

The site also takes requests from phones Fridolin paired, the Plaud Sync
app ([PLAUD.md](PLAUD.md#through-your-site)): `POST /_fridolin/devices/pair`,
`POST /_fridolin/devices/plaud/transcripts`, and for
[voice mode](PLAUD.md#voice-mode) `POST /_fridolin/devices/voice/ask` and
`POST /_fridolin/devices/voice/reply`, whose answer is encrypted for the
phone and which Fridolin answers off the spool's loop, holding it up to 15
seconds until the answer is there. They need no sign-in, but
each request carries a JWT signed with a key only that phone has. The site
checks it against the public keys Fridolin writes to
`<data_dir>/site/devices.json` and passes what passes on through the spool,
where Fridolin checks it again before anything reaches a brain. A client
whose requests keep failing waits 15 minutes.

### Searching a brain's folder

A module cannot run Claude itself: the site's sandbox keeps the keychain and
`~/.claude` closed, and every module in the one site process reaches what any
of them declares. `ctx.search` has the bridge run it instead, for the brains
the module lists in `capabilities.search`:

```ts
for await (const event of ctx.search({ brain: 'office', prompt, signal })) {
  // Claude Code's stream-json events, as they come
}
```

The bridge runs a read-only agent (`--tools Read,Grep,Glob`) in the brain's
folder, with only that folder's own Claude Code setup and the bridge's login,
under **that brain's sandbox**: it reaches what the brain reaches and nothing
of another brain's folder, whatever the site process can read. It starts
Claude as a brain's runs do, with the prompt on stdin, out of process
listings, and stops whatever the agent left running when it ends. At most
four searches run at once, each for up to three minutes; aborting `signal`
stops one.

### OpenAI and ElevenLabs

OpenAI and ElevenLabs are connections of Fridolin: it holds one key for
each, set once in the dashboard (Integrations). A module never sees a key.
It declares what it calls in `module.json`, and its server code calls
`ctx.openai` and `ctx.elevenlabs`. The site passes each call to the bridge,
naming the module. The bridge checks that the live commit's `module.json`
declares the call and the model, checks the module's monthly budget, calls
the provider with the key and logs the usage. Setting the keys, what brains
can do with them and the usage per module are in
[CONNECTIONS.md](CONNECTIONS.md).

```json
"capabilities": {
  "openai": { "use": ["transcribe", "responses"], "models": ["gpt-4o-mini-transcribe", "gpt-5-mini"], "monthly_usd": 20 },
  "elevenlabs": { "use": ["tts", "voices"], "monthly_usd": 5 }
}
```

- `use` (required): the calls the module makes. OpenAI: `transcribe` and
  `responses`. ElevenLabs: `tts`, `transcribe` and `voices`.
- `models`: the models it may use, each 1 to 64 letters, digits, `.`, `_`,
  `:` or `-`. Without it, any model. A call that names no model uses the
  default, which must then be in the list too.
- `monthly_usd`: its limit per calendar month (UTC) in US dollars, above 0
  and at most 10000. Without it, Fridolin sets no limit.

```ts
import express from 'express'
import type { ModuleContext } from '../../types/fridolin-site'

export default function dictation(ctx: ModuleContext): express.Router {
  const api = express.Router()
  api.post('/transcribe', express.raw({ type: 'audio/*', limit: '25mb' }), async (req, res) => {
    try {
      const { text } = await ctx.openai.transcribe({ audio: req.body, contentType: req.get('content-type'), language: 'de' })
      res.json({ text })
    } catch (err) {
      // A ConnectionError: its status says why (see below).
      const status = (err as { status?: number }).status === 429 ? 429 : 503
      res.status(status).json({ error: (err as Error).message })
    }
  })
  return api
}
```

| Call | What it does | Answers |
| --- | --- | --- |
| `ctx.openai.transcribe({ audio, contentType?, filename?, model?, language?, prompt? })` | Transcribes a clip of up to 25 MiB. Default model `gpt-4o-mini-transcribe`. | `{ text }` |
| `ctx.openai.responses(body)` | OpenAI's Responses API, with the body as OpenAI takes it, up to 20 MiB. `model` is required; `stream: true` and `background: true` are refused. | OpenAI's JSON as it is |
| `ctx.elevenlabs.tts({ text, voice?, model?, format?, language?, voiceSettings? })` | Text to speech, up to 10,000 characters. Defaults: the voice set in the dashboard, `eleven_multilingual_v2`, `mp3_44100_128`. | `{ audio, contentType }`: a Buffer and its type, e.g. `audio/mpeg` |
| `ctx.elevenlabs.transcribe({ audio, contentType?, filename?, model?, language? })` | Speech to text, up to 25 MiB. Default model `scribe_v1`. | `{ text, language? }`, with ElevenLabs's language code as it is, which may have three letters (`deu`) |
| `ctx.elevenlabs.voices()` | The voices of the account. | `[{ id, name }]` |

`audio` is a Buffer, a Uint8Array, an ArrayBuffer or a Blob. The provider
tells the format by the file name's extension: give `contentType`
(`audio/webm`) or `filename` (`note.webm`); a File keeps its own name. Each
call takes a `signal` to abort it: in its options, or as
`responses(body, { signal })` and `voices({ signal })`.

A call that fails throws a `ConnectionError` (`err.name`), with the
bridge's `status`, and `upstreamStatus` and `code` when the provider
answered an error:

| Status | Why |
| --- | --- |
| 400 | A bad request: no model, `stream: true` or `background: true`, audio over 25 MiB, text too long, no voice. |
| 401 | The call names no module in `X-Fridolin-Module`, or has not the site's token. `ctx` always sends both. |
| 403 | `module.json` does not declare the call or the model. The site checks the call itself before it asks the bridge. |
| 404 | The module is not in the live commit. |
| 429 | The module is over its `monthly_usd` this month. |
| 502 | The provider failed or said no, e.g. `upstreamStatus` 404 and `code` `model_not_found`. |
| 503 | No key is set in the dashboard, or there is no bridge (a preview or a check). |
| 504 | The provider did not answer in time: 2 minutes for OpenAI's transcription and for speech, 3 minutes for responses and ElevenLabs's transcription, 30 seconds for the voices. |

**Budgets are estimates.** The bridge prices each call from a small table:
transcription by the minute, responses by their tokens, speech by its
characters, and a model it does not know at the dearest rate. A module at or
over its `monthly_usd` is refused until the month ends. The provider's bill
is what counts: for a hard limit, set a budget on the OpenAI project as well.

**Previews and checks** have no bridge. There every call throws "not
available in previews" (503), after the same check of `module.json`, so a
builder sees a missing declaration there too. Call them in a request
handler, so that the module still loads.

Not covered yet: streaming, OpenAI's Realtime API for live transcription,
and the providers' other endpoints. For another service, a module keeps its
own key as a secret (below).

### Secrets

For OpenAI and ElevenLabs, use the connections above. Module secrets stay for
other services. A module lists the secrets it needs by name:

```json
"capabilities": { "secrets": ["STRIPE_API_KEY"] }
```

Site → **Secrets** lists every secret the live modules declare; set the values
there. They are stored in `<data_dir>/site/secrets/<module>.json`, readable
only by you (and, under the sandbox, by no module), never shown again and
never in the repository. When the site starts, Fridolin hands it the values
each module declares through a pipe the site reads once and closes — in no
file and no environment variable module code could read. Server code asks for
one with `ctx.secret('STRIPE_API_KEY')`, which throws for a name its
`module.json` does not list and for one without a value yet. Saving or removing
a secret restarts the site. Previews and checks get no secrets.

## Old addresses

When the built-in Files and Wiki take over what modules did, or a module
moves, `modules/redirects.json` in the repository keeps old links
working:

```json
{
  "redirects": [
    { "from": "/wiki-team", "to": "/wiki/team" },
    { "from": "/files-team/api/download", "to": "/files/team" },
    { "from": "/files-team", "to": "/files/team" }
  ]
}
```

A request for `from`, or a path below it, is sent (`302`) to `to` with the
rest of its path and its query: `/wiki-team/people/alex` goes to
`/wiki/team/people/alex`. The longest `from` that matches wins. Both are
paths of this site; `/_fridolin` and `/auth` are not taken, and a
module's own route comes first. A file that does not read is reported in
the site's log, and the site runs without redirects. It comes with a
commit you approve, as modules do.

## Files

Everything lives in `<data_dir>/site/` (`~/.fridolin/site/`), readable only by you:

| Path | What |
| --- | --- |
| `settings.json` | repository, domain, roles, the approved commit and earlier deploys |
| `users.json` | accounts; password accounts as PBKDF2-SHA256 hashes, or the bcrypt hashes they were brought over with |
| `session.key` | signs the site's sign-in cookies; replacing it signs everyone out |
| `deploy_key`, `deploy_key.pub`, `known_hosts` | the repository's deploy key |
| `tunnel.token`, `google_client.json` | the Cloudflare tunnel token; the site's own Google client, if any |
| `deploys/none/` | the empty deploy the site runs from while no commit is approved: Files and Wiki alone |
| `repo/` | Fridolin's clone of the repository |
| `deploys/<commit>/` | one checkout per deployed commit (the live one and the last few) |
| `shell/` | the site process, unpacked from the Fridolin binary |
| `site.runtime.json` | what the site process is told when it starts |
| `secrets/` | the modules' secrets, one `<module>.json` each |
| `tmp/` | the site process's temp folder |
