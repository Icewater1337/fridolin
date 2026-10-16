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

The client file is stored in `~/.whatsapp-claude-bridge/google/`, which brains
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

## What the brain gets

The sign-in is saved as the brain's token,
`google_calendar_token_<brain>.json`, in `sandbox.credentials_dir` (or the
bridge's data folder). The sandbox lets only that brain read it. The chosen
calendars are stored in the brain's `brain.json`:

```json
"calendar": {
  "account": "you@example.com",
  "calendars": ["you@example.com", "<id>@group.calendar.google.com"],
  "default": "you@example.com"
}
```

When the bridge runs a brain, it tells `tools/calendar_tool.py` which brain is
running and which calendars it chose, so inside a brain:

```bash
python3 <bridge-repo>/tools/calendar_tool.py calendars                 # what it may use
python3 <bridge-repo>/tools/calendar_tool.py list --from 2026-01-05     # all chosen calendars
python3 <bridge-repo>/tools/calendar_tool.py free --date 2026-01-06     # busy across all of them
python3 <bridge-repo>/tools/calendar_tool.py create --title "Dentist" \
    --start 2026-01-06T14:00:00 --end 2026-01-06T15:00:00               # into the default
```

`--calendar-id` selects one of the chosen calendars; others are refused. The
tool needs `pip3 install -r tools/requirements.txt`.

**Limits.** Google grants access to a whole account, not to single calendars.
Keeping a brain to the calendars you ticked is done by the calendar tool, not
by Google — a brain that wrote its own API calls with the token could reach
the account's other calendars. If that matters, give the brain its own Google
account and share just the calendars it needs with it.

## Disconnecting

**Disconnect** on the brain's page revokes the brain's access at Google and
deletes its token. Your calendars are not touched. You can also remove access
at [myaccount.google.com/permissions](https://myaccount.google.com/permissions).

## Troubleshooting

- **"redirect_uri_mismatch"** — you are using a *Web application* client; add
  `http://127.0.0.1:8765/` to its redirect URIs, or create a Desktop app client.
- **"Access blocked: app has not completed verification"** — the account is
  not a test user of an app in *Testing*. Add it, or publish the app.
- **Works for a week, then fails** — the app is in *Testing*; publish it and
  connect again.
- **"did not return a refresh token"** — remove the app at
  myaccount.google.com/permissions and connect again.
