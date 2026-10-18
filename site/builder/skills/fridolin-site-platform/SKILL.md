---
name: fridolin-site-platform
description: How Fridolin runs the user's website and what a builder brain can and cannot do — the site process, sign-in and roles, deploys and approval, previews, sandboxing, where module data can come from. Use when planning a site feature, explaining the site to the user, or when something about the site does not behave as expected.
---

# How Fridolin runs the site

Fridolin is the assistant you are part of: a program on the user's computer
that links WhatsApp to Claude Code. Each **brain** (you are one) is a folder
with its own prompt, memory and history, run in a sandbox that keeps it out of
the other brains' folders, Fridolin's data folder and the usual secrets.

The **site** is an optional website Fridolin serves on the user's domain.

```
site repository (private) ──approved commit──► site process (Node) ──tunnel──► https://<domain>
        ▲                                             ▲
   your proposal                          accounts, roles, domain:
   (branch builder/<you>)                 only in Fridolin's dashboard
```

- **The site process** is separate from the bridge. It serves the home page
  (a tile per module the visitor may open), sign-in (Google, or a username and
  password), and the modules of the **approved commit**.
- **Accounts and roles** live in Fridolin's dashboard (Site → People). Admins
  always sign in with Google. A module's `access.roles` names who may open it.
  You cannot add people or roles; ask the user to.
- **Deploys**: the user approves a commit in the dashboard; Fridolin checks it
  out into its own folder, runs `npm install` and each module's `app.build`,
  and switches the site to it. A failed build leaves the live site as it was.
  They can roll back to an earlier deploy.
- **Proposals**: `site_propose` pushes your branch `builder/<brain id>`. The
  user sees its commits and, per module, the review list (access, server code,
  files, commands, public files, build commands), then merges and deploys it —
  or not. One open proposal per brain; proposing again replaces it.

## What you can and cannot do

| You can | You cannot |
| --- | --- |
| edit, build and commit in your workspace | deploy, or change the live site directly |
| `site_check` and `site_preview` your work | read the site's accounts, session key or Fridolin's config |
| propose, and update your proposal | approve or merge anything |
| tell the user what to set up (people, roles, secrets, domain) | set those up yourself |

Previews and checks run **inside your sandbox**, with no accounts: a preview is
opened with its link (the dashboard has it) or, for a signed-in admin, through
the real domain (`/_fridolin/preview/<brain>`), and shows a "Preview" banner.

## Where a module's data comes from

Module server code runs on the user's machine after approval, so it can read
local files — the brain folders' documents, a SQLite database, a CSV export —
**if its `module.json` declares them** in `capabilities.files`. Typical
features:

- a page over a brain's files (declare the folder with `read`);
- a small app with its own data: a JSON or SQLite file the module owns
  (declare it `write`, keep it outside the repository, e.g. `~/site-data/<id>/`);
- a form that stores entries for an admin to read later.

What a module may not do: call Fridolin's local API, send WhatsApp messages,
read another module's files without declaring them, or reach anything that
needs a secret it did not declare. On macOS the live site enforces this: it
runs in a sandbox built from the modules' `capabilities`, where an undeclared
file or program fails with `EPERM`, Fridolin's data folder and the usual
secrets (`~/.ssh`, keychains, …) stay closed whatever a module declares, and
Fridolin's own HTTP port cannot be reached. Secrets reach a module only
through `ctx.secret(name)`, with the values the user sets in the dashboard;
previews and checks get none. When a feature needs something closed, say so
and let the user decide.

## When something is off

- **A module does not appear**: `site_check` lists why it would be skipped.
  On the live site the reason shows to admins on the home page.
- **"not built"**: its `app.dist` has no `index.html` — build it, or the deploy
  will (`app.build`).
- **Works in preview, not live**: it is not deployed yet — the proposal waits
  for the user.
- **The proposal does not merge**: the branch moved on; `site_sync`, check,
  propose again.
