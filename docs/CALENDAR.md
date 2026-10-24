# Google Calendar

Each brain can sign in to its own Google account and use some or all of that
account's calendars: list events, find free time, create and delete events.

## One-time setup: a Google OAuth client

Google only lets an app ask for calendar access through an OAuth client that
belongs to a Google Cloud project. You create your own once, and every
brain uses it to sign in; the same client connects the
[Drive backup](BACKUP.md#use-your-own-google-client). A build whose
publisher baked a client in needs none of this (see
[Built-in client](#built-in-client)).

The dashboard walks you through it: **Settings → Google sign-in** (or a
brain's Calendar card, before any client is set up). It lists the steps
below, each with a link straight to the right page of Google Cloud
Console, the values to type with copy buttons, and, once you type your
project's ID, links that open that project. It takes about ten minutes.

1. **Create a project** ([New project](https://console.cloud.google.com/projectcreate)),
   named *Fridolin* say, or use one you have. Type its project ID into the
   dashboard, and the following links open it.
2. **Enable the APIs**: one page enables the Google Calendar API and the
   Google Drive API together
   ([link](https://console.cloud.google.com/flows/enableapi?apiid=calendar-json.googleapis.com,drive.googleapis.com)).
   Drive is for backups; leave it out if you will not back up to Drive.
3. **Consent screen** ([Google Auth Platform](https://console.cloud.google.com/auth/overview);
   older consoles: APIs & Services → OAuth consent screen) → *Get started*:
   - App name *Fridolin*, your address as the user support email;
     audience **External**; your address as the contact.
   - [Branding](https://console.cloud.google.com/auth/branding): leave
     **Authorised domains** empty. A Desktop app client returns to the
     bridge on this machine (`http://127.0.0.1:8765/`), which needs no
     domain, and Fridolin needs no homepage or privacy-policy link.
   - [Data access](https://console.cloud.google.com/auth/scopes) → *Add or
     remove scopes* → *Manually add scopes*: the dashboard lists the four
     with copy buttons — `calendar.events`, `calendar.events.freebusy`,
     `calendar.calendarlist.readonly` (the calendar's, below) and
     `drive.file` (the backup's), each under
     `https://www.googleapis.com/auth/`. A brain's calendar sign-in never
     asks for Drive, nor the Drive sign-in for calendars.
   - [Audience](https://console.cloud.google.com/auth/audience) →
     **Publish app**, so the status reads *In production*. An app left in
     *Testing* works only for the test users you list, and Google ends
     their sign-ins after **7 days**.
4. **Create the client** ([Create client](https://console.cloud.google.com/auth/clients/create)):
   application type **Desktop app**, name *Fridolin*. A Desktop app client
   needs no redirect URI: Google lets it return to the bridge on
   `http://127.0.0.1:<port>/` on its own.
5. **Add it in the dashboard**: paste the JSON the dialog offers to
   download (or pick the file), or copy the client ID and secret into the
   two fields. Google shows a new client's secret only once, when it is
   created; on the client's page you can add a new one.
6. **The check.** Saving checks the client right away: that the ID looks
   like Google's, which redirect it needs, and whether Google knows the ID
   and secret. For the last the bridge asks Google's token endpoint to
   trade in a made-up code, which grants nothing: Google refuses an ID or
   secret it does not know (or a deleted client) differently from a bad
   code. A client the check finds broken is saved but not taken quietly:
   the dashboard says what is wrong, and you paste it again or keep it
   anyway. **Check the client** under Settings runs it again.

A *Web application* client also works if you add the bridge's address
(`http://127.0.0.1:8765/` by default, the dashboard shows it) to its
authorised redirect URIs; Google checks that only at the first sign-in, so
the check can only remind you. The address is the one the bridge is
serving on: a changed `http_listen` counts for sign-ins only after the
restart that moves the dashboard there.

The client file is stored in `~/.fridolin/google/`, which brains cannot
read. The dashboard never shows the secret again, and the bridge never
logs it.

### "Google hasn't verified this app", and the 100-user cap

Your app is in production but not verified, so every sign-in first shows
**"Google hasn't verified this app"**. That is expected: Google verifies
apps offered to the public, and this one is yours. Click **Advanced**, then
**Go to Fridolin (unsafe)** (your app's name), and allow access.

An unverified app may be used by at most **100 Google accounts** in its
lifetime. Each account that signs in counts once; signing in again does
not count again. One person's accounts, and their family's, are far from
that, so there is nothing to apply for.

### Built-in client

A publisher who builds Fridolin for other people can bake a Desktop app
client of their own project into the binary, so that its users connect
with one click and skip the setup:

```bash
FRIDOLIN_GOOGLE_CLIENT_ID=….apps.googleusercontent.com \
FRIDOLIN_GOOGLE_CLIENT_SECRET=GOCSPX-… make build
```

`make build` and `scripts/build-release.sh` read the two variables from
the environment (the line `make` prints names them without their values)
and pass them to `go build -ldflags -X` (`internal/google/builtin.go`). The
public builds leave them unset and have no built-in client. With one,
Settings → Google sign-in says **Built-in client** and the setup steps are
skipped; the Drive backup uses it too, so its project needs both APIs and
all four scopes. A client you paste under Settings always takes its place;
brains that connected with the built-in one keep refreshing with it until
they connect again. Users of a published build see the publisher's app
name on Google's consent screen, and count towards that app's 100-user cap
until the publisher has it verified.

### A dashboard opened from another machine

Google sends the browser back to `http://127.0.0.1:8765/`: the bridge on
the machine that runs the browser. When you open the dashboard from
another machine — the core over the tailnet, say — that address does not
load there. The dashboard then opens Google's sign-in in a tab of its own
and shows a box: copy the whole address of the tab that did not load
(`http://127.0.0.1:8765/?state=…&code=…`) and paste it there. Its
one-time state ties it to the sign-in you started, so it works once, within
15 minutes. The Backup page does the same for the Drive link.

## Connecting a brain

Dashboard → **Brains → *brain* → Calendar → Connect Google Calendar**. Google's
sign-in opens; pick the account for this brain and allow calendar access. You
come back to the brain's page, where the account's calendars are listed:

- tick the calendars this brain may use,
- choose the **default** — where new events go,
- save.

Brains can use different accounts (a personal brain your personal account, a
work brain the work account), or the same account with different calendars.

The sign-in asks Google for three things, and nothing more: to see and edit
the events on the account's calendars (`calendar.events`), to see when its
calendars are busy (`calendar.events.freebusy`), and to see the list of its
calendars (`calendar.calendarlist.readonly`). None of them lets a brain
delete a calendar or change who it is shared with. Leave every box ticked
on Google's consent screen: a sign-in without one of them does not finish,
and the brain's page says which was missing.

A brain connected with an older version has the full `calendar` scope,
which includes both. It keeps working, and its page says so: **Disconnect**
it and connect again to narrow it. Signing in again without disconnecting
first is not enough, since Google keeps the old grant until it is revoked.
Disconnect revokes it, but not while another brain is signed in to the same
account (see below): disconnect that brain too, or remove the access at
[myaccount.google.com/permissions](https://myaccount.google.com/permissions),
before you connect again.

## What the brain gets

The sign-in is saved as the brain's token,
`google_calendar_token_<brain>.json`, in `sandbox.credentials_dir` (or the
bridge's data folder), which the sandbox denies every brain, its own token
included. The bridge uses it for the brain: it makes the brain's calendar
calls (below), lists the calendars on the brain's page, refreshes the
token and revokes it on disconnect. So a brain never sees a refresh token
or the OAuth client's secret, and cannot call Google itself with them.
Older versions let each brain read and rewrite its token, and copied the
OAuth client, its secret included, next to it as
`google_calendar_credentials_<brain>.json`. The bridge no longer reads
that copy. It names the files of that name that hold a client secret in a
warning in its log when it starts, and on every brain's page, but deletes
none itself: going by the name and the secret only, it cannot tell such a
copy from a file of yours that something else uses. A symlink of that
name is not followed, so it is not named either. Delete the copies by
hand. That matters most on Linux or with the sandbox off, where a brain
can read the folder.

The bridge takes only a regular file as a token: a brain the sandbox does
not hold (on Linux, or with the sandbox off) could otherwise put a symlink
or a hard link to another brain's token in place of its own and have the
bridge refresh that token into its own file, or revoke it. It refuses a
token file over 256 KiB too, far more than a real one holds; the brain's
page then shows why its calendars cannot be listed, and connecting again
fixes it. Nor does the bridge take the `token_uri` or the client in that
file: it refreshes at Google's token endpoint with the client under
Settings → Google sign-in. A refresh keeps a new refresh token Google
hands out, and every field an older version's calendar tool added to the
file.

The chosen calendars are stored in the brain's `brain.json`:

```json
"calendar": {
  "account": "you@example.com",
  "calendars": ["you@example.com", "<id>@group.calendar.google.com"],
  "default": "you@example.com"
}
```

A brain uses its calendar with `fridolin calendar`, which asks the bridge:
the bridge makes the call to Google with the brain's token, and only on the
calendars ticked on the brain's page. Inside a brain, where `FRIDOLIN_BIN`
names the bridge's binary:

```bash
"$FRIDOLIN_BIN" calendar calendars                  # what it may use
"$FRIDOLIN_BIN" calendar list --from 2026-01-05     # all chosen calendars
"$FRIDOLIN_BIN" calendar free --date 2026-01-06     # busy across all of them
"$FRIDOLIN_BIN" calendar create --title "Dentist" \
    --start 2026-01-06T14:00:00 --end 2026-01-06T15:00:00    # into the default
"$FRIDOLIN_BIN" calendar delete --event-id EVENT_ID
```

`--calendar-id` selects one of the chosen calendars, and the bridge refuses
any other, whatever the brain asks for. A brain with no calendars chosen in
its `brain.json` gets its primary calendar only. `list` returns at most
`--max-results` events (default 100) across all of them, and says
`"truncated": true` when there were more. `free` reports the busy times
and the free windows within `--hours` (default `08:00-18:00`), and fails
rather than call a day free when Google cannot read one of the calendars.
Times are in `--timezone`, else `CALENDAR_TZ`, else the system's timezone.
Every command prints JSON, and an error as `{"error": …}`.

Nothing in the brain signs in. When a brain's sign-in is revoked or
expires, or its token cannot be used, every command answers with an error
asking you to connect again on the brain's Calendar page; a Google outage
says it is temporary instead. A calendar Google cannot read for `free`,
one deleted or no longer shared with the account, is named with a hint to
check it and the brain's calendars, since connecting again would not help.

**Limits.** Google grants access to a whole account, not to single
calendars: keeping a brain to the calendars you ticked is done by the
bridge, which makes every calendar call itself. That holds while the brain
cannot read its token, nor the brain API token of another brain's run,
which takes the sandbox (macOS). Without it, a brain can read the
credentials folder like any other file of yours and call Google with the
whole account's access; then give each brain its own Google account,
sharing just the calendars it needs with it.

`tools/calendar_tool.py` still works for one release, as a wrapper that
runs `fridolin calendar` with the same arguments (`--profile` is ignored,
and `auth` points to the dashboard). It needs no Python packages any more.

## Disconnecting

**Disconnect** on the brain's page revokes the brain's access at Google and
deletes its token. Your calendars are not touched. You can also remove access
at [myaccount.google.com/permissions](https://myaccount.google.com/permissions).

Google keeps one grant per app and account, and revoking any brain's token
revokes that grant. So when another brain is signed in to the same account
with the same OAuth client (or the bridge cannot tell, because a token does
not record its account), Disconnect deletes only this brain's token and
says that the access stayed at Google; disconnect the other brain too, or
remove the access yourself. A token file left by a brain that was removed
without a disconnect counts too: the message names it, so that you can
delete it if nothing else uses it. When Google does not confirm a
revocation (it is down, the network is), the token is kept and the page
offers to delete it here only. **Switch account** revokes the grant of the
account the brain leaves, unless another brain uses it too; signing in to
the same account again keeps it, since the new token belongs to it. It
revokes only once the new sign-in is saved: one that fails keeps the
brain's old token.

## Troubleshooting

- **The check says "Google does not accept this client ID and secret"** —
  a typo, a secret Google replaced, or a deleted client. Copy both again
  from the client's page in Google Cloud Console, or create a new client.
- **"redirect_uri_mismatch"** — you are using a *Web application* client; add
  `http://127.0.0.1:8765/` to its redirect URIs, or create a Desktop app client.
- **"Access blocked: app has not completed verification"** — the account is
  not a test user of an app in *Testing*. Add it, or publish the app.
- **Works for a week, then fails** — the app is in *Testing*; publish it and
  connect again.
- **The brain says to reconnect the calendar**, or its page says Google no
  longer accepts the sign-in — Google refused the saved sign-in (access
  removed, the 7-day *Testing* limit, a changed password). Connect again on
  the brain's Calendar page.
- **"did not return a refresh token"** — remove the app at
  myaccount.google.com/permissions and connect again.
- **"signed in with another Google OAuth client"** on a brain's page — the
  client under Settings → Google sign-in was replaced after the brain
  connected, and its token works only with the old one. Connect the brain
  again.
