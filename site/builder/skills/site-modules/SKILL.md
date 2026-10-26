---
name: site-modules
description: The module format of a Fridolin site — module.json fields, apps (static or built with Vite), server code (Express router with ctx.file), access rules, public files, the look of the site and working examples. Use when creating or editing anything under modules/ in a site repository.
---

# Site modules

```
<site repository>/
├── package.json          "type": "module"; dependencies modules import (express, …)
├── CLAUDE.md             this site's conventions
├── types/fridolin-site.d.ts
└── modules/
    └── <id>/             the folder name is the module's id
        ├── module.json
        ├── server.ts     optional server code
        └── app/ or spa/  the page(s)
```

## module.json

```json
{
  "version": 1,
  "id": "expenses",
  "title": "Expenses",
  "description": "This month's spending by category",
  "icon": "💳",
  "color": "#0ea5e9",
  "order": 20,
  "route": "/expenses",
  "access": { "roles": ["admin"] },
  "app": { "dist": "spa/dist", "base": "built", "build": { "cwd": "spa", "run": ["npx", "vite", "build"] } },
  "server": { "entry": "server.ts" },
  "capabilities": {
    "files": { "ledger": { "path": "~/finance/ledger.csv", "access": "read" } },
    "commands": [],
    "secrets": []
  }
}
```

