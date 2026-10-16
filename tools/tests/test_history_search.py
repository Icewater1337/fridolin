"""Tests for history_search.py, against a scratch history file."""

from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

import support  # noqa: F401  (puts tools/ on sys.path)
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

    def test_missing_file(self):
        self.assertEqual(self.search("x")[0], 2)


if __name__ == "__main__":
    unittest.main()
