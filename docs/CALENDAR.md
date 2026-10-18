# Google Calendar

Each brain can sign in to its own Google account and use some or all of that
account's calendars: list events, find free time, create and delete events.

## One-time setup: a Google OAuth client

Google only lets an app ask for calendar access through an OAuth client that
belongs to a Google Cloud project. The bridge does not ship one — you create
your own once, and every brain uses it to sign in.

1. Open [Google Cloud Console](https://console.cloud.google.com/), pick or
   create a project, and **enable the Google Calendar API**
   (APIs & Services → Library → Google Calendar API → Enable).
2. **Consent screen** (APIs & Services → OAuth consent screen): choose
   *External*, fill in an app name and your email.
   - While the app is in *Testing*, add every Google account a brain will
     sign in with as a *test user*. Google expires sign-ins of apps in
     Testing after **7 days**.
   - To avoid that, *publish* the app (Publishing status → In production).
     Since it is only used by you, there is nothing to verify; Google shows
     an "unverified app" notice during sign-in, which you can continue past.
3. **Credentials → Create credentials → OAuth client ID → Desktop app.**
   Download the JSON.
4. In the dashboard: **Settings → Google sign-in**, paste the JSON (or pick
   the file) and save.

A *Desktop app* client is the right type: Google lets it return to the bridge
on `http://127.0.0.1:<port>/` without any further setup. A *Web application*
client also works if you add the bridge's address
(`http://127.0.0.1:8765/` by default) to its authorised redirect URIs.

The client file is stored in `~/.fridolin/google/`, which brains
cannot read.

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
first is not enough, since Google keeps the old grant until it is revoked,
and Disconnect revokes it.

## What the brain gets

The sign-in is saved as the brain's token,
`google_calendar_token_<brain>.json`, in `sandbox.credentials_dir` (or the
bridge's data folder), which the sandbox denies every brain, its own token
included. The bridge uses it for the brain: it makes the brain's calendar
calls (below), lists the calendars on the brain's page, refreshes the
token and revokes it on disconnect. So a brain never sees a refresh token
or the OAuth client's secret, and cannot call Google itself with them.
Older versions let each brain read and rewrite its token, and copied the
OAuth client next to it as `google_calendar_credentials_<brain>.json`;
nothing reads that copy any more, and you can delete it.

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
remove the access yourself. When Google does not confirm a revocation (it
is down, the network is), the token is kept and the page offers to delete
it here only. **Switch account** revokes the grant of the account the brain
leaves, unless another brain uses it too; signing in to the same account
again keeps it, since the new token belongs to it.

## Troubleshooting

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
