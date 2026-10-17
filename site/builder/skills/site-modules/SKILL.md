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
| `capabilities.files` | every path the server code uses, named, with `read` or `write` |
| `capabilities.commands`, `.secrets` | programs it runs; secrets it needs, by name |

Everything is behind sign-in and `access`. There are no public pages.

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
