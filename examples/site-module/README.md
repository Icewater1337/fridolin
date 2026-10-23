# A starter site module

A module for your site ([docs/SITE.md](../../docs/SITE.md)): a page with a
small API, to copy into the repository your site is deployed from and make
your own.

```
modules/notes/          the folder's name is the module's id
├── module.json         where it lives, who may open it, what it touches
├── server.ts           its API: an Express router
└── app/                its page: plain HTML, CSS and JavaScript
    ├── index.html
    ├── app.js
    └── style.css
```

1. Copy this folder into your site repository as `modules/notes/` (or
   another name: then change `id` in `module.json` to match, and `route`).
   The repository's `package.json` must say `"type": "module"` and list
   `express`; the starter files the Site page adds have both.
2. Change `title`, `route` and `access.roles` to suit. `"*"` lets anyone
   signed in open it; `/files` and `/wiki` are the site's own.
3. Commit and push, then on the dashboard's Site page: *Check for
   changes*, look at what the review lists for the module, and *Approve
   and deploy*.

What it shows:

- `server.ts` answers `GET /notes/api/notes` and `POST /notes/api/notes`
  for signed-in people with the module's `access`, and keeps the notes in
  `ctx.dataDir`, a folder of the module's own that survives deploys, so it
  declares no `capabilities` at all.
- `app/` is served at `/notes/` with `"base": "inject"`, so it links its
  files relatively. The site's policy allows no inline script or style:
  keep them in files, as here.
- `req.user` is the signed-in account (`email`, `name`, `role`).

To read a file of your own, run a program or use an API key, declare it
under `capabilities` and reach it through `ctx.file(name)` or
`ctx.secret(name)`; on macOS the site's sandbox holds module code to what
`module.json` declares (docs/SITE.md, "What a module can reach").
