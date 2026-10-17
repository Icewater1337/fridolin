---
name: site-builder
description: Build or change a page, app or feature of the user's own website (the site Fridolin serves on their domain). Use whenever they ask for something on "the site", "the website", "the dashboard on <domain>", a new page or module, or a change to one. Covers the workflow from site_context to site_propose.
---

# Building the site

The user's website is served by Fridolin. Every page is a **module**: a folder
in the site repository with a `module.json`. You work in your own workspace (a
clone of that repository on your own branch) and **propose** your work. You
cannot deploy: the user reviews and deploys proposals in Fridolin's dashboard
(Site → Proposals). That is by design; do not look for a way around it.

Read the `site-modules` skill for the module format and the
`fridolin-site-platform` skill for how Fridolin runs the site, before your first
change.

## Workflow

1. **`site_context`** first. It tells you the site's name, domain and roles,
   which modules are live (and who may open them), where your workspace is,
   your preview and any proposal you already have open.
2. **Agree on the change** with the user when the request is open-ended: what
   the page shows, who may open it (which roles), where its data comes from.
   One question with a sensible default beats a long interview. Keep it short —
   they are probably on their phone.
3. **Work in the workspace** (`workspace.path`). Only there: the live site and
   Fridolin's own folders are out of reach, and that is intended.
   - One module per feature. Change an existing module only when asked to.
   - Read the site's `CLAUDE.md` (in the repository root) for its conventions
     and follow them; add a line there when the user tells you a lasting
     preference ("all pages in German", "use our green").
   - Match the look of the site (see `site-modules` → Look and feel).
4. **`site_check`** after every change. Fix everything it reports as broken.
   Read its `review` list: it is exactly what the user will be asked to
   approve. Keep it as small as the feature allows — no `"*"` access when a
   role will do, no server code for a static page, no files or commands the
   module does not use.
5. **Build** apps that need it yourself (`npx vite build` in the module's
   `spa/`), then **`site_preview`** and look at the result: fetch the local
   link's pages with curl, or open them if you have a browser. Fix what is
   wrong before the user sees it.
6. **Tell the user** what you built and send the **public** preview link if
   there is one (it works for admins signed in on the real domain), else say
   the preview is in the dashboard. Ask whether to propose it.
7. **Commit** (`git add -A && git commit -m "Add the expenses page"` — a short
   sentence saying what it does), then **`site_propose`** with a one-line title
   and a summary that names the modules and anything the approver must look at
   (new access, files, server code). Tell the user it is waiting in the
   dashboard under Site → Proposals.

If `site_context` or a failed proposal says the branch moved on, run
**`site_sync`** (rebases your commits) and check again.

## Rules

- **Never put secrets in the repository** — no API keys, tokens or passwords,
  not even "for now". A module that needs one lists its name in
  `capabilities.secrets` and you tell the user it must be set up.
- **Declare honestly** in `capabilities` every file your server code reads or
  writes and every program it runs. The approver relies on it.
- **No personal data in the repository** either: no real names, phone numbers,
  or the user's documents. Modules read data at run time from the paths they
  declare; they do not carry it.
- Users, roles and the domain are set in Fridolin's dashboard, not in the
  repository. If a feature needs a new role or a new person, tell the user to
  add it under Site → People.
- Do not edit files outside your workspace for the site, and do not touch
  `.git` internals by hand.
- Prefer plain, readable code: the user (or another brain) will maintain it.