| Field | Rule |
| --- | --- |
| `version` | `1` |
| `id` | lowercase letters, digits, `-`; equals the folder name |
| `title`, `description`, `icon`, `color`, `order` | the home page tile (order: lower first, default 100) |
| `route` | one segment, `/expenses`; not `/auth` or `/api`; unique |
| `access.roles` | required; roles from `site_context` (`site.roles`); `"*"` = any signed-in user |
| `access.emails` | optional; only Google-verified addresses match |
| `app.dist` | folder with `index.html` (relative to the module, absolute, or `~/…`) |
| `app.base` | `"built"` if built for the route (Vite `base: '/expenses/'`); `"inject"` for relative asset paths (`<base href="/expenses/">` is added) |
| `app.public` | plain file names served without sign-in (only a PWA's `manifest.webmanifest` / `sw.js`) |
| `app.build` | `cwd` + `run` (a list); omit when `dist` is committed |
| `server.entry` | file in the module; default export returns an Express router |
| `server.mount` | default `<route>/api`; must be under the route or `/api/<id>` |
| `server.access` | default: `access` |
| `server.public` | paths under the mount that answer without sign-in (`["/intake"]`: that path and below); the router checks a token or signature itself, `req.user` is not set; 120 requests/min per client |
| `capabilities.files` | every path the server code uses, named, with `read` or `write`; a path ending in `/` is a folder and everything in it |
| `capabilities.commands`, `.secrets` | programs it runs (a name on PATH or a path); secrets it needs, by name (`STRIPE_API_KEY`), never for OpenAI or ElevenLabs |
| `capabilities.ingest` | brains it may hand files to with `ctx.ingest` (their ids) |
| `capabilities.search` | brains whose folder it may search with `ctx.search` (their ids) |
| `capabilities.openai` | `{ "use": [...], "models"?: [...], "monthly_usd"?: N }` for `ctx.openai`; `use` from `transcribe`, `responses` |
| `capabilities.elevenlabs` | the same for `ctx.elevenlabs`; `use` from `tts`, `transcribe`, `voices` |

Everything is behind sign-in and `access`, except `app.public` files and `server.public` paths. Use those only when the person asks for something people without an account must reach (a form opened from an email link, a webhook), and say so in the proposal.

## A static page (no build)

`modules/notes/module.json`:
```json
{ "version": 1, "id": "notes", "title": "Notes", "icon": "📝", "route": "/notes",
  "access": { "roles": ["admin", "member"] }, "app": { "dist": "app", "base": "inject" } }
```
`modules/notes/app/index.html` with relative links (`style.css`, `app.js`).
Fine for small pages: plain HTML, CSS and JS. The `welcome` module of the
starter is one.

## An app built with Vite (React or plain)

`spa/vite.config.ts`:
```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
export default defineConfig({ base: '/expenses/', plugins: [react()], build: { outDir: 'dist', emptyOutDir: true } })
```
`spa/package.json` lists its own dev dependencies; the deploy runs the build
command from `app.build` (`npx vite build` in `spa`). Add `spa/dist` and
`spa/node_modules` to the module's `.gitignore` — build output is not
committed. Call the module's API with paths under the base:
`fetch(import.meta.env.BASE_URL + 'api/summary')`.

## Server code

```ts
// modules/expenses/server.ts
import express from 'express'
import { readFileSync } from 'node:fs'
import type { ModuleContext, SignedInRequest } from '../../types/fridolin-site'

export default function expenses(ctx: ModuleContext): express.Router {
  const ledger = ctx.file('ledger')       // declared in capabilities.files
  const api = express.Router()
  api.get('/summary', (req, res) => {
    const user = (req as SignedInRequest).user  // { id, email, name, role, authType }
    const rows = readFileSync(ledger, 'utf8').trim().split('\n').slice(1)
    res.json({ rows: rows.length, for: user.name })
  })
  return api                                // mounted at /expenses/api
}
```

- TypeScript runs as is (no build). ES modules only.
- `ctx.file(name)` returns a declared path and throws for any other name.
  Never hard-code paths.
- `ctx.secret(name)` returns a declared secret's value, set by the user in
  Fridolin's dashboard (Site → Secrets). It throws for an undeclared name, for
  one not set yet, and always in previews and `site_check`, which get no
  secrets: call it in a request handler and answer 503 with "not set up yet"
  when it throws, so the module still loads.
- `ctx.dataDir` is a folder of the module's own that survives deploys
  (nothing to declare): logs, state, a SQLite file, uploads on their way to a
  brain. Prefer it to declaring a folder of your own.
- `ctx.ingest({ brain, files, text, sender })` hands files the module wrote to
  a brain listed in `capabilities.ingest`, as a WhatsApp attachment would reach
  it (the brain files them and answers in its chat). It is the only way to
  give a brain work: modules cannot call Fridolin's API. It throws in previews.
- `ctx.search({ brain, prompt, signal })` runs a read-only Claude agent in the
  folder of a brain listed in `capabilities.search`, under that brain's
  sandbox, and yields its stream-json events. Modules cannot start Claude
  themselves. It throws in previews.
- OpenAI and ElevenLabs go through Fridolin, which holds the keys: never
  declare an `OPENAI_API_KEY` or ElevenLabs secret, and never call their APIs
  with `fetch`. Declare the calls in `capabilities.openai` /
  `.elevenlabs` (`use`; `models` to pin the models; `monthly_usd` as the
  module's monthly limit, an estimate) and call:
  ```ts
  const { text } = await ctx.openai.transcribe({ audio: buf, contentType: 'audio/webm', language: 'de', prompt })
  const out = await ctx.openai.responses({ model: 'gpt-5-mini', input })   // OpenAI's JSON; no stream
  const { audio, contentType } = await ctx.elevenlabs.tts({ text, voice })  // a Buffer, e.g. audio/mpeg
  const { text: said, language } = await ctx.elevenlabs.transcribe({ audio: buf, contentType: 'audio/wav' })
  const voices = await ctx.elevenlabs.voices()                              // [{ id, name }]
  ```
  `audio` is a Buffer, Uint8Array, ArrayBuffer or Blob, up to 25 MiB; give
  `contentType` or `filename` so the provider knows the format. A call
  throws a `ConnectionError` (`err.name`) with `status`: 403 not declared,
  429 over the monthly limit, 502 the provider said no (`upstreamStatus`,
  `code`), 503 no key set, and always in previews and `site_check`. Call it
  in a request handler and answer with an error when it throws, so the
  module still loads. In the proposal, say what the module calls and why
  its `monthly_usd` is what it is.
- On the live site (macOS) the code runs in a sandbox built from
  `capabilities`: it reads and writes only the declared paths (a file declared
  `write` includes SQLite's `-journal`/`-wal`/`-shm`; declare the folder to
  replace a file by renaming), runs only declared programs, and cannot reach
  Fridolin's local API. Write data outside the repository, e.g.
  `~/site-data/<id>/` (Fridolin creates a declared folder that is missing).
- Anything the code imports must be in the repository's `package.json`
  (`npm install <pkg>` in the repository root, then commit `package.json` and
  the lock file). Node built-ins (`node:fs`, `node:sqlite`, …) need nothing.
- `ctx.log(...)` writes to the site's log.
- Validate input, return JSON errors with a status, never echo a stack trace.
- A module that throws while loading is skipped — `site_check` shows why.

## Look and feel

The site's own pages use a light/dark design with the system font. Match it:

```css
:root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
body { margin: 0; background: Canvas; color: CanvasText; }
main { max-width: 960px; margin: 0 auto; padding: 32px 16px 64px; }
```

- Start each page with a link back to the home page: `<a href="/">← All pages</a>`.
- Works on a phone first: no fixed widths, tables scroll sideways inside
  their own box, tap targets at least 40px.
- Light and dark both readable; no colours that only work on white.
- Real headings, labels on inputs, alt text on images.
- Follow the site's `CLAUDE.md` where it says more.
