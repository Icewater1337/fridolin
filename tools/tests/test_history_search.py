"""Tests for history_search.py, the wrapper around fridolin history."""

from __future__ import annotations

import json
import unittest

import support  # first: it puts tools/ on sys.path
import history_search

HITS = '{"hits": [{"line": 3, "text": "invoice"}], "count": 1, "truncated": false, "file": "h.jsonl"}'


class TranslateTest(unittest.TestCase):
    def test_arguments(self):
        cases = [
            ("text by default", ["--since", "7d", "invoice"], ["history", "--text", "--since", "7d", "invoice"], False),
            ("--json", ["--json", "--regex", "x"], ["history", "--json", "--regex", "x"], True),
            ("--json as the query", ["--", "--json"], ["history", "--text", "--", "--json"], False),
        ]
        for name, argv, want, as_json in cases:
            with self.subTest(name):
                self.assertEqual(history_search.translate(argv), (want, as_json))


class WrapperTest(unittest.TestCase):
    def test_text(self):
        fridolin, argv = support.fake_fridolin(self, stdout="2026-01-15T10:00:00Z [in 15551234567] invoice")
        code, out, err = support.run_tool("history_search.py", ["invoice"], {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(argv.read_text(encoding="utf-8")), ["history", "--text", "invoice"])
        self.assertEqual(out, "2026-01-15T10:00:00Z [in 15551234567] invoice\n")
        self.assertIn("history_search.py is deprecated", err)

    def test_json_is_still_a_list(self):
        fridolin, _ = support.fake_fridolin(self, stdout=HITS)
        code, out, err = support.run_tool("history_search.py", ["--json", "invoice"], {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out), [{"line": 3, "text": "invoice"}])

    def test_help_with_json(self):
        # fridolin history -h prints the usage on stderr and nothing on stdout.
        fridolin, argv = support.fake_fridolin(self, stdout="")
        code, out, err = support.run_tool("history_search.py", ["--json", "-h"], {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual((code, out.strip()), (0, ""), err)
        self.assertNotIn("Traceback", err)
        self.assertEqual(json.loads(argv.read_text(encoding="utf-8")), ["history", "--json", "-h"])

    def test_errors_are_json(self):
        fridolin, _ = support.fake_fridolin(self, stdout='{"error": "history file not found: h.jsonl"}', code=1)
        for argv in (["x"], ["--json", "x"]):
            with self.subTest(argv):
                code, out, _ = support.run_tool("history_search.py", argv, {"FRIDOLIN_BIN": str(fridolin)})
                self.assertEqual((code, json.loads(out)), (1, {"error": "history file not found: h.jsonl"}))


if __name__ == "__main__":
    unittest.main()
