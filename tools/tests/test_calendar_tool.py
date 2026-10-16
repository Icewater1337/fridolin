"""Tests for calendar_tool.py, against a fake Calendar service."""

from __future__ import annotations

import json
import unittest
from unittest import mock
from zoneinfo import ZoneInfo

import support  # noqa: F401  (puts tools/ on sys.path)
import calendar_tool

BERLIN = ZoneInfo("Europe/Berlin")


class CalendarTestCase(unittest.TestCase):
    """Pins the module's configuration so no test depends on the host."""

    allowed: list[str] = []
    default = "primary"

    def setUp(self):
        for name, value in {
            "ALLOWED": list(self.allowed),
            "DEFAULT_CALENDAR": self.default,
            "_timezone": BERLIN,
            "_profile": None,
        }.items():
            patcher = mock.patch.object(calendar_tool, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)


class TargetCalendarsTest(CalendarTestCase):
    allowed = ["me@example.com", "team@example.com"]
    default = "me@example.com"

    def test_resolution(self):
        cases = [
            ("read commands default to every allowed calendar", None, True,
             ["me@example.com", "team@example.com"]),
            ("write commands default to the default calendar", None, False, ["me@example.com"]),
            ("primary maps to the brain's default", "primary", False, ["me@example.com"]),
            ("an allowed calendar is used as given", "team@example.com", True,
             ["team@example.com"]),
        ]
        for name, requested, many, want in cases:
            with self.subTest(name):
                self.assertEqual(calendar_tool.target_calendars(requested, many=many), want)

    def test_refuses_a_calendar_the_brain_may_not_use(self):
        code, out, _ = support.run(calendar_tool.target_calendars, "other@example.com")
        self.assertEqual(code, 1)
        self.assertIn("not enabled for this brain", json.loads(out)["error"])


class EnsureTimezoneTest(CalendarTestCase):
    def test_cases(self):
        cases = [
            ("naive gets the local offset", "2026-01-06T14:00:00", "2026-01-06T14:00:00+01:00"),
            ("summer time offset", "2026-07-06T14:00:00", "2026-07-06T14:00:00+02:00"),
            ("an explicit offset is kept", "2026-01-06T14:00:00-05:00", "2026-01-06T14:00:00-05:00"),
            ("UTC is kept", "2026-01-06T14:00:00Z", "2026-01-06T14:00:00Z"),
            ("an all-day date is kept", "2026-01-06", "2026-01-06"),
        ]
        for name, given, want in cases:
            with self.subTest(name):
                self.assertEqual(calendar_tool.ensure_timezone(given), want)

    def test_rejects_garbage(self):
        code, out, _ = support.run(calendar_tool.ensure_timezone, "2026-01-06Tnoon")
        self.assertEqual(code, 1)
        self.assertIn("Cannot parse datetime", json.loads(out)["error"])


class FormatEventTest(unittest.TestCase):
    def test_timed_and_all_day(self):
        timed = calendar_tool.format_event({
            "id": "e1", "summary": "Standup",
            "start": {"dateTime": "2026-01-06T09:00:00+01:00"},
            "end": {"dateTime": "2026-01-06T09:15:00+01:00"},
            "attendees": [{"email": "you@example.com"}],
        })
        self.assertEqual(timed["title"], "Standup")
        self.assertEqual(timed["start"], "2026-01-06T09:00:00+01:00")
        self.assertEqual(timed["attendees"], ["you@example.com"])

        all_day = calendar_tool.format_event({"id": "e2", "start": {"date": "2026-01-06"},
                                              "end": {"date": "2026-01-07"}})
        self.assertEqual(all_day["title"], "(no title)")
        self.assertEqual((all_day["start"], all_day["end"]), ("2026-01-06", "2026-01-07"))


if __name__ == "__main__":
    unittest.main()
