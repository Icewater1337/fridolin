"""Tests for history_search.py, against a scratch history file."""

from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

import support  # first: it puts tools/ on sys.path
import history_search

CHAT = "120363000000000000@g.us"


def ts(dt: datetime) -> str:
    return dt.isoformat(timespec="seconds")


class HistorySearchTestCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.file = Path(tmp.name) / history_search.HISTORY_FILENAME

    def write(self, rows: list[dict]):
        with self.file.open("w", encoding="utf-8") as f:
            for row in rows:
                f.write(json.dumps(row) + "\n")

    def search(self, *argv: str) -> tuple[int, list, str]:
        code, out, err = support.run(history_search.main, ["--file", str(self.file), "--json", *argv])
        return code, (json.loads(out) if code == 0 else None), err


class SearchTest(HistorySearchTestCase):
    def test_substring_newest_first(self):
        now = datetime.now(timezone.utc)
        self.write([
            {"ts": ts(now - timedelta(hours=2)), "type": "in", "chat": CHAT, "text": "Invoice 1"},
            {"ts": ts(now - timedelta(hours=1)), "type": "out", "chat": CHAT, "text": "no match"},
            {"ts": ts(now), "type": "in", "chat": CHAT, "text": "the invoice again"},
        ])
        code, hits, err = self.search("invoice")
        self.assertEqual(code, 0, err)
        self.assertEqual([h["text"] for h in hits], ["the invoice again", "Invoice 1"])

    def test_since_with_offset_timestamps(self):
        now = datetime.now(timezone(timedelta(hours=2)))
        self.write([
            {"ts": ts(now - timedelta(days=10)), "type": "in", "text": "old invoice"},
            {"ts": ts(now - timedelta(hours=1)), "type": "in", "text": "new invoice"},
        ])
        code, hits, err = self.search("--since", "7d", "invoice")
        self.assertEqual(code, 0, err)
        self.assertEqual([h["text"] for h in hits], ["new invoice"])

    def test_since_with_utc_z_timestamps(self):
        # The bridge's RFC 3339 timestamps end in Z on a host running in UTC.
        now = datetime.now(timezone.utc).replace(microsecond=0)
        self.write([
            {"ts": (now - timedelta(days=10)).strftime("%Y-%m-%dT%H:%M:%SZ"), "type": "in", "text": "old invoice"},
            {"ts": (now - timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"), "type": "in", "text": "new invoice"},
        ])
        code, hits, err = self.search("--since", "7d", "invoice")
        self.assertEqual(code, 0, err)
        self.assertEqual([h["text"] for h in hits], ["new invoice"])

    def test_parse_ts(self):
        utc = timezone.utc
        cases = [
            ("Z suffix", "2026-01-15T10:00:00Z", datetime(2026, 1, 15, 10, tzinfo=utc)),
            ("numeric offset", "2026-01-15T11:00:00+01:00", datetime(2026, 1, 15, 10, tzinfo=utc)),
            ("garbage", "yesterday", None),
            ("missing", "", None),
            ("not a string", 1736935200, None),
        ]
        for name, given, want in cases:
            with self.subTest(name):
                self.assertEqual(history_search.parse_ts(given), want)

    def test_bad_since_is_a_usage_error(self):
        self.write([])
        for given in ("7days", "last week"):
            with self.subTest(given):
                code, _, err = self.search("--since", given, "x")
                self.assertEqual(code, 2)
                self.assertIn("argument --since: use 30m, 24h, 7d, 2w or an ISO timestamp", err)
                self.assertNotIn("Traceback", err)

    def test_since_accepts_iso_timestamps(self):
        self.assertEqual(history_search.parse_since("2026-01-15T10:00:00Z"),
                         datetime(2026, 1, 15, 10, tzinfo=timezone.utc))

    def test_missing_file(self):
        self.assertEqual(self.search("x")[0], 2)


if __name__ == "__main__":
    unittest.main()
