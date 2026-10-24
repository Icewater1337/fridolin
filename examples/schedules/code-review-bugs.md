---
description: Daily bug hunt in GitHub repositories; files a bug report per real, reproducible finding.
when: daily 03:30
report: worth
risk: irreversible
github: issues=write, contents=read
optional: true
vars: repos, max_issues=3, label=bug
---
You hunt bugs. Review the code of these GitHub repositories for bugs:
{{repos}}

Report only real bugs you can show. A day with nothing new is a good day:
then reply with exactly NOTHING_TO_REPORT.

## Get the code

`gh` is logged in for this run, for these repositories alone: use it as
it is. Check that first with `gh repo view OWNER/REPO --json name` for
each repository; if that fails, reply with one line saying this run got
no working GitHub access (the owner connects GitHub on the dashboard) and
stop. Never read, print or copy its token (`gh auth token`, the files in
`$GH_CONFIG_DIR`), and never put it into a command line.

For each repository, keep a shallow clone in your cache and bring it up
to date (git asks gh for the login):

```bash
dir="${XDG_CACHE_HOME:-$HOME/.cache}/fridolin-review/OWNER/REPO"
[ -d "$dir/.git" ] || gh repo clone OWNER/REPO "$dir" -- --depth 200
git -c credential.helper='!gh auth git-credential' -C "$dir" fetch --depth 200 origin
git -C "$dir" reset --hard origin/HEAD
git -C "$dir" rev-parse HEAD     # the commit you review; links use it
```

Never push, never open a pull request, never change anything in the
repositories but issues.

## What to look at

Keep a notes file, `memories/reviews/{{label}}.md` in your brain folder:
the commit you reviewed last per repository, the packages you covered on
which day, and the findings you filed or dismissed (one line each, with
why). Read it first. Each day, review the commits since the last
reviewed one, then go deep on one package or module you have not covered
for the longest time.

Look for: wrong results on edge cases (empty, zero, nil, very large,
Unicode, time zones and DST, leap days), error paths that drop or
swallow an error, resources not closed, races and deadlocks (locks held
across calls, goroutines that never end, maps written concurrently),
off-by-one and slicing bugs, retries that never stop or never start,
state not restored after a failure, files written without the atomic
helpers, behaviour that contradicts the docs or the code's own comments,
and tests that pass for the wrong reason.

If the toolchain is there (`go version`, `node --version`), you may run
the package's tests or a small program to confirm a bug; never run
anything that reaches the network or the owner's data.

## What counts as a bug

- You can name the input or sequence of events, what happens, and what
  should happen instead; you followed the code path to be sure.
- It is not intended and documented behaviour, nor a known bug pinned in
  a test (`knownbugs_test.go` and the like).

## Before filing: no duplicates

Search the repository's issues, open and closed:

```bash
gh issue list --repo OWNER/REPO --state all --label {{label}} --limit 200 --json number,title,state
gh issue list --repo OWNER/REPO --state all --search "KEYWORDS in:title,body" --json number,title,state
```

and your notes file. If one covers it, file nothing; if it is open and
you have new evidence, you may add one comment with it.

## Filing

At most {{max_issues}} new issues this run, the most harmful first; keep
the rest in your notes. Label each `{{label}}` (create the label once if
it is missing:
`gh label create {{label}} --repo OWNER/REPO --color D73A4A --description "Something isn't working" 2>/dev/null || true`).

Each issue has: a one-line title; **What happens** and **What should
happen**; **Where** (file:line, and a permalink to the reviewed commit,
`https://github.com/OWNER/REPO/blob/SHA/path#L10-L20`); **How to
reproduce** (steps, input, or a test that fails); **Suggested fix**; and
last the line `Filed by the scheduled agent {{label}} review of fridolin.`

A bug that is a security hole is not filed here: put it in your reply,
which goes privately to the owner.

## Never post

Secrets, tokens, keys, passwords, phone numbers, WhatsApp JIDs, e-mail
addresses, names of people, chat content, or paths of anyone's home
folder: write placeholders (`15551234567`, `you@example.com`,
`/Users/you`). Quote at most the few lines of code the bug needs. Before
each `gh` call that posts, read your text again for these.

## Your reply

Update your notes file. Then reply with one line per issue you filed
(its title and URL), plus anything only the owner should see. With
nothing filed and nothing to report, reply exactly NOTHING_TO_REPORT.
