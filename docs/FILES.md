# Files and Wiki

Every Fridolin shows the folders it is linked to and its brains' wikis, in
two places:

1. **The dashboard**: the **Files** and **Wiki** pages, for you, the owner.
2. **Your site**, once it is on ([SITE.md](SITE.md)): `https://<domain>/files`
   and `https://<domain>/wiki`, behind the site's sign-in, for the brains
   each role may see.

Both read the same folders through the same code in the bridge, which
decides what each reader may see and writes every view to the audit log.
Both only read: nothing here changes a file.

## Which folders

| Folder | Where it is | On the site |
| --- | --- | --- |
| Each running brain's folder | the brain's `brain_dirs` entry, on the core | yes, per role |
| Each brain's media inbox: what came in on WhatsApp and through `/ingest` | `<media_dir>/<brain>` | no |
| The folders an approved node lends (its `fs` roots) | on the node; shown while it is online | no |

The Files page groups them by kind, with where each lives (*On the core*,
*On <node>*), whether it is backed up, and the brain's `backup.exclude`
lines, which it hides (below). A node's folder is read through the node
(`fs.list`, `fs.read`); one file of it opens up to 16 MiB.

## What is never shown

In every folder, to everyone:

- every name that starts with `.`: `.env`, `.git`, `.claude`, `.mcp.json`,
  `.fridolin-site`, and the rest;
- the files a backup always leaves out as secrets
  ([BACKUP.md](BACKUP.md)): `.env.*`, `*.pem`, `*.p12`, private SSH keys,
  calendar tokens, `.credentials.json`;
- what the brain's `backup.exclude` leaves out, in the same syntax: its
  work folders, `node_modules/`, logs;
- Fridolin's own data folder, database, config and credentials folder,
  `sandbox.deny_paths` and what the brains' sandbox keeps from them, where
  any of them lies inside a folder;
- anything outside the folder.

A site account whose role is not `admin` does not see the brain's history
and run logs either (`conversation-history.jsonl`, `claude-stream*.log`),
nor `brain.json` and `memories/people/whatsapp-identity-map.md`, which
name chats and numbers.

What is hidden cannot be opened by its name either: it answers as a file
that does not exist. A path is relative to its folder, with no `..`, and
is opened through `os.Root`, which refuses a step out of the folder. No
symlink is followed, not even one that stays inside: a brain can make one
anywhere in its folder, and a link to a hidden place inside would get past
the rules above.

## The Files page

`#/files` lists the folders; a folder lists its files and folders, with
size and date, sorted by name, date or size. *Find by name* searches the
names below the open folder (up to 200 hits, 50 000 entries or five
seconds, whichever comes first).

A file opens as what it is:

| Kind | Shown |
| --- | --- |
| Markdown | rendered (below) |
| Text and code (`.txt`, `.json`, `.csv`, `.py`, `.go`, `.html`, …), or a file that reads as UTF-8 | as text, the first 512 KiB |
| Images, audio, video | in the page; audio and video stream |
| PDF | in a tab of its own, in the browser's viewer |
| Anything else | to download |

*Download* saves any file. The page loads a file's bytes from
`/files/raw` with a *ticket*: a signature the bridge makes for one folder,
valid 30 minutes, that the page asks for through the API (an `<img>`, a
`<video>` and a download link carry no session header). A restart of the
bridge ends every ticket.

A file is served from the bridge's own address under a policy that runs
nothing (`Content-Security-Policy: … sandbox`): an SVG's script or an
HTML file opened directly does not run. HTML is shown as text. Only
images, PDFs, audio and video are served as what they are; every other
file is `text/plain` or a download.

## The wiki

A brain's pages are the Markdown files in its `wiki/` folder and, as a
section of their own, its memory notes in `memories/`, with the same rules
as on the Files page. The front page is `wiki/index.md`, `wiki/_index.md`,
`wiki/README.md` or `wiki/home.md`, the first that exists.

- **Titles** come from the frontmatter's `title:`, else the first `#`
  heading, else the file name. `tags:` show under the title.
- **Links**: `[[page]]`, `[[page|text]]` and `[[page#heading]]` find a
  page as Obsidian does: by its path from the linking page's folder, from
  `wiki/` or from the brain folder, else the page of that name anywhere in
  the wiki (the shortest path wins). A name that is no page but a file of
  the brain folder, with or without its extension
  (`[[documents/plan]]` → `documents/plan.pdf`), links to that file. A
  link to nothing shows as a dashed name. Relative Markdown links
  (`[text](../people/alex.md)`) work too.
