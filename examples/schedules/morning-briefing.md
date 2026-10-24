---
description: Each morning, today's appointments from the brain's calendar, sent only when there are any.
when: daily 06:00
report: worth
risk: read
vars: greeting=Good morning
---
Send the owner today's appointments.

1. List today's events from your calendar, today being the date the
   prompt gives as now, in the bridge's time zone:

   ```bash
   "$FRIDOLIN_BIN" calendar list --from TODAY --to TODAY
   ```

   with TODAY as YYYY-MM-DD. It prints JSON; each event has a `title`
   (or `summary`) and a `start`, a time (`…T09:30:00…`) or, for an
   all-day event, a date.
2. If the calendar is not connected or the command fails, reply with one
   line saying so and what the owner can do (connect Google Calendar on
   this brain's page in the dashboard).
3. If there are no events today, reply with exactly NOTHING_TO_REPORT.
4. Otherwise reply with exactly this, in the order of their start times,
   all-day events first, and nothing else:

   ```
   {{greeting}}! You have N appointment(s) today:
   • HH:MM — Title
   • all day — Title
   ```

   with N the number of events, "appointment" for one and "appointments"
   for more.

Only read the calendar: create, move or delete nothing.
