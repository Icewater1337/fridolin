"""Tests for poll_results.py, the wrapper around fridolin polls."""

from __future__ import annotations

import json
import unittest

import support  # first: it puts tools/ on sys.path


class WrapperTest(unittest.TestCase):
    def test_runs_fridolin_polls(self):
        fridolin, argv = support.fake_fridolin(self, stdout='{"polls": [], "count": 0}')
        code, out, err = support.run_tool("poll_results.py", ["--recent", "5", "--chat", "120363000000000000@g.us"],
                                          {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(argv.read_text(encoding="utf-8")),
                         ["polls", "--recent", "5", "--chat", "120363000000000000@g.us"])
        self.assertEqual(json.loads(out), {"polls": [], "count": 0})
        self.assertIn("poll_results.py is deprecated", err)

    def test_passes_the_error_on(self):
        fridolin, _ = support.fake_fridolin(self, stdout='{"error": "Poll \'x\' not found"}', code=1)
        code, out, _ = support.run_tool("poll_results.py", ["--id", "x"], {"FRIDOLIN_BIN": str(fridolin)})
        self.assertEqual((code, json.loads(out)), (1, {"error": "Poll 'x' not found"}))

    def test_without_the_binary(self):
        code, out, _ = support.run_tool("poll_results.py", [], {"PATH": ""})
        self.assertEqual(code, 1)
        self.assertIn("FRIDOLIN_BIN", json.loads(out)["error"])


if __name__ == "__main__":
    unittest.main()