- **Images**: `![alt](picture.png)` and `![[picture.png]]` from the brain
  folder. An image from the web is shown as a link to it, never loaded,
  so that reading a page tells nobody it was read.
- Each page has a table of contents of its headings, the pages that link
  to it (*Linked from*), and its path, which opens on the Files page.
- **Search** finds the pages holding every word, in the title or the
  text, in one brain's wiki or, from the wiki's front page, in all of
  them.

### Markdown is never HTML

What a brain writes is untrusted: it often comes from what someone sent
it. The bridge parses Markdown (CommonMark, with GitHub's tables,
strikethrough, task lists and autolinks) into a tree of a fixed list of
element names (`p`, `h1`–`h6`, lists, `blockquote`, `pre`, `code`, `em`,
`strong`, `del`, `a`, `img`, tables, `hr`, `br`) and texts, and the page
builds it with DOM calls. HTML in the Markdown stays text; a link goes to
the web only for `http`, `https` and `mailto`; `javascript:`, `data:` and
the like stay text. The dashboard and the site both run under a strict
Content Security Policy.

## On your site

The site serves `/files` and `/wiki` to everyone signed in, with or
without a repository of modules; no module may take those routes. Its
home page shows a tile for each when the account's role sees a brain.

**Who sees what** is set on the dashboard's Site page, under *Files and
Wiki*: each role is given the brains it may see. `admin` sees every
brain, and the parts of it hidden from other roles. A role with no brains
sees an empty page. Media inboxes and node folders are never on the site.

**`wiki.<domain>`**: a host name `wiki.<domain>` (or `files.<domain>`) that
the tunnel sends to the site redirects to `https://<domain>/wiki/…`
(`/files/…`), since the sign-in belongs to `<domain>`. An old link
`wiki.<domain>/<brain>/<page>` lands on that page.

**How the site reaches the folders**: the site process cannot read brain
folders, and its sandbox keeps it off the bridge's own port. The bridge
answers it on a port of its own on the loopback, the *site bridge*, which
serves only these reads, with a token made at startup and handed to the
site through its secrets pipe. The site names the signed-in account in
each request; the bridge looks it up in the site's accounts, and lets it
read what its role may see. Module code runs in the site's process and
could make the same requests, as it can read the site's accounts and
session key: approving module code trusts it with what the site's roles
may read ([SITE.md](SITE.md#what-a-module-can-reach)).

## Audit

Every view is a line of kind `file_view` in the audit log
(`<data_dir>/audit/`, the dashboard's Audit page): `by` is `dashboard` or
`site:<account>`, `brain` the brain, `detail` the action (`view`, `open`,
`download`, `wiki`; on the site also `list`, `search` and `wiki-search`),
the folder and the path, and `outcome` `ok` or why it was refused. A video
that streams in pieces is one line.

## API

The dashboard's routes, with its token or session like the rest of
`/api/` (not a contract for scripts):

| Route | Answers |
| --- | --- |
| `GET /api/files/roots` | `{"roots": [...]}`: every folder, its `id` (`brain:<id>`, `media:<id>`, `node:<node>:<n>`), `kind`, `label`, `path`, `location`, `writable`, `backup`, `excluded`, `online` |
| `GET /api/files/list?root=&path=` | the folder's `entries` (`name`, `dir`, `size`, `mod_time`, `kind`), at most 5000 (`truncated`) |
| `GET /api/files/search?root=&path=&q=` | the files and folders below `path` whose name holds `q` |
| `GET /api/files/view?root=&path=` | the file's `name`, `size`, `mod_time`, `kind`, and its `text` or rendered Markdown `doc` (`title`, `tags`, `body`, `toc`) |
| `GET /api/files/raw?root=&path=&download=1` | the file itself |
| `GET /api/files/ticket?root=` | `{"ticket", "expires"}` for `GET /files/raw?root=&path=&ticket=` |
| `GET /api/wiki` | the brains with pages: `brain`, `label`, `pages`, `home` |
| `GET /api/wiki/{brain}/pages` | the brain's `pages` (`path`, `name`, `title`, `section`), its `home` |
| `GET /api/wiki/{brain}/page?path=` | the page rendered (`doc`), with its `backlinks`; without `path`, the front page |
| `GET /api/wiki/search?q=&brain=` | up to 50 `hits` with a `snippet` |

A path that is hidden, leaves the folder or does not exist is `404` alike.
The site bridge serves the same calls under `/site-api/`.

## Not yet

Editing a page or a text file in place, uploading, renaming, moving and
deleting are a second phase; until then everything is read-only.
