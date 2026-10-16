#!/usr/bin/env python3
"""Google Calendar CLI for Fridolin.

Every command prints JSON on stdout, so the assistant can parse the result and
summarise it in plain language.

Connecting from the dashboard
-----------------------------
The easy way: open the bridge dashboard → Brains → <brain> → Calendar →
"Connect Google Calendar", sign in, and tick the calendars the brain may use.
When the bridge runs a brain it tells this tool which brain it is and which
calendars it chose (BRIDGE_BRAIN_ID, BRIDGE_CREDENTIALS_DIR, BRIDGE_CALENDARS,
BRIDGE_CALENDAR_DEFAULT), so inside a brain no flags are needed:

    calendar_tool.py calendars                 # the calendars this brain may use
    calendar_tool.py list --from 2026-01-05    # events across all of them
    calendar_tool.py create --title … --start … --end …   # into the default one

--calendar-id picks one of the allowed calendars; anything else is refused.

Profiles
--------
A profile is just a name. Each one gets its own OAuth credentials and token in
the bridge data directory, which keeps separate calendars separate:

    <data_dir>/google_calendar_credentials_<profile>.json   (you download this)
    <data_dir>/google_calendar_token_<profile>.json         (created on first auth)

Name profiles after your brains ("personal", "work", …) so the sandbox profile
generated for a brain grants it exactly its own calendar files. With no
--profile, the unsuffixed google_calendar_credentials.json / _token.json pair
is used.

Usage
-----
    calendar_tool.py --profile personal list --from 2026-01-05 --to 2026-01-12
    calendar_tool.py --profile work create --title "Design review" \
        --start 2026-01-06T14:00:00 --end 2026-01-06T15:00:00 \
        --attendees "alex@example.com,sam@example.com"
    calendar_tool.py --profile personal free --date 2026-01-06
    calendar_tool.py --profile personal delete --event-id EVENT_ID

Setup
-----
    1. console.cloud.google.com → new project → enable the Google Calendar API
    2. APIs & Services → Credentials → Create OAuth 2.0 Client ID (Desktop app)
    3. Download the JSON to the credentials path above
    4. Run any command once — a browser opens for consent and the token is saved

Timezone
--------
Defaults to the system timezone. Override per run with --timezone, or globally
with the CALENDAR_TZ environment variable (e.g. CALENDAR_TZ=Europe/Berlin).
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, date, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

SCOPES = ["https://www.googleapis.com/auth/calendar"]

DATA_DIR = Path(
    os.environ.get("BRIDGE_CREDENTIALS_DIR")
    or os.environ.get("BRIDGE_DATA_DIR")
    or next((p for p in (Path.home() / ".fridolin", Path.home() / ".whatsapp-claude-bridge") if p.is_dir()),
            Path.home() / ".fridolin")
)

# The calendars the dashboard allowed for this brain (empty: no restriction).
ALLOWED = [c for c in os.environ.get("BRIDGE_CALENDARS", "").split(",") if c]
DEFAULT_CALENDAR = os.environ.get("BRIDGE_CALENDAR_DEFAULT") or (ALLOWED[0] if ALLOWED else "primary")

# Set from CLI args in main().
_profile: str | None = None
_timezone: ZoneInfo | None = None


def err(msg: str) -> None:
    print(json.dumps({"error": msg}))
    sys.exit(1)


def tz() -> ZoneInfo:
    """Return the active timezone (system local unless overridden)."""
    assert _timezone is not None, "timezone not initialised"
    return _timezone


def system_timezone_name() -> str | None:
    """Best-effort IANA name for the system timezone.

    Google Calendar only accepts IANA names, and the abbreviation that
    datetime gives back ("CEST") is not one. On macOS and Linux the real name
    is recoverable from where /etc/localtime points.
    """
    if tz_env := os.environ.get("TZ"):
        return tz_env
    try:
        parts = Path("/etc/localtime").resolve().parts
        if "zoneinfo" in parts:
            return "/".join(parts[parts.index("zoneinfo") + 1:])
    except OSError:
        pass
    return None


def resolve_timezone(name: str | None) -> ZoneInfo:
    """Resolve --timezone, then CALENDAR_TZ, then the system timezone."""
    explicit = name or os.environ.get("CALENDAR_TZ")
    if explicit:
        try:
            return ZoneInfo(explicit)
        except Exception:
            err(f"Unknown timezone: {explicit!r}. Use an IANA name such as 'Europe/Berlin'.")

    if detected := system_timezone_name():
        try:
            return ZoneInfo(detected)
        except Exception:
            pass

    # Guessing here would create events at the wrong time and look like the
    # tool worked, so stop and ask instead.
    err(
        "Could not determine your timezone. Set it explicitly, either with "
        "--timezone Europe/Berlin or by exporting CALENDAR_TZ=Europe/Berlin."
    )


def credential_paths() -> tuple[Path, Path]:
    """Return (credentials_file, token_file) for the active profile."""
    suffix = f"_{_profile}" if _profile else ""
    return (
        DATA_DIR / f"google_calendar_credentials{suffix}.json",
        DATA_DIR / f"google_calendar_token{suffix}.json",
    )


def get_service():
    """Authenticate and return the Google Calendar API service."""
    try:
        from google.oauth2.credentials import Credentials
        from google.auth.transport.requests import Request
        from google_auth_oauthlib.flow import InstalledAppFlow
        from googleapiclient.discovery import build
    except ImportError as e:
        err(
            f"Missing Python package: {e}. Install the calendar extras with:\n"
            "  pip3 install -r tools/requirements.txt"
        )

    credentials_file, token_file = credential_paths()

    creds = None
    if token_file.exists():
        creds = Credentials.from_authorized_user_file(str(token_file), SCOPES)

    if not creds or not creds.valid:
        if creds and creds.expired and creds.refresh_token:
            try:
                creds.refresh(Request())
            except Exception:
                creds = None  # refresh failed; fall through to a fresh consent

        if not creds:
            if not credentials_file.exists():
                err(
                    f"Credentials file not found: {credentials_file}\n"
                    "Create OAuth credentials in the Google Cloud Console:\n"
                    "  1. console.cloud.google.com → APIs & Services → Credentials\n"
                    "  2. Create OAuth 2.0 Client ID (Desktop app)\n"
                    "  3. Download the JSON and save it to the path above\n"
                    "  4. Re-run this command to complete the OAuth flow"
                )
            flow = InstalledAppFlow.from_client_secrets_file(str(credentials_file), SCOPES)
            creds = flow.run_local_server(port=0)

        DATA_DIR.mkdir(parents=True, exist_ok=True)
        token_file.write_text(creds.to_json())
        token_file.chmod(0o600)

    return build("calendar", "v3", credentials=creds)


def target_calendars(requested: str | None, many: bool = False) -> list[str]:
    """Resolve --calendar-id against the brain's allowed calendars.

    With no --calendar-id, read commands (many=True) use every allowed
    calendar and write commands use the default one.
    """
    if requested in (None, ""):
        if many and ALLOWED:
            return ALLOWED
        return [DEFAULT_CALENDAR]
    if requested == "primary" and ALLOWED:
        requested = DEFAULT_CALENDAR
    if ALLOWED and requested not in ALLOWED:
        err(
            f"Calendar {requested!r} is not enabled for this brain. "
            f"Allowed: {', '.join(ALLOWED)}. Change it in the dashboard (Brains → Calendar)."
        )
    return [requested]


def format_event(e: dict) -> dict:
    """Return a compact, stable dict for one calendar event."""
    start = e.get("start", {})
    end = e.get("end", {})
    return {
        "id": e.get("id"),
        "title": e.get("summary", "(no title)"),
        "start": start.get("dateTime") or start.get("date"),
        "end": end.get("dateTime") or end.get("date"),
        "location": e.get("location"),
        "description": e.get("description"),
        "attendees": [a.get("email") for a in e.get("attendees", [])],
        "status": e.get("status"),
        "html_link": e.get("htmlLink"),
    }


def parse_day(s: str) -> datetime:
    """Parse YYYY-MM-DD into a timezone-aware midnight."""
    try:
        return datetime.strptime(s, "%Y-%m-%d").replace(tzinfo=tz())
    except ValueError:
        err(f"Cannot parse date {s!r}. Use YYYY-MM-DD.")


def ensure_timezone(dt_str: str) -> str:
    """Give a naive datetime an explicit UTC offset.

    Google Calendar can treat a naive dateTime as UTC even when a timeZone
    field is present, which silently shifts events by hours.
    """
    if "T" not in dt_str:
        return dt_str  # all-day event, a plain date
    if dt_str.endswith("Z") or "+" in dt_str[10:] or "-" in dt_str[10:]:
        return dt_str  # already has an offset
    try:
        return datetime.fromisoformat(dt_str).replace(tzinfo=tz()).isoformat()
    except ValueError:
        err(f"Cannot parse datetime {dt_str!r}. Use YYYY-MM-DDTHH:MM:SS.")


# ── commands ──────────────────────────────────────────────────────────────

def cmd_list(args) -> None:
    service = get_service()

    from_date = args.from_date or date.today().isoformat()
    to_date = args.to_date or from_date

    # timeMax is the start of the day *after* to_date, so the last day is
    # fully included and all-day events resolve correctly.
    time_min = parse_day(from_date)
    time_max = parse_day(to_date) + timedelta(days=1)

    calendars = target_calendars(args.calendar_id, many=True)
    events = []
    for cal in calendars:
        result = service.events().list(
            calendarId=cal,
            timeMin=time_min.isoformat(),
            timeMax=time_max.isoformat(),
            timeZone=str(tz()),
            singleEvents=True,
            orderBy="startTime",
            maxResults=args.max_results,
        ).execute()
        for e in result.get("items", []):
            item = format_event(e)
            if len(calendars) > 1:
                item["calendar"] = cal
            events.append(item)
    events.sort(key=lambda e: e.get("start") or "")
    print(json.dumps({"events": events, "count": len(events)}, indent=2))


def cmd_create(args) -> None:
    service = get_service()

    all_day = "T" not in args.start
    if all_day:
        body = {
            "summary": args.title,
            "start": {"date": args.start},
            "end": {"date": args.end},
        }
    else:
        body = {
            "summary": args.title,
            "start": {"dateTime": ensure_timezone(args.start), "timeZone": str(tz())},
            "end": {"dateTime": ensure_timezone(args.end), "timeZone": str(tz())},
        }

    if args.description:
        body["description"] = args.description
    if args.location:
        body["location"] = args.location
    if args.attendees:
        body["attendees"] = [{"email": e.strip()} for e in args.attendees.split(",") if e.strip()]

    # Reminders default to a single 10-minute popup; override with
    # --reminders "popup:10,email:60".
    if args.reminders:
        overrides = []
        for r in args.reminders.split(","):
            if ":" not in r:
                err(f"Bad --reminders entry {r!r}. Use method:minutes, e.g. popup:10")
            method, minutes = r.strip().split(":", 1)
            try:
                overrides.append({"method": method.strip(), "minutes": int(minutes.strip())})
            except ValueError:
                err(f"Bad reminder minutes in {r!r}")
        body["reminders"] = {"useDefault": False, "overrides": overrides}
    else:
        body["reminders"] = {"useDefault": False, "overrides": [{"method": "popup", "minutes": 10}]}

    # sendUpdates="all" emails the invitation to attendees. If you would rather
    # create events quietly, change this to "none".
    event = service.events().insert(
        calendarId=target_calendars(args.calendar_id)[0], body=body, sendUpdates=args.send_updates
    ).execute()
    print(json.dumps({"created": format_event(event)}, indent=2))


def cmd_free(args) -> None:
    """Report busy slots and the free windows between them for one day."""
    service = get_service()

    day_start = parse_day(args.date)
    day_end = day_start + timedelta(days=1)

    calendars = target_calendars(args.calendar_id, many=True)
    result = service.freebusy().query(body={
        "timeMin": day_start.isoformat(),
        "timeMax": day_end.isoformat(),
        "timeZone": str(tz()),
        "items": [{"id": c} for c in calendars],
    }).execute()
    busy = [slot for c in calendars for slot in result.get("calendars", {}).get(c, {}).get("busy", [])]

    def to_local(iso_str: str) -> datetime:
        return datetime.fromisoformat(iso_str.replace("Z", "+00:00")).astimezone(tz())

    hours_start, hours_end = args.hours.split("-", 1)
    window_start = datetime.fromisoformat(f"{args.date}T{hours_start}:00").replace(tzinfo=tz())
    window_end = datetime.fromisoformat(f"{args.date}T{hours_end}:00").replace(tzinfo=tz())

    busy_periods = [
        {"start": to_local(s["start"]).strftime("%H:%M"), "end": to_local(s["end"]).strftime("%H:%M")}
        for s in busy
    ]

    free_windows = []
    cursor = window_start
    for slot in sorted(busy, key=lambda x: x["start"]):
        s, e = to_local(slot["start"]), to_local(slot["end"])
        if s > cursor:
            free_windows.append({
                "start": cursor.strftime("%H:%M"),
                "end": s.strftime("%H:%M"),
                "duration_minutes": int((s - cursor).total_seconds() / 60),
            })
        cursor = max(cursor, e)
    if cursor < window_end:
        free_windows.append({
            "start": cursor.strftime("%H:%M"),
            "end": window_end.strftime("%H:%M"),
            "duration_minutes": int((window_end - cursor).total_seconds() / 60),
        })

    print(json.dumps({
        "date": args.date,
        "timezone": str(tz()),
        "hours": args.hours,
        "busy": busy_periods,
        "free_windows": [w for w in free_windows if w["duration_minutes"] >= args.min_slot],
    }, indent=2))


def cmd_delete(args) -> None:
    service = get_service()
    service.events().delete(calendarId=target_calendars(args.calendar_id)[0], eventId=args.event_id).execute()
    print(json.dumps({"deleted": args.event_id}))


def cmd_calendars(args) -> None:
    """List the calendars this brain may use (all of the account's, if unrestricted)."""
    service = get_service()
    items = service.calendarList().list(maxResults=250).execute().get("items", [])
    out = []
    for c in items:
        if ALLOWED and c["id"] not in ALLOWED:
            continue
        out.append({
            "id": c["id"],
            "name": c.get("summaryOverride") or c.get("summary"),
            "primary": c.get("primary", False),
            "default": c["id"] == DEFAULT_CALENDAR or (DEFAULT_CALENDAR == "primary" and c.get("primary", False)),
            "access": c.get("accessRole"),
        })
    print(json.dumps({"calendars": out, "restricted": bool(ALLOWED)}, indent=2))


# ── main ──────────────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Google Calendar tool for Fridolin",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--profile", default=os.environ.get("BRIDGE_BRAIN_ID") or None,
                        help="Credential profile name, e.g. the brain's name "
                             "(default: the brain running this tool, else the unsuffixed files).")
    parser.add_argument("--calendar-id", default=None,
                        help="Calendar ID (default: all of the brain's calendars for list/free, "
                             "its default calendar for create/delete; 'primary' without the dashboard)")
    parser.add_argument("--timezone", default=None,
                        help="IANA timezone, e.g. Europe/Berlin (default: CALENDAR_TZ or system)")

    sub = parser.add_subparsers(dest="command")

    p_list = sub.add_parser("list", help="List events in a date range")
    p_list.add_argument("--from", dest="from_date", help="Start date (YYYY-MM-DD, default: today)")
    p_list.add_argument("--to", dest="to_date", help="End date (YYYY-MM-DD, default: --from)")
    p_list.add_argument("--max-results", type=int, default=50)

    p_create = sub.add_parser("create", help="Create an event")
    p_create.add_argument("--title", required=True)
    p_create.add_argument("--start", required=True, help="YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD for all-day")
    p_create.add_argument("--end", required=True, help="YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD for all-day")
    p_create.add_argument("--description", default="")
    p_create.add_argument("--location", default="")
    p_create.add_argument("--attendees", default="", help="Comma-separated email addresses")
    p_create.add_argument("--reminders", default="",
                          help="Comma-separated method:minutes pairs, e.g. 'popup:10,email:60'")
    p_create.add_argument("--send-updates", choices=["all", "externalOnly", "none"], default="all",
                          help="Whether to email attendees about the invitation (default: all)")

    p_free = sub.add_parser("free", help="Show busy slots and free windows for a date")
    p_free.add_argument("--date", required=True, help="YYYY-MM-DD")
    p_free.add_argument("--hours", default="08:00-18:00",
                        help="Window to search, HH:MM-HH:MM (default: 08:00-18:00)")
    p_free.add_argument("--min-slot", type=int, default=30,
                        help="Ignore free windows shorter than this many minutes (default: 30)")

    p_delete = sub.add_parser("delete", help="Delete an event by ID")
    p_delete.add_argument("--event-id", required=True)

    sub.add_parser("calendars", help="List the calendars this brain may use")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    global _profile, _timezone
    _profile = args.profile
    _timezone = resolve_timezone(args.timezone)

    {
        "list": cmd_list,
        "create": cmd_create,
        "free": cmd_free,
        "delete": cmd_delete,
        "calendars": cmd_calendars,
    }[args.command](args)


if __name__ == "__main__":
    main()
