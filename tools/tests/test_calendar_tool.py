"""Tests for calendar_tool.py, the wrapper around fridolin calendar."""

from __future__ import annotations

import json
import unittest

import support  # first: it puts tools/ on sys.path
import calendar_tool


class TranslateTest(unittest.TestCase):
    def test_arguments(self):
        cases = [
            ("a command", ["list", "--from", "2026-01-05"], ["list", "--from", "2026-01-05"]),
            ("the global flags",
             ["--calendar-id", "team@example.com", "--timezone", "Europe/Zurich", "free", "--date", "2026-01-06"],
             ["--calendar-id", "team@example.com", "--timezone", "Europe/Zurich", "free", "--date", "2026-01-06"]),
            ("--profile", ["--profile", "work", "create", "--title", "x"], ["create", "--title", "x"]),
            ("--profile=", ["--profile=work", "calendars"], ["calendars"]),
            ("--profile with nothing after it", ["--profile"], []),
        ]
        for name, argv, want in cases:
            with self.subTest(name):
                self.assertEqual(calendar_tool.translate(argv), ["calendar", *want])


class WrapperTest(unittest.TestCase):
    def test_runs_fridolin_calendar(self):
        fridolin, argv = support.fake_fridolin(self, stdout='{"events": [], "count": 0, "truncated": false}')
        code, out, err = support.run_tool("calendar_tool.py", ["--profile", "work", "list", "--from", "2026-01-05"],
                                          {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(argv.read_text(encoding="utf-8")), ["calendar", "list", "--from", "2026-01-05"])
        self.assertEqual(json.loads(out), {"events": [], "count": 0, "truncated": False})
        self.assertIn('calendar_tool.py is deprecated', err)
        self.assertIn('"$FRIDOLIN_BIN" calendar', err)

    def test_passes_the_error_on(self):
        fridolin, _ = support.fake_fridolin(self, stdout='{"error": "not connected"}', code=1)
        code, out, _ = support.run_tool("calendar_tool.py", ["list"], {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual((code, json.loads(out)), (1, {"error": "not connected"}))


if __name__ == "__main__":
    unittest.main()
